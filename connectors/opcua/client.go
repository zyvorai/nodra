// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package opcua implements a dependency-free OPC-UA (UA-TCP binary) client
// suitable for Nodra adapters. Default scope is SecurityPolicy None with an
// anonymous session, plus Read, Write, Subscribe/MonitoredItems,
// GetEndpoints/FindServers/Browse. Basic256Sha256 is accepted as config
// scaffolding (security_policy / security_mode / cert paths) but establishing
// a real Sign/SignAndEncrypt channel still needs crypto that this build does
// not ship — see SecurityConfig and docs/INDUSTRIAL_PROTOCOLS.md. The Poller
// connector polls Read or Subscribe; Write/Browse/discovery are Client /
// package-level Go API calls, not poller-only surfaces. The configured
// endpoint URL is dialed directly.
package opcua

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"time"
)

// Well-known OPC-UA namespace-0 numeric NodeIds for the services used here
// (Part 6 NodeIds — stable across UA 1.0x). Response ids are always
// request id + 3 for these services.
const (
	idOpenSecureChannelRequest  = 446
	idOpenSecureChannelResponse = 449
	idCloseSecureChannelRequest = 452
	idCreateSessionRequest      = 461
	idCreateSessionResponse     = 464
	idActivateSessionRequest    = 467
	idActivateSessionResponse   = 470
	idCloseSessionRequest       = 473
	idCloseSessionResponse      = 476
	idReadRequest               = 631
	idReadResponse              = 634
	idAnonymousIdentityToken    = 321

	attributeIDValue = 13 // AttributeId.Value

	securityPolicyNone = "http://opcfoundation.org/UA/SecurityPolicy#None"

	// uaEpochOffsetTicks is the number of 100ns ticks between the UA/Windows
	// FILETIME epoch (1601-01-01) and the Unix epoch (1970-01-01).
	uaEpochOffsetTicks int64 = 116444736000000000
)

// --- primitive encode helpers (OPC-UA binary is little-endian throughout) ---

func writeUint16(buf *bytes.Buffer, v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	buf.Write(b[:])
}
func writeUint32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}
func writeInt32(buf *bytes.Buffer, v int32) { writeUint32(buf, uint32(v)) }
func writeUint64(buf *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	buf.Write(b[:])
}
func writeInt64(buf *bytes.Buffer, v int64)     { writeUint64(buf, uint64(v)) }
func writeFloat64(buf *bytes.Buffer, v float64) { writeUint64(buf, math.Float64bits(v)) }

// String and ByteString share the same length-prefixed encoding: Int32
// length (-1 for null) followed by the raw bytes.
func writeString(buf *bytes.Buffer, s string, isNull bool) {
	if isNull {
		writeInt32(buf, -1)
		return
	}
	writeInt32(buf, int32(len(s)))
	buf.WriteString(s)
}
func writeByteString(buf *bytes.Buffer, b []byte, isNull bool) {
	if isNull || b == nil {
		writeInt32(buf, -1)
		return
	}
	writeInt32(buf, int32(len(b)))
	buf.Write(b)
}

func typeIDBytes(id uint16) []byte {
	// 4-byte NodeId form (namespace=0, UInt16 identifier) — every service
	// TypeId used here is a small namespace-0 numeric id.
	return []byte{0x01, 0x00, byte(id), byte(id >> 8)}
}

func nowUA() int64 {
	return time.Now().UnixNano()/100 + uaEpochOffsetTicks
}
func decodeDateTime(ticks int64) time.Time {
	ns := (ticks - uaEpochOffsetTicks) * 100
	return time.Unix(0, ns).UTC()
}

// --- decode helpers over a byte slice; the first error sticks. ---

