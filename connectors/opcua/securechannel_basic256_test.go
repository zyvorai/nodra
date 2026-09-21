// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Basic256Sha256 mock server ---------------------------------------
//
// This is the counterpart of the SecurityPolicy None mock in client_test.go
// and, like it, answers a fixed script rather than implementing a real
// server. It deliberately does *not* call the session's own framing
// methods: those are hard-wired to the client role (client keys to send,
// server keys to verify), so driving the other half from the raw primitives
// is what makes the test able to catch a swapped key set, a signature
// computed over the wrong bytes, or padding the other side won't accept.

type secureMock struct {
	t             *testing.T
	mode          int32
	serverKey     *rsa.PrivateKey
	serverCertDER []byte
	clientCertDER []byte
	clientPub     *rsa.PublicKey

	keys      *channelKeys
	channelID uint32
	tokenID   uint32
	seq       uint32

	sessionServerNonce []byte
	// tamperRead flips a byte of the Read response so the client's
	// signature check has something to reject.
	tamperRead bool
}

func skipRequestHeader(r *reader) {
	readNodeIdRaw(r) // AuthenticationToken
	r.i64()          // Timestamp
	r.u32()          // RequestHandle
	r.u32()          // ReturnDiagnostics
	r.str()          // AuditEntryId
	r.u32()          // TimeoutHint
	readExtensionObjectNull(r)
}

// readOPN decrypts and verifies the client's asymmetric OpenSecureChannel
// chunk and returns its body, starting at the service TypeId.
func (m *secureMock) readOPN(conn net.Conn) []byte {
	m.t.Helper()
	msgType, hdr, payload, err := readChunkRaw(conn)
	if err != nil || msgType != "OPN" {
		m.t.Fatalf("expected OPN, got %q err=%v", msgType, err)
	}
	r := &reader{b: payload}
	r.u32() // SecureChannelId
	policy := r.str()
	senderCert := r.byteStr()
	thumbprint := r.byteStr()
	if r.err != nil {
		m.t.Fatalf("asymmetric security header: %v", r.err)
	}
	if policy != securityPolicyBasic256Sha256 {
		m.t.Fatalf("SecurityPolicyUri=%q", policy)
	}
	if !bytes.Equal(thumbprint, certThumbprint(m.serverCertDER)) {
		m.t.Fatal("ReceiverCertificateThumbprint does not identify the server certificate")
	}
	cert, err := x509.ParseCertificate(senderCert)
	if err != nil {
		m.t.Fatalf("SenderCertificate: %v", err)
	}
	m.clientCertDER = senderCert
	m.clientPub = cert.PublicKey.(*rsa.PublicKey)
	secHeaderEnd := r.off

	plain, err := rsaOAEPDecrypt(m.serverKey, payload[secHeaderEnd:])
	if err != nil {
		m.t.Fatalf("decrypt OPN: %v", err)
	}
	signedLen := len(plain) - m.clientPub.Size()
	if signedLen < 8 {
		m.t.Fatal("OPN plaintext too short")
	}
	if err := rsaVerifySHA256(m.clientPub, concat(hdr, payload[:secHeaderEnd], plain[:signedLen]), plain[signedLen:]); err != nil {
		m.t.Fatalf("OPN signature: %v", err)
	}
	body, err := stripMessagePadding(plain[8:signedLen], m.serverKey.N.BitLen() > basic256MinAsymKeyBits)
	if err != nil {
		m.t.Fatalf("OPN padding: %v", err)
	}
	return body
}

