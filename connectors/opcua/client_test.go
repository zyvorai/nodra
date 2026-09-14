// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

// --- mock UA-TCP server helpers ---
// The mock never decodes request bodies; the client sends CreateSession,
// ActivateSession and Read in a fixed order, so the mock just answers each
// in turn according to a predetermined script. This is the integration
// test for OPC-UA: unlike Modbus RTU's socat/pty harness, OPC-UA is
// TCP end-to-end, so a mock listener exercises the real wire format.

func mockResponseHeader(status uint32) []byte {
	buf := &bytes.Buffer{}
	writeInt64(buf, nowUA())
	writeUint32(buf, 1)
	writeUint32(buf, status)
	buf.WriteByte(0x00)        // DiagnosticInfo: none
	writeInt32(buf, 0)         // StringTable: empty
	buf.Write([]byte{0, 0, 0}) // AdditionalHeader: null ExtensionObject
	return buf.Bytes()
}

func mockWriteOPN(t *testing.T, conn net.Conn, channelID, tokenID, seqNum, reqID uint32) {
	t.Helper()
	buf := &bytes.Buffer{}
	writeUint32(buf, channelID)
	writeString(buf, securityPolicyNone, false)
	writeByteString(buf, nil, true)
	writeByteString(buf, nil, true)
	writeUint32(buf, seqNum)
	writeUint32(buf, reqID)
	buf.Write(typeIDBytes(idOpenSecureChannelResponse))
	buf.Write(mockResponseHeader(0))
	writeUint32(buf, 0) // ServerProtocolVersion
	writeUint32(buf, channelID)
	writeUint32(buf, tokenID)
	writeInt64(buf, nowUA())
	writeUint32(buf, 600000)
	writeByteString(buf, nil, true)
	if err := writeChunk(conn, "OPN", buf.Bytes()); err != nil {
		t.Errorf("write OPN: %v", err)
	}
}

func mockWriteMSG(t *testing.T, conn net.Conn, channelID, tokenID, seqNum, reqID uint32, typeID uint16, body []byte) {
	t.Helper()
	buf := &bytes.Buffer{}
	writeUint32(buf, channelID)
	writeUint32(buf, tokenID)
	writeUint32(buf, seqNum)
	writeUint32(buf, reqID)
	buf.Write(typeIDBytes(typeID))
	buf.Write(body)
	if err := writeChunk(conn, "MSG", buf.Bytes()); err != nil {
		t.Errorf("write MSG: %v", err)
	}
}

func encodeMockVariant(buf *bytes.Buffer, v any) {
	switch x := v.(type) {
	case bool:
		buf.WriteByte(1)
		if x {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	case int32:
		buf.WriteByte(6)
		writeInt32(buf, x)
	case float64:
		buf.WriteByte(11)
		writeFloat64(buf, x)
	case string:
		buf.WriteByte(12)
		writeString(buf, x, false)
	default:
		panic("encodeMockVariant: unsupported type")
	}
}

func encodeMockDataValue(v any, statusCode uint32, ts time.Time) []byte {
	buf := &bytes.Buffer{}
	buf.WriteByte(0x07) // Value | StatusCode | SourceTimestamp
	encodeMockVariant(buf, v)
	writeUint32(buf, statusCode)
	writeInt64(buf, ts.UnixNano()/100+uaEpochOffsetTicks)
	return buf.Bytes()
}

// runMockServer answers exactly one client connection through Hello/Ack,
// OpenSecureChannel, CreateSession, ActivateSession and one Read, returning
// readValues for the Read. It best-effort drains (and ignores) the
// CloseSession/CloseSecureChannel the client sends afterward.
func runMockServer(t *testing.T, ln net.Listener, readValues []struct {
	value  any
	status uint32
	ts     time.Time
}) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		t.Errorf("accept: %v", err)
		return
	}
	defer conn.Close()

	// Hello / Ack
	if msgType, _, err := readChunk(conn); err != nil || msgType != "HEL" {
		t.Errorf("expected HEL, got %q err=%v", msgType, err)
		return
	}
	ackBody := &bytes.Buffer{}
	writeUint32(ackBody, 0)
	writeUint32(ackBody, 65536)
	writeUint32(ackBody, 65536)
	writeUint32(ackBody, 4*1024*1024)
	writeUint32(ackBody, 0)
	if err := writeChunk(conn, "ACK", ackBody.Bytes()); err != nil {
		t.Errorf("write ACK: %v", err)
		return
	}

	// OpenSecureChannel
	if msgType, _, err := readChunk(conn); err != nil || msgType != "OPN" {
		t.Errorf("expected OPN, got %q err=%v", msgType, err)
		return
	}
	const channelID, tokenID = 42, 7
	mockWriteOPN(t, conn, channelID, tokenID, 1, 1)

	// CreateSession
	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (CreateSession), got %q err=%v", msgType, err)
		return
	}
	authToken := (NodeID{Namespace: 0, Numeric: 99}).encode()
	csBody := &bytes.Buffer{}
	csBody.Write(mockResponseHeader(0))
	csBody.Write([]byte{0x00, 0x00}) // SessionId: null
	csBody.Write(authToken)          // AuthenticationToken
	writeFloat64(csBody, 1200000)
	writeByteString(csBody, nil, true)
	writeByteString(csBody, nil, true)
	mockWriteMSG(t, conn, channelID, tokenID, 2, 2, idCreateSessionResponse, csBody.Bytes())

	// ActivateSession
	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (ActivateSession), got %q err=%v", msgType, err)
		return
	}
	mockWriteMSG(t, conn, channelID, tokenID, 3, 3, idActivateSessionResponse, mockResponseHeader(0))

	// Read
	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (Read), got %q err=%v", msgType, err)
		return
	}
	rBody := &bytes.Buffer{}
	rBody.Write(mockResponseHeader(0))
	writeInt32(rBody, int32(len(readValues)))
	for _, v := range readValues {
		rBody.Write(encodeMockDataValue(v.value, v.status, v.ts))
	}
	writeInt32(rBody, 0) // DiagnosticInfos
	mockWriteMSG(t, conn, channelID, tokenID, 4, 4, idReadResponse, rBody.Bytes())

	// Best-effort drain of CloseSession/CloseSecureChannel; the client
	// doesn't wait for responses to either, so just answer CloseSession
	// if it arrives and then let the connection close naturally.
	if msgType, _, err := readChunk(conn); err == nil && msgType == "MSG" {
		mockWriteMSG(t, conn, channelID, tokenID, 5, 5, idCloseSessionResponse, mockResponseHeader(0))
	}
}

func TestClientReadEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	sourceTS := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	want := []struct {
		value  any
		status uint32
		ts     time.Time
	}{
		{int32(72), 0, sourceTS},
		{31.5, 0, sourceTS},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockServer(t, ln, want)
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	nodeIDs := []NodeID{{Namespace: 2, Numeric: 1001}, {Namespace: 2, Numeric: 1002}}
	values, err := cli.Read(context.Background(), nodeIDs)
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if len(values) != 2 {
		t.Fatalf("got %d values, want 2", len(values))
	}
	if values[0].Value != int32(72) || values[1].Value != 31.5 {
		t.Fatalf("values=%+v", values)
	}
	for i, v := range values {
		if v.SourceTimestamp == nil || !v.SourceTimestamp.Equal(sourceTS) {
			t.Fatalf("value[%d].SourceTimestamp=%v want=%v", i, v.SourceTimestamp, sourceTS)
		}
	}
}

func TestClientReadRejectsBadOpenSecureChannelStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if msgType, _, err := readChunk(conn); err != nil || msgType != "HEL" {
			return
		}
		ackBody := &bytes.Buffer{}
		writeUint32(ackBody, 0)
		writeUint32(ackBody, 65536)
		writeUint32(ackBody, 65536)
		writeUint32(ackBody, 4*1024*1024)
		writeUint32(ackBody, 0)
		if err := writeChunk(conn, "ACK", ackBody.Bytes()); err != nil {
			return
		}
		if msgType, _, err := readChunk(conn); err != nil || msgType != "OPN" {
			return
		}
		buf := &bytes.Buffer{}
		writeUint32(buf, 0)
		writeString(buf, securityPolicyNone, false)
		writeByteString(buf, nil, true)
		writeByteString(buf, nil, true)
		writeUint32(buf, 1)
		writeUint32(buf, 1)
		buf.Write(typeIDBytes(idOpenSecureChannelResponse))
		buf.Write(mockResponseHeader(0x80010000)) // Bad_SecurityChecksFailed-ish nonzero status
		_ = writeChunk(conn, "OPN", buf.Bytes())
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	if _, err := cli.Read(context.Background(), []NodeID{{Namespace: 0, Numeric: 1}}); err == nil {
		t.Fatal("expected error for non-Good OpenSecureChannel status")
	}
}

func TestClientReadRequiresAtLeastOneNodeID(t *testing.T) {
	cli := &Client{Endpoint: "opc.tcp://127.0.0.1:1/nodra", Timeout: time.Second}
	if _, err := cli.Read(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty NodeID list")
	}
}
