// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/transform"
)

const goodDoc = `{
  "version": 1,
  "rules": [
    {
      "name": "hot",
      "topic": "plant/+/temp",
      "field": "c",
      "op": "above",
      "raise": 90,
      "clear": 80,
      "debounce": "10s",
      "clear_debounce": "5s",
      "rate_limit": {"max": 2, "per": "1h"},
      "on_fire": [{"type": "alert", "severity": "critical", "message": "too hot"}],
      "on_clear": [{"type": "emit", "topic": "plant/alarms/cleared", "transform": {"set_fields": {"ok": true}}}]
    }
  ]
}`

func mustParse(t *testing.T, doc string) *RuleSet {
	t.Helper()
	rs, errs := Parse([]byte(doc))
	if errs != nil {
		t.Fatalf("parse: %v", errs)
	}
	return rs
}

var t0 = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func msg(v float64) []byte { return []byte(fmt.Sprintf(`{"c":%v}`, v)) }

func TestParseGood(t *testing.T) {
	rs := mustParse(t, goodDoc)
	if rs.Len() != 1 || rs.Names()[0] != "hot" {
		t.Fatalf("names %v", rs.Names())
	}
}

func TestParseErrorsHaveLines(t *testing.T) {
	cases := []struct {
		name, doc string
		line      int
		contains  string
	}{
		{"syntax", "{\n \"rules\": [\n  {\"name\": }\n ]}", 3, "invalid JSON"},
		{"not object", "[1]", 1, "must be a JSON object"},
		{"unknown top", "{\n\"rules\": [],\n\"extra\": 1}", 3, "unknown top-level"},
		{"missing rules", "{}", 1, "missing required"},
		{"trailing", "{\"rules\":[]} {}", 1, "unexpected data"},
		{"dup rules key", "{\"rules\":[],\n\"rules\":[]}", 2, "duplicate key"},
		{"bad version", "{\"version\":2,\n\"rules\":[]}", 1, "unsupported version"},
		{"unknown field", "{\"rules\":[\n{\"name\":\"a\",\n\"topic\":\"t\",\n\"shell\":\"rm -rf /\"}]}", 4, "unknown field"},
		{"wrong type", "{\"rules\":[\n{\"name\":\"a\",\n\"topic\":5}]}", 3, "topic"},
		{"dup key", "{\"rules\":[\n{\"name\":\"a\",\n\"name\":\"b\"}]}", 3, "duplicate key"},
		{"bad op", mk(`"op":"sideways"`), 5, "above"},
		{"hyst above", mk(`"raise":10,"clear":20`), 8, "clear must be <= raise"},
		{"bad debounce", mk(`"debounce":"soon"`), 6, "invalid duration"},
		{"huge debounce", mk(`"debounce":"100h"`), 6, "out of range"},
		{"neg debounce", mk(`"debounce":"-1s"`), 6, "out of range"},
		{"rate max", mk(`"rate_limit":{"max":0,"per":"1m"}`), 6, "rate_limit"},
		{"rate per", mk(`"rate_limit":{"max":1,"per":"1ms"}`), 6, "rate_limit"},
		{"window bad time", mk(`"window":{"start":"25:00","end":"26:00"}`), 6, "HH:MM"},
		{"window equal", mk(`"window":{"start":"08:00","end":"08:00"}`), 6, "differ"},
		{"window day", mk(`"window":{"start":"08:00","end":"09:00","days":["funday"]}`), 6, "unknown day"},
		{"window tz", mk(`"window":{"start":"08:00","end":"09:00","timezone":"Mars/Base"}`), 6, "timezone"},
		{"window tz traversal", mk(`"window":{"start":"08:00","end":"09:00","timezone":"../../etc/passwd"}`), 6, "timezone"},
		{"bad topic", mk(`"topic":"a/#/b"`), 3, "topic filter"},
		{"no actions", mk(`"on_fire":[]`), 9, "at least one action"},
		{"shell action", mk(`"on_fire":[{"type":"exec","topic":"x"}]`), 9, "unsupported action"},
		{"emit wildcard", mk(`"on_fire":[{"type":"emit","topic":"a/+"}]`), 9, "fixed topic"},
		{"emit dollar", mk(`"on_fire":[{"type":"emit","topic":"$SYS/x"}]`), 9, "fixed topic"},
		{"alert sev", mk(`"on_fire":[{"type":"alert","severity":"apocalyptic"}]`), 9, "severity"},
		{"transform topic", mk(`"on_fire":[{"type":"emit","topic":"x","transform":{"topic_rewrite":"y"}}]`), 9, "rewrite the topic"},
		{"max keys", mk(`"max_keys":999999`), 6, "max_keys"},
		{"bad name", strings.Replace(mk(``), `"a"`, `"bad name!"`, 1), 2, "name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs, errs := Parse([]byte(c.doc))
			if rs != nil || len(errs) == 0 {
				t.Fatalf("expected errors, got rs=%v", rs)
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), c.contains) {
					found = true
					if e.Line != c.line {
						t.Errorf("line=%d want %d (%v)", e.Line, c.line, e)
					}
				}
			}
			if !found {
				t.Fatalf("no error containing %q in %v", c.contains, errs)
			}
		})
	}
}

