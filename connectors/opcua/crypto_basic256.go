// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SHA-1 is mandated by Part 7 for thumbprints and the rsa-oaep MGF1.
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// Basic256Sha256 algorithm parameters (Part 7 §6.6.2 / Part 6 §6.7):
//
//	AsymmetricEncryption  RSA-OAEP (MGF1 with SHA-1)
//	AsymmetricSignature   RSA-PKCS#1 v1.5 with SHA-256
//	SymmetricEncryption   AES-256-CBC
//	SymmetricSignature    HMAC-SHA256
//	KeyDerivation         P_SHA256
const (
	// basic256NonceLength is SecureChannelNonceLength for this policy.
	basic256NonceLength = 32
	// basic256SignatureSize is the HMAC-SHA256 output length.
	basic256SignatureSize = 32
	// basic256SymKeyLength is the AES-256 key length.
	basic256SymKeyLength = 32
	// basic256SymIVLength is the AES CBC block/IV length.
	basic256SymIVLength = 16
	// basic256MinAsymKeyBits is the smallest RSA key this policy allows.
	basic256MinAsymKeyBits = 2048

	// rsaOAEPSHA1Overhead is 2*hLen+2 for OAEP with SHA-1 (hLen = 20), i.e.
	// how much of each RSA block the padding consumes.
	rsaOAEPSHA1Overhead = 42

	// signatureAlgorithmRSASHA256 is the SignatureData.Algorithm URI used by
	// the CreateSession/ActivateSession signatures under this policy.
	signatureAlgorithmRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
)

// channelCrypto is the parsed key material for one Basic256Sha256 channel:
// our own application-instance certificate and private key, plus the
// server's certificate, which is the trust anchor every server signature is
// checked against.
type channelCrypto struct {
	clientCertDER []byte
	clientKey     *rsa.PrivateKey
	serverCertDER []byte
	serverKey     *rsa.PublicKey
}

// newChannelCrypto parses PEM (or bare DER) certificates and a PEM private
// key, and rejects anything Basic256Sha256 cannot use: non-RSA keys, keys
// below 2048 bits, or a private key that does not belong to the client
// certificate.
func newChannelCrypto(clientCertPEM, clientKeyPEM, serverCertPEM []byte) (*channelCrypto, error) {
	clientCertDER, clientPub, err := parseCertificate(clientCertPEM)
	if err != nil {
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	clientKey, err := parseRSAPrivateKey(clientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("client private key: %w", err)
	}
	if clientKey.PublicKey.N.Cmp(clientPub.N) != 0 || clientKey.PublicKey.E != clientPub.E {
		return nil, errors.New("client private key does not match the client certificate")
	}
	serverCertDER, serverPub, err := parseCertificate(serverCertPEM)
	if err != nil {
		return nil, fmt.Errorf("server certificate: %w", err)
	}
	if n := clientKey.N.BitLen(); n < basic256MinAsymKeyBits {
		return nil, fmt.Errorf("client RSA key is %d bits, Basic256Sha256 requires at least %d", n, basic256MinAsymKeyBits)
	}
	if n := serverPub.N.BitLen(); n < basic256MinAsymKeyBits {
		return nil, fmt.Errorf("server RSA key is %d bits, Basic256Sha256 requires at least %d", n, basic256MinAsymKeyBits)
	}
	return &channelCrypto{
		clientCertDER: clientCertDER,
		clientKey:     clientKey,
		serverCertDER: serverCertDER,
		serverKey:     serverPub,
	}, nil
}

// parseCertificate accepts a PEM CERTIFICATE block or bare DER and returns
// the DER bytes (which is what goes on the wire) plus the RSA public key.
func parseCertificate(data []byte) ([]byte, *rsa.PublicKey, error) {
	der := data
	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "CERTIFICATE" {
			return nil, nil, fmt.Errorf("expected a CERTIFICATE PEM block, got %q", block.Type)
		}
		der = block.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse DER: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("public key is %T, Basic256Sha256 requires RSA", cert.PublicKey)
	}
	return der, pub, nil
}

