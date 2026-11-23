// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

// Package sparkplug decodes Sparkplug B (spBv1.0) MQTT topics and payloads
// with the standard library only, and tracks edge-node/device birth and
// death state. Payload bytes come from the wire and are hostile: every
// length is bounds-checked, nesting is not followed, and counts are capped.
package sparkplug

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

const Namespace = "spBv1.0"

// Limits bound work done for one payload.
const (
	MaxPayloadBytes = 1 << 20
	MaxMetrics      = 4096
	MaxStringBytes  = 64 << 10
)

// Message types defined by the Sparkplug B topic namespace.
const (
	NBIRTH = "NBIRTH"
	NDEATH = "NDEATH"
	DBIRTH = "DBIRTH"
	DDEATH = "DDEATH"
	NDATA  = "NDATA"
	DDATA  = "DDATA"
	NCMD   = "NCMD"
	DCMD   = "DCMD"
	STATE  = "STATE"
)

var validTypes = map[string]bool{NBIRTH: true, NDEATH: true, DBIRTH: true, DDEATH: true, NDATA: true, DDATA: true, NCMD: true, DCMD: true}

// Topic is a parsed Sparkplug topic.
type Topic struct {
	Group  string
	Type   string
	Node   string
	Device string // empty for node-level messages
	Host   string // only for STATE topics
}

// IsSparkplug reports whether topic is in the spBv1.0 namespace.
func IsSparkplug(topic string) bool {
	return topic == Namespace || strings.HasPrefix(topic, Namespace+"/")
}

// ParseTopic parses spBv1.0/<group>/<type>/<node>[/<device>] and
// spBv1.0/STATE/<host>. Wildcards, empty elements and type/device mismatches
// (D* needs a device, N* must not have one) are rejected.
func ParseTopic(topic string) (Topic, error) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || parts[0] != Namespace {
		return Topic{}, errors.New("not a sparkplug topic")
	}
	for _, p := range parts {
		if p == "" || strings.ContainsAny(p, "+#\x00") {
			return Topic{}, errors.New("invalid sparkplug topic element")
		}
	}
	if parts[1] == STATE {
		if len(parts) != 3 {
			return Topic{}, errors.New("STATE topic must be spBv1.0/STATE/<host>")
		}
		return Topic{Type: STATE, Host: parts[2]}, nil
	}
	if len(parts) != 4 && len(parts) != 5 {
		return Topic{}, errors.New("sparkplug topic needs group/type/node[/device]")
	}
	t := Topic{Group: parts[1], Type: parts[2], Node: parts[3]}
	if !validTypes[t.Type] {
		return Topic{}, fmt.Errorf("unknown sparkplug message type %q", t.Type)
	}
	if len(parts) == 5 {
		t.Device = parts[4]
	}
	deviceType := strings.HasPrefix(t.Type, "D")
	if deviceType && t.Device == "" {
		return Topic{}, fmt.Errorf("%s requires a device id", t.Type)
	}
	if !deviceType && t.Device != "" {
		return Topic{}, fmt.Errorf("%s must not carry a device id", t.Type)
	}
	return t, nil
}

// Metric is one decoded Sparkplug metric. Value is nil for null metrics and
// for datatypes this decoder does not expand (datasets, templates, bytes
// are reported as base-less placeholders via Unsupported).
type Metric struct {
	Name        string `json:"name,omitempty"`
	Alias       uint64 `json:"alias,omitempty"`
	HasAlias    bool   `json:"-"`
	Timestamp   uint64 `json:"timestamp,omitempty"`
	DataType    uint32 `json:"datatype"`
	Value       any    `json:"value,omitempty"`
	IsNull      bool   `json:"is_null,omitempty"`
	Historical  bool   `json:"is_historical,omitempty"`
	Transient   bool   `json:"is_transient,omitempty"`
	Unsupported bool   `json:"unsupported,omitempty"`
}

// Payload is a decoded org.eclipse.tahu.protobuf.Payload.
type Payload struct {
	Timestamp uint64   `json:"timestamp,omitempty"`
	Seq       uint64   `json:"seq"`
	HasSeq    bool     `json:"-"`
	UUID      string   `json:"uuid,omitempty"`
	Metrics   []Metric `json:"metrics"`
}

// BdSeq returns the value of the "bdSeq" metric carried by NBIRTH/NDEATH.
func (p Payload) BdSeq() (uint64, bool) {
	for _, m := range p.Metrics {
		if m.Name == "bdSeq" {
			switch v := m.Value.(type) {
			case uint64:
				return v, true
			case int64:
				if v >= 0 {
					return uint64(v), true
				}
			}
		}
	}
	return 0, false
}

type reader struct {
	b []byte
	i int
}

var errTrunc = errors.New("sparkplug: truncated or malformed protobuf")