// mk builds a rules doc where each given extra property replaces the default
// of the same key. Layout is fixed so line numbers are predictable:
// line 2 = rule start, 3 topic, 4 field, 5 op, 6 extras, 7 clear.
func mk(extra string) string {
	if strings.Contains(extra, `"raise":10,"clear":20`) {
		return strings.Replace(mk(""), `"clear":5`, `"clear":20`, 1)
	}
	m := map[string]string{
		"topic": `"t/+"`, "op": `"above"`, "raise": `10`, "clear": `5`, "on_fire": `[{"type":"alert"}]`,
	}
	line6 := ""
	if extra != "" {
		key := strings.SplitN(extra, `"`, 3)[1]
		if _, ok := m[key]; ok {
			m[key] = strings.TrimPrefix(extra, `"`+key+`":`)
			extra = ""
		}
	}
	// "extra" like `"raise":10,"clear":20` carries two defaults; handle by overriding both.
	if strings.Contains(extra, `"raise":10,"clear":20`) {
		m["raise"], m["clear"] = "10", "20"
		extra = ""
	}
	if extra != "" {
		line6 = extra + ","
	}
	return "{\"rules\":[\n{\"name\":\"a\",\n\"topic\":" + m["topic"] + ",\n\"field\":\"c\",\n\"op\":" + m["op"] + ",\n" + line6 + "\n\"raise\":" + m["raise"] + ",\n\"clear\":" + m["clear"] + ",\n\"on_fire\":" + m["on_fire"] + "}]}"
}

func TestParseDuplicateNamesAndLimits(t *testing.T) {
	one := `{"name":"a","topic":"t","field":"c","op":"above","raise":1,"clear":1,"on_fire":[{"type":"alert"}]}`
	_, errs := Parse([]byte(`{"rules":[` + one + "," + "\n" + one + `]}`))
	if len(errs) != 1 || !strings.Contains(errs[0].Msg, "duplicate rule name") || errs[0].Line != 2 {
		t.Fatalf("%v", errs)
	}
	var sb strings.Builder
	sb.WriteString(`{"rules":[`)
	for i := 0; i <= MaxRules; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(strings.Replace(one, `"a"`, fmt.Sprintf(`"r%d"`, i), 1))
	}
	sb.WriteString(`]}`)
	if _, errs := Parse([]byte(sb.String())); len(errs) == 0 || !strings.Contains(errs[0].Msg, "more than") {
		t.Fatalf("%v", errs)
	}
	if _, errs := Parse(make([]byte, MaxConfigBytes+1)); len(errs) == 0 {
		t.Fatal("oversize accepted")
	}
	// total key budget
	var big strings.Builder
	big.WriteString(`{"rules":[`)
	for i := 0; i < 5; i++ {
		if i > 0 {
			big.WriteString(",")
		}
		big.WriteString(fmt.Sprintf(`{"name":"r%d","topic":"t","field":"c","op":"above","raise":1,"clear":1,"max_keys":4096,"on_fire":[{"type":"alert"}]}`, i))
	}
	big.WriteString(`]}`)
	if _, errs := Parse([]byte(big.String())); len(errs) == 0 || !strings.Contains(errs[0].Msg, "in total") {
		t.Fatalf("%v", errs)
	}
}