func (m *secureMock) writeOPN(conn net.Conn, reqID uint32, body []byte) {
	m.t.Helper()
	m.seq++
	sec := &bytes.Buffer{}
	writeString(sec, securityPolicyBasic256Sha256, false)
	writeByteString(sec, m.serverCertDER, false)
	writeByteString(sec, certThumbprint(m.clientCertDER), false)

	seqHeader := sequenceHeader(m.seq, reqID)
	cipherBlock := m.clientPub.Size()
	plainBlock := cipherBlock - rsaOAEPSHA1Overhead
	sigSize := m.serverKey.Size()
	pad := messagePadding(len(seqHeader)+len(body), sigSize, plainBlock, m.clientPub.N.BitLen() > basic256MinAsymKeyBits)
	encryptedLen := (len(seqHeader) + len(body) + len(pad) + sigSize) / plainBlock * cipherBlock
	hdr := chunkHeader("OPN", 12+sec.Len()+encryptedLen, m.channelID)

	sig, err := rsaSignSHA256(m.serverKey, concat(hdr, sec.Bytes(), seqHeader, body, pad))
	if err != nil {
		m.t.Fatal(err)
	}
	encrypted, err := rsaOAEPEncrypt(m.clientPub, concat(seqHeader, body, pad, sig))
	if err != nil {
		m.t.Fatal(err)
	}
	if _, err := conn.Write(concat(hdr, sec.Bytes(), encrypted)); err != nil {
		m.t.Errorf("write OPN: %v", err)
	}
}

// readMSG decrypts and verifies one symmetric chunk from the client.
func (m *secureMock) readMSG(conn net.Conn) (msgType string, body []byte, reqID uint32) {
	m.t.Helper()
	msgType, hdr, payload, err := readChunkRaw(conn)
	if err != nil {
		m.t.Fatalf("read symmetric chunk: %v", err)
	}
	r := &reader{b: payload}
	r.u32() // SecureChannelId
	if got := r.u32(); got != m.tokenID {
		m.t.Fatalf("TokenId=%d, want %d", got, m.tokenID)
	}
	secHeaderEnd := r.off

	data := payload[secHeaderEnd:]
	encrypted := m.mode == messageSecurityModeSignAndEncrypt
	if encrypted {
		if data, err = aesCBCDecrypt(m.keys.clientEncrypting, m.keys.clientIV, data); err != nil {
			m.t.Fatalf("decrypt %s: %v", msgType, err)
		}
	}
	signedLen := len(data) - basic256SignatureSize
	if signedLen < 8 {
		m.t.Fatalf("%s plaintext too short", msgType)
	}
	if !verifyHMACSHA256(m.keys.clientSigning, concat(hdr, payload[:secHeaderEnd], data[:signedLen]), data[signedLen:]) {
		m.t.Fatalf("%s signature does not verify", msgType)
	}
	reqID = binary.LittleEndian.Uint32(data[4:8])
	body = data[8:signedLen]
	if encrypted {
		if body, err = stripMessagePadding(body, false); err != nil {
			m.t.Fatalf("%s padding: %v", msgType, err)
		}
	}
	return msgType, body, reqID
}

func (m *secureMock) writeMSG(conn net.Conn, reqID uint32, typeID uint16, body []byte) {
	m.t.Helper()
	m.seq++
	full := concat(typeIDBytes(typeID), body)

	secHeader := make([]byte, 4)
	binary.LittleEndian.PutUint32(secHeader, m.tokenID)
	seqHeader := sequenceHeader(m.seq, reqID)

	encrypt := m.mode == messageSecurityModeSignAndEncrypt
	var pad []byte
	if encrypt {
		pad = messagePadding(len(seqHeader)+len(full), basic256SignatureSize, basic256SymIVLength, false)
	}
	hdr := chunkHeader("MSG", 12+len(secHeader)+len(seqHeader)+len(full)+len(pad)+basic256SignatureSize, m.channelID)
	sig := hmacSHA256(m.keys.serverSigning, concat(hdr, secHeader, seqHeader, full, pad))
	payload := concat(seqHeader, full, pad, sig)
	if encrypt {
		var err error
		if payload, err = aesCBCEncrypt(m.keys.serverEncrypting, m.keys.serverIV, payload); err != nil {
			m.t.Fatal(err)
		}
	}
	frame := concat(hdr, secHeader, payload)
	if m.tamperRead && typeID == idReadResponse {
		frame[len(frame)-1] ^= 0xFF
	}
	if _, err := conn.Write(frame); err != nil {
		m.t.Errorf("write MSG: %v", err)
	}
}