// parseRSAPrivateKey accepts PKCS#1 or PKCS#8 PEM, or bare DER in either
// encoding.
func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	der := data
	if block, _ := pem.Decode(data); block != nil {
		der = block.Bytes
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	anyKey, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse as PKCS#1 or PKCS#8: %w", err)
	}
	key, ok := anyKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is %T, Basic256Sha256 requires RSA", anyKey)
	}
	return key, nil
}

// certThumbprint is the SHA-1 digest of a DER certificate — the
// ReceiverCertificateThumbprint carried in the asymmetric security header.
// SHA-1 here is the algorithm the spec names, not a security choice.
func certThumbprint(der []byte) []byte {
	sum := sha1.Sum(der) //nolint:gosec // Part 6 §6.7.2 mandates SHA-1 thumbprints.
	return sum[:]
}

// rsaOAEPEncrypt encrypts plain block by block with RSA-OAEP/MGF1-SHA-1.
// plain must already be a whole number of plaintext blocks, which the
// message padding guarantees.
func rsaOAEPEncrypt(pub *rsa.PublicKey, plain []byte) ([]byte, error) {
	blockSize := pub.Size() - rsaOAEPSHA1Overhead
	if blockSize <= 0 {
		return nil, errors.New("opcua: RSA key too small for OAEP")
	}
	if len(plain) == 0 || len(plain)%blockSize != 0 {
		return nil, fmt.Errorf("opcua: %d plaintext bytes is not a multiple of the %d-byte OAEP block", len(plain), blockSize)
	}
	out := make([]byte, 0, len(plain)/blockSize*pub.Size())
	for off := 0; off < len(plain); off += blockSize {
		block, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, plain[off:off+blockSize], nil) //nolint:gosec // MGF1-SHA-1 is what the rsa-oaep URI means for this policy.
		if err != nil {
			return nil, err
		}
		out = append(out, block...)
	}
	return out, nil
}

// rsaOAEPDecrypt is the inverse of rsaOAEPEncrypt.
func rsaOAEPDecrypt(priv *rsa.PrivateKey, ciphertext []byte) ([]byte, error) {
	blockSize := priv.Size()
	if len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return nil, fmt.Errorf("opcua: %d ciphertext bytes is not a multiple of the %d-byte RSA block", len(ciphertext), blockSize)
	}
	out := make([]byte, 0, len(ciphertext))
	for off := 0; off < len(ciphertext); off += blockSize {
		block, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, ciphertext[off:off+blockSize], nil) //nolint:gosec // see rsaOAEPEncrypt.
		if err != nil {
			return nil, err
		}
		out = append(out, block...)
	}
	return out, nil
}

// rsaSignSHA256 produces the RSA-PKCS#1 v1.5 SHA-256 signature this policy
// uses for asymmetric message signatures and for the session signatures.
func rsaSignSHA256(priv *rsa.PrivateKey, data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)
	return rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
}

func rsaVerifySHA256(pub *rsa.PublicKey, data, sig []byte) error {
	sum := sha256.Sum256(data)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
}

func aesCBCEncrypt(key, iv, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("opcua: IV is %d bytes, want %d", len(iv), block.BlockSize())
	}
	if len(plain)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("opcua: %d plaintext bytes is not a multiple of the AES block size", len(plain))
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	return out, nil
}

func aesCBCDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("opcua: IV is %d bytes, want %d", len(iv), block.BlockSize())
	}
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("opcua: %d ciphertext bytes is not a multiple of the AES block size", len(ciphertext))
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	return out, nil
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func verifyHMACSHA256(key, data, sig []byte) bool {
	return hmac.Equal(hmacSHA256(key, data), sig)
}

