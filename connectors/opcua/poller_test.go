// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func TestNewPollerDefaults(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{
		Endpoint: "opc.tcp://plc.local:4840", NodeIDs: []string{"ns=2;i=1001"}, Topic: "factory/plc",
	})
	c, err := NewPoller("plc", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*Poller)
	if p.ival != 5*time.Second {
		t.Fatalf("interval default=%v", p.ival)
	}
	if len(p.nodeIDs) != 1 || p.nodeIDs[0].Namespace != 2 || p.nodeIDs[0].Numeric != 1001 {
		t.Fatalf("nodeIDs=%+v", p.nodeIDs)
	}
	cli, ok := p.cli.(*Client)
	if !ok || cli.Timeout != 5*time.Second {
		t.Fatalf("client=%+v", p.cli)
	}
}

func TestNewPollerRejectsMissingFields(t *testing.T) {
	cases := []string{
		`{"node_ids":["ns=2;i=1"],"topic":"x"}`,                                             // missing endpoint
		`{"endpoint":"opc.tcp://x:4840","node_ids":["ns=2;i=1"]}`,                           // missing topic
		`{"endpoint":"opc.tcp://x:4840","topic":"x"}`,                                       // missing node_ids
		`{"endpoint":"opc.tcp://x:4840","node_ids":["bad"],"topic":"x"}`,                    // unparsable node id
		`{"endpoint":"opc.tcp://x:4840","node_ids":["ns=2;i=1"],"topic":"x","mode":"push"}`, // invalid mode
	}
	for _, raw := range cases {
		if _, err := NewPoller("p", json.RawMessage(raw)); err == nil {
			t.Fatalf("expected error for config %s", raw)
		}
	}
}

func TestNewPollerAcceptsSubscribeMode(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{
		Endpoint: "opc.tcp://plc.local:4840", NodeIDs: []string{"ns=2;i=1001"}, Topic: "factory/plc", Mode: "subscribe",
	})
	c, err := NewPoller("plc", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*Poller)
	if _, ok := p.subCli.(*Client); !ok {
		t.Fatalf("subCli=%+v, want *Client", p.subCli)
	}
}

// fakeReader substitutes for *Client in poll() tests, avoiding any network.
type fakeReader struct {
	values []DataValue
	err    error
	gotIDs []NodeID
}

func (f *fakeReader) Read(_ context.Context, nodeIDs []NodeID) ([]DataValue, error) {
	f.gotIDs = nodeIDs
	if f.err != nil {
		return nil, f.err
	}
	return f.values, nil
}

func TestPollEmitsExpectedEventShape(t *testing.T) {
	ts := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	fr := &fakeReader{values: []DataValue{
		{Value: int32(72), StatusCode: 0, SourceTimestamp: &ts},
		{Value: "ok", StatusCode: 0},
	}}
	p := &Poller{
		name: "plc", cli: fr,
		cfg:     PollerConfig{Endpoint: "opc.tcp://plc.local:4840", NodeIDs: []string{"ns=2;i=1001", "ns=2;s=Status"}, Topic: "factory/plc"},
		nodeIDs: []NodeID{{Namespace: 2, Numeric: 1001}, {Namespace: 2, StringID: "Status", IsString: true}},
	}

	var got connector.Event
	var calls int
	p.poll(context.Background(), func(_ context.Context, ev connector.Event) error {
		calls++
		got = ev
		return nil
	})

	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
	if got.Topic != "factory/plc" {
		t.Fatalf("topic=%q", got.Topic)
	}
	if got.Headers["x-nodra-ingress"] != "opcua" || got.Headers["x-nodra-connector"] != "plc" {
		t.Fatalf("headers=%v", got.Headers)
	}
	var payload struct {
		Endpoint string       `json:"endpoint"`
		Values   []opcuaValue `json:"values"`
	}
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Endpoint != "opc.tcp://plc.local:4840" {
		t.Fatalf("endpoint=%q", payload.Endpoint)
	}
	if len(payload.Values) != 2 {
		t.Fatalf("values=%+v", payload.Values)
	}
	if payload.Values[0].NodeID != "ns=2;i=1001" || payload.Values[0].Value != float64(72) {
		t.Fatalf("values[0]=%+v", payload.Values[0])
	}
	if payload.Values[0].SourceTimestamp == "" {
		t.Fatalf("values[0] missing source_timestamp")
	}
	if payload.Values[1].NodeID != "ns=2;s=Status" || payload.Values[1].Value != "ok" {
		t.Fatalf("values[1]=%+v", payload.Values[1])
	}
	if len(fr.gotIDs) != 2 {
		t.Fatalf("cli.Read called with %d node ids, want 2", len(fr.gotIDs))
	}
	if p.last != "ok" {
		t.Fatalf("last=%q", p.last)
	}
}

func TestPollHandlesReadError(t *testing.T) {
	fr := &fakeReader{err: errors.New("boom")}
	p := &Poller{name: "plc", cli: fr, cfg: PollerConfig{Endpoint: "opc.tcp://x:4840", Topic: "t"}, nodeIDs: []NodeID{{Namespace: 0, Numeric: 1}}}
	var calls int
	p.poll(context.Background(), func(context.Context, connector.Event) error {
		calls++
		return nil
	})
	if calls != 0 {
		t.Fatalf("handler should not be called on read error, got %d calls", calls)
	}
	if h := p.Health(context.Background()); h.Healthy {
		t.Fatalf("expected unhealthy after read error, got %+v", h)
	}
}

// fakeSubscriber substitutes for *Client in subscribeLoop() tests, avoiding
// any network. It always returns err (simulating a dropped connection) after
// optionally invoking handler once on its first call, so tests can assert
// both that an emitted event's shape is correct and that subscribeLoop
// actually reconnects (calls Subscribe again) rather than giving up.
type fakeSubscriber struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeSubscriber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSubscriber) Subscribe(_ context.Context, nodeIDs []NodeID, _ time.Duration, handler func(NodeID, DataValue)) error {
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first && len(nodeIDs) > 0 {
		handler(nodeIDs[0], DataValue{Value: "first"})
	}
	return f.err
}

func TestSubscribeLoopEmitsAndReconnectsOnError(t *testing.T) {
	fs := &fakeSubscriber{err: errors.New("connection dropped")}
	p := &Poller{
		name: "plc", subCli: fs, ival: 5 * time.Millisecond,
		cfg:     PollerConfig{Endpoint: "opc.tcp://x:4840", Topic: "factory/plc"},
		nodeIDs: []NodeID{{Namespace: 2, Numeric: 1}},
	}

	var mu sync.Mutex
	var events []connector.Event
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.subscribeLoop(ctx, func(_ context.Context, ev connector.Event) error {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			return nil
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fs.callCount() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if fs.callCount() < 2 {
		t.Fatalf("expected subscribeLoop to reconnect (>=2 Subscribe calls), got %d", fs.callCount())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected at least one emitted event")
	}
	if events[0].Topic != "factory/plc" {
		t.Fatalf("topic=%q", events[0].Topic)
	}
	if events[0].Headers["x-nodra-ingress"] != "opcua" {
		t.Fatalf("headers=%v", events[0].Headers)
	}
	var payload struct {
		Values []opcuaValue `json:"values"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Values) != 1 || payload.Values[0].Value != "first" {
		t.Fatalf("payload values=%+v", payload.Values)
	}
}
