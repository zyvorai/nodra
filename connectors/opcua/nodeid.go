// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// NodeID identifies one OPC-UA node, in either numeric ("ns=2;i=1001") or
// string ("ns=2;s=Temperature") form.
type NodeID struct {
	Namespace uint16
	Numeric   uint32
	StringID  string
	IsString  bool
}

// ParseNodeID parses the "ns=<n>;i=<id>" or "ns=<n>;s=<string>" forms.
func ParseNodeID(s string) (NodeID, error) {
	var ns uint64
	var identPart string
	for _, p := range strings.Split(s, ";") {
		switch {
		case strings.HasPrefix(p, "ns="):
			n, err := strconv.ParseUint(strings.TrimPrefix(p, "ns="), 10, 16)
			if err != nil {
				return NodeID{}, fmt.Errorf("invalid node id %q: bad namespace: %w", s, err)
			}
			ns = n
		case strings.HasPrefix(p, "i="), strings.HasPrefix(p, "s="):
			identPart = p
		}
	}
	if identPart == "" {
		return NodeID{}, fmt.Errorf("invalid node id %q: expected ns=<n>;i=<id> or ns=<n>;s=<string>", s)
	}
	if v, ok := strings.CutPrefix(identPart, "i="); ok {
		id, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return NodeID{}, fmt.Errorf("invalid node id %q: bad numeric identifier: %w", s, err)
		}
		return NodeID{Namespace: uint16(ns), Numeric: uint32(id)}, nil
	}
	v := strings.TrimPrefix(identPart, "s=")
	if v == "" {
		return NodeID{}, fmt.Errorf("invalid node id %q: empty string identifier", s)
	}
	return NodeID{Namespace: uint16(ns), StringID: v, IsString: true}, nil
}

func (n NodeID) String() string {
	if n.IsString {
		return fmt.Sprintf("ns=%d;s=%s", n.Namespace, n.StringID)
	}
	return fmt.Sprintf("ns=%d;i=%d", n.Namespace, n.Numeric)
}

// encode produces the OPC-UA binary NodeId encoding, choosing the smallest
// form that fits (two-byte, four-byte, numeric, or string).
func (n NodeID) encode() []byte {
	buf := &bytes.Buffer{}
	if n.IsString {
		buf.WriteByte(0x03)
		writeUint16(buf, n.Namespace)
		writeString(buf, n.StringID, false)
		return buf.Bytes()
	}
	switch {
	case n.Namespace == 0 && n.Numeric <= 255:
		buf.WriteByte(0x00)
		buf.WriteByte(byte(n.Numeric))
	case n.Namespace <= 255 && n.Numeric <= 65535:
		buf.WriteByte(0x01)
		buf.WriteByte(byte(n.Namespace))
		writeUint16(buf, uint16(n.Numeric))
	default:
		buf.WriteByte(0x02)
		writeUint16(buf, n.Namespace)
		writeUint32(buf, n.Numeric)
	}
	return buf.Bytes()
}

// decodeNodeID decodes any NodeId binary form; used by tests to verify
// encode()'s output round-trips to the same value.
func decodeNodeID(b []byte) (NodeID, int, error) {
	if len(b) < 2 {
		return NodeID{}, 0, fmt.Errorf("short NodeId")
	}
	switch b[0] {
	case 0x00:
		return NodeID{Namespace: 0, Numeric: uint32(b[1])}, 2, nil
	case 0x01:
		if len(b) < 4 {
			return NodeID{}, 0, fmt.Errorf("short NodeId")
		}
		return NodeID{Namespace: uint16(b[1]), Numeric: uint32(binary.LittleEndian.Uint16(b[2:4]))}, 4, nil
	case 0x02:
		if len(b) < 7 {
			return NodeID{}, 0, fmt.Errorf("short NodeId")
		}
		return NodeID{Namespace: binary.LittleEndian.Uint16(b[1:3]), Numeric: binary.LittleEndian.Uint32(b[3:7])}, 7, nil
	case 0x03:
		if len(b) < 7 {
			return NodeID{}, 0, fmt.Errorf("short NodeId")
		}
		ns := binary.LittleEndian.Uint16(b[1:3])
		n := int32(binary.LittleEndian.Uint32(b[3:7]))
		if n < 0 {
			return NodeID{Namespace: ns, IsString: true}, 7, nil
		}
		end := 7 + int(n)
		if len(b) < end {
			return NodeID{}, 0, fmt.Errorf("short NodeId string")
		}
		return NodeID{Namespace: ns, StringID: string(b[7:end]), IsString: true}, end, nil
	default:
		return NodeID{}, 0, fmt.Errorf("unsupported NodeId encoding 0x%02x", b[0])
	}
}