type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}
func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || r.off+n > len(r.b) {
		r.fail(io.ErrUnexpectedEOF)
		return false
	}
	return true
}
func (r *reader) raw(n int) []byte {
	if !r.need(n) {
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}
func (r *reader) u8() byte {
	v := r.raw(1)
	if v == nil {
		return 0
	}
	return v[0]
}
func (r *reader) u16() uint16 {
	v := r.raw(2)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(v)
}
func (r *reader) u32() uint32 {
	v := r.raw(4)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}
func (r *reader) i32() int32 { return int32(r.u32()) }
func (r *reader) u64() uint64 {
	v := r.raw(8)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(v)
}
func (r *reader) i64() int64   { return int64(r.u64()) }
func (r *reader) f32() float32 { return math.Float32frombits(r.u32()) }
func (r *reader) f64() float64 { return math.Float64frombits(r.u64()) }
func (r *reader) str() string {
	n := r.i32()
	if n <= 0 || r.err != nil {
		return ""
	}
	b := r.raw(int(n))
	if b == nil {
		return ""
	}
	return string(b)
}
func (r *reader) byteStr() []byte {
	n := r.i32()
	if n <= 0 || r.err != nil {
		return nil
	}
	b := r.raw(int(n))
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// readNodeIdRaw consumes one NodeId (any encoding form) and returns the
// exact bytes it occupied, so callers (e.g. the session AuthenticationToken)
// can store and re-emit it verbatim without re-encoding it themselves.
func readNodeIdRaw(r *reader) []byte {
	start := r.off
	enc := r.u8()
	switch enc {
	case 0x00:
		r.u8()
	case 0x01:
		r.u8()
		r.u16()
	case 0x02:
		r.u16()
		r.u32()
	case 0x03:
		r.u16()
		r.str()
	case 0x04:
		r.u16()
		r.raw(16)
	case 0x05:
		r.u16()
		r.byteStr()
	default:
		r.fail(fmt.Errorf("unsupported NodeId encoding 0x%02x", enc))
	}
	if r.err != nil {
		return nil
	}
	return append([]byte(nil), r.b[start:r.off]...)
}

// readTypeID reads a service TypeId NodeId and returns its numeric
// identifier. Standard service TypeIds are always namespace-0 numeric ids.
func readTypeID(r *reader) uint32 {
	start := r.off
	enc := r.u8()
	switch enc {
	case 0x00:
		return uint32(r.u8())
	case 0x01:
		r.u8()
		return uint32(r.u16())
	case 0x02:
		r.u16()
		return r.u32()
	default:
		r.off = start
		readNodeIdRaw(r)
		return 0
	}
}

func readDiagnosticInfo(r *reader) {
	mask := r.u8()
	if mask == 0 || r.err != nil {
		return
	}
	if mask&0x01 != 0 {
		r.i32()
	}
	if mask&0x02 != 0 {
		r.i32()
	}
	if mask&0x04 != 0 {
		r.i32()
	}
	if mask&0x08 != 0 {
		r.i32()
	}
	if mask&0x10 != 0 {
		r.str()
	}
	if mask&0x20 != 0 {
		r.u32()
	}
	if mask&0x40 != 0 {
		readDiagnosticInfo(r)
	}
}
func readStringArray(r *reader) {
	n := r.i32()
	if n <= 0 || r.err != nil {
		return
	}
	for i := int32(0); i < n; i++ {
		r.str()
	}
}
func readExtensionObjectNull(r *reader) {
	readNodeIdRaw(r)
	enc := r.u8()
	switch enc {
	case 1:
		r.byteStr()
	case 2:
		r.str()
	}
}

// readResponseHeader decodes ResponseHeader and returns ServiceResult
// (StatusCode); 0 is Good.
func readResponseHeader(r *reader) uint32 {
	r.i64() // Timestamp
	r.u32() // RequestHandle
	status := r.u32()
	readDiagnosticInfo(r)
	readStringArray(r)
	readExtensionObjectNull(r)
	return status
}

// --- UA-TCP chunk framing ---

func writeChunk(conn net.Conn, msgType string, payload []byte) error {
	hdr := make([]byte, 8)
	copy(hdr[0:3], msgType)
	hdr[3] = 'F'
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(8+len(payload)))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}
func readChunk(conn net.Conn) (msgType string, payload []byte, err error) {
	hdr := make([]byte, 8)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return "", nil, err
	}
	msgType = string(hdr[0:3])
	size := binary.LittleEndian.Uint32(hdr[4:8])
	if size < 8 {
		return "", nil, fmt.Errorf("invalid UA-TCP message size %d", size)
	}
	payload = make([]byte, size-8)
	_, err = io.ReadFull(conn, payload)
	return msgType, payload, err
}

// --- session: one connect->hello->OpenSecureChannel->CreateSession->
// ActivateSession->Read->Close cycle, mirroring modbus.Client's per-call
// dial pattern rather than holding a long-lived connection across polls. ---

type session struct {
	conn      net.Conn
	timeout   time.Duration
	security  *securityMaterial
	channelID uint32
	tokenID   uint32
	seqNum    uint32
	reqID     uint32
	handle    uint32
	authToken []byte
}

