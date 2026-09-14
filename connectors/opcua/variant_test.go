// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func decodeOneVariant(t *testing.T, b []byte) any {
	t.Helper()
	r := &reader{b: b}
	v, err := decodeVariant(r)
	if err != nil {
		t.Fatalf("decodeVariant: %v", err)
	}
	if r.off != len(b) {
		t.Fatalf("consumed %d of %d bytes", r.off, len(b))
	}
	return v
}

func TestDecodeVariantBoolean(t *testing.T) {
	if v := decodeOneVariant(t, []byte{0x01, 0x01}); v != true {
		t.Fatalf("%v", v)
	}
	if v := decodeOneVariant(t, []byte{0x01, 0x00}); v != false {
		t.Fatalf("%v", v)
	}
}

func TestDecodeVariantInt32(t *testing.T) {
	b := make([]byte, 5)
	b[0] = 0x06
	var want int32 = -42
	binary.LittleEndian.PutUint32(b[1:], uint32(want))
	v := decodeOneVariant(t, b)
	if v != int32(-42) {
		t.Fatalf("%v (%T)", v, v)
	}
}

func TestDecodeVariantDouble(t *testing.T) {
	b := make([]byte, 9)
	b[0] = 0x0B
	binary.LittleEndian.PutUint64(b[1:], math.Float64bits(31.25))
	v := decodeOneVariant(t, b)
	if v != 31.25 {
		t.Fatalf("%v", v)
	}
}

func TestDecodeVariantString(t *testing.T) {
	s := "hello"
	b := make([]byte, 1+4+len(s))
	b[0] = 0x0C
	binary.LittleEndian.PutUint32(b[1:5], uint32(len(s)))
	copy(b[5:], s)
	v := decodeOneVariant(t, b)
	if v != s {
		t.Fatalf("%v", v)
	}
}

func TestDecodeVariantArrayRejected(t *testing.T) {
	r := &reader{b: []byte{0x86, 0x00, 0x00, 0x00, 0x00}} // array flag | Int32
	if _, err := decodeVariant(r); err == nil {
		t.Fatal("expected array variant to be rejected")
	}
}

func TestDecodeVariantUnsupportedTypeRejected(t *testing.T) {
	r := &reader{b: []byte{17, 0x00, 0x00}} // NodeId (type id 17), not supported
	if _, err := decodeVariant(r); err == nil {
		t.Fatal("expected unsupported variant type to be rejected")
	}
}

func TestDecodeDataValueFieldOrderAndOffsets(t *testing.T) {
	// mask: Value(0x01) | StatusCode(0x02) | SourceTimestamp(0x04) |
	// SourcePicoseconds(0x10) | ServerTimestamp(0x08) | ServerPicoseconds(0x20)
	buf := []byte{0x3F}
	// Value: Int32 = 7
	v := make([]byte, 5)
	v[0] = 0x06
	binary.LittleEndian.PutUint32(v[1:], 7)
	buf = append(buf, v...)
	// StatusCode
	sc := make([]byte, 4)
	binary.LittleEndian.PutUint32(sc, 0)
	buf = append(buf, sc...)
	// SourceTimestamp: a known instant
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ticks := want.UnixNano()/100 + uaEpochOffsetTicks
	ts := make([]byte, 8)
	binary.LittleEndian.PutUint64(ts, uint64(ticks))
	buf = append(buf, ts...)
	// SourcePicoseconds
	buf = append(buf, 0x00, 0x00)
	// ServerTimestamp (different instant, must be skipped correctly)
	buf = append(buf, ts...)
	// ServerPicoseconds
	buf = append(buf, 0x00, 0x00)
	// sentinel byte marking where the next field in a real response would start
	buf = append(buf, 0xAA)

	r := &reader{b: buf}
	dv, err := decodeDataValue(r)
	if err != nil {
		t.Fatal(err)
	}
	if dv.Value != int32(7) {
		t.Fatalf("value=%v", dv.Value)
	}
	if dv.SourceTimestamp == nil || !dv.SourceTimestamp.Equal(want) {
		t.Fatalf("source timestamp=%v want=%v", dv.SourceTimestamp, want)
	}
	if r.off != len(buf)-1 {
		t.Fatalf("offset after decode=%d, want %d (sentinel not reached)", r.off, len(buf)-1)
	}
	if r.b[r.off] != 0xAA {
		t.Fatalf("misaligned: next byte=0x%02x, want 0xAA", r.b[r.off])
	}
}

func TestDecodeDataValueNoOptionalFields(t *testing.T) {
	r := &reader{b: []byte{0x00}}
	dv, err := decodeDataValue(r)
	if err != nil {
		t.Fatal(err)
	}
	if dv.Value != nil || dv.StatusCode != 0 || dv.SourceTimestamp != nil {
		t.Fatalf("%+v", dv)
	}
}
