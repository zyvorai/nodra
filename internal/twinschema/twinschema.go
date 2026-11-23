// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

// Package twinschema validates device-twin documents against a small,
// strictly-checked JSON Schema subset and computes desired/reported drift and
// desired-state diffs. Unsupported keywords are rejected when a schema is
// registered instead of being silently ignored, so a schema never claims to
// enforce something it does not.
package twinschema

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"unicode/utf8"
)

// ReservedKeys are twin keys owned by Nodra subsystems (OTA lifecycle) and
// are skipped by drift detection: desired and reported shapes differ by design.
var ReservedKeys = map[string]bool{"ota": true}

const (
	maxDepth       = 16
	maxSchemaNodes = 2000
	maxViolations  = 100
)

var allowedKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "enum": true,
	"minimum": true, "maximum": true, "minLength": true, "maxLength": true, "items": true,
	"minItems": true, "maxItems": true, "description": true, "title": true,
}

var allowedTypes = map[string]bool{"object": true, "string": true, "number": true, "integer": true, "boolean": true, "array": true, "null": true}

// Violation is one validation failure at a JSON-pointer-like path.
type Violation struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// CheckSchema reports whether schema only uses the supported subset.
func CheckSchema(schema map[string]any) error {
	nodes := 0
	return checkSchema(schema, "", 0, &nodes)
}

func checkSchema(s map[string]any, path string, depth int, nodes *int) error {
	if depth > maxDepth {
		return fmt.Errorf("%s: schema nested deeper than %d", pathOrRoot(path), maxDepth)
	}
	if *nodes++; *nodes > maxSchemaNodes {
		return errors.New("schema too large")
	}
	for k := range s {
		if !allowedKeywords[k] {
			return fmt.Errorf("%s: unsupported schema keyword %q", pathOrRoot(path), k)
		}
	}
	if v, ok := s["type"]; ok {
		t, isStr := v.(string)
		if !isStr || !allowedTypes[t] {
			return fmt.Errorf("%s: invalid type %v", pathOrRoot(path), v)
		}
	}
	if v, ok := s["properties"]; ok {
		props, isMap := v.(map[string]any)
		if !isMap {
			return fmt.Errorf("%s: properties must be an object", pathOrRoot(path))
		}
		for name, sub := range props {
			m, isMap := sub.(map[string]any)
			if !isMap {
				return fmt.Errorf("%s/%s: property schema must be an object", path, name)
			}
			if err := checkSchema(m, path+"/"+name, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	if v, ok := s["items"]; ok {
		m, isMap := v.(map[string]any)
		if !isMap {
			return fmt.Errorf("%s: items must be an object", pathOrRoot(path))
		}
		if err := checkSchema(m, path+"/[]", depth+1, nodes); err != nil {
			return err
		}
	}
	if v, ok := s["required"]; ok {
		list, isList := v.([]any)
		if !isList {
			return fmt.Errorf("%s: required must be an array of strings", pathOrRoot(path))
		}
		for _, r := range list {
			if _, isStr := r.(string); !isStr {
				return fmt.Errorf("%s: required must be an array of strings", pathOrRoot(path))
			}
		}
	}
	if v, ok := s["additionalProperties"]; ok {
		if _, isBool := v.(bool); !isBool {
			return fmt.Errorf("%s: additionalProperties must be a boolean", pathOrRoot(path))
		}
	}
	if v, ok := s["enum"]; ok {
		if l, isList := v.([]any); !isList || len(l) == 0 {
			return fmt.Errorf("%s: enum must be a non-empty array", pathOrRoot(path))
		}
	}
	for _, k := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"} {
		if v, ok := s[k]; ok {
			f, isNum := toFloat(v)
			if !isNum || math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("%s: %s must be a number", pathOrRoot(path), k)
			}
			if k != "minimum" && k != "maximum" && f < 0 {
				return fmt.Errorf("%s: %s must not be negative", pathOrRoot(path), k)
			}
		}
	}
	return nil
}

func pathOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// Validate checks doc against schema and returns up to 100 violations.
// The schema is assumed to have passed CheckSchema.
func Validate(schema map[string]any, doc any) []Violation {
	var out []Violation
	validate(schema, doc, "", 0, &out)
	return out
}

func add(out *[]Violation, path, format string, args ...any) {
	if len(*out) < maxViolations {
		*out = append(*out, Violation{Path: pathOrRoot(path), Message: fmt.Sprintf(format, args...)})
	}
}

func typeOf(v any) string {
	switch n := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		if f, ok := toFloat(n); ok {
			if f == math.Trunc(f) && !math.IsInf(f, 0) {
				return "integer"
			}
			return "number"
		}
	}
	return "unknown"
}