// pSHA256 is the TLS 1.2 P_hash construction (RFC 5246 §5) with HMAC-SHA256,
// which Part 6 §6.7.5 reuses verbatim as the OPC-UA key-derivation function:
//
//	A(0) = seed, A(i) = HMAC(secret, A(i-1))
//	P_hash = HMAC(secret, A(1)||seed) || HMAC(secret, A(2)||seed) || ...
func pSHA256(secret, seed []byte, length int) []byte {
	if length <= 0 {
		return nil
	}
	out := make([]byte, 0, length+sha256.Size)
	a := seed
	for len(out) < length {
		a = hmacSHA256(secret, a)
		block := make([]byte, 0, len(a)+len(seed))
		block = append(block, a...)
		block = append(block, seed...)
		out = append(out, hmacSHA256(secret, block)...)
	}
	return out[:length]
}

// channelKeys is one derived key set for a secure channel. The client
// secures the messages it sends with the client* keys and verifies/decrypts
// what the server sends with the server* keys; the server does the mirror
// image.
type channelKeys struct {
	clientSigning    []byte
	clientEncrypting []byte
	clientIV         []byte
	serverSigning    []byte
	serverEncrypting []byte
	serverIV         []byte
}

// deriveChannelKeys implements Part 6 §6.7.5: the client key set is derived
// with the server nonce as the secret and the client nonce as the seed, and
// the server key set the other way round. Each set is
// signingKey || encryptingKey || initializationVector.
func deriveChannelKeys(clientNonce, serverNonce []byte) *channelKeys {
	const total = basic256SymKeyLength*2 + basic256SymIVLength
	c := pSHA256(serverNonce, clientNonce, total)
	s := pSHA256(clientNonce, serverNonce, total)
	split := func(b []byte) (sign, enc, iv []byte) {
		return b[:basic256SymKeyLength],
			b[basic256SymKeyLength : basic256SymKeyLength*2],
			b[basic256SymKeyLength*2:]
	}
	var k channelKeys
	k.clientSigning, k.clientEncrypting, k.clientIV = split(c)
	k.serverSigning, k.serverEncrypting, k.serverIV = split(s)
	return &k
}

// messagePadding builds the Part 6 §6.7.2 Padding field —
// PaddingSize, the padding bytes themselves (all equal to the low byte of
// the count), and ExtraPaddingSize when the encrypting key is larger than
// 2048 bits — sized so that dataLen plus the padding field plus the
// signature is a whole number of blockSize plaintext blocks.
func messagePadding(dataLen, signatureSize, blockSize int, extraByte bool) []byte {
	header := 1
	if extraByte {
		header = 2
	}
	n := (blockSize - (dataLen+header+signatureSize)%blockSize) % blockSize
	out := make([]byte, 0, n+header)
	out = append(out, byte(n))
	for i := 0; i < n; i++ {
		out = append(out, byte(n))
	}
	if extraByte {
		out = append(out, byte(n>>8))
	}
	return out
}

// stripMessagePadding removes and validates the padding messagePadding
// added. b must already have the signature removed.
func stripMessagePadding(b []byte, extraByte bool) ([]byte, error) {
	header := 1
	if extraByte {
		header = 2
	}
	if len(b) < header {
		return nil, errors.New("opcua: padded message is shorter than its padding field")
	}
	n := int(b[len(b)-1])
	if extraByte {
		n = int(b[len(b)-1])<<8 | int(b[len(b)-2])
	}
	dataEnd := len(b) - n - header
	if dataEnd < 0 {
		return nil, fmt.Errorf("opcua: padding of %d bytes exceeds the %d-byte message", n, len(b))
	}
	if b[dataEnd] != byte(n) {
		return nil, errors.New("opcua: PaddingSize does not match the padding bytes")
	}
	for _, p := range b[dataEnd+1 : dataEnd+1+n] {
		if p != byte(n) {
			return nil, errors.New("opcua: padding byte does not match PaddingSize")
		}
	}
	return b[:dataEnd], nil
}

// newNonce returns a fresh SecureChannelNonceLength random nonce.
func newNonce() ([]byte, error) {
	b := make([]byte, basic256NonceLength)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("opcua: generate nonce: %w", err)
	}
	return b, nil
}