// serve runs one full Basic256Sha256 connection: Hello/Ack, the asymmetric
// OpenSecureChannel, then CreateSession, ActivateSession and one Read over
// the derived symmetric keys.
func (m *secureMock) serve(ln net.Listener, readValues []struct {
	value  any
	status uint32
	ts     time.Time
}) {
	m.t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	m.channelID = 4711
	m.tokenID = 13

	if msgType, _, err := readChunk(conn); err != nil || msgType != "HEL" {
		m.t.Errorf("expected HEL, got %q err=%v", msgType, err)
		return
	}
	ack := &bytes.Buffer{}
	writeUint32(ack, 0)
	writeUint32(ack, 65536)
	writeUint32(ack, 65536)
	writeUint32(ack, 4*1024*1024)
	writeUint32(ack, 0)
	if err := writeChunk(conn, "ACK", ack.Bytes()); err != nil {
		m.t.Errorf("write ACK: %v", err)
		return
	}

	// OpenSecureChannel: read the client nonce, answer with ours, and
	// derive the same key sets the client is deriving.
	opn := m.readOPN(conn)
	r := &reader{b: opn}
	if got := readTypeID(r); got != idOpenSecureChannelRequest {
		m.t.Errorf("OPN body type %d", got)
		return
	}
	skipRequestHeader(r)
	r.u32() // ClientProtocolVersion
	r.i32() // RequestType
	if got := r.i32(); got != m.mode {
		m.t.Errorf("SecurityMode=%d, want %d", got, m.mode)
		return
	}
	clientNonce := r.byteStr()
	r.u32() // RequestedLifetime
	if r.err != nil {
		m.t.Errorf("OpenSecureChannelRequest: %v", r.err)
		return
	}
	if len(clientNonce) != basic256NonceLength {
		m.t.Errorf("ClientNonce is %d bytes, want %d", len(clientNonce), basic256NonceLength)
		return
	}
	serverNonce := bytes.Repeat([]byte{0x5C}, basic256NonceLength)
	m.keys = deriveChannelKeys(clientNonce, serverNonce)

	opnResp := &bytes.Buffer{}
	opnResp.Write(typeIDBytes(idOpenSecureChannelResponse))
	opnResp.Write(mockResponseHeader(0))
	writeUint32(opnResp, 0) // ServerProtocolVersion
	writeUint32(opnResp, m.channelID)
	writeUint32(opnResp, m.tokenID)
	writeInt64(opnResp, nowUA())
	writeUint32(opnResp, 600000)
	writeByteString(opnResp, serverNonce, false)
	m.writeOPN(conn, 1, opnResp.Bytes())

	// CreateSession
	_, body, reqID := m.readMSG(conn)
	clientNonceSession, clientCert := m.parseCreateSession(body)
	if !bytes.Equal(clientCert, m.clientCertDER) {
		m.t.Error("CreateSession ClientCertificate differs from the OPN SenderCertificate")
		return
	}
	m.sessionServerNonce = bytes.Repeat([]byte{0x3A}, basic256NonceLength)
	serverSignature, err := rsaSignSHA256(m.serverKey, concat(clientCert, clientNonceSession))
	if err != nil {
		m.t.Error(err)
		return
	}
	authToken := (NodeID{Namespace: 0, Numeric: 99}).encode()
	cs := &bytes.Buffer{}
	cs.Write(mockResponseHeader(0))
	cs.Write([]byte{0x00, 0x00}) // SessionId: null
	cs.Write(authToken)
	writeFloat64(cs, 1200000)
	writeByteString(cs, m.sessionServerNonce, false)
	writeByteString(cs, m.serverCertDER, false)
	writeInt32(cs, 1) // ServerEndpoints
	mockEncodeEndpointDescription(cs, EndpointDescription{
		EndpointURL:         "opc.tcp://127.0.0.1:0/nodra",
		Server:              ApplicationDescription{ApplicationURI: "urn:test:server", ProductURI: "urn:test", ApplicationName: "test", ApplicationType: 0},
		ServerCertificate:   m.serverCertDER,
		SecurityMode:        m.mode,
		SecurityPolicyURI:   securityPolicyBasic256Sha256,
		TransportProfileURI: "http://opcfoundation.org/UA-Profile/Transport/uatcp-uasc-uabinary",
		SecurityLevel:       3,
	})
	writeInt32(cs, 0) // ServerSoftwareCertificates
	writeString(cs, signatureAlgorithmRSASHA256, false)
	writeByteString(cs, serverSignature, false)
	writeUint32(cs, 4*1024*1024) // MaxRequestMessageSize
	m.writeMSG(conn, reqID, idCreateSessionResponse, cs.Bytes())

	// ActivateSession: the client must prove it holds the private key for
	// the certificate it sent, over our certificate and session nonce.
	_, body, reqID = m.readMSG(conn)
	m.verifyActivateSession(body)
	as := &bytes.Buffer{}
	as.Write(mockResponseHeader(0))
	writeByteString(as, bytes.Repeat([]byte{0x77}, basic256NonceLength), false) // ServerNonce
	writeInt32(as, 0)                                                           // Results
	writeInt32(as, 0)                                                           // DiagnosticInfos
	m.writeMSG(conn, reqID, idActivateSessionResponse, as.Bytes())

	// Read
	_, _, reqID = m.readMSG(conn)
	rd := &bytes.Buffer{}
	rd.Write(mockResponseHeader(0))
	writeInt32(rd, int32(len(readValues)))
	for _, v := range readValues {
		rd.Write(encodeMockDataValue(v.value, v.status, v.ts))
	}
	writeInt32(rd, 0) // DiagnosticInfos
	m.writeMSG(conn, reqID, idReadResponse, rd.Bytes())

	if m.tamperRead {
		return
	}
	// CloseSession, then the client sends CLO without waiting.
	if msgType, _, reqID := m.readMSG(conn); msgType == "MSG" {
		m.writeMSG(conn, reqID, idCloseSessionResponse, mockResponseHeader(0))
	}
}

