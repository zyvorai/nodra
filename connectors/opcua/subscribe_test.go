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

// runMockSubscribeServer answers the full handshake (Hello/Ack, OPN,
// CreateSession, ActivateSession — same as runMockServer) plus
// CreateSubscription, CreateMonitoredItems, and exactly one PublishResponse
// carrying one DataChangeNotification. On the second PublishRequest it
// closes the connection without responding, so the client's blocking
// publish() call fails fast instead of hanging until its long publish
// deadline — that's how this test ends Client.Subscribe's otherwise
// infinite loop.
func runMockSubscribeServer(t *testing.T, ln net.Listener, clientHandle uint32, value any, status uint32, ts time.Time) {
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
	csBody.Write([]byte{0x00, 0x00}) // SessionId: null
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

	// CreateSubscription
	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (CreateSubscription), got %q err=%v", msgType, err)
		return
	}
	const subID uint32 = 5
	csrBody := &bytes.Buffer{}
	csrBody.Write(mockResponseHeader(0))
	writeUint32(csrBody, subID)
	writeFloat64(csrBody, 100) // RevisedPublishingInterval
	writeUint32(csrBody, 60)   // RevisedLifetimeCount
	writeUint32(csrBody, 20)   // RevisedMaxKeepAliveCount
	mockWriteMSG(t, conn, channelID, tokenID, 4, 4, idCreateSubscriptionResponse, csrBody.Bytes())

	// CreateMonitoredItems
	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (CreateMonitoredItems), got %q err=%v", msgType, err)
		return
	}
	cmiBody := &bytes.Buffer{}
	cmiBody.Write(mockResponseHeader(0))
	writeInt32(cmiBody, 1)  // one result
	writeUint32(cmiBody, 0) // StatusCode Good
	writeUint32(cmiBody, 1) // MonitoredItemId
	writeFloat64(cmiBody, 100)
	writeUint32(cmiBody, 1)
	cmiBody.Write([]byte{0x00, 0x00, 0x00}) // Filter result: null ExtensionObject
	mockWriteMSG(t, conn, channelID, tokenID, 5, 5, idCreateMonitoredItemsResponse, cmiBody.Bytes())

	// First Publish -> one DataChangeNotification.
	if msgType, _, err := readChunk(conn); err != nil || msgType != "MSG" {
		t.Errorf("expected MSG (Publish #1), got %q err=%v", msgType, err)
		return
	}
	dcn := &bytes.Buffer{}
	writeInt32(dcn, 1) // one MonitoredItemNotification
	writeUint32(dcn, clientHandle)
	dcn.Write(encodeMockDataValue(value, status, ts))
	writeInt32(dcn, 0) // DiagnosticInfos

	pubBody := &bytes.Buffer{}
	pubBody.Write(mockResponseHeader(0))
	writeUint32(pubBody, subID)
	writeInt32(pubBody, 0)  // AvailableSequenceNumbers: none
	pubBody.WriteByte(0x00) // MoreNotifications = false
	writeUint32(pubBody, 1) // SequenceNumber
	writeInt64(pubBody, nowUA())
	writeInt32(pubBody, 1) // one NotificationData entry
	pubBody.Write(typeIDBytes(idDataChangeNotification))
	pubBody.WriteByte(0x01) // ExtensionObject body encoding = ByteString
	writeByteString(pubBody, dcn.Bytes(), false)
	writeInt32(pubBody, 0) // Results (no acks were sent on the first Publish)
	writeInt32(pubBody, 0) // DiagnosticInfos
	mockWriteMSG(t, conn, channelID, tokenID, 6, 6, idPublishResponse, pubBody.Bytes())

	// Second Publish -> close without responding, so Client.Subscribe's
	// blocking publish() fails fast instead of hanging on its long deadline.
	_, _, _ = readChunk(conn)
}

func TestClientSubscribeDeliversDataChangeNotification(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	sourceTS := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockSubscribeServer(t, ln, 1, 31.5, 0, sourceTS)
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	nodeIDs := []NodeID{{Namespace: 2, Numeric: 1001}}

	type notice struct {
		id NodeID
		dv DataValue
	}
	got := make(chan notice, 4)
	subErr := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		subErr <- cli.Subscribe(ctx, nodeIDs, 100*time.Millisecond, func(id NodeID, dv DataValue) {
			got <- notice{id, dv}
		})
	}()

	select {
	case n := <-got:
		if n.id != nodeIDs[0] {
			t.Fatalf("notification for %v, want %v", n.id, nodeIDs[0])
		}
		if n.dv.Value != 31.5 {
			t.Fatalf("value=%v, want 31.5", n.dv.Value)
		}
		if n.dv.SourceTimestamp == nil || !n.dv.SourceTimestamp.Equal(sourceTS) {
			t.Fatalf("SourceTimestamp=%v, want %v", n.dv.SourceTimestamp, sourceTS)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification delivered")
	}

	select {
	case err := <-subErr:
		if err == nil {
			t.Fatal("expected Subscribe to return an error once the mock server closed the connection")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Subscribe did not return after the connection closed")
	}
	<-done
}

func TestClientSubscribeRequiresAtLeastOneNodeID(t *testing.T) {
	cli := &Client{Endpoint: "opc.tcp://127.0.0.1:1/nodra", Timeout: time.Second}
	err := cli.Subscribe(context.Background(), nil, time.Second, func(NodeID, DataValue) {})
	if err == nil {
		t.Fatal("expected error for empty NodeID list")
	}
}
