// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package sparkplug

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func uv(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}
func fVar(f int, v uint64) []byte { return append(uv(uint64(f)<<3), uv(v)...) }
func fLen(f int, b []byte) []byte {
	return append(append(uv(uint64(f)<<3|2), uv(uint64(len(b)))...), b...)
}

type mspec struct {
	name   string
	alias  *uint64
	dt     uint64
	intVal *uint64 // field 10
	long   *uint64 // field 11
	dbl    *float64
	flt    *float32
	boolv  *bool
	str    *string
	null   bool
}

func metric(m mspec) []byte {
	var b []byte
	if m.name != "" {
		b = append(b, fLen(1, []byte(m.name))...)
	}
	if m.alias != nil {
		b = append(b, fVar(2, *m.alias)...)
	}
	b = append(b, fVar(4, m.dt)...)
	if m.null {
		b = append(b, fVar(7, 1)...)
	}
	if m.intVal != nil {
		b = append(b, fVar(10, *m.intVal)...)
	}
	if m.long != nil {
		b = append(b, fVar(11, *m.long)...)
	}
	if m.flt != nil {
		b = append(b, uv(12<<3|5)...)
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(*m.flt))
	}
	if m.dbl != nil {
		b = append(b, uv(13<<3|1)...)
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(*m.dbl))
	}
	if m.boolv != nil {
		v := uint64(0)
		if *m.boolv {
			v = 1
		}
		b = append(b, fVar(14, v)...)
	}
	if m.str != nil {
		b = append(b, fLen(15, []byte(*m.str))...)
	}
	return b
}

func payload(seq int, ts uint64, ms ...mspec) []byte {
	b := fVar(1, ts)
	for _, m := range ms {
		b = append(b, fLen(2, metric(m))...)
	}
	if seq >= 0 {
		b = append(b, fVar(3, uint64(seq))...)
	}
	return b
}

func u64(v uint64) *uint64 { return &v }

func TestParseTopic(t *testing.T) {
	tests := []struct {
		in      string
		want    Topic
		wantErr bool
	}{
		{"spBv1.0/g/NBIRTH/n1", Topic{Group: "g", Type: NBIRTH, Node: "n1"}, false},
		{"spBv1.0/g/DDATA/n1/d1", Topic{Group: "g", Type: DDATA, Node: "n1", Device: "d1"}, false},
		{"spBv1.0/STATE/scada", Topic{Type: STATE, Host: "scada"}, false},
		{"spBv1.0/g/DBIRTH/n1", Topic{}, true},     // device type without device
		{"spBv1.0/g/NDATA/n1/d1", Topic{}, true},   // node type with device
		{"spBv1.0/g/BOGUS/n1", Topic{}, true},      // unknown type
		{"spBv1.0/g/NDATA/+", Topic{}, true},       // wildcard
		{"spBv1.0/g//n1", Topic{}, true},           // empty element
		{"spBv1.0/g/NDATA/n1/d1/x", Topic{}, true}, // too long
		{"spBv1.0/STATE/a/b", Topic{}, true},       // bad STATE
		{"spAv1.0/g/NDATA/n1", Topic{}, true},      // wrong namespace
		{"spBv1.0", Topic{}, true},                 // too short
		{"spBv1.0/g/NDATA/n\x001", Topic{}, true},  // NUL
	}
	for _, tc := range tests {
		got, err := ParseTopic(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("%q: got %+v want %+v", tc.in, got, tc.want)
		}
	}
	if !IsSparkplug("spBv1.0/x") || IsSparkplug("spBv1.0x/y") || IsSparkplug("factory/spBv1.0") {
		t.Error("IsSparkplug wrong")
	}
}

