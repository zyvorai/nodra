// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import "testing"

func TestParseNodeIDNumeric(t *testing.T) {
	id, err := ParseNodeID("ns=2;i=1001")
	if err != nil {
		t.Fatal(err)
	}
	if id.Namespace != 2 || id.Numeric != 1001 || id.IsString {
		t.Fatalf("%+v", id)
	}
	if id.String() != "ns=2;i=1001" {
		t.Fatalf("String()=%q", id.String())
	}
}

func TestParseNodeIDString(t *testing.T) {
	id, err := ParseNodeID("ns=2;s=Temperature")
	if err != nil {
		t.Fatal(err)
	}
	if id.Namespace != 2 || !id.IsString || id.StringID != "Temperature" {
		t.Fatalf("%+v", id)
	}
	if id.String() != "ns=2;s=Temperature" {
		t.Fatalf("String()=%q", id.String())
	}
}

func TestParseNodeIDRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "ns=2", "ns=2;s=", "ns=abc;i=1"} {
		if _, err := ParseNodeID(s); err == nil {
			t.Fatalf("expected error for %q", s)
		}
	}
}

func TestParseNodeIDDefaultsNamespaceZero(t *testing.T) {
	id, err := ParseNodeID("i=5")
	if err != nil {
		t.Fatal(err)
	}
	if id.Namespace != 0 || id.Numeric != 5 || id.IsString {
		t.Fatalf("%+v", id)
	}
}

func TestNodeIDEncodeDecodeRoundTrip(t *testing.T) {
	cases := []NodeID{
		{Namespace: 0, Numeric: 5},        // two-byte form
		{Namespace: 2, Numeric: 1001},     // four-byte form
		{Namespace: 300, Numeric: 100000}, // numeric form (ns and id both large)
		{Namespace: 2, StringID: "Temperature", IsString: true},
	}
	for _, want := range cases {
		enc := want.encode()
		got, n, err := decodeNodeID(enc)
		if err != nil {
			t.Fatalf("decode(%+v): %v", want, err)
		}
		if n != len(enc) {
			t.Fatalf("decode(%+v): consumed %d, want %d", want, n, len(enc))
		}
		if got.Namespace != want.Namespace || got.IsString != want.IsString ||
			got.Numeric != want.Numeric || got.StringID != want.StringID {
			t.Fatalf("round-trip mismatch: want %+v got %+v", want, got)
		}
	}
}

func TestNodeIDEncodeChoosesSmallestForm(t *testing.T) {
	if enc := (NodeID{Namespace: 0, Numeric: 5}).encode(); enc[0] != 0x00 {
		t.Fatalf("want two-byte form, got encoding 0x%02x", enc[0])
	}
	if enc := (NodeID{Namespace: 2, Numeric: 1001}).encode(); enc[0] != 0x01 {
		t.Fatalf("want four-byte form, got encoding 0x%02x", enc[0])
	}
	if enc := (NodeID{Namespace: 2, Numeric: 100000}).encode(); enc[0] != 0x02 {
		t.Fatalf("want numeric form, got encoding 0x%02x", enc[0])
	}
	if enc := (NodeID{Namespace: 2, StringID: "x", IsString: true}).encode(); enc[0] != 0x03 {
		t.Fatalf("want string form, got encoding 0x%02x", enc[0])
	}
}