func (r *reader) done() bool { return r.i >= len(r.b) }
func (r *reader) varint() (uint64, error) {
	var v uint64
	for s := uint(0); s < 70; s += 7 {
		if r.i >= len(r.b) {
			return 0, errTrunc
		}
		c := r.b[r.i]
		r.i++
		if s == 63 && c > 1 {
			return 0, errors.New("sparkplug: varint overflow")
		}
		v |= uint64(c&0x7f) << s
		if c < 0x80 {
			return v, nil
		}
	}
	return 0, errors.New("sparkplug: varint too long")
}
func (r *reader) bytes() ([]byte, error) {
	n, err := r.varint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)-r.i) {
		return nil, errTrunc
	}
	out := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return out, nil
}
func (r *reader) fixed(n int) ([]byte, error) {
	if len(r.b)-r.i < n {
		return nil, errTrunc
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out, nil
}

// skip consumes a field value of the given wire type.
func (r *reader) skip(wt uint64) error {
	switch wt {
	case 0:
		_, err := r.varint()
		return err
	case 1:
		_, err := r.fixed(8)
		return err
	case 2:
		_, err := r.bytes()
		return err
	case 5:
		_, err := r.fixed(4)
		return err
	}
	return fmt.Errorf("sparkplug: unsupported wire type %d", wt)
}
func (r *reader) tag() (field, wt uint64, err error) {
	t, err := r.varint()
	if err != nil {
		return 0, 0, err
	}
	if t>>3 == 0 {
		return 0, 0, errors.New("sparkplug: field number 0")
	}
	return t >> 3, t & 7, nil
}

// DecodePayload decodes a Sparkplug B protobuf payload.
func DecodePayload(b []byte) (Payload, error) {
	var p Payload
	if len(b) > MaxPayloadBytes {
		return p, errors.New("sparkplug: payload too large")
	}
	r := &reader{b: b}
	for !r.done() {
		f, wt, err := r.tag()
		if err != nil {
			return p, err
		}
		switch {
		case f == 1 && wt == 0:
			p.Timestamp, err = r.varint()
		case f == 3 && wt == 0:
			p.Seq, err = r.varint()
			p.HasSeq = true
		case f == 4 && wt == 2:
			var s []byte
			if s, err = r.bytes(); err == nil && len(s) <= MaxStringBytes {
				p.UUID = string(s)
			}
		case f == 2 && wt == 2:
			if len(p.Metrics) >= MaxMetrics {
				return p, errors.New("sparkplug: too many metrics")
			}
			var s []byte
			if s, err = r.bytes(); err == nil {
				var m Metric
				if m, err = decodeMetric(s); err == nil {
					p.Metrics = append(p.Metrics, m)
				}
			}
		default:
			err = r.skip(wt)
		}
		if err != nil {
			return Payload{}, err
		}
	}
	return p, nil
}

// Sparkplug datatype ids used for value interpretation.
const (
	tInt8, tInt16, tInt32, tInt64      = 1, 2, 3, 4
	tUInt8, tUInt16, tUInt32, tUInt64  = 5, 6, 7, 8
	tFloat, tDouble, tBoolean, tString = 9, 10, 11, 12
	tDateTime, tText                   = 13, 14
)

func decodeMetric(b []byte) (Metric, error) {
	var m Metric
	r := &reader{b: b}
	var raw uint64
	var rawSet bool
	var rawKind int // 1 varint(int/long/bool), 2 float32, 3 float64, 4 string
	var str string
	for !r.done() {
		f, wt, err := r.tag()
		if err != nil {
			return m, err
		}
		switch {
		case f == 1 && wt == 2:
			var s []byte
			if s, err = r.bytes(); err == nil {
				if len(s) > MaxStringBytes {
					return m, errors.New("sparkplug: metric name too long")
				}
				m.Name = string(s)
			}
		case f == 2 && wt == 0:
			m.Alias, err = r.varint()
			m.HasAlias = true
		case f == 3 && wt == 0:
			m.Timestamp, err = r.varint()
		case f == 4 && wt == 0:
			var v uint64
			v, err = r.varint()
			m.DataType = uint32(v)
		case f == 5 && wt == 0:
			var v uint64
			v, err = r.varint()
			m.Historical = v != 0
		case f == 6 && wt == 0:
			var v uint64
			v, err = r.varint()
			m.Transient = v != 0
		case f == 7 && wt == 0:
			var v uint64
			v, err = r.varint()
			m.IsNull = v != 0
		case (f == 10 || f == 11 || f == 14) && wt == 0:
			raw, err = r.varint()
			rawSet, rawKind = true, 1
		case f == 12 && wt == 5:
			var s []byte
			if s, err = r.fixed(4); err == nil {
				raw, rawSet, rawKind = uint64(binary.LittleEndian.Uint32(s)), true, 2
			}
		case f == 13 && wt == 1:
			var s []byte
			if s, err = r.fixed(8); err == nil {
				raw, rawSet, rawKind = binary.LittleEndian.Uint64(s), true, 3
			}
		case f == 15 && wt == 2:
			var s []byte
			if s, err = r.bytes(); err == nil {
				if len(s) > MaxStringBytes {
					return m, errors.New("sparkplug: string value too long")
				}
				str, rawSet, rawKind = string(s), true, 4
			}
		case f == 16 || f == 17 || f == 18 || f == 19 || f == 8 || f == 9:
			// bytes, dataset, template, extension, metadata, properties
			if f >= 16 && f <= 19 {
				m.Unsupported = true
			}
			err = r.skip(wt)
		default:
			err = r.skip(wt)
		}
		if err != nil {
			return m, err
		}
	}
	if m.IsNull || !rawSet {
		return m, nil
	}
	m.Value = interpret(m.DataType, rawKind, raw, str)
	return m, nil
}

func interpret(dt uint32, kind int, raw uint64, s string) any {
	switch kind {
	case 4:
		return s
	case 2:
		f := float64(math.Float32frombits(uint32(raw)))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil // JSON cannot carry NaN/Inf
		}
		return f
	case 3:
		f := math.Float64frombits(raw)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil
		}
		return f
	}
	switch dt {
	case tInt8:
		return int64(int8(raw))
	case tInt16:
		return int64(int16(raw))
	case tInt32:
		return int64(int32(raw))
	case tInt64:
		return int64(raw)
	case tBoolean:
		return raw != 0
	case tUInt8, tUInt16, tUInt32, tUInt64, tDateTime:
		return raw
	}
	return raw
}
