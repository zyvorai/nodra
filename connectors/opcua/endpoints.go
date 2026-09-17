// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"time"
)

// Well-known OPC-UA namespace-0 numeric NodeIds for FindServers,
// GetEndpoints and Browse (Part 6 NodeIds — stable across UA 1.0x).
const (
	idFindServersRequest   = 420
	idFindServersResponse  = 423
	idGetEndpointsRequest  = 428
	idGetEndpointsResponse = 431
	idBrowseRequest        = 527
	idBrowseResponse       = 530
)

// ApplicationDescription is a decoded ApplicationDescription (Part 4 §7.1).
type ApplicationDescription struct {
	ApplicationURI      string
	ProductURI          string
	ApplicationName     string
	ApplicationType     int32
	GatewayServerURI    string
	DiscoveryProfileURI string
	DiscoveryURLs       []string
}

// EndpointDescription is a decoded EndpointDescription (Part 4 §7.10).
// UserIdentityTokens are consumed to keep the reader offset correct but not
// exposed — v1 only ever connects anonymously regardless of what a server
// advertises.
type EndpointDescription struct {
	EndpointURL         string
	Server              ApplicationDescription
	ServerCertificate   []byte
	SecurityMode        int32
	SecurityPolicyURI   string
	TransportProfileURI string
	SecurityLevel       byte
}

// ReferenceDescription is a decoded ReferenceDescription (Part 4 §7.28).
// TypeDefinition is consumed to keep the reader offset correct but not
// exposed, matching this package's v1 "don't guess, don't expose the half
// we don't need" posture.
type ReferenceDescription struct {
	ReferenceTypeID     NodeID
	IsForward           bool
	TargetNodeID        NodeID
	TargetNamespaceURI  string
	TargetServerIndex   uint32
	BrowseNameNamespace uint16
	BrowseName          string
	DisplayName         string
	NodeClass           int32
}

// BrowseDescription is one entry of a Browse request (Part 4 §7.4).
// ReferenceTypeID nil means "no filter" (encoded as a null NodeId).
type BrowseDescription struct {
	NodeID          NodeID
	Direction       int32 // 0=Forward, 1=Inverse, 2=Both
	ReferenceTypeID *NodeID
	IncludeSubtypes bool
	NodeClassMask   uint32
	ResultMask      uint32
}

// BrowseResult is one decoded BrowseResult (Part 4 §7.5).
type BrowseResult struct {
	StatusCode        uint32
	ContinuationPoint []byte
	References        []ReferenceDescription
}

func writeStrings(buf *bytes.Buffer, arr []string) {
	if arr == nil {
		writeInt32(buf, -1)
		return
	}
	writeInt32(buf, int32(len(arr)))
	for _, s := range arr {
		writeString(buf, s, false)
	}
}

func readStrings(r *reader) []string {
	n := r.i32()
	if n <= 0 || r.err != nil {
		return nil
	}
	out := make([]string, 0, n)
	for i := int32(0); i < n; i++ {
		out = append(out, r.str())
	}
	return out
}

// readLocalizedText decodes a LocalizedText (Part 6 §5.2.2.14): a mask byte
// (bit0=Locale present, bit1=Text present) followed by whichever fields it
// flags, in that order.
func readLocalizedText(r *reader) (locale, text string) {
	mask := r.u8()
	if mask&0x01 != 0 {
		locale = r.str()
	}
	if mask&0x02 != 0 {
		text = r.str()
	}
	return
}

func readApplicationDescription(r *reader) ApplicationDescription {
	var a ApplicationDescription
	a.ApplicationURI = r.str()
	a.ProductURI = r.str()
	_, a.ApplicationName = readLocalizedText(r)
	a.ApplicationType = r.i32()
	a.GatewayServerURI = r.str()
	a.DiscoveryProfileURI = r.str()
	a.DiscoveryURLs = readStrings(r)
	return a
}

// skipUserTokenPolicy consumes one UserTokenPolicy (Part 4 §7.41) to keep
// the reader offset correct; v1 always connects anonymously so the decoded
// fields aren't exposed.
func skipUserTokenPolicy(r *reader) {
	r.str() // PolicyId
	r.i32() // TokenType
	r.str() // IssuedTokenType
	r.str() // IssuerEndpointUrl
	r.str() // SecurityPolicyUri
}

