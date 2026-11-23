// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/zyvorai/nodra/internal/transform"
)

// Hard limits keep memory and CPU bounded no matter what a rules file says.
const (
	MaxRules          = 256
	MaxActions        = 8
	MaxKeysPerRule    = 4096
	DefaultKeys       = 256
	MaxTotalKeys      = 16384
	MaxRateLimit      = 60
	MaxDuration       = 24 * time.Hour
	MaxTopicLen       = 256
	MaxFieldDepth     = 8
	MaxPayloadBytes   = 64 << 10
	MaxConfigBytes    = 1 << 20
	maxReportedErrors = 50
)

// Rule is one local, offline alarm rule: a numeric field of matching messages
// is compared against separate raise and clear thresholds (hysteresis), with
// optional debounce, a site-local time window and a rate limit on firing.
type Rule struct {
	Name          string     `json:"name"`
	Topic         string     `json:"topic"`
	Field         string     `json:"field"`
	Op            string     `json:"op"` // "above" or "below"
	Raise         *float64   `json:"raise"`
	Clear         *float64   `json:"clear"`
	Debounce      string     `json:"debounce,omitempty"`
	ClearDebounce string     `json:"clear_debounce,omitempty"`
	RateLimit     *RateLimit `json:"rate_limit,omitempty"`
	Window        *Window    `json:"window,omitempty"`
	MaxKeys       int        `json:"max_keys,omitempty"`
	OnFire        []Action   `json:"on_fire"`
	OnClear       []Action   `json:"on_clear,omitempty"`
}

// RateLimit allows at most Max firings per sliding Per interval; further
// firings still change state but run no actions.
type RateLimit struct {
	Max int    `json:"max"`
	Per string `json:"per"`
}

