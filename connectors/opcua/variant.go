// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"fmt"
	"math"
	"time"
)

// DataValue is one decoded Read result: the scalar Go value (see
// decodeVariant for the supported type set), its StatusCode, and its
// SourceTimestamp when the server returned one.
type DataValue struct {
	Value           any
	StatusCode      uint32
	SourceTimestamp *time.Time
}

// decodeVariant decodes a scalar Boolean/Int16/UInt16/Int32/UInt32/Int64/
// UInt64/Float/Double/String/DateTime Variant. Array values and any other
// built-in type are rejected: v1 only supports scalar telemetry reads, and
// bailing out here (rather than guessing a byte length) keeps the reader
// offset from silently corrupting the rest of the response.
func decodeVariant(r *reader) (any, error) {
	mask := r.u8()
	if r.err != nil {
		return nil, r.err
	}
	if mask&0x80 != 0 {
		return nil, fmt.Errorf("array variants are not supported in v1 (type mask 0x%02x)", mask)
	}
	switch mask & 0x3F {
	case 0:
		return nil, nil // Null
	case 1:
		return r.u8() != 0, nil // Boolean
	case 4:
		return int16(r.u16()), nil // Int16
	case 5:
		return r.u16(), nil // UInt16
	case 6:
		return r.i32(), nil // Int32
	case 7:
		return r.u32(), nil // UInt32
	case 8:
		return r.i64(), nil // Int64
	case 9:
		return r.u64(), nil // UInt64
	case 10:
		return r.f32(), nil // Float
	case 11:
		return r.f64(), nil // Double
	case 12:
		return r.str(), nil // String
	case 13:
		return decodeDateTime(r.i64()), nil // DateTime
	default:
		return nil, fmt.Errorf("unsupported variant type id %d", mask&0x3F)
	}
}

// encodeVariant encodes v as a scalar Variant, the encode-side counterpart
// to decodeVariant. Supports exactly the same scalar type set decodeVariant
// decodes (bool, (u)int16/32/64, float32/64, string) — anything else is
// rejected with a clear error rather than guessed, mirroring decodeVariant's
// own rejection of array variants.
func encodeVariant(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case bool:
		buf.WriteByte(1)
		if x {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	case int16:
		buf.WriteByte(4)
		writeUint16(buf, uint16(x))
	case uint16:
		buf.WriteByte(5)
		writeUint16(buf, x)
	case int32:
		buf.WriteByte(6)
		writeInt32(buf, x)
	case uint32:
		buf.WriteByte(7)
		writeUint32(buf, x)
	case int64:
		buf.WriteByte(8)
		writeInt64(buf, x)
	case uint64:
		buf.WriteByte(9)
		writeUint64(buf, x)
	case float32:
		buf.WriteByte(10)
		writeUint32(buf, math.Float32bits(x))
	case float64:
		buf.WriteByte(11)
		writeFloat64(buf, x)
	case string:
		buf.WriteByte(12)
		writeString(buf, x, false)
	default:
		return fmt.Errorf("encodeVariant: unsupported Go type %T for OPC-UA scalar write", v)
	}
	return nil
}

// encodeDataValueForWrite encodes a DataValue with only the Value field set
// (mask 0x01) — Write conventionally omits timestamps.
func encodeDataValueForWrite(buf *bytes.Buffer, v any) error {
	buf.WriteByte(0x01)
	return encodeVariant(buf, v)
}

// decodeDataValue decodes one DataValue per Part 6 §5.2.2.17: an encoding
// mask byte followed by whichever of Value/StatusCode/SourceTimestamp/
// SourcePicoseconds/ServerTimestamp/ServerPicoseconds it flags, in that
// field order. Fields we don't expose (Server*) are still read, in order,
// to keep the offset correct for whatever follows in the response.
func decodeDataValue(r *reader) (DataValue, error) {
	var dv DataValue
	mask := r.u8()
	if r.err != nil {
		return dv, r.err
	}
	if mask&0x01 != 0 {
		v, err := decodeVariant(r)
		if err != nil {
			return dv, err
		}
		dv.Value = v
	}
	if mask&0x02 != 0 {
		dv.StatusCode = r.u32()
	}
	if mask&0x04 != 0 {
		ts := decodeDateTime(r.i64())
		dv.SourceTimestamp = &ts
	}
	if mask&0x10 != 0 {
		r.u16() // SourcePicoseconds
	}
	if mask&0x08 != 0 {
		r.i64() // ServerTimestamp
	}
	if mask&0x20 != 0 {
		r.u16() // ServerPicoseconds
	}
	if r.err != nil {
		return dv, r.err
	}
	return dv, nil
}