func TestParseHostileDoesNotPanic(t *testing.T) {
	for _, s := range []string{"", "{", "}", "null", `{"rules":null}`, `{"rules":[null]}`, `{"rules":[1]}`, `{"rules":[[]]}`, `{"rules":["x"]}`,
		strings.Repeat("[", 10000), `{"rules":[{"name":"a","raise":1e999}]}`, "\x00\xff\xfe", `{"rules":{"x":1}}`} {
		rs, errs := Parse([]byte(s))
		if rs != nil && len(errs) > 0 {
			t.Fatalf("both for %q", s)
		}
		if rs == nil && len(errs) == 0 {
			t.Fatalf("neither for %q", s)
		}
	}
}

func fires(os []Outcome) (f, c int) {
	for _, o := range os {
		if o.Kind == "fire" {
			f++
		} else {
			c++
		}
	}
	return
}

func TestHysteresisDebounce(t *testing.T) {
	e := NewEngine(mustParse(t, goodDoc))
	topic := "plant/a/temp"
	at := t0
	step := func(v float64, dt time.Duration) []Outcome {
		at = at.Add(dt)
		return e.Evaluate(at, topic, msg(v))
	}
	if o := step(95, 0); len(o) != 0 {
		t.Fatal("fired before debounce")
	}
	if o := step(95, 5*time.Second); len(o) != 0 {
		t.Fatal("fired before debounce elapsed")
	}
	// dip below raise (but in the dead band) resets the pending raise
	step(85, time.Second)
	if o := step(95, 6*time.Second); len(o) != 0 {
		t.Fatal("debounce did not restart after the dip")
	}
	o := step(95, 10*time.Second)
	if f, _ := fires(o); f != 1 || len(o[0].Actions) != 1 || o[0].Actions[0].Type != "alert" {
		t.Fatalf("expected one fire, got %+v", o)
	}
	// still latched: more highs and dead-band values do nothing
	for _, v := range []float64{99, 91, 85, 81, 80} {
		if o := step(v, time.Second); len(o) != 0 {
			t.Fatalf("v=%v produced %+v", v, o)
		}
	}
	// clear needs 5s below 80
	if o := step(70, time.Second); len(o) != 0 {
		t.Fatal("cleared without debounce")
	}
	step(85, time.Second) // back into the dead band: pending clear reset
	step(70, time.Second)
	if o := step(70, 5*time.Second); len(o) != 1 || o[0].Kind != "clear" || o[0].Actions[0].Topic != "plant/alarms/cleared" {
		t.Fatalf("clear: %+v", o)
	}
	if e.Counters().Fired != 1 || e.Counters().Cleared != 1 {
		t.Fatalf("%+v", e.Counters())
	}
}