func dial(ctx context.Context, endpoint string, timeout time.Duration, sec SecurityConfig) (*session, error) {
	mat, err := resolveSecurity(sec)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid opc-ua endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid opc-ua endpoint %q: missing host", endpoint)
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	s := &session{conn: conn, timeout: timeout, security: mat}
	if err := s.hello(endpoint); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	if err := s.openSecureChannel(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open secure channel: %w", err)
	}
	if err := s.createSession(endpoint); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create session: %w", err)
	}
	if err := s.activateSession(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("activate session: %w", err)
	}
	return s, nil
}

func (s *session) setDeadline() error { return s.conn.SetDeadline(time.Now().Add(s.timeout)) }
func (s *session) nextHandle() uint32 { s.handle++; return s.handle }

func (s *session) requestHeader(authToken []byte, handle uint32) []byte {
	buf := &bytes.Buffer{}
	if len(authToken) > 0 {
		buf.Write(authToken)
	} else {
		buf.Write([]byte{0x00, 0x00}) // null NodeId (two-byte form, ns=0 id=0)
	}
	writeInt64(buf, nowUA())
	writeUint32(buf, handle)
	writeUint32(buf, 0) // ReturnDiagnostics
	writeString(buf, "", true)
	writeUint32(buf, uint32(s.timeout.Milliseconds()))
	buf.Write([]byte{0x00, 0x00, 0x00}) // AdditionalHeader: null ExtensionObject
	return buf.Bytes()
}

func (s *session) hello(endpoint string) error {
	if err := s.setDeadline(); err != nil {
		return err
	}
	body := &bytes.Buffer{}
	writeUint32(body, 0)           // ProtocolVersion
	writeUint32(body, 65536)       // ReceiveBufferSize
	writeUint32(body, 65536)       // SendBufferSize
	writeUint32(body, 4*1024*1024) // MaxMessageSize
	writeUint32(body, 0)           // MaxChunkCount (0 = unlimited)
	writeString(body, endpoint, false)
	if err := writeChunk(s.conn, "HEL", body.Bytes()); err != nil {
		return err
	}
	msgType, payload, err := readChunk(s.conn)
	if err != nil {
		return err
	}
	if msgType == "ERR" {
		r := &reader{b: payload}
		code, reason := r.u32(), r.str()
		return fmt.Errorf("server rejected hello: status 0x%08x: %s", code, reason)
	}
	if msgType != "ACK" {
		return fmt.Errorf("unexpected response to Hello: %q", msgType)
	}
	r := &reader{b: payload}
	r.u32()
	r.u32()
	r.u32()
	r.u32()
	r.u32()
	return r.err
}

func (s *session) writeSecureMessage(msgType string, channelID uint32, securityHeader, body []byte) error {
	s.seqNum++
	s.reqID++
	buf := &bytes.Buffer{}
	writeUint32(buf, channelID)
	buf.Write(securityHeader)
	writeUint32(buf, s.seqNum)
	writeUint32(buf, s.reqID)
	buf.Write(body)
	return writeChunk(s.conn, msgType, buf.Bytes())
}

// readSecureMessage reads one OPN/MSG/CLO chunk, strips the SecureChannelId
// and security header (whose shape depends on msgType), and returns the
// remaining bytes starting at the service TypeId.
func (s *session) readSecureMessage() (msgType string, body []byte, err error) {
	msgType, payload, err := readChunk(s.conn)
	if err != nil {
		return "", nil, err
	}
	if msgType == "ERR" {
		r := &reader{b: payload}
		code, reason := r.u32(), r.str()
		return msgType, nil, fmt.Errorf("server error 0x%08x: %s", code, reason)
	}
	r := &reader{b: payload}
	r.u32() // SecureChannelId
	if msgType == "OPN" {
		r.str()     // SecurityPolicyUri
		r.byteStr() // SenderCertificate
		r.byteStr() // ReceiverCertificateThumbprint
	} else {
		r.u32() // TokenId
	}
	r.u32() // SequenceNumber
	r.u32() // RequestId
	if r.err != nil {
		return msgType, nil, r.err
	}
	return msgType, r.b[r.off:], nil
}

// serviceCall wraps reqBody (which must already start with its own TypeId)
// in a Symmetric MSG frame, sends it, and returns the response bytes
// starting right after the validated response TypeId.
func (s *session) serviceCall(expectedRespTypeID uint32, reqBody []byte) ([]byte, error) {
	return s.serviceCallDeadline(expectedRespTypeID, reqBody, s.timeout)
}