func readEndpointDescription(r *reader) EndpointDescription {
	var e EndpointDescription
	e.EndpointURL = r.str()
	e.Server = readApplicationDescription(r)
	e.ServerCertificate = r.byteStr()
	e.SecurityMode = r.i32()
	e.SecurityPolicyURI = r.str()
	if n := r.i32(); n > 0 {
		for i := int32(0); i < n; i++ {
			skipUserTokenPolicy(r)
		}
	}
	e.TransportProfileURI = r.str()
	e.SecurityLevel = r.u8()
	return e
}

// readExpandedNodeID decodes an ExpandedNodeId (Part 6 §5.2.2.10): a normal
// NodeId encoding whose top two encoding-mask bits (0x80=NamespaceUri
// present, 0x40=ServerIndex present) are set independently of the base
// NodeId form in the low 6 bits.
func readExpandedNodeID(r *reader) (nodeID []byte, namespaceURI string, serverIndex uint32) {
	start := r.off
	enc := r.u8()
	switch enc & 0x3F {
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
		r.fail(fmt.Errorf("unsupported ExpandedNodeId encoding 0x%02x", enc))
		return nil, "", 0
	}
	if r.err != nil {
		return nil, "", 0
	}
	nodeID = append([]byte(nil), r.b[start:r.off]...)
	if enc&0x80 != 0 {
		namespaceURI = r.str()
	}
	if enc&0x40 != 0 {
		serverIndex = r.u32()
	}
	return
}

func readReferenceDescription(r *reader) ReferenceDescription {
	var rd ReferenceDescription
	if raw := readNodeIdRaw(r); raw != nil {
		if id, _, err := decodeNodeID(raw); err == nil {
			rd.ReferenceTypeID = id
		}
	}
	rd.IsForward = r.u8() != 0
	targetRaw, ns, idx := readExpandedNodeID(r)
	if targetRaw != nil {
		if id, _, err := decodeNodeID(targetRaw); err == nil {
			rd.TargetNodeID = id
		}
	}
	rd.TargetNamespaceURI = ns
	rd.TargetServerIndex = idx
	rd.BrowseNameNamespace = r.u16()
	rd.BrowseName = r.str()
	_, rd.DisplayName = readLocalizedText(r)
	rd.NodeClass = r.i32()
	readExpandedNodeID(r) // TypeDefinition — consumed, not exposed
	return rd
}

func readBrowseResult(r *reader) BrowseResult {
	var br BrowseResult
	br.StatusCode = r.u32()
	br.ContinuationPoint = r.byteStr()
	if n := r.i32(); n > 0 {
		br.References = make([]ReferenceDescription, 0, n)
		for i := int32(0); i < n; i++ {
			br.References = append(br.References, readReferenceDescription(r))
		}
	}
	return br
}

func writeBrowseDescription(buf *bytes.Buffer, bd BrowseDescription) {
	buf.Write(bd.NodeID.encode())
	writeInt32(buf, bd.Direction)
	if bd.ReferenceTypeID != nil {
		buf.Write(bd.ReferenceTypeID.encode())
	} else {
		buf.Write([]byte{0x00, 0x00}) // null NodeId (two-byte form, ns=0 id=0)
	}
	if bd.IncludeSubtypes {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	writeUint32(buf, bd.NodeClassMask)
	writeUint32(buf, bd.ResultMask)
}

// openChannel dials and completes Hello/Ack + OpenSecureChannel only — the
// unsecured pre-session channel Discovery services (GetEndpoints,
// FindServers) run over, per Part 4 §5.4. Deliberately does not reuse
// dial()'s body (which continues on to CreateSession/ActivateSession) to
// avoid touching that already-tested function.
func openChannel(ctx context.Context, endpoint string, timeout time.Duration) (*session, error) {
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
	s := &session{
		conn: conn, timeout: timeout,
		security: &securityMaterial{PolicyURI: securityPolicyNone, Mode: messageSecurityModeNone},
	}
	if err := s.hello(endpoint); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	if err := s.openSecureChannel(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open secure channel: %w", err)
	}
	return s, nil
}

// GetEndpoints returns the endpoints a server advertises for endpoint. Runs
// over the unsecured pre-session channel — no CreateSession/ActivateSession
// is performed. session.close() safely skips CloseSession here since no
// session (authToken) was ever established.
func GetEndpoints(ctx context.Context, endpoint string, timeout time.Duration) ([]EndpointDescription, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	s, err := openChannel(ctx, endpoint, timeout)
	if err != nil {
		return nil, err
	}
	defer s.close()
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idGetEndpointsRequest))
	body.Write(s.requestHeader(nil, handle))
	writeString(body, endpoint, false) // EndpointUrl
	writeStrings(body, nil)            // LocaleIds
	writeStrings(body, nil)            // ProfileUris
	respBody, err := s.serviceCall(idGetEndpointsResponse, body.Bytes())
	if err != nil {
		return nil, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return nil, fmt.Errorf("status 0x%08x", status)
	}
	n := r.i32()
	if r.err != nil {
		return nil, r.err
	}
	if n < 0 {
		n = 0
	}
	out := make([]EndpointDescription, 0, n)
	for i := int32(0); i < n; i++ {
		out = append(out, readEndpointDescription(r))
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}