// Window restricts when a rule may raise. Times are wall-clock in Timezone
// (an IANA name if the host has tzdata, a fixed offset such as "+05:30",
// "UTC", or "Local" - the default - for the device local zone).
// Start > End wraps past midnight. An empty Days list means every day.
type Window struct {
	Days     []string `json:"days,omitempty"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Timezone string   `json:"timezone,omitempty"`
}

// Action is deliberately closed: "emit" ingests a JSON event on a fixed topic
// (so existing local routes and the cloud uplink handle it) and "alert" ingests
// an alert event on nodra/rules/alert/<rule>. No code, shell or new egress.
type Action struct {
	Type      string               `json:"type"`
	Topic     string               `json:"topic,omitempty"`
	Severity  string               `json:"severity,omitempty"`
	Message   string               `json:"message,omitempty"`
	Transform *transform.Transform `json:"transform,omitempty"`
}

// ConfigError is one validation problem with the line it was found on.
type ConfigError struct {
	Line  int    `json:"line"`
	Rule  string `json:"rule,omitempty"`
	Field string `json:"field,omitempty"`
	Msg   string `json:"message"`
}

func (e ConfigError) Error() string {
	s := fmt.Sprintf("line %d", e.Line)
	if e.Rule != "" {
		s += fmt.Sprintf(": rule %q", e.Rule)
	}
	if e.Field != "" {
		s += ": " + e.Field
	}
	return s + ": " + e.Msg
}

// ConfigErrors is a list of ConfigError that satisfies error.
type ConfigErrors []ConfigError

func (es ConfigErrors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// RuleSet is a validated, compiled set of rules.
type RuleSet struct {
	rules []*compiled
}

// Names returns the rule names in file order.
func (rs *RuleSet) Names() []string {
	out := make([]string, len(rs.rules))
	for i, r := range rs.rules {
		out[i] = r.Name
	}
	return out
}

// Len is the number of rules.
func (rs *RuleSet) Len() int { return len(rs.rules) }

type compiled struct {
	Rule
	raise, clear  float64
	above         bool
	debounce      time.Duration
	clearDebounce time.Duration
	rateMax       int
	ratePer       time.Duration
	fieldPath     []string
	loc           *time.Location
	days          [7]bool
	startMin      int
	endMin        int
	hasWindow     bool
	maxKeys       int
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var dayNames = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

func lineAt(src []byte, off int) int {
	if off > len(src) {
		off = len(src)
	}
	if off < 0 {
		off = 0
	}
	return 1 + bytes.Count(src[:off], []byte("\n"))
}

// Parse strictly decodes and validates a rules document:
//
//	{"rules": [ {...}, ... ]}
//
// Unknown fields, duplicate keys, wrong types, bad thresholds, durations,
// windows, topics and actions are all reported with the 1-based line they
// occur on. On any error no RuleSet is returned.
func Parse(src []byte) (*RuleSet, ConfigErrors) {
	var errs ConfigErrors
	add := func(line int, rule, field, format string, args ...any) {
		if len(errs) < maxReportedErrors {
			errs = append(errs, ConfigError{Line: line, Rule: rule, Field: field, Msg: fmt.Sprintf(format, args...)})
		}
	}
	if len(src) > MaxConfigBytes {
		add(1, "", "", "rules document exceeds %d bytes", MaxConfigBytes)
		return nil, errs
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	synErr := func(err error) {
		var se *json.SyntaxError
		line := 1
		if errors.As(err, &se) {
			line = lineAt(src, int(se.Offset))
		} else {
			line = lineAt(src, int(dec.InputOffset()))
		}
		add(line, "", "", "invalid JSON: %v", err)
	}
	tok, err := dec.Token()
	if err != nil {
		synErr(err)
		return nil, errs
	}
	if d, ok := tok.(json.Delim); !ok || d != 0x7b {
		add(lineAt(src, int(dec.InputOffset())), "", "", "document must be a JSON object with a \"rules\" array")
		return nil, errs
	}
	type rawRule struct {
		raw  json.RawMessage
		line int
		off  int
	}
	var raws []rawRule
	seenRules := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			synErr(err)
			return nil, errs
		}
		key, _ := kt.(string)
		kline := lineAt(src, int(dec.InputOffset()))
		switch key {
		case "rules":
			if seenRules {
				add(kline, "", "rules", "duplicate key")
			}
			seenRules = true
			tk, err := dec.Token()
			if err != nil {
				synErr(err)
				return nil, errs
			}
			if d, ok := tk.(json.Delim); !ok || d != 0x5b {
				add(lineAt(src, int(dec.InputOffset())), "", "rules", "must be an array")
				return nil, errs
			}
			for dec.More() {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					synErr(err)
					return nil, errs
				}
				end := int(dec.InputOffset())
				start := end - len(raw)
				if len(raws) >= MaxRules {
					add(lineAt(src, start), "", "rules", "more than %d rules", MaxRules)
					return nil, errs
				}
				raws = append(raws, rawRule{raw: raw, line: lineAt(src, start), off: start})
			}
			if _, err := dec.Token(); err != nil {
				synErr(err)
				return nil, errs
			}
		case "version":
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				synErr(err)
				return nil, errs
			}
			if string(bytes.TrimSpace(v)) != "1" {
				add(kline, "", "version", "unsupported version %s (only 1)", bytes.TrimSpace(v))
			}
		default:
			add(kline, "", key, "unknown top-level field")
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				synErr(err)
				return nil, errs
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		synErr(err)
		return nil, errs
	}
	if _, err := dec.Token(); err != io.EOF {
		add(lineAt(src, int(dec.InputOffset())), "", "", "unexpected data after the JSON document")
		return nil, errs
	}
	if !seenRules {
		add(1, "", "rules", "missing required \"rules\" array")
	}
	rs := &RuleSet{}
	names := map[string]int{}
	totalKeys := 0
	for _, rr := range raws {
		c, rerrs := compileRule(rr.raw, rr.line)
		for _, e := range rerrs {
			if len(errs) < maxReportedErrors {
				errs = append(errs, e)
			}
		}
		if c == nil {
			continue
		}
		if prev, dup := names[c.Name]; dup {
			add(rr.line, c.Name, "name", "duplicate rule name (first defined on line %d)", prev)
			continue
		}
		names[c.Name] = rr.line
		totalKeys += c.maxKeys
		rs.rules = append(rs.rules, c)
	}
	if totalKeys > MaxTotalKeys && len(errs) == 0 {
		add(1, "", "max_keys", "rules reserve %d keys in total, more than the %d allowed", totalKeys, MaxTotalKeys)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return rs, nil
}

// keyLine finds the line of "key": inside raw (first occurrence), falling back
// to the line the rule starts on.
func keyLine(raw json.RawMessage, base int, key string) int {
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:`)
	if loc := re.FindIndex(raw); loc != nil {
		return base + bytes.Count(raw[:loc[0]], []byte("\n"))
	}
	return base
}

var unknownFieldRe = regexp.MustCompile(`unknown field "([^"]*)"`)

