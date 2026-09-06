package router

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		f, t string
		want bool
	}{{"a/b", "a/b", true}, {"a/b", "a/c", false}, {"sensors/+/telemetry", "sensors/plc1/telemetry", true}, {"sensors/+/telemetry", "sensors/a/b/telemetry", false}, {"edge/#", "edge/a/b/c", true}, {"#", "anything/here", true}, {"a/+", "a/b", true}, {"a/+", "a", false}, {"a/#", "a", true}, {"a/#", "a/b", true}, {"/a/b/", "a/b", true}}
	for _, c := range cases {
		if got := Match(c.f, c.t); got != c.want {
			t.Errorf("Match(%q,%q)=%v want %v", c.f, c.t, got, c.want)
		}
	}
}
