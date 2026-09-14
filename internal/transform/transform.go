// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package transform implements deterministic local-route filters and payload
// rewrites. Rules are data, not a scripting language: they run offline at the
// edge before a local delivery is queued.
package transform

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Filter drops an event when any configured predicate fails.
// Numeric compares apply to JSON numbers (or numeric strings) at the given keys.
type Filter struct {
	Exists []string           `json:"exists,omitempty"`
	Equals map[string]any     `json:"equals,omitempty"`
	Min    map[string]float64 `json:"min,omitempty"`
	Max    map[string]float64 `json:"max,omitempty"`
	In     map[string][]any   `json:"in,omitempty"`
}

// Transform rewrites topic, headers, or a JSON object payload.
type Transform struct {
	SetHeaders   map[string]string `json:"set_headers,omitempty"`
	DropFields   []string          `json:"drop_fields,omitempty"`
	SetFields    map[string]any    `json:"set_fields,omitempty"`
	WrapAs       string            `json:"wrap_as,omitempty"`
	TopicPrefix  string            `json:"topic_prefix,omitempty"`
	TopicRewrite string            `json:"topic_rewrite,omitempty"`
}

type Result struct {
	Keep    bool
	Topic   string
	Payload json.RawMessage
	Headers map[string]string
}

// Apply returns Keep=false when the event should not be locally delivered.
func Apply(topic string, payload json.RawMessage, headers map[string]string, f *Filter, tr *Transform) (Result, error) {
	out := Result{Keep: true, Topic: topic, Payload: append(json.RawMessage(nil), payload...), Headers: cloneHeaders(headers)}
	obj, objOK := asObject(payload)

	if f != nil {
		if !matchFilter(obj, objOK, f) {
			out.Keep = false
			return out, nil
		}
	}
	if tr == nil {
		return out, nil
	}
	if len(tr.SetHeaders) > 0 {
		if out.Headers == nil {
			out.Headers = map[string]string{}
		}
		for k, v := range tr.SetHeaders {
			out.Headers[k] = v
		}
	}
	if objOK && (len(tr.DropFields) > 0 || len(tr.SetFields) > 0 || tr.WrapAs != "") {
		for _, k := range tr.DropFields {
			delete(obj, k)
		}
		for k, v := range tr.SetFields {
			obj[k] = v
		}
		var body any = obj
		if tr.WrapAs != "" {
			body = map[string]any{tr.WrapAs: obj}
		}
		b, err := json.Marshal(body)
		if err != nil {
			return out, fmt.Errorf("transform marshal: %w", err)
		}
		out.Payload = b
	} else if !objOK && tr.WrapAs != "" {
		b, err := json.Marshal(map[string]any{tr.WrapAs: json.RawMessage(payload)})
		if err != nil {
			return out, err
		}
		out.Payload = b
	}
	if tr.TopicRewrite != "" {
		out.Topic = strings.ReplaceAll(tr.TopicRewrite, "{{topic}}", topic)
	} else if tr.TopicPrefix != "" {
		out.Topic = strings.TrimSuffix(tr.TopicPrefix, "/") + "/" + strings.TrimPrefix(topic, "/")
	}
	return out, nil
}

func matchFilter(obj map[string]any, objOK bool, f *Filter) bool {
	if !objOK {
		return len(f.Exists) == 0 && len(f.Equals) == 0 && len(f.Min) == 0 && len(f.Max) == 0 && len(f.In) == 0
	}
	for _, k := range f.Exists {
		if _, ok := obj[k]; !ok {
			return false
		}
	}
	for k, want := range f.Equals {
		got, ok := obj[k]
		if !ok || !equalJSON(got, want) {
			return false
		}
	}
	for k, min := range f.Min {
		n, ok := asFloat(obj[k])
		if !ok || n < min {
			return false
		}
	}
	for k, max := range f.Max {
		n, ok := asFloat(obj[k])
		if !ok || n > max {
			return false
		}
	}
	for k, allowed := range f.In {
		got, ok := obj[k]
		if !ok {
			return false
		}
		found := false
		for _, a := range allowed {
			if equalJSON(got, a) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func asObject(p json.RawMessage) (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal(p, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func equalJSON(a, b any) bool {
	if fa, ok := asFloat(a); ok {
		if fb, ok := asFloat(b); ok {
			return fa == fb
		}
	}
	sa, oka := a.(string)
	sb, okb := b.(string)
	if oka && okb {
		return sa == sb
	}
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ba) == string(bb)
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
