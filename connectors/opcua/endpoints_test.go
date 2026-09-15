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

func mockEncodeLocalizedText(buf *bytes.Buffer, locale, text string) {
	mask := byte(0)
	if locale != "" {
		mask |= 0x01
	}
	if text != "" {
		mask |= 0x02
	}
	buf.WriteByte(mask)
	if mask&0x01 != 0 {
		writeString(buf, locale, false)
	}
	if mask&0x02 != 0 {
		writeString(buf, text, false)
	}
}

func mockEncodeApplicationDescription(buf *bytes.Buffer, a ApplicationDescription) {
	writeString(buf, a.ApplicationURI, false)
	writeString(buf, a.ProductURI, false)
	mockEncodeLocalizedText(buf, "en", a.ApplicationName)
	writeInt32(buf, a.ApplicationType)
	writeString(buf, a.GatewayServerURI, a.GatewayServerURI == "")
	writeString(buf, a.DiscoveryProfileURI, a.DiscoveryProfileURI == "")
	writeStrings(buf, a.DiscoveryURLs)
}

func mockEncodeEndpointDescription(buf *bytes.Buffer, e EndpointDescription) {
	writeString(buf, e.EndpointURL, false)
	mockEncodeApplicationDescription(buf, e.Server)
	writeByteString(buf, e.ServerCertificate, e.ServerCertificate == nil)
	writeInt32(buf, e.SecurityMode)
	writeString(buf, e.SecurityPolicyURI, false)
	writeInt32(buf, 0) // UserIdentityTokens: empty
	writeString(buf, e.TransportProfileURI, false)
	buf.WriteByte(e.SecurityLevel)
}

func mockEncodeExpandedNodeID(buf *bytes.Buffer, id NodeID, namespaceURI string, serverIndex uint32) {
	enc := id.encode()
	if namespaceURI != "" {
		enc[0] |= 0x80
	}
	if serverIndex != 0 {
		enc[0] |= 0x40
	}
	buf.Write(enc)
	if namespaceURI != "" {
		writeString(buf, namespaceURI, false)
	}
	if serverIndex != 0 {
		writeUint32(buf, serverIndex)
	}
}

func mockEncodeReferenceDescription(buf *bytes.Buffer, rd ReferenceDescription) {
	buf.Write(rd.ReferenceTypeID.encode())
	if rd.IsForward {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	mockEncodeExpandedNodeID(buf, rd.TargetNodeID, rd.TargetNamespaceURI, rd.TargetServerIndex)
	writeUint16(buf, rd.BrowseNameNamespace)
	writeString(buf, rd.BrowseName, false)
	mockEncodeLocalizedText(buf, "en", rd.DisplayName)
	writeInt32(buf, rd.NodeClass)
	mockEncodeExpandedNodeID(buf, NodeID{}, "", 0) // TypeDefinition: null
}

func mockEncodeBrowseResult(buf *bytes.Buffer, br BrowseResult) {
	writeUint32(buf, br.StatusCode)
	writeByteString(buf, br.ContinuationPoint, br.ContinuationPoint == nil)
	writeInt32(buf, int32(len(br.References)))
	for _, rd := range br.References {
		mockEncodeReferenceDescription(buf, rd)
	}
}

// runMockDiscoveryServer answers Hello/Ack + OpenSecureChannel, then one
// discovery request (GetEndpoints or FindServers) with a canned response —
// no CreateSession/ActivateSession at all, proving the discovery path
// doesn't require a session.
func runMockDiscoveryServer(t *testing.T, ln net.Listener, respTypeID uint16, body []byte) {
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
		t.Errorf("expected MSG (discovery request), got %q err=%v", msgType, err)
		return
	}
	mockWriteMSG(t, conn, channelID, tokenID, 2, 2, respTypeID, body)
}

func TestClientGetEndpointsEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	want := []EndpointDescription{{
		EndpointURL: "opc.tcp://host:4840/nodra",
		Server: ApplicationDescription{
			ApplicationURI: "urn:test:server", ProductURI: "urn:test:product",
			ApplicationName: "Test Server", DiscoveryURLs: []string{"opc.tcp://host:4840/nodra"},
		},
		SecurityMode: 1, SecurityPolicyURI: securityPolicyNone,
		TransportProfileURI: "http://opcfoundation.org/UA-Profile/Transport/uatcp-uasc-uabinary",
	}}
	body := &bytes.Buffer{}
	body.Write(mockResponseHeader(0))
	writeInt32(body, int32(len(want)))
	for _, e := range want {
		mockEncodeEndpointDescription(body, e)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockDiscoveryServer(t, ln, idGetEndpointsResponse, body.Bytes())
	}()

	got, err := GetEndpoints(context.Background(), "opc.tcp://"+ln.Addr().String()+"/nodra", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if len(got) != 1 {
		t.Fatalf("got %d endpoints, want 1", len(got))
	}
	if got[0].EndpointURL != want[0].EndpointURL || got[0].Server.ApplicationURI != want[0].Server.ApplicationURI ||
		got[0].Server.ApplicationName != want[0].Server.ApplicationName || got[0].SecurityPolicyURI != want[0].SecurityPolicyURI {
		t.Fatalf("got=%+v want=%+v", got[0], want[0])
	}
	if len(got[0].Server.DiscoveryURLs) != 1 || got[0].Server.DiscoveryURLs[0] != want[0].Server.DiscoveryURLs[0] {
		t.Fatalf("discovery urls=%+v", got[0].Server.DiscoveryURLs)
	}
}

func TestClientFindServersEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	want := []ApplicationDescription{{
		ApplicationURI: "urn:test:server", ProductURI: "urn:test:product",
		ApplicationName: "Test Server", DiscoveryURLs: []string{"opc.tcp://host:4840/nodra"},
	}}
	body := &bytes.Buffer{}
	body.Write(mockResponseHeader(0))
	writeInt32(body, int32(len(want)))
	for _, a := range want {
		mockEncodeApplicationDescription(body, a)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockDiscoveryServer(t, ln, idFindServersResponse, body.Bytes())
	}()

	got, err := FindServers(context.Background(), "opc.tcp://"+ln.Addr().String()+"/nodra", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if len(got) != 1 {
		t.Fatalf("got %d servers, want 1", len(got))
	}
	if got[0].ApplicationURI != want[0].ApplicationURI || got[0].ApplicationName != want[0].ApplicationName {
		t.Fatalf("got=%+v want=%+v", got[0], want[0])
	}
}

// runMockBrowseServer answers one connection through Hello/Ack,
// OpenSecureChannel, CreateSession, ActivateSession and one Browse.
func runMockBrowseServer(t *testing.T, ln net.Listener, results []BrowseResult) {
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
		t.Errorf("expected MSG (Browse), got %q err=%v", msgType, err)
		return
	}
	bBody := &bytes.Buffer{}
	bBody.Write(mockResponseHeader(0))
	writeInt32(bBody, int32(len(results)))
	for _, br := range results {
		mockEncodeBrowseResult(bBody, br)
	}
	writeInt32(bBody, 0) // DiagnosticInfos
	mockWriteMSG(t, conn, channelID, tokenID, 4, 4, idBrowseResponse, bBody.Bytes())

	if msgType, _, err := readChunk(conn); err == nil && msgType == "MSG" {
		mockWriteMSG(t, conn, channelID, tokenID, 5, 5, idCloseSessionResponse, mockResponseHeader(0))
	}
}

func TestClientBrowseEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	refType := NodeID{Namespace: 0, Numeric: 40}
	want := []ReferenceDescription{{
		ReferenceTypeID: refType, IsForward: true,
		TargetNodeID:        NodeID{Namespace: 2, Numeric: 2001},
		BrowseNameNamespace: 2, BrowseName: "Temperature",
		DisplayName: "Temperature", NodeClass: 2,
	}}
	results := []BrowseResult{{StatusCode: 0, References: want}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockBrowseServer(t, ln, results)
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	got, err := cli.Browse(context.Background(), NodeID{Namespace: 0, Numeric: 85}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if len(got) != 1 {
		t.Fatalf("got %d references, want 1", len(got))
	}
	if got[0].BrowseName != "Temperature" || got[0].TargetNodeID != want[0].TargetNodeID || !got[0].IsForward {
		t.Fatalf("got=%+v", got[0])
	}
	if got[0].ReferenceTypeID != refType {
		t.Fatalf("reference type=%+v want=%+v", got[0].ReferenceTypeID, refType)
	}
}

func TestClientBrowseRejectsBadStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockBrowseServer(t, ln, []BrowseResult{{StatusCode: 0x80340000}}) // Bad_NodeIdUnknown-ish
	}()

	cli := &Client{Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra", Timeout: 3 * time.Second}
	if _, err := cli.Browse(context.Background(), NodeID{Namespace: 0, Numeric: 85}, 0, nil); err == nil {
		t.Fatal("expected error for non-Good browse status")
	}
	<-done
}