func validate(s map[string]any, v any, path string, depth int, out *[]Violation) {
	if depth > maxDepth*2 || len(*out) >= maxViolations {
		return
	}
	if t, ok := s["type"].(string); ok {
		got := typeOf(v)
		if !(got == t || (t == "number" && got == "integer")) {
			add(out, path, "expected %s, got %s", t, got)
			return
		}
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if equalValues(e, v) {
				found = true
				break
			}
		}
		if !found {
			add(out, path, "value %v is not one of the allowed values", v)
		}
	}
	switch x := v.(type) {
	case string:
		n := utf8.RuneCountInString(x)
		if m, ok := toFloat(s["minLength"]); ok && float64(n) < m {
			add(out, path, "string shorter than minLength %g", m)
		}
		if m, ok := toFloat(s["maxLength"]); ok && float64(n) > m {
			add(out, path, "string longer than maxLength %g", m)
		}
	case []any:
		if m, ok := toFloat(s["minItems"]); ok && float64(len(x)) < m {
			add(out, path, "array has fewer than %g items", m)
		}
		if m, ok := toFloat(s["maxItems"]); ok && float64(len(x)) > m {
			add(out, path, "array has more than %g items", m)
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				validate(items, e, fmt.Sprintf("%s/%d", path, i), depth+1, out)
			}
		}
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				name, _ := r.(string)
				if _, present := x[name]; !present {
					add(out, path+"/"+name, "required property is missing")
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub, known := props[k].(map[string]any)
			if known {
				validate(sub, x[k], path+"/"+k, depth+1, out)
			} else if ap, ok := s["additionalProperties"].(bool); ok && !ap {
				add(out, path+"/"+k, "property is not allowed by the schema")
			}
		}
	default:
		if f, ok := toFloat(v); ok {
			if m, ok := toFloat(s["minimum"]); ok && f < m {
				add(out, path, "%g is below minimum %g", f, m)
			}
			if m, ok := toFloat(s["maximum"]); ok && f > m {
				add(out, path, "%g is above maximum %g", f, m)
			}
		}
	}
}

func equalValues(a, b any) bool {
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			return fa == fb
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

// Change is one leaf-level difference between two documents.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // add|remove|change
	From any    `json:"from,omitempty"`
	To   any    `json:"to,omitempty"`
}

// Diff lists leaf differences going from -> to, sorted by path. Objects are
// walked recursively; arrays and scalars are compared whole.
func Diff(from, to map[string]any) []Change {
	var out []Change
	diff(from, to, "", 0, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func diff(a, b map[string]any, path string, depth int, out *[]Change) {
	if depth > maxDepth*2 {
		return
	}
	for k, av := range a {
		p := path + "/" + k
		bv, ok := b[k]
		if !ok {
			*out = append(*out, Change{Path: p, Kind: "remove", From: av})
			continue
		}
		am, aok := av.(map[string]any)
		bm, bok := bv.(map[string]any)
		if aok && bok {
			diff(am, bm, p, depth+1, out)
		} else if !equalValues(av, bv) {
			*out = append(*out, Change{Path: p, Kind: "change", From: av, To: bv})
		}
	}
	for k, bv := range b {
		if _, ok := a[k]; !ok {
			*out = append(*out, Change{Path: path + "/" + k, Kind: "add", To: bv})
		}
	}
}

// DriftItem is one desired property the device has not (yet) reported.
type DriftItem struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"` // missing|mismatch
	Desired  any    `json:"desired"`
	Reported any    `json:"reported,omitempty"`
}

// Drift returns desired leaves that are missing from, or different in,
// reported. Properties only present in reported are not drift, and
// ReservedKeys are ignored at the top level.
func Drift(desired, reported map[string]any) []DriftItem {
	var out []DriftItem
	drift(desired, reported, "", 0, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func drift(d, r map[string]any, path string, depth int, out *[]DriftItem) {
	if depth > maxDepth*2 {
		return
	}
	for k, dv := range d {
		if depth == 0 && ReservedKeys[k] {
			continue
		}
		p := path + "/" + k
		rv, ok := r[k]
		if !ok {
			*out = append(*out, DriftItem{Path: p, Kind: "missing", Desired: dv})
			continue
		}
		dm, dok := dv.(map[string]any)
		rm, rok := rv.(map[string]any)
		switch {
		case dok && rok:
			drift(dm, rm, p, depth+1, out)
		case !equalValues(dv, rv):
			*out = append(*out, DriftItem{Path: p, Kind: "mismatch", Desired: dv, Reported: rv})
		}
	}
}