// FindServers returns the servers a discovery endpoint knows about. Runs
// over the unsecured pre-session channel, same as GetEndpoints.
func FindServers(ctx context.Context, endpoint string, timeout time.Duration) ([]ApplicationDescription, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	s, err := openChannel(ctx, endpoint, timeout)
	if err != nil {
		return nil, err
	}
	defer s.close()
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idFindServersRequest))
	body.Write(s.requestHeader(nil, handle))
	writeString(body, endpoint, false) // EndpointUrl
	writeStrings(body, nil)            // LocaleIds
	writeStrings(body, nil)            // ServerUris
	respBody, err := s.serviceCall(idFindServersResponse, body.Bytes())
	if err != nil {
		return nil, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return nil, fmt.Errorf("status 0x%08x", status)
	}
	n := r.i32()
	if r.err != nil {
		return nil, r.err
	}
	if n < 0 {
		n = 0
	}
	out := make([]ApplicationDescription, 0, n)
	for i := int32(0); i < n; i++ {
		out = append(out, readApplicationDescription(r))
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}

// browse issues a BrowseRequest for the given targets over a null View.
func (s *session) browse(nodesToBrowse []BrowseDescription) ([]BrowseResult, error) {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idBrowseRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	body.Write([]byte{0x00, 0x00}) // ViewDescription.ViewId: null NodeId
	writeInt64(body, 0)            // ViewDescription.Timestamp
	writeUint32(body, 0)           // ViewDescription.ViewVersion
	writeUint32(body, 0)           // RequestedMaxReferencesPerNode: server decides
	writeInt32(body, int32(len(nodesToBrowse)))
	for _, bd := range nodesToBrowse {
		writeBrowseDescription(body, bd)
	}
	respBody, err := s.serviceCall(idBrowseResponse, body.Bytes())
	if err != nil {
		return nil, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return nil, fmt.Errorf("status 0x%08x", status)
	}
	n := r.i32()
	if r.err != nil {
		return nil, r.err
	}
	if n < 0 {
		n = 0
	}
	out := make([]BrowseResult, 0, n)
	for i := int32(0); i < n; i++ {
		out = append(out, readBrowseResult(r))
	}
	if dn := r.i32(); dn > 0 {
		for i := int32(0); i < dn; i++ {
			readDiagnosticInfo(r)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}

// Browse walks the address space from nodeID over a fresh
// connect/handshake/Browse/close cycle, mirroring Client.Read. A nil
// referenceTypeID browses all reference types.
func (c *Client) Browse(ctx context.Context, nodeID NodeID, direction int32, referenceTypeID *NodeID) ([]ReferenceDescription, error) {
	to := c.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	s, err := dial(ctx, c.Endpoint, to, c.Security)
	if err != nil {
		return nil, err
	}
	defer s.close()
	results, err := s.browse([]BrowseDescription{{
		NodeID: nodeID, Direction: direction, ReferenceTypeID: referenceTypeID,
		IncludeSubtypes: true, NodeClassMask: 0, ResultMask: 0x3F,
	}})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	if results[0].StatusCode != 0 {
		return nil, fmt.Errorf("browse status 0x%08x", results[0].StatusCode)
	}
	return results[0].References, nil
}