func TestDecodeValues(t *testing.T) {
	f32, f64, tr, s := float32(1.5), 2.25, true, "hello"
	neg8 := uint64(0xfffffffb)          // int8 -5 carried in uint32 field
	neg64 := uint64(math.MaxUint64 - 1) // int64 -2
	p, err := DecodePayload(payload(3, 1700000000000,
		mspec{name: "i8", dt: tInt8, intVal: &neg8},
		mspec{name: "i64", dt: tInt64, long: &neg64},
		mspec{name: "u32", dt: tUInt32, intVal: u64(4000000000)},
		mspec{name: "f", dt: tFloat, flt: &f32},
		mspec{name: "d", dt: tDouble, dbl: &f64},
		mspec{name: "b", dt: tBoolean, boolv: &tr},
		mspec{name: "s", dt: tString, str: &s},
		mspec{name: "n", dt: tInt32, null: true},
	))
	if err != nil {
		t.Fatal(err)
	}
	if p.Seq != 3 || !p.HasSeq || p.Timestamp != 1700000000000 || len(p.Metrics) != 8 {
		t.Fatalf("bad envelope %+v", p)
	}
	want := []any{int64(-5), int64(-2), uint64(4000000000), 1.5, 2.25, true, "hello", nil}
	for i, w := range want {
		if !reflect.DeepEqual(p.Metrics[i].Value, w) {
			t.Errorf("metric %s = %#v want %#v", p.Metrics[i].Name, p.Metrics[i].Value, w)
		}
	}
	if !p.Metrics[7].IsNull {
		t.Error("null flag lost")
	}
}

func TestDecodeHostile(t *testing.T) {
	good := payload(1, 5, mspec{name: "a", dt: tInt32, intVal: u64(7)}, mspec{name: "b", dt: tString, str: func() *string { s := "xyz"; return &s }()})
	// every truncation must error or succeed, never panic
	for i := 0; i < len(good); i++ {
		_, _ = DecodePayload(good[:i])
	}
	bad := map[string][]byte{
		"varint overflow":   append([]byte{0x08}, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}...),
		"varint unfinished": {0x08, 0x80, 0x80},
		"len past end":      {0x12, 0x7f, 0x01},
		"huge len":          append([]byte{0x12}, uv(math.MaxUint64)...),
		"field zero":        {0x00, 0x01},
		"group wire type":   {0x0b},
		"inner truncated":   fLen(2, []byte{0x0a, 0x05, 'a'}),
		"fixed32 short":     fLen(2, []byte{uint8(12<<3 | 5), 1, 2}),
	}
	for name, b := range bad {
		if _, err := DecodePayload(b); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// too many metrics
	var many []byte
	for i := 0; i < MaxMetrics+1; i++ {
		many = append(many, fLen(2, nil)...)
	}
	if _, err := DecodePayload(many); err == nil {
		t.Error("metric cap not enforced")
	}
	if _, err := DecodePayload(make([]byte, MaxPayloadBytes+1)); err == nil {
		t.Error("size cap not enforced")
	}
	// NaN must not leak into JSON
	nan := math.NaN()
	p, err := DecodePayload(payload(0, 1, mspec{name: "x", dt: tDouble, dbl: &nan}))
	if err != nil || p.Metrics[0].Value != nil {
		t.Errorf("NaN handling: %v %#v", err, p.Metrics)
	}
	if _, err := (Observation{Payload: p}).JSON(); err != nil {
		t.Errorf("NaN broke JSON: %v", err)
	}
	// random garbage never panics
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		b := make([]byte, rng.Intn(64))
		rng.Read(b)
		_, _ = DecodePayload(b)
	}
	// unknown fields are skipped
	extra := append(good, fVar(99, 1)...)
	extra = append(extra, fLen(98, []byte("zz"))...)
	if _, err := DecodePayload(extra); err != nil {
		t.Errorf("unknown fields should be skipped: %v", err)
	}
}

func FuzzDecodePayload(f *testing.F) {
	f.Add(payload(1, 1, mspec{name: "a", dt: tInt32, intVal: u64(1)}))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = DecodePayload(b) })
}