func compileRule(raw json.RawMessage, base int) (*compiled, []ConfigError) {
	var errs []ConfigError
	line := func(field string) int {
		if field == "" {
			return base
		}
		return keyLine(raw, base, field)
	}
	name := ""
	{
		var probe struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &probe)
		name = probe.Name
	}
	add := func(field, format string, args ...any) {
		errs = append(errs, ConfigError{Line: line(field), Rule: name, Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	// Duplicate top-level keys silently win in encoding/json; refuse them.
	if dups := duplicateKeys(raw); len(dups) > 0 {
		for _, d := range dups {
			errs = append(errs, ConfigError{Line: base + bytes.Count(raw[:min(d.off, len(raw))], []byte("\n")), Rule: name, Field: d.key, Msg: "duplicate key"})
		}
		return nil, errs
	}
	var r Rule
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &te):
			l := base + bytes.Count(raw[:min(int(te.Offset), len(raw))], []byte("\n"))
			errs = append(errs, ConfigError{Line: l, Rule: name, Field: te.Field, Msg: "must be " + te.Type.String() + " (got " + te.Value + ")"})
		case unknownFieldRe.MatchString(err.Error()):
			f := unknownFieldRe.FindStringSubmatch(err.Error())[1]
			add(f, "unknown field")
		default:
			var se *json.SyntaxError
			l := base
			if errors.As(err, &se) {
				l = base + bytes.Count(raw[:min(int(se.Offset), len(raw))], []byte("\n"))
			}
			errs = append(errs, ConfigError{Line: l, Rule: name, Msg: "invalid rule: " + err.Error()})
		}
		return nil, errs
	}
	c := &compiled{Rule: r}
	if !nameRe.MatchString(r.Name) {
		add("name", "required; 1-64 chars of letters, digits, ., _, - starting with a letter or digit")
	}
	if r.Topic == "" || len(r.Topic) > MaxTopicLen || strings.ContainsAny(r.Topic, "\x00") || !validFilter(r.Topic) {
		add("topic", "must be a non-empty MQTT topic filter (+ and # as whole levels, # last), at most %d bytes", MaxTopicLen)
	}
	if r.Field == "" {
		add("field", "is required")
	} else {
		c.fieldPath = strings.Split(r.Field, ".")
		if len(c.fieldPath) > MaxFieldDepth {
			add("field", "path deeper than %d levels", MaxFieldDepth)
		}
		for _, p := range c.fieldPath {
			if p == "" {
				add("field", "empty path segment")
				break
			}
		}
	}
	switch r.Op {
	case "above":
		c.above = true
	case "below":
	default:
		add("op", "must be \"above\" or \"below\"")
	}
	if r.Raise == nil {
		add("raise", "is required")
	}
	if r.Clear == nil {
		add("clear", "is required (use the same value as raise for no hysteresis band)")
	}
	if r.Raise != nil && r.Clear != nil {
		c.raise, c.clear = *r.Raise, *r.Clear
		if c.above && c.clear > c.raise {
			add("clear", "for op \"above\" clear must be <= raise")
		}
		if r.Op == "below" && c.clear < c.raise {
			add("clear", "for op \"below\" clear must be >= raise")
		}
	}
	var derr error
	if c.debounce, derr = parseDur(r.Debounce); derr != nil {
		add("debounce", "%v", derr)
	}
	if c.clearDebounce, derr = parseDur(r.ClearDebounce); derr != nil {
		add("clear_debounce", "%v", derr)
	}
	if r.RateLimit != nil {
		c.rateMax = r.RateLimit.Max
		if c.rateMax < 1 || c.rateMax > MaxRateLimit {
			add("rate_limit", "max must be between 1 and %d", MaxRateLimit)
		}
		if c.ratePer, derr = parseDur(r.RateLimit.Per); derr != nil || c.ratePer < time.Second {
			add("rate_limit", "per must be a duration between 1s and %s", MaxDuration)
		}
	}
	if r.Window != nil {
		compileWindow(c, r.Window, add)
	}
	c.maxKeys = r.MaxKeys
	switch {
	case c.maxKeys == 0:
		c.maxKeys = DefaultKeys
	case c.maxKeys < 0 || c.maxKeys > MaxKeysPerRule:
		add("max_keys", "must be between 1 and %d", MaxKeysPerRule)
	}
	if len(r.OnFire) == 0 {
		add("on_fire", "needs at least one action")
	}
	checkActions := func(field string, acts []Action) {
		if len(acts) > MaxActions {
			add(field, "more than %d actions", MaxActions)
		}
		for i, a := range acts {
			if msg := validateAction(a); msg != "" {
				add(field, "action %d: %s", i, msg)
			}
		}
	}
	checkActions("on_fire", r.OnFire)
	checkActions("on_clear", r.OnClear)
	if len(errs) > 0 {
		return nil, errs
	}
	return c, nil
}