// serviceCallDeadline is serviceCall with an explicit deadline instead of
// the session's default s.timeout — Publish (see subscribe.go) needs to
// wait far longer than a normal request/response, since the server may
// legitimately hold the request open until there's something to report.
func (s *session) serviceCallDeadline(expectedRespTypeID uint32, reqBody []byte, deadline time.Duration) ([]byte, error) {
	if err := s.conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, err
	}
	symHeader := make([]byte, 4)
	binary.LittleEndian.PutUint32(symHeader, s.tokenID)
	if err := s.writeSecureMessage("MSG", s.channelID, symHeader, reqBody); err != nil {
		return nil, err
	}
	msgType, respBody, err := s.readSecureMessage()
	if err != nil {
		return nil, err
	}
	if msgType != "MSG" {
		return nil, fmt.Errorf("unexpected response message type %q", msgType)
	}
	r := &reader{b: respBody}
	gotType := readTypeID(r)
	if r.err != nil {
		return nil, r.err
	}
	if gotType != expectedRespTypeID {
		return nil, fmt.Errorf("unexpected response type %d, want %d", gotType, expectedRespTypeID)
	}
	return respBody[r.off:], nil
}

func (s *session) openSecureChannel() error {
	if err := s.setDeadline(); err != nil {
		return err
	}
	mat := s.security
	if mat == nil {
		mat = &securityMaterial{PolicyURI: securityPolicyNone, Mode: messageSecurityModeNone}
	}
	if mat.PolicyURI != securityPolicyNone {
		// Basic256Sha256 selection is validated in resolveSecurity; the
		// asymmetric OPN + symmetric MSG crypto path is still scaffolding.
		return fmt.Errorf("opcua: security policy %s is configured but secure-channel crypto is not implemented", mat.PolicyURI)
	}
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idOpenSecureChannelRequest))
	body.Write(s.requestHeader(nil, handle))
	writeUint32(body, 0)             // ClientProtocolVersion
	writeInt32(body, 0)              // RequestType = Issue
	writeInt32(body, mat.Mode)       // SecurityMode
	writeByteString(body, nil, true) // ClientNonce
	writeUint32(body, 600000)        // RequestedLifetime (ms)

	secHeader := &bytes.Buffer{}
	writeString(secHeader, mat.PolicyURI, false)
	writeByteString(secHeader, nil, true) // SenderCertificate
	writeByteString(secHeader, nil, true) // ReceiverCertificateThumbprint

	if err := s.writeSecureMessage("OPN", 0, secHeader.Bytes(), body.Bytes()); err != nil {
		return err
	}
	msgType, respBody, err := s.readSecureMessage()
	if err != nil {
		return err
	}
	if msgType != "OPN" {
		return fmt.Errorf("unexpected response message type %q", msgType)
	}
	r := &reader{b: respBody}
	gotType := readTypeID(r)
	if gotType != idOpenSecureChannelResponse {
		return fmt.Errorf("unexpected OpenSecureChannel response type %d", gotType)
	}
	status := readResponseHeader(r)
	if status != 0 {
		return fmt.Errorf("status 0x%08x", status)
	}
	r.u32() // ServerProtocolVersion
	channelID := r.u32()
	tokenID := r.u32()
	r.i64() // CreatedAt
	r.u32() // RevisedLifetime
	if r.err != nil {
		return r.err
	}
	s.channelID = channelID
	s.tokenID = tokenID
	return nil
}

func (s *session) createSession(endpoint string) error {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idCreateSessionRequest))
	body.Write(s.requestHeader(nil, handle))
	writeString(body, "urn:nodra:opcua-client", false) // ApplicationUri
	writeString(body, "urn:zyvor:nodra", false)        // ProductUri
	body.WriteByte(0x03)                               // LocalizedText mask: locale+text present
	writeString(body, "en", false)
	writeString(body, "Nodra OPC-UA connector", false)
	writeInt32(body, 1)                     // ApplicationType = Client
	writeString(body, "", true)             // GatewayServerUri
	writeString(body, "", true)             // DiscoveryProfileUri
	writeInt32(body, 0)                     // DiscoveryUrls count
	writeString(body, "", true)             // ServerUri
	writeString(body, endpoint, false)      // EndpointUrl
	writeString(body, "nodra-opcua", false) // SessionName
	writeByteString(body, nil, true)        // ClientNonce
	writeByteString(body, nil, true)        // ClientCertificate
	writeFloat64(body, 1200000)             // RequestedSessionTimeout (ms)
	writeUint32(body, 4*1024*1024)          // MaxResponseMessageSize

	respBody, err := s.serviceCall(idCreateSessionResponse, body.Bytes())
	if err != nil {
		return err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return fmt.Errorf("status 0x%08x", status)
	}
	readNodeIdRaw(r) // SessionId
	authToken := readNodeIdRaw(r)
	r.f64()     // RevisedSessionTimeout
	r.byteStr() // ServerNonce
	r.byteStr() // ServerCertificate
	if r.err != nil {
		return r.err
	}
	s.authToken = authToken
	return nil
}