func obsOf(t *testing.T, tr *Tracker, topic string, b []byte, now time.Time) Observation {
	t.Helper()
	tp, err := ParseTopic(topic)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodePayload(b)
	if err != nil {
		t.Fatal(err)
	}
	return tr.Observe(tp, p, now)
}

func has(o Observation, a string) bool {
	for _, x := range o.Anomalies {
		if x == a {
			return true
		}
	}
	return false
}

func bd(n uint64) mspec { return mspec{name: "bdSeq", dt: tUInt64, long: u64(n)} }

func TestLifecycle(t *testing.T) {
	tr := NewTracker()
	now := time.Unix(1000, 0)
	nb := "spBv1.0/g/NBIRTH/n1"

	o := obsOf(t, tr, nb, payload(0, 1, bd(7), mspec{name: "Temp", alias: u64(1), dt: tInt32, intVal: u64(20)}), now)
	if !o.Changed || !o.State.Online || o.State.BdSeq != 7 || len(o.Anomalies) != 0 {
		t.Fatalf("nbirth: %+v", o)
	}
	o = obsOf(t, tr, "spBv1.0/g/DBIRTH/n1/d1", payload(1, 2, mspec{name: "Speed", alias: u64(2), dt: tInt32, intVal: u64(1)}), now)
	if !o.Changed || !o.State.Online || len(o.Anomalies) != 0 {
		t.Fatalf("dbirth: %+v", o)
	}
	// alias-only data resolves names
	o = obsOf(t, tr, "spBv1.0/g/NDATA/n1", payload(2, 3, mspec{alias: u64(1), dt: tInt32, intVal: u64(21)}), now)
	if o.Payload.Metrics[0].Name != "Temp" || len(o.Anomalies) != 0 {
		t.Fatalf("alias resolve: %+v", o)
	}
	o = obsOf(t, tr, "spBv1.0/g/DDATA/n1/d1", payload(3, 3, mspec{alias: u64(2), dt: tInt32, intVal: u64(5)}), now)
	if o.Payload.Metrics[0].Name != "Speed" {
		t.Fatalf("device alias: %+v", o)
	}
	// unknown alias flagged
	o = obsOf(t, tr, "spBv1.0/g/DDATA/n1/d1", payload(4, 3, mspec{alias: u64(99), dt: tInt32, intVal: u64(5)}), now)
	if !has(o, AnomalyUnknownAlias) {
		t.Fatalf("expected unknown_alias: %+v", o.Anomalies)
	}
	// sequence gap
	o = obsOf(t, tr, "spBv1.0/g/NDATA/n1", payload(9, 3), now)
	if !has(o, AnomalySeqGap) || !o.State.NeedsRebirth {
		t.Fatalf("expected seq gap: %+v", o)
	}
	// stale NDEATH (wrong bdSeq) is ignored
	o = obsOf(t, tr, "spBv1.0/g/NDEATH/n1", payload(-1, 4, bd(6)), now)
	if !has(o, AnomalyStaleDeath) || !o.State.Online || o.Changed {
		t.Fatalf("stale death must be ignored: %+v", o)
	}
	// matching NDEATH takes node and devices offline
	o = obsOf(t, tr, "spBv1.0/g/NDEATH/n1", payload(-1, 5, bd(7)), now.Add(time.Second))
	if !o.Changed || o.State.Online {
		t.Fatalf("ndeath: %+v", o)
	}
	for _, s := range tr.Snapshot() {
		if s.Online {
			t.Errorf("%+v still online", s)
		}
	}
	// rebirth resets seq and clears rebirth flag
	o = obsOf(t, tr, nb, payload(0, 6, bd(8)), now.Add(2*time.Second))
	if !o.Changed || o.State.NeedsRebirth || o.State.BdSeq != 8 {
		t.Fatalf("rebirth: %+v", o)
	}
	// a late NDEATH from the old session is stale
	o = obsOf(t, tr, "spBv1.0/g/NDEATH/n1", payload(-1, 7, bd(7)), now.Add(3*time.Second))
	if !has(o, AnomalyStaleDeath) || !o.State.Online {
		t.Fatalf("old will must not kill the new session: %+v", o)
	}
}