func (m *secureMock) parseCreateSession(body []byte) (clientNonce, clientCert []byte) {
	m.t.Helper()
	r := &reader{b: body}
	if got := readTypeID(r); got != idCreateSessionRequest {
		m.t.Fatalf("CreateSession body type %d", got)
	}
	skipRequestHeader(r)
	readApplicationDescription(r) // ClientDescription
	r.str()                       // ServerUri
	r.str()                       // EndpointUrl
	r.str()                       // SessionName
	clientNonce = r.byteStr()
	clientCert = r.byteStr()
	r.f64() // RequestedSessionTimeout
	r.u32() // MaxResponseMessageSize
	if r.err != nil {
		m.t.Fatalf("CreateSessionRequest: %v", r.err)
	}
	if len(clientNonce) != basic256NonceLength {
		m.t.Fatalf("CreateSession ClientNonce is %d bytes, want %d", len(clientNonce), basic256NonceLength)
	}
	return clientNonce, clientCert
}

func (m *secureMock) verifyActivateSession(body []byte) {
	m.t.Helper()
	r := &reader{b: body}
	if got := readTypeID(r); got != idActivateSessionRequest {
		m.t.Fatalf("ActivateSession body type %d", got)
	}
	skipRequestHeader(r)
	algorithm := r.str()
	signature := r.byteStr()
	if r.err != nil {
		m.t.Fatalf("ActivateSessionRequest: %v", r.err)
	}
	if algorithm != signatureAlgorithmRSASHA256 {
		m.t.Fatalf("ClientSignature.Algorithm=%q", algorithm)
	}
	if err := rsaVerifySHA256(m.clientPub, concat(m.serverCertDER, m.sessionServerNonce), signature); err != nil {
		m.t.Fatalf("ClientSignature: %v", err)
	}
}

// --- tests -------------------------------------------------------------