func (s *session) activateSession() error {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idActivateSessionRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	writeString(body, "", true)      // ClientSignature.Algorithm
	writeByteString(body, nil, true) // ClientSignature.Signature
	writeInt32(body, 0)              // ClientSoftwareCertificates count
	writeInt32(body, 0)              // LocaleIds count

	inner := &bytes.Buffer{}
	writeString(inner, "anonymous", false) // AnonymousIdentityToken.PolicyId
	body.Write(typeIDBytes(idAnonymousIdentityToken))
	body.WriteByte(0x01) // ExtensionObject body encoding = ByteString
	writeByteString(body, inner.Bytes(), false)

	writeString(body, "", true)      // UserTokenSignature.Algorithm
	writeByteString(body, nil, true) // UserTokenSignature.Signature

	respBody, err := s.serviceCall(idActivateSessionResponse, body.Bytes())
	if err != nil {
		return err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return fmt.Errorf("status 0x%08x", status)
	}
	return nil
}

func (s *session) read(nodeIDs []NodeID) ([]DataValue, error) {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idReadRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	writeFloat64(body, 0) // MaxAge
	writeInt32(body, 0)   // TimestampsToReturn = Source
	writeInt32(body, int32(len(nodeIDs)))
	for _, id := range nodeIDs {
		body.Write(id.encode())
		writeUint32(body, attributeIDValue)
		writeString(body, "", true) // IndexRange
		writeUint16(body, 0)        // DataEncoding.NamespaceIndex
		writeString(body, "", true) // DataEncoding.Name
	}
	respBody, err := s.serviceCall(idReadResponse, body.Bytes())
	if err != nil {
		return nil, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return nil, fmt.Errorf("status 0x%08x", status)
	}
	n := r.i32()
	if n < 0 {
		n = 0
	}
	out := make([]DataValue, 0, n)
	for i := int32(0); i < n; i++ {
		dv, err := decodeDataValue(r)
		if err != nil {
			return nil, fmt.Errorf("decode DataValue[%d]: %w", i, err)
		}
		out = append(out, dv)
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}

func (s *session) close() error {
	if len(s.authToken) > 0 {
		_ = s.closeSession()
	}
	_ = s.closeSecureChannel()
	return s.conn.Close()
}
func (s *session) closeSession() error {
	if err := s.setDeadline(); err != nil {
		return err
	}
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idCloseSessionRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	body.WriteByte(0x01) // DeleteSubscriptions = true
	_, err := s.serviceCall(idCloseSessionResponse, body.Bytes())
	return err
}
func (s *session) closeSecureChannel() error {
	if err := s.setDeadline(); err != nil {
		return err
	}
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idCloseSecureChannelRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	symHeader := make([]byte, 4)
	binary.LittleEndian.PutUint32(symHeader, s.tokenID)
	// Best-effort: many servers close the socket without acknowledging CLO.
	return s.writeSecureMessage("CLO", s.channelID, symHeader, body.Bytes())
}

// Client reads OPC-UA node values over a fresh connect/handshake/Read/close
// cycle per call, mirroring connectors/modbus's per-call-dial Client.
type Client struct {
	Endpoint string
	Timeout  time.Duration
	Security SecurityConfig
}

func (c *Client) Read(ctx context.Context, nodeIDs []NodeID) ([]DataValue, error) {
	if len(nodeIDs) == 0 {
		return nil, errors.New("opcua: at least one NodeID is required")
	}
	to := c.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	s, err := dial(ctx, c.Endpoint, to, c.Security)
	if err != nil {
		return nil, err
	}
	defer s.close()
	return s.read(nodeIDs)
}