func TestBelowOp(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"name":"low","topic":"t","field":"p.bar","op":"below","raise":2,"clear":3,"on_fire":[{"type":"alert"}],"on_clear":[{"type":"alert","severity":"info"}]}]}`)
	e := NewEngine(rs)
	ev := func(v string, s int) []Outcome {
		return e.Evaluate(t0.Add(time.Duration(s)*time.Second), "t", []byte(`{"p":{"bar":`+v+`}}`))
	}
	if o := ev("2", 0); len(o) != 0 {
		t.Fatal("2 is not below 2")
	}
	if o := ev("1.5", 1); len(o) != 1 || o[0].Kind != "fire" {
		t.Fatal("should fire")
	}
	if o := ev("2.5", 2); len(o) != 0 {
		t.Fatal("dead band")
	}
	if o := ev("3.5", 3); len(o) != 1 || o[0].Kind != "clear" {
		t.Fatal("should clear")
	}
}

func TestRateLimit(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"name":"r","topic":"t","field":"c","op":"above","raise":10,"clear":5,"rate_limit":{"max":2,"per":"10m"},"on_fire":[{"type":"alert"}]}]}`)
	e := NewEngine(rs)
	at := t0
	cycle := func() Outcome {
		at = at.Add(time.Minute)
		o := e.Evaluate(at, "t", msg(20))
		at = at.Add(time.Minute)
		e.Evaluate(at, "t", msg(1))
		return o[0]
	}
	if o := cycle(); o.Suppressed || len(o.Actions) == 0 {
		t.Fatal("first should run")
	}
	if o := cycle(); o.Suppressed {
		t.Fatal("second should run")
	}
	if o := cycle(); !o.Suppressed || len(o.Actions) != 0 {
		t.Fatal("third must be rate limited")
	}
	if e.Counters().RateLimited != 1 {
		t.Fatalf("%+v", e.Counters())
	}
	at = at.Add(11 * time.Minute)
	if o := e.Evaluate(at, "t", msg(20)); o[0].Suppressed {
		t.Fatal("window should have expired")
	}
}

func TestWindow(t *testing.T) {
	// Mon-Fri 08:00-18:00 in +05:30 (site time).
	rs := mustParse(t, `{"rules":[{"name":"w","topic":"t","field":"c","op":"above","raise":10,"clear":5,
	  "window":{"days":["mon","tue","wed","thu","fri"],"start":"08:00","end":"18:00","timezone":"+05:30"},"on_fire":[{"type":"alert"}]}]}`)
	cases := []struct {
		utc  string
		want bool
	}{
		{"2026-11-23T03:00:00Z", true},  // Mon 08:30 IST
		{"2026-11-23T02:29:00Z", false}, // Mon 07:59 IST
		{"2026-11-23T12:29:00Z", true},  // Mon 17:59 IST
		{"2026-11-23T12:30:00Z", false}, // Mon 18:00 IST (end exclusive)
		{"2026-11-22T06:00:00Z", false}, // Sun 11:30 IST
		{"2026-11-27T20:00:00Z", false}, // Sat 01:30 IST
	}
	for _, c := range cases {
		e := NewEngine(rs)
		ts, _ := time.Parse(time.RFC3339, c.utc)
		got := len(e.Evaluate(ts, "t", msg(20))) == 1
		if got != c.want {
			t.Errorf("%s: fired=%v want %v", c.utc, got, c.want)
		}
	}
}

func TestWindowOvernight(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"name":"w","topic":"t","field":"c","op":"above","raise":10,"clear":5,
	  "window":{"days":["fri"],"start":"22:00","end":"06:00","timezone":"UTC"},"on_fire":[{"type":"alert"}]}]}`)
	cases := []struct {
		utc  string
		want bool
	}{
		{"2026-11-27T23:00:00Z", true},  // Fri 23:00
		{"2026-11-28T05:59:00Z", true},  // Sat 05:59, window started Fri
		{"2026-11-28T06:00:00Z", false}, // Sat 06:00
		{"2026-11-27T05:00:00Z", false}, // Fri 05:00 belongs to Thursday night
		{"2026-11-28T23:00:00Z", false}, // Sat 23:00
	}
	for _, c := range cases {
		e := NewEngine(rs)
		ts, _ := time.Parse(time.RFC3339, c.utc)
		if got := len(e.Evaluate(ts, "t", msg(20))) == 1; got != c.want {
			t.Errorf("%s: fired=%v want %v", c.utc, got, c.want)
		}
	}
}

func TestOutOfWindowStillClears(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"name":"w","topic":"t","field":"c","op":"above","raise":10,"clear":5,
	  "window":{"start":"08:00","end":"09:00","timezone":"UTC"},"on_fire":[{"type":"alert"}],"on_clear":[{"type":"alert"}]}]}`)
	e := NewEngine(rs)
	if o := e.Evaluate(t0.Add(-3*time.Hour-30*time.Minute), "t", msg(20)); len(o) != 1 { // 08:30
		t.Fatalf("expected fire %v", o)
	}
	if o := e.Evaluate(t0, "t", msg(1)); len(o) != 1 || o[0].Kind != "clear" { // 12:00, outside
		t.Fatalf("should clear outside the window: %v", o)
	}
}