func newSecureTestClient(t *testing.T, addr string, mode string) *Client {
	t.Helper()
	certPath, keyPath, serverCertPath := writeTestCertFiles(t, t.TempDir())
	return &Client{
		Endpoint: "opc.tcp://" + addr + "/nodra",
		Timeout:  5 * time.Second,
		Security: SecurityConfig{
			SecurityPolicy: "Basic256Sha256",
			SecurityMode:   mode,
			ClientCertPath: certPath,
			ClientKeyPath:  keyPath,
			ServerCertPath: serverCertPath,
		},
	}
}

func TestClientReadBasic256Sha256(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode int32
	}{
		{"Sign", messageSecurityModeSign},
		{"SignAndEncrypt", messageSecurityModeSignAndEncrypt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, serverKey, serverDER := testIdentities(t)
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

			mock := &secureMock{t: t, mode: tc.mode, serverKey: serverKey, serverCertDER: serverDER}
			done := make(chan struct{})
			go func() {
				defer close(done)
				mock.serve(ln, want)
			}()

			cli := newSecureTestClient(t, ln.Addr().String(), tc.name)
			values, err := cli.Read(context.Background(), []NodeID{{Namespace: 2, Numeric: 1001}, {Namespace: 2, Numeric: 1002}})
			if err != nil {
				t.Fatalf("Read over %s: %v", tc.name, err)
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
		})
	}
}

// TestClientBasic256RejectsTamperedMessage proves the symmetric signature
// is actually checked rather than merely computed.
func TestClientBasic256RejectsTamperedMessage(t *testing.T) {
	_, _, serverKey, serverDER := testIdentities(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	mock := &secureMock{
		t: t, mode: messageSecurityModeSignAndEncrypt,
		serverKey: serverKey, serverCertDER: serverDER, tamperRead: true,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		mock.serve(ln, []struct {
			value  any
			status uint32
			ts     time.Time
		}{{int32(1), 0, time.Now().UTC()}})
	}()

	cli := newSecureTestClient(t, ln.Addr().String(), "SignAndEncrypt")
	_, err = cli.Read(context.Background(), []NodeID{{Namespace: 2, Numeric: 1001}})
	<-done
	if err == nil {
		t.Fatal("expected the tampered Read response to be rejected")
	}
	if !strings.Contains(err.Error(), "signature") && !strings.Contains(err.Error(), "padding") {
		t.Fatalf("want a signature/padding rejection, got %v", err)
	}
}

// TestClientBasic256RejectsWrongServerCertificate covers the trust check:
// a server holding a different key than server_cert_path must not get a
// session, even though its own crypto is internally consistent.
func TestClientBasic256RejectsWrongServerCertificate(t *testing.T) {
	_, impostorDER := mustSelfSigned("urn:nodra:impostor")
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
		ack := &bytes.Buffer{}
		writeUint32(ack, 0)
		writeUint32(ack, 65536)
		writeUint32(ack, 65536)
		writeUint32(ack, 4*1024*1024)
		writeUint32(ack, 0)
		if err := writeChunk(conn, "ACK", ack.Bytes()); err != nil {
			return
		}
		// The client encrypts the OPN to the configured server certificate,
		// which the impostor cannot decrypt, so just read and drop it.
		_, _, _, _ = readChunkRaw(conn)
	}()

	dir := t.TempDir()
	certPath, keyPath, _ := writeTestCertFiles(t, dir)
	impostorPath := filepath.Join(dir, "impostor.pem")
	writePEMFile(t, impostorPath, "CERTIFICATE", impostorDER)

	cli := &Client{
		Endpoint: "opc.tcp://" + ln.Addr().String() + "/nodra",
		Timeout:  2 * time.Second,
		Security: SecurityConfig{
			SecurityPolicy: "Basic256Sha256",
			SecurityMode:   "SignAndEncrypt",
			ClientCertPath: certPath,
			ClientKeyPath:  keyPath,
			ServerCertPath: impostorPath,
		},
	}
	if _, err := cli.Read(context.Background(), []NodeID{{Namespace: 2, Numeric: 1}}); err == nil {
		t.Fatal("expected the handshake to fail against a server that cannot answer")
	}
}
