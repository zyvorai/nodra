// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Basic256Sha256 chunk layout (Part 6 §6.7.2). Every chunk this package
// sends is a single final ("F") chunk:
//
//	MessageHeader      3-byte type, chunk type, UInt32 size, SecureChannelId
//	SecurityHeader     asymmetric (OPN) or symmetric (MSG/CLO)
//	SequenceHeader     SequenceNumber, RequestId   ── encrypted
//	Body                                           ── encrypted
//	Padding            PaddingSize, padding, [ExtraPaddingSize] ── encrypted
//	Signature                                      ── encrypted
//
// The signature covers the plaintext of everything from the message header
// through the padding; encryption then covers everything from the sequence
// header through the signature. In Sign mode the symmetric chunk carries
// the signature but is not encrypted and therefore has no padding. OPN is
// always signed and encrypted regardless of MessageSecurityMode, because it
// carries the nonces the symmetric keys are derived from.

// secured reports whether this session runs Basic256Sha256 channel crypto.
func (s *session) secured() bool {
	return s.security != nil &&
		s.security.PolicyURI == securityPolicyBasic256Sha256 &&
		s.crypto != nil
}

// chunkHeader is the 12-byte MessageHeader a secure-conversation chunk
// starts with: message type, the final-chunk marker, the total size
// including this header, and the SecureChannelId.
func chunkHeader(msgType string, size int, channelID uint32) []byte {
	hdr := make([]byte, 12)
	copy(hdr[0:3], msgType)
	hdr[3] = 'F'
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(size))
	binary.LittleEndian.PutUint32(hdr[8:12], channelID)
	return hdr
}

