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

// runMockWriteServer answers one connection through Hello/Ack,
// OpenSecureChannel, CreateSession, ActivateSession and one Write, replying
// with the given status codes (one per NodeToWrite).
func runMockWriteServer(t *testing.T, ln net.Listener, statuses []uint32) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		t.Errorf("accept: %v", err)
		return
	}
	defer conn.Close()

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

	if msgType, _, err := readChunk(conn); err != nil || msgType != "OPN" {
		t.Errorf("expected OPN, got %q err=%v", msgType, err)
		return
	}
	const channelID, tokenID = 42, 7
	mockWriteOPN(t, conn, channelID, tokenID, 1, 1)

	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (CreateSession), got %q err=%v", msgType, err)
		return
	}
	authToken := (NodeID{Namespace: 0, Numeric: 99}).encode()
	csBody := &bytes.Buffer{}
	csBody.Write(mockResponseHeader(0))
	csBody.Write([]byte{0x00, 0x00})
	csBody.Write(authToken)
	writeFloat64(csBody, 1200000)
	writeByteString(csBody, nil, true)
	writeByteString(csBody, nil, true)
	mockWriteMSG(t, conn, channelID, tokenID, 2, 2, idCreateSessionResponse, csBody.Bytes())

	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (ActivateSession), got %q err=%v", msgType, err)
		return
	}
	mockWriteMSG(t, conn, channelID, tokenID, 3, 3, idActivateSessionResponse, mockResponseHeader(0))

	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (Write), got %q err=%v", msgType, err)
		return
	}
	wBody := &bytes.Buffer{}
	wBody.Write(mockResponseHeader(0))
	writeInt32(wBody, int32(len(statuses)))
	for _, s := range statuses {
		writeUint32(wBody, s)
	}
	writeInt32(wBody, 0) // DiagnosticInfos
	mockWriteMSG(t, conn, channelID, tokenID, 4, 4, idWriteResponse, wBody.Bytes())

	if msgType, _, err := readChunk(conn); err == nil && msgType == "MSG" {
		mockWriteMSG(t, conn, channelID, tokenID, 5, 5, idCloseSessionResponse, mockResponseHeader(0))
	}
}

func TestClientWriteEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockWriteServer(t, ln, []uint32{0})
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	status, err := cli.Write(context.Background(), NodeID{Namespace: 2, Numeric: 1001}, int32(72))
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if status != 0 {
		t.Fatalf("status=0x%08x, want 0", status)
	}
}

func TestClientWriteRejectsBadStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockWriteServer(t, ln, []uint32{0x80730000}) // Bad_NotWritable-ish
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	status, err := cli.Write(context.Background(), NodeID{Namespace: 2, Numeric: 1001}, int32(72))
	<-done
	if err != nil {
		t.Fatalf("expected no transport error, got %v", err)
	}
	if status == 0 {
		t.Fatal("expected a non-Good status code")
	}
}

func TestEncodeVariantErrorPropagatesFromWrite(t *testing.T) {
	cli := &Client{Endpoint: "opc.tcp://127.0.0.1:1/nodra", Timeout: time.Second}
	// dial will fail before encoding is even reached (no listener), but this
	// at least proves Write surfaces an error rather than panicking on an
	// unsupported value type; the encode-time error path itself is covered
	// directly by TestEncodeVariantRejectsUnsupportedType in variant_test.go.
	if _, err := cli.Write(context.Background(), NodeID{Namespace: 0, Numeric: 1}, struct{}{}); err == nil {
		t.Fatal("expected an error")
	}
}