type dupKey struct {
	key string
	off int
}

func duplicateKeys(raw json.RawMessage) []dupKey {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil {
		return nil
	} else if dl, ok := t.(json.Delim); !ok || dl != 0x7b {
		return nil
	}
	seen := map[string]bool{}
	var dups []dupKey
	for d.More() {
		kt, err := d.Token()
		if err != nil {
			return dups
		}
		k, _ := kt.(string)
		if seen[k] {
			dups = append(dups, dupKey{k, int(d.InputOffset())})
		}
		seen[k] = true
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return dups
		}
	}
	return dups
}

func parseDur(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if d < 0 || d > MaxDuration {
		return 0, fmt.Errorf("duration %q out of range 0..%s", s, MaxDuration)
	}
	return d, nil
}

func validFilter(f string) bool {
	parts := strings.Split(f, "/")
	for i, p := range parts {
		switch {
		case p == "#":
			if i != len(parts)-1 {
				return false
			}
		case p == "+":
		case strings.ContainsAny(p, "+#"):
			return false
		}
	}
	return true
}

func parseHM(s string) (int, bool) {
	if len(s) != 5 || s[2] != 0x3a {
		return 0, false
	}
	h := int(s[0]-0x30)*10 + int(s[1]-0x30)
	m := int(s[3]-0x30)*10 + int(s[4]-0x30)
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < 0x30 || s[i] > 0x39 {
			return 0, false
		}
	}
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func loadZone(tz string) (*time.Location, error) {
	switch tz {
	case "", "Local":
		return time.Local, nil
	case "UTC":
		return time.UTC, nil
	}
	if (tz[0] == 0x2b || tz[0] == 0x2d) && len(tz) == 6 && tz[3] == 0x3a {
		h, okh := parseHM(tz[1:])
		if !okh {
			return nil, fmt.Errorf("invalid offset %q (want +HH:MM)", tz)
		}
		sec := h * 60
		if tz[0] == 0x2d {
			sec = -sec
		}
		return time.FixedZone(tz, sec), nil
	}
	if len(tz) > 64 || strings.Contains(tz, "..") || strings.HasPrefix(tz, "/") {
		return nil, fmt.Errorf("invalid timezone %q", tz)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("timezone %q not available on this host (use a fixed offset such as +05:30): %v", tz, err)
	}
	return loc, nil
}

func compileWindow(c *compiled, w *Window, add func(field, format string, args ...any)) {
	var ok1, ok2 bool
	c.startMin, ok1 = parseHM(w.Start)
	c.endMin, ok2 = parseHM(w.End)
	if !ok1 || !ok2 {
		add("window", "start and end must be HH:MM (00:00-23:59)")
	} else if c.startMin == c.endMin {
		add("window", "start and end must differ")
	}
	loc, err := loadZone(w.Timezone)
	if err != nil {
		add("window", "%v", err)
	}
	c.loc = loc
	if len(w.Days) == 0 {
		for i := range c.days {
			c.days[i] = true
		}
	}
	for _, d := range w.Days {
		wd, ok := dayNames[strings.ToLower(d)]
		if !ok {
			add("window", "unknown day %q (use mon..sun)", d)
			continue
		}
		c.days[wd] = true
	}
	c.hasWindow = true
}

func validateAction(a Action) string {
	switch a.Type {
	case "emit":
		if a.Topic == "" || len(a.Topic) > MaxTopicLen || strings.ContainsAny(a.Topic, "+#\x00") || strings.HasPrefix(a.Topic, "$") {
			return "emit needs a fixed topic (no wildcards, no $-prefix), at most 256 bytes"
		}
		if a.Severity != "" || a.Message != "" {
			return "severity/message are only valid on alert actions"
		}
	case "alert":
		if a.Topic != "" {
			return "alert actions use the fixed topic nodra/rules/alert/<rule>; use emit for a custom topic"
		}
		switch a.Severity {
		case "", "info", "warning", "critical":
		default:
			return "severity must be info, warning or critical"
		}
		if len(a.Message) > 512 {
			return "message longer than 512 bytes"
		}
	default:
		return fmt.Sprintf("unsupported action type %q (only emit and alert)", a.Type)
	}
	if t := a.Transform; t != nil {
		if t.TopicPrefix != "" || t.TopicRewrite != "" {
			return "transform may not rewrite the topic"
		}
	}
	return ""
}