func sequenceHeader(seqNum, reqID uint32) []byte {
	h := make([]byte, 8)
	binary.LittleEndian.PutUint32(h[0:4], seqNum)
	binary.LittleEndian.PutUint32(h[4:8], reqID)
	return h
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// writeAsymmetricMessage frames, signs, encrypts and sends one OPN chunk.
func (s *session) writeAsymmetricMessage(msgType string, body []byte) error {
	c := s.crypto
	s.seqNum++
	s.reqID++

	secHeader := &bytes.Buffer{}
	writeString(secHeader, s.security.PolicyURI, false)
	writeByteString(secHeader, c.clientCertDER, false)
	writeByteString(secHeader, certThumbprint(c.serverCertDER), false)

	seqHeader := sequenceHeader(s.seqNum, s.reqID)
	cipherBlock := c.serverKey.Size()
	plainBlock := cipherBlock - rsaOAEPSHA1Overhead
	sigSize := c.clientKey.Size()
	pad := messagePadding(len(seqHeader)+len(body), sigSize, plainBlock, c.serverKey.N.BitLen() > basic256MinAsymKeyBits)

	plainLen := len(seqHeader) + len(body) + len(pad) + sigSize
	encryptedLen := plainLen / plainBlock * cipherBlock
	hdr := chunkHeader(msgType, 12+secHeader.Len()+encryptedLen, s.channelID)

	signed := concat(hdr, secHeader.Bytes(), seqHeader, body, pad)
	sig, err := rsaSignSHA256(c.clientKey, signed)
	if err != nil {
		return fmt.Errorf("opcua: sign %s chunk: %w", msgType, err)
	}
	encrypted, err := rsaOAEPEncrypt(c.serverKey, concat(seqHeader, body, pad, sig))
	if err != nil {
		return fmt.Errorf("opcua: encrypt %s chunk: %w", msgType, err)
	}
	_, err = s.conn.Write(concat(hdr, secHeader.Bytes(), encrypted))
	return err
}

// writeSymmetricMessage frames, signs, optionally encrypts and sends one
// MSG or CLO chunk using the derived channel keys.
func (s *session) writeSymmetricMessage(msgType string, body []byte) error {
	k := s.keys
	if k == nil {
		return errors.New("opcua: symmetric channel keys have not been derived")
	}
	s.seqNum++
	s.reqID++

	secHeader := make([]byte, 4)
	binary.LittleEndian.PutUint32(secHeader, s.tokenID)
	seqHeader := sequenceHeader(s.seqNum, s.reqID)

	encrypt := s.security.Mode == messageSecurityModeSignAndEncrypt
	var pad []byte
	if encrypt {
		pad = messagePadding(len(seqHeader)+len(body), basic256SignatureSize, basic256SymIVLength, false)
	}
	// AES-CBC preserves length, so the announced size is the same whether
	// or not the chunk is encrypted.
	hdr := chunkHeader(msgType, 12+len(secHeader)+len(seqHeader)+len(body)+len(pad)+basic256SignatureSize, s.channelID)

	sig := hmacSHA256(k.clientSigning, concat(hdr, secHeader, seqHeader, body, pad))
	payload := concat(seqHeader, body, pad, sig)
	if encrypt {
		var err error
		if payload, err = aesCBCEncrypt(k.clientEncrypting, k.clientIV, payload); err != nil {
			return fmt.Errorf("opcua: encrypt %s chunk: %w", msgType, err)
		}
	}
	_, err := s.conn.Write(concat(hdr, secHeader, payload))
	return err
}

// decodeSecuredChunk strips the security header, decrypts and verifies one
// received chunk, and returns the service body (starting at the TypeId).
func (s *session) decodeSecuredChunk(msgType string, hdr, payload []byte) ([]byte, error) {
	r := &reader{b: payload}
	r.u32() // SecureChannelId
	var senderCert []byte
	if msgType == "OPN" {
		r.str() // SecurityPolicyUri
		senderCert = r.byteStr()
		thumbprint := r.byteStr()
		if r.err == nil && len(thumbprint) > 0 && !bytes.Equal(thumbprint, certThumbprint(s.crypto.clientCertDER)) {
			return nil, errors.New("opcua: server addressed the response to a different client certificate")
		}
	} else {
		r.u32() // TokenId
	}
	if r.err != nil {
		return nil, r.err
	}
	secHeaderEnd := r.off
	if msgType == "OPN" {
		return s.decodeAsymmetricChunk(hdr, payload, secHeaderEnd, senderCert)
	}
	return s.decodeSymmetricChunk(hdr, payload, secHeaderEnd)
}

func (s *session) decodeAsymmetricChunk(hdr, payload []byte, secHeaderEnd int, senderCert []byte) ([]byte, error) {
	c := s.crypto
	// A server may send its certificate chain, in which case its own
	// certificate comes first.
	if len(senderCert) > 0 && !bytes.HasPrefix(senderCert, c.serverCertDER) {
		return nil, errors.New("opcua: OPN sender certificate does not match server_cert_path")
	}
	plain, err := rsaOAEPDecrypt(c.clientKey, payload[secHeaderEnd:])
	if err != nil {
		return nil, fmt.Errorf("opcua: decrypt OPN chunk: %w", err)
	}
	sigSize := c.serverKey.Size()
	if len(plain) < 8+sigSize {
		return nil, errors.New("opcua: OPN chunk is too short to hold a sequence header and signature")
	}
	signedLen := len(plain) - sigSize
	if err := rsaVerifySHA256(c.serverKey, concat(hdr, payload[:secHeaderEnd], plain[:signedLen]), plain[signedLen:]); err != nil {
		return nil, fmt.Errorf("opcua: OPN signature: %w", err)
	}
	// The server encrypted with our public key, so whether an
	// ExtraPaddingSize byte is present depends on our key size.
	body, err := stripMessagePadding(plain[8:signedLen], c.clientKey.N.BitLen() > basic256MinAsymKeyBits)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (s *session) decodeSymmetricChunk(hdr, payload []byte, secHeaderEnd int) ([]byte, error) {
	k := s.keys
	if k == nil {
		return nil, errors.New("opcua: symmetric channel keys have not been derived")
	}
	data := payload[secHeaderEnd:]
	encrypted := s.security.Mode == messageSecurityModeSignAndEncrypt
	if encrypted {
		var err error
		if data, err = aesCBCDecrypt(k.serverEncrypting, k.serverIV, data); err != nil {
			return nil, fmt.Errorf("opcua: decrypt MSG chunk: %w", err)
		}
	}
	if len(data) < 8+basic256SignatureSize {
		return nil, errors.New("opcua: MSG chunk is too short to hold a sequence header and signature")
	}
	signedLen := len(data) - basic256SignatureSize
	if !verifyHMACSHA256(k.serverSigning, concat(hdr, payload[:secHeaderEnd], data[:signedLen]), data[signedLen:]) {
		return nil, errors.New("opcua: MSG signature does not verify")
	}
	body := data[8:signedLen]
	if encrypted {
		var err error
		if body, err = stripMessagePadding(body, false); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// openSecureChannelBasic256 runs the asymmetric OpenSecureChannel exchange
// and derives the symmetric keys every later MSG on this channel uses.
func (s *session) openSecureChannelBasic256() error {
	nonce, err := newNonce()
	if err != nil {
		return err
	}
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idOpenSecureChannelRequest))
	body.Write(s.requestHeader(nil, handle))
	writeUint32(body, 0)                // ClientProtocolVersion
	writeInt32(body, 0)                 // RequestType = Issue
	writeInt32(body, s.security.Mode)   // SecurityMode
	writeByteString(body, nonce, false) // ClientNonce
	writeUint32(body, 600000)           // RequestedLifetime (ms)

	if err := s.writeAsymmetricMessage("OPN", body.Bytes()); err != nil {
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
	if gotType := readTypeID(r); gotType != idOpenSecureChannelResponse {
		return fmt.Errorf("unexpected OpenSecureChannel response type %d", gotType)
	}
	if status := readResponseHeader(r); status != 0 {
		return fmt.Errorf("status 0x%08x", status)
	}
	r.u32() // ServerProtocolVersion
	channelID := r.u32()
	tokenID := r.u32()
	r.i64() // CreatedAt
	r.u32() // RevisedLifetime
	serverNonce := r.byteStr()
	if r.err != nil {
		return r.err
	}
	if len(serverNonce) != basic256NonceLength {
		return fmt.Errorf("server nonce is %d bytes, Basic256Sha256 requires %d", len(serverNonce), basic256NonceLength)
	}
	s.channelID = channelID
	s.tokenID = tokenID
	s.keys = deriveChannelKeys(nonce, serverNonce)
	return nil
}

// verifyCreateSessionResponse consumes the remainder of a
// CreateSessionResponse (everything after ServerCertificate) and checks the
// ServerSignature over our certificate and session nonce, which is what
// proves the peer holds the private key for server_cert_path.
func (s *session) verifyCreateSessionResponse(r *reader, serverCert []byte) error {
	if len(serverCert) == 0 || !bytes.HasPrefix(serverCert, s.crypto.serverCertDER) {
		return errors.New("server application certificate does not match server_cert_path")
	}
	if n := r.i32(); n > 0 { // ServerEndpoints
		for i := int32(0); i < n; i++ {
			readEndpointDescription(r)
		}
	}
	if n := r.i32(); n > 0 { // ServerSoftwareCertificates
		for i := int32(0); i < n; i++ {
			r.byteStr() // CertificateData
			r.byteStr() // Signature
		}
	}
	algorithm := r.str()
	signature := r.byteStr()
	if r.err != nil {
		return r.err
	}
	if algorithm != "" && algorithm != signatureAlgorithmRSASHA256 {
		return fmt.Errorf("unsupported ServerSignature algorithm %q", algorithm)
	}
	if err := rsaVerifySHA256(s.crypto.serverKey, concat(s.crypto.clientCertDER, s.sessionNonce), signature); err != nil {
		return fmt.Errorf("ServerSignature: %w", err)
	}
	return nil
}

// clientSessionSignature is the ActivateSession ClientSignature: our
// signature over the server's certificate followed by the server nonce from
// CreateSession (Part 4 §5.6.3).
func (s *session) clientSessionSignature() ([]byte, error) {
	return rsaSignSHA256(s.crypto.clientKey, concat(s.serverSessionCert, s.serverSessionNonce))
}
