// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package twinschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func js(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const schemaJSON = `{
 "type":"object","required":["mode"],"additionalProperties":false,
 "properties":{
  "mode":{"type":"string","enum":["auto","manual"]},
  "setpoint":{"type":"number","minimum":0,"maximum":100},
  "retries":{"type":"integer","minimum":0},
  "name":{"type":"string","minLength":2,"maxLength":5},
  "tags":{"type":"array","maxItems":2,"items":{"type":"string"}},
  "net":{"type":"object","properties":{"dhcp":{"type":"boolean"}}}
 }}`

func TestValidate(t *testing.T) {
	schema := js(t, schemaJSON)
	if err := CheckSchema(schema); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		doc  string
		want []string // violation paths
	}{
		{"valid", `{"mode":"auto","setpoint":50,"retries":3,"name":"abc","tags":["a"],"net":{"dhcp":true}}`, nil},
		{"missing required", `{}`, []string{"/mode"}},
		{"bad enum", `{"mode":"x"}`, []string{"/mode"}},
		{"range", `{"mode":"auto","setpoint":101}`, []string{"/setpoint"}},
		{"below min", `{"mode":"auto","setpoint":-0.5}`, []string{"/setpoint"}},
		{"integer given float", `{"mode":"auto","retries":1.5}`, []string{"/retries"}},
		{"integer accepts 2.0", `{"mode":"auto","retries":2.0}`, nil},
		{"string length", `{"mode":"auto","name":"a"}`, []string{"/name"}},
		{"unicode length counts runes", `{"mode":"auto","name":"日本"}`, nil},
		{"extra property", `{"mode":"auto","zzz":1}`, []string{"/zzz"}},
		{"array too long and bad item", `{"mode":"auto","tags":["a","b",3]}`, []string{"/tags", "/tags/2"}},
		{"wrong type nested", `{"mode":"auto","net":{"dhcp":"yes"}}`, []string{"/net/dhcp"}},
		{"null where string", `{"mode":null}`, []string{"/mode"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, v := range Validate(schema, js(t, tc.doc)) {
				got = append(got, v.Path)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("violations %v want %v", got, tc.want)
			}
		})
	}
}

func TestCheckSchemaRejects(t *testing.T) {
	bad := map[string]string{
		"unsupported ref":      `{"$ref":"#/x"}`,
		"unsupported oneOf":    `{"oneOf":[]}`,
		"unsupported pattern":  `{"properties":{"a":{"pattern":"^x"}}}`,
		"bad type":             `{"type":"float"}`,
		"type not string":      `{"type":1}`,
		"properties not map":   `{"properties":[]}`,
		"property not object":  `{"properties":{"a":true}}`,
		"required not strings": `{"required":[1]}`,
		"required not array":   `{"required":"a"}`,
		"addl not bool":        `{"additionalProperties":{}}`,
		"empty enum":           `{"enum":[]}`,
		"minLength string":     `{"minLength":"3"}`,
		"negative maxItems":    `{"maxItems":-1}`,
		"items not object":     `{"items":[]}`,
	}
	for name, s := range bad {
		if err := CheckSchema(js(t, s)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// depth bomb
	deep := `{"type":"object"}`
	for i := 0; i < maxDepth+3; i++ {
		deep = `{"properties":{"a":` + deep + `}}`
	}
	if err := CheckSchema(js(t, deep)); err == nil {
		t.Error("depth bomb accepted")
	}
	// node bomb
	var props []string
	for i := 0; i < maxSchemaNodes+1; i++ {
		props = append(props, `"p`+strings.Repeat("x", 1)+string(rune('a'+i%26))+itoa(i)+`":{}`)
	}
	if err := CheckSchema(js(t, `{"properties":{`+strings.Join(props, ",")+`}}`)); err == nil {
		t.Error("node bomb accepted")
	}
	if err := CheckSchema(map[string]any{}); err != nil {
		t.Errorf("empty schema should be valid: %v", err)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestViolationCap(t *testing.T) {
	schema := js(t, `{"type":"array","items":{"type":"string"}}`)
	doc := make([]any, 1000)
	for i := range doc {
		doc[i] = 1.0
	}
	if got := len(Validate(schema, doc)); got != maxViolations {
		t.Fatalf("violations = %d, want cap %d", got, maxViolations)
	}
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		want     []Change
	}{
		{"identical", `{"a":1,"b":{"c":2}}`, `{"a":1,"b":{"c":2}}`, nil},
		{"add remove change", `{"a":1,"gone":true}`, `{"a":2,"new":"x"}`, []Change{
			{Path: "/a", Kind: "change", From: 1.0, To: 2.0},
			{Path: "/gone", Kind: "remove", From: true},
			{Path: "/new", Kind: "add", To: "x"}}},
		{"nested", `{"n":{"x":1,"y":2}}`, `{"n":{"x":1,"y":3}}`, []Change{{Path: "/n/y", Kind: "change", From: 2.0, To: 3.0}}},
		{"type change object to scalar", `{"n":{"x":1}}`, `{"n":5}`, []Change{{Path: "/n", Kind: "change", From: map[string]any{"x": 1.0}, To: 5.0}}},
		{"arrays compared whole", `{"l":[1,2]}`, `{"l":[1,3]}`, []Change{{Path: "/l", Kind: "change", From: []any{1.0, 2.0}, To: []any{1.0, 3.0}}}},
		{"nil from", `{}`, `{"a":1}`, []Change{{Path: "/a", Kind: "add", To: 1.0}}},
	}
	for _, tc := range tests {
		got := Diff(js(t, tc.from), js(t, tc.to))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	if got := Diff(nil, nil); got != nil {
		t.Errorf("nil diff: %v", got)
	}
}

func TestDrift(t *testing.T) {
	tests := []struct {
		name              string
		desired, reported string
		want              []DriftItem
	}{
		{"in sync", `{"a":1,"n":{"x":true}}`, `{"a":1,"n":{"x":true},"extra":9}`, nil},
		{"missing and mismatch", `{"a":1,"b":2,"n":{"x":true}}`, `{"a":1,"b":3,"n":{}}`, []DriftItem{
			{Path: "/b", Kind: "mismatch", Desired: 2.0, Reported: 3.0},
			{Path: "/n/x", Kind: "missing", Desired: true}}},
		{"ota key ignored", `{"ota":{"version":"2"},"a":1}`, `{"a":1}`, nil},
		{"desired object but reported scalar", `{"n":{"x":1}}`, `{"n":3}`, []DriftItem{{Path: "/n", Kind: "mismatch", Desired: map[string]any{"x": 1.0}, Reported: 3.0}}},
		{"nothing reported", `{"a":1}`, `{}`, []DriftItem{{Path: "/a", Kind: "missing", Desired: 1.0}}},
		{"empty desired", `{}`, `{"a":1}`, nil},
	}
	for _, tc := range tests {
		got := Drift(js(t, tc.desired), js(t, tc.reported))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	if got := Drift(nil, nil); got != nil {
		t.Errorf("nil drift: %v", got)
	}
}

func TestDeepDocumentsDoNotRecurseForever(t *testing.T) {
	deep := map[string]any{}
	cur := deep
	for i := 0; i < 5000; i++ {
		next := map[string]any{}
		cur["n"] = next
		cur = next
	}
	_ = Drift(deep, deep)
	_ = Diff(deep, map[string]any{})
	_ = Validate(map[string]any{"type": "object"}, deep)
}
