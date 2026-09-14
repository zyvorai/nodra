// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"encoding/json"
	"testing"
)

func TestFilterDropsOutOfRange(t *testing.T) {
	f := &Filter{Exists: []string{"c"}, Min: map[string]float64{"c": 0}, Max: map[string]float64{"c": 80}}
	r, err := Apply("factory/line1/temp", json.RawMessage(`{"c":91}`), nil, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Keep {
		t.Fatal("expected drop")
	}
}

func TestTransformRewrites(t *testing.T) {
	tr := &Transform{
		SetHeaders:   map[string]string{"x-src": "nodra"},
		DropFields:   []string{"raw"},
		SetFields:    map[string]any{"unit": "C"},
		WrapAs:       "reading",
		TopicRewrite: "mes/{{topic}}",
	}
	r, err := Apply("factory/line1/temp", json.RawMessage(`{"c":31.2,"raw":"x"}`), map[string]string{"k": "v"}, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Keep || r.Topic != "mes/factory/line1/temp" || r.Headers["x-src"] != "nodra" {
		t.Fatalf("%+v", r)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Payload, &body); err != nil {
		t.Fatal(err)
	}
	inner, _ := body["reading"].(map[string]any)
	if inner["c"].(float64) != 31.2 || inner["unit"] != "C" {
		t.Fatalf("body=%v", body)
	}
	if _, ok := inner["raw"]; ok {
		t.Fatal("raw should be dropped")
	}
}

func TestFilterInAndEquals(t *testing.T) {
	f := &Filter{Equals: map[string]any{"status": "ok"}, In: map[string][]any{"line": {"1", "2"}}}
	r, err := Apply("t", json.RawMessage(`{"status":"ok","line":"1"}`), nil, f, nil)
	if err != nil || !r.Keep {
		t.Fatalf("keep=%v err=%v", r.Keep, err)
	}
	r, err = Apply("t", json.RawMessage(`{"status":"ok","line":"9"}`), nil, f, nil)
	if err != nil || r.Keep {
		t.Fatalf("expected drop line=9")
	}
}