func TestAnomalies(t *testing.T) {
	tr := NewTracker()
	now := time.Unix(1, 0)
	o := obsOf(t, tr, "spBv1.0/g/NDATA/ghost", payload(5, 1), now)
	if !has(o, AnomalyNoNodeBirth) {
		t.Errorf("data before birth: %v", o.Anomalies)
	}
	o = obsOf(t, tr, "spBv1.0/g/DBIRTH/ghost/d", payload(6, 1), now)
	if !has(o, AnomalyDeviceBeforeNode) {
		t.Errorf("dbirth before nbirth: %v", o.Anomalies)
	}
	o = obsOf(t, tr, "spBv1.0/g/NBIRTH/bad", payload(4, 1), now)
	if !has(o, AnomalyBirthSeq) {
		t.Errorf("birth seq: %v", o.Anomalies)
	}
	o = obsOf(t, tr, "spBv1.0/g/NDATA/bad", payload(-1, 1), now)
	if !has(o, AnomalyMissingSeq) {
		t.Errorf("missing seq: %v", o.Anomalies)
	}
	// death for unknown node tracks nothing
	before := len(tr.Snapshot())
	obsOf(t, tr, "spBv1.0/g/NDEATH/unknown", payload(-1, 1, bd(1)), now)
	obsOf(t, tr, "spBv1.0/g/DDEATH/unknown/d", payload(0, 1), now)
	if len(tr.Snapshot()) != before {
		t.Error("death for unknown node created state")
	}
	// seq wraps at 256
	tr2 := NewTracker()
	obsOf(t, tr2, "spBv1.0/g/NBIRTH/w", payload(0, 1, bd(0)), now)
	for i := 1; i <= 300; i++ {
		o := obsOf(t, tr2, "spBv1.0/g/NDATA/w", payload(i%256, 1), now)
		if len(o.Anomalies) != 0 {
			t.Fatalf("seq %d flagged: %v", i, o.Anomalies)
		}
	}
}

func TestTrackerBounds(t *testing.T) {
	tr := NewTracker()
	now := time.Unix(1, 0)
	for i := 0; i < MaxNodes+5; i++ {
		tp := Topic{Group: "g", Type: NBIRTH, Node: strings.Repeat("n", 1) + string(rune('a'+i%26)) + itoa(i)}
		tr.Observe(tp, Payload{HasSeq: true}, now)
	}
	if got := len(tr.Snapshot()); got != MaxNodes {
		t.Fatalf("nodes tracked = %d, want cap %d", got, MaxNodes)
	}
	for i := 0; i < 300; i++ {
		tr.SetHost(itoa(i), true)
	}
	if len(tr.Hosts()) > 256 {
		t.Error("host cap not enforced")
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestHostState(t *testing.T) {
	for in, want := range map[string][2]bool{`{"online":true,"timestamp":1}`: {true, true}, `{"online":false}`: {false, true}, "ONLINE": {true, true}, " offline\n": {false, true}, `{}`: {false, false}, "junk": {false, false}} {
		on, ok := ParseHostState([]byte(in))
		if on != want[0] || ok != want[1] {
			t.Errorf("%q -> %v,%v", in, on, ok)
		}
	}
}

func TestEnvelopeJSON(t *testing.T) {
	tr := NewTracker()
	o := obsOf(t, tr, "spBv1.0/g/NBIRTH/n", payload(0, 9, bd(1)), time.Unix(1, 0))
	b, err := o.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "NBIRTH" || m["node"] != "n" || m["namespace"] != Namespace {
		t.Errorf("envelope: %s", b)
	}
}