func TestHostilePayloads(t *testing.T) {
	e := NewEngine(mustParse(t, goodDoc))
	big := `{"c":95,"pad":"` + strings.Repeat("x", MaxPayloadBytes) + `"}`
	deep := strings.Repeat(`{"c":`, 5000) + "1" + strings.Repeat("}", 5000)
	for _, p := range []string{"", "null", "[]", `"str"`, `{"c":"95"}`, `{"c":null}`, `{"c":true}`, `{"c":[95]}`, `{"c":1e999}`, `{"x":1}`, "\xff\xfe", big, deep, `{"c":`} {
		if o := e.Evaluate(t0, "plant/a/temp", []byte(p)); len(o) != 0 {
			t.Fatalf("payload %.20q produced %v", p, o)
		}
	}
	if c := e.Counters(); c.Skipped != 14 || c.Fired != 0 {
		t.Fatalf("%+v", c)
	}
	if o := e.Evaluate(t0, "other/topic", msg(99)); len(o) != 0 {
		t.Fatal("unrelated topic matched")
	}
}

func TestKeyCapAndEviction(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"name":"r","topic":"d/+","field":"c","op":"above","raise":10,"clear":5,"max_keys":3,"debounce":"1h","on_fire":[{"type":"alert"}]}]}`)
	e := NewEngine(rs)
	// two pending keys (active: never evicted) and one idle key
	e.Evaluate(t0, "d/a", msg(20))
	e.Evaluate(t0, "d/b", msg(20))
	e.Evaluate(t0.Add(time.Second), "d/idle", msg(1))
	// new key evicts the idle one
	e.Evaluate(t0.Add(2*time.Second), "d/new", msg(1))
	st := e.Status()[0]
	if len(st.Keys) != 3 {
		t.Fatalf("keys=%d", len(st.Keys))
	}
	if _, ok := st.Keys["d/idle"]; ok {
		t.Fatal("idle key survived")
	}
	// make everything active, then a new key must be rejected
	e.Evaluate(t0.Add(3*time.Second), "d/new", msg(20))
	e.Evaluate(t0.Add(4*time.Second), "d/overflow", msg(20))
	if e.Counters().KeyOverflow != 1 || len(e.Status()[0].Keys) != 3 {
		t.Fatalf("%+v", e.Counters())
	}
	// many topics never grow state beyond the cap
	for i := 0; i < 5000; i++ {
		e.Evaluate(t0, fmt.Sprintf("d/%d", i), msg(1))
	}
	if n := len(e.Status()[0].Keys); n > 3 {
		t.Fatalf("keys=%d", n)
	}
}

func TestClockGoesBackwards(t *testing.T) {
	e := NewEngine(mustParse(t, goodDoc))
	e.Evaluate(t0.Add(time.Hour), "plant/a/temp", msg(95)) // pending since +1h
	// clock jumps back 2h: debounce must restart from the new time, not wedge
	back := t0.Add(-time.Hour)
	e.Evaluate(back, "plant/a/temp", msg(95))
	if o := e.Evaluate(back.Add(11*time.Second), "plant/a/temp", msg(95)); len(o) != 1 {
		t.Fatalf("did not fire after clock step back: %v", o)
	}
}

func TestSnapshotRestoreNoRefire(t *testing.T) {
	rs := mustParse(t, goodDoc)
	e := NewEngine(rs)
	topic := "plant/a/temp"
	e.Evaluate(t0, topic, msg(95))
	if o := e.Evaluate(t0.Add(11*time.Second), topic, msg(95)); len(o) != 1 {
		t.Fatal("setup fire failed")
	}
	e.Evaluate(t0.Add(20*time.Second), topic, msg(70)) // pending clear
	b, err := json.Marshal(e.Snapshot(t0.Add(21 * time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err = json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(rs)
	if n := e2.Restore(snap); n != 1 {
		t.Fatalf("restored %d", n)
	}
	// no re-fire of an already-firing alarm
	if o := e2.Evaluate(t0.Add(22*time.Second), topic, msg(99)); len(o) != 0 {
		t.Fatalf("re-fired after restart: %v", o)
	}
	// pending clear resumed: 70 again after >=5s from the original pending time clears
	if o := e2.Evaluate(t0.Add(26*time.Second), topic, msg(70)); len(o) != 0 {
		// pending was reset by the 99 above (dead-band/raise value): expected no clear yet
		t.Fatalf("unexpected %v", o)
	}
	if o := e2.Evaluate(t0.Add(32*time.Second), topic, msg(70)); len(o) != 1 || o[0].Kind != "clear" {
		t.Fatalf("expected clear after restore: %v", o)
	}
	// rate limit history survives: one fire already counted
	st := e.Snapshot(t0.Add(time.Minute)).Rules["hot"][topic]
	if len(st.Fires) != 1 {
		t.Fatalf("fires=%v", st.Fires)
	}
}

func TestRestoreHostileSnapshot(t *testing.T) {
	rs := mustParse(t, goodDoc)
	fires := make([]time.Time, 100000)
	snap := Snapshot{Rules: map[string]map[string]KeyState{
		"gone": {"x": {Firing: true}},
		"hot": {
			"plant/a/temp":            {Firing: true, Pending: "raise", Fires: fires},
			"plant/b/temp":            {Pending: "bogus", LastValue: 0},
			strings.Repeat("k", 5000): {Firing: true},
		},
	}}
	for i := 0; i < 1000; i++ {
		snap.Rules["hot"][fmt.Sprintf("plant/%d/temp", i)] = KeyState{Firing: true}
	}
	e := NewEngine(rs)
	n := e.Restore(snap)
	if n > DefaultKeys {
		t.Fatalf("restored %d keys, over the cap", n)
	}
	st := e.Status()[0].Keys
	if k := st["plant/a/temp"]; len(k.Fires) > 2 || k.Pending != "" {
		t.Fatalf("not sanitised: %+v", k)
	}
	if len(e.Firing()) == 0 {
		t.Fatal("firing lost")
	}
	// empty/zero snapshot is fine
	NewEngine(rs).Restore(Snapshot{})
}

func TestSnapshotCompactsIdleKeys(t *testing.T) {
	e := NewEngine(mustParse(t, `{"rules":[{"name":"r","topic":"d/+","field":"c","op":"above","raise":10,"clear":5,"on_fire":[{"type":"alert"}]}]}`))
	for i := 0; i < 50; i++ {
		e.Evaluate(t0, fmt.Sprintf("d/%d", i), msg(1))
	}
	e.Evaluate(t0, "d/hot", msg(50))
	s := e.Snapshot(t0)
	if len(s.Rules["r"]) != 1 {
		t.Fatalf("snapshot kept %d keys", len(s.Rules["r"]))
	}
}

func TestRender(t *testing.T) {
	o := Outcome{Rule: "hot", Key: "plant/a/temp", Kind: "fire", Value: 95, At: t0}
	ev, err := Render(o, Action{Type: "alert", Severity: "critical", Message: "m"})
	if err != nil || ev.Topic != "nodra/rules/alert/hot" || !strings.Contains(string(ev.Payload), `"severity":"critical"`) {
		t.Fatalf("%v %+v", err, ev)
	}
	ev, err = Render(o, Action{Type: "emit", Topic: "x/y", Transform: nil})
	if err != nil || ev.Topic != "x/y" {
		t.Fatal(err)
	}
	ev, err = Render(o, Action{Type: "emit", Topic: "x/y", Transform: transformSet()})
	if err != nil || !strings.Contains(string(ev.Payload), `"site":"a"`) || ev.Headers["x-nodra-rule"] != "hot" {
		t.Fatalf("%v %+v", err, ev)
	}
	if _, err = Render(o, Action{Type: "exec"}); err == nil {
		t.Fatal("exec rendered")
	}
}

func TestReplay(t *testing.T) {
	rs := mustParse(t, goodDoc)
	msgs := []Message{
		{Topic: "plant/a/temp", Payload: msg(95), Time: t0},
		{Topic: "plant/a/temp", Payload: msg(96)},                                 // +1s default
		{Topic: "plant/a/temp", Payload: msg(97), Time: t0.Add(11 * time.Second)}, // fires
		{Topic: "plant/a/temp", Payload: msg(10), Time: t0.Add(20 * time.Second)},
		{Topic: "plant/a/temp", Payload: msg(10), Time: t0.Add(30 * time.Second)}, // clears
		{Topic: "plant/a/temp", Payload: []byte(`garbage`)},
		{Topic: "nope", Payload: msg(1)},
	}
	rep, err := Replay(rs, t0, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fires != 1 || rep.Clears != 1 || rep.Skipped != 1 || len(rep.Transitions) != 2 || rep.Messages != 7 {
		t.Fatalf("%+v", rep)
	}
	if rep.Transitions[0].Events[0].Topic != "nodra/rules/alert/hot" || rep.Transitions[1].Events[0].Topic != "plant/alarms/cleared" {
		t.Fatalf("%+v", rep.Transitions)
	}
	if len(rep.FinalFiring) != 0 {
		t.Fatal("still firing")
	}
	if _, err = Replay(rs, t0, make([]Message, MaxReplayMessages+1)); err == nil {
		t.Fatal("oversize replay accepted")
	}
	// determinism
	rep2, _ := Replay(rs, t0, msgs)
	a, _ := json.Marshal(rep)
	b, _ := json.Marshal(rep2)
	if string(a) != string(b) {
		t.Fatal("replay not deterministic")
	}
}

func TestReplayTruncatesReport(t *testing.T) {
	rs := mustParse(t, `{"rules":[{"name":"r","topic":"d/+","field":"c","op":"above","raise":10,"clear":5,"on_fire":[{"type":"alert"}]}]}`)
	var msgs []Message
	for i := 0; i < MaxReplayMessages; i++ {
		msgs = append(msgs, Message{Topic: fmt.Sprintf("d/%d", i%2000), Payload: msg(float64(i%2) * 100)})
	}
	rep, err := Replay(rs, t0, msgs)
	if err != nil || len(rep.Transitions) > MaxReplayReport {
		t.Fatalf("%v %d", err, len(rep.Transitions))
	}
}

func TestMemoryBoundedUnderTopicStorm(t *testing.T) {
	e := NewEngine(mustParse(t, `{"rules":[{"name":"a","topic":"#","field":"c","op":"above","raise":10,"clear":5,"on_fire":[{"type":"alert"}]},
	 {"name":"b","topic":"#","field":"c","op":"above","raise":1,"clear":0,"on_fire":[{"type":"alert"}]}]}`))
	for i := 0; i < 20000; i++ {
		e.Evaluate(t0.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("s/%d", i), msg(50))
	}
	n := 0
	for _, s := range e.Status() {
		n += len(s.Keys)
	}
	if n > 2*DefaultKeys {
		t.Fatalf("state grew to %d keys", n)
	}
}

func transformSet() *transform.Transform {
	return &transform.Transform{SetFields: map[string]any{"site": "a"}}
}
