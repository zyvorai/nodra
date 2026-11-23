// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func spUV(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

// spPayload encodes seq, timestamp and one int32 metric (name, value).
func spPayload(seq, ts uint64, name string, val uint64, bd *uint64) []byte {
	metric := func(n string, dt, f uint64, v uint64) []byte {
		b := append(spUV(1<<3|2), spUV(uint64(len(n)))...)
		b = append(b, n...)
		b = append(b, spUV(4<<3)...)
		b = append(b, spUV(dt)...)
		b = append(b, spUV(f<<3)...)
		return append(b, spUV(v)...)
	}
	out := append(spUV(1<<3), spUV(ts)...)
	m := metric(name, 3, 10, val)
	out = append(append(out, spUV(2<<3|2)...), append(spUV(uint64(len(m))), m...)...)
	if bd != nil {
		m = metric("bdSeq", 8, 11, *bd)
		out = append(append(out, spUV(2<<3|2)...), append(spUV(uint64(len(m))), m...)...)
	}
	return append(append(out, spUV(3<<3)...), spUV(seq)...)
}

func TestSparkplugIngress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	cfg.MQTTListen = ""
	cfg.SparkplugEnabled = true
	a, err := New(cfg, filepath.Join(t.TempDir(), "n.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	zero := uint64(3)
	if err := a.handleMQTT(ctx, "spBv1.0/plant/NBIRTH/n1", spPayload(0, 1700000000000, "Temp", 20, &zero)); err != nil {
		t.Fatal(err)
	}
	if err := a.handleMQTT(ctx, "spBv1.0/plant/NDATA/n1", spPayload(1, 1700000001000, "Temp", 21, nil)); err != nil {
		t.Fatal(err)
	}
	// plain MQTT still works
	if err := a.handleMQTT(ctx, "plain/topic", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// hostile input is rejected and counted, not spooled
	before := a.Pending()
	for _, bad := range [][]byte{{0x12, 0x7f}, {0x08}, []byte(strings.Repeat("\xff", 20))} {
		if err := a.handleMQTT(ctx, "spBv1.0/plant/NDATA/n1", bad); err == nil {
			t.Errorf("hostile payload %x accepted", bad)
		}
	}
	if err := a.handleMQTT(ctx, "spBv1.0/plant/BOGUS/n1", nil); err == nil {
		t.Error("bad topic accepted")
	}
	if a.Pending() != before || a.sparkplugInvalid.Load() != 4 {
		t.Errorf("pending %d->%d invalid=%d", before, a.Pending(), a.sparkplugInvalid.Load())
	}
	if err := a.handleMQTT(ctx, "spBv1.0/STATE/scada", []byte(`{"online":true,"timestamp":1}`)); err != nil {
		t.Fatal(err)
	}

	evs, _ := a.spool.List()
	var sawDecoded, sawState bool
	for _, ev := range evs {
		switch {
		case ev.Topic == "spBv1.0/plant/NDATA/n1":
			var doc struct {
				Type    string `json:"type"`
				Metrics []struct {
					Name  string `json:"name"`
					Value any    `json:"value"`
				} `json:"metrics"`
			}
			if err := json.Unmarshal(ev.Payload, &doc); err != nil || doc.Type != "NDATA" || doc.Metrics[0].Name != "Temp" || doc.Metrics[0].Value != float64(21) {
				t.Fatalf("decoded payload wrong: %s %v", ev.Payload, err)
			}
			if ev.EventTime.UnixMilli() != 1700000001000 {
				t.Errorf("event time not taken from payload: %v", ev.EventTime)
			}
			sawDecoded = true
		case ev.Topic == "nodra/sparkplug/state/plant/n1":
			sawState = strings.Contains(string(ev.Payload), `"online":true`)
		}
	}
	if !sawDecoded || !sawState {
		t.Fatalf("decoded=%v state=%v events=%d", sawDecoded, sawState, len(evs))
	}

	a.cfg.LocalToken = "tok"
	req := httptest.NewRequest("GET", "/v1/sparkplug", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"node":"n1"`) || !strings.Contains(rr.Body.String(), `"scada":true`) {
		t.Fatalf("GET /v1/sparkplug: %d %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest("GET", "/v1/sparkplug", nil)
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Errorf("unauthenticated sparkplug read: %d", rr.Code)
	}
}

func TestSparkplugDisabledPassthrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	a, err := New(cfg, filepath.Join(t.TempDir(), "n.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.handleMQTT(context.Background(), "spBv1.0/g/NDATA/n", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if a.Pending() != 1 {
		t.Fatal("disabled sparkplug should pass through")
	}
}

func TestSpoolForecastEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	cfg.LocalToken = "tok"
	cfg.MaxSpoolEvents = 1000
	a, err := New(cfg, filepath.Join(t.TempDir(), "n.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an uplink that has been dark for 10 minutes while events arrive.
	a.lastUplink.Store(time.Now().Add(-10 * time.Minute).UnixNano())
	base := time.Now().Add(-100 * time.Second)
	for i := 0; i < 11; i++ {
		for j := 0; j < 20; j++ {
			if err := a.handleMQTT(context.Background(), "t", []byte(`{"v":1}`)); err != nil {
				t.Fatal(err)
			}
		}
		a.forecast.Observe(base.Add(time.Duration(i)*10*time.Second), a.spool.Stats())
	}
	for _, path := range []string{"/v1/spool/forecast", "/healthz"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("%s: %d", path, rr.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		fc := body
		if path == "/healthz" {
			fc, _ = body["spool_forecast"].(map[string]any)
		}
		if fc["growth_items_per_sec"].(float64) <= 0 || fc["time_to_full_seconds"].(float64) <= 0 || fc["outage_seconds"].(float64) < 590 {
			t.Fatalf("%s: unexpected forecast %v", path, fc)
		}
	}
	req := httptest.NewRequest("GET", "/v1/spool/forecast", nil)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Errorf("unauthenticated forecast: %d", rr.Code)
	}
}
