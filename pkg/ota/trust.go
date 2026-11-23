// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package ota

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Verification errors. They are sentinels so callers (and tests) can tell a
// tampered manifest from a downgrade from a clock problem.
var (
	ErrUnknownKey      = errors.New("ota: manifest signed by an unknown key")
	ErrBadSignature    = errors.New("ota: manifest signature invalid")
	ErrNotYetValid     = errors.New("ota: manifest is not yet valid (issued in the future beyond allowed clock skew)")
	ErrExpired         = errors.New("ota: manifest has expired")
	ErrBadValidity     = errors.New("ota: manifest validity window is missing or malformed")
	ErrClockRegression = errors.New("ota: device clock is behind a manifest already accepted (possible clock rollback)")
	ErrCounterMissing  = errors.New("ota: manifest counter must be positive")
	ErrRollback        = errors.New("ota: manifest counter is older than the installed version (downgrade)")
	ErrReplay          = errors.New("ota: manifest counter equals the installed version (replay)")
	ErrDigestMismatch  = errors.New("ota: artifact sha256 does not match the manifest")
	ErrTruncated       = errors.New("ota: artifact is shorter than size_bytes (truncated)")
	ErrOversized       = errors.New("ota: artifact is longer than size_bytes")
)

const signingDomain = "nodra-ota-manifest-v1\n"

// SigningBytes is the exact byte string that is signed: a domain-separation
// prefix plus the JSON encoding of the manifest with the signature emptied and
// times normalised to UTC. Every other field, including counter, key_id and
// the validity window, is covered, so any change invalidates the signature.
func (m Manifest) SigningBytes() []byte {
	c := m
	c.Signature = ""
	c.IssuedAt = c.IssuedAt.UTC()
	c.ExpiresAt = c.ExpiresAt.UTC()
	b, _ := json.Marshal(c) // struct field order is fixed; cannot fail for this type
	return append([]byte(signingDomain), b...)
}

// Sign sets m.Signature to the base64 Ed25519 signature of SigningBytes. It is
// provided for release tooling and tests; devices only verify.
func Sign(priv ed25519.PrivateKey, m *Manifest) {
	m.Signature = ""
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.SigningBytes()))
}

// Floor is the anti-rollback high-water mark for one lineage.
type Floor struct {
	Counter  uint64    `json:"counter"`
	SHA256   string    `json:"sha256"`
	IssuedAt time.Time `json:"issued_at"`
}

// CounterStore persists anti-rollback floors. Implementations must fail
// closed: a store that cannot be read or written must return an error rather
// than report "no floor".
type CounterStore interface {
	Get(lineage string) (Floor, bool, error)
	Set(lineage string, f Floor) error
}

// Lineage is the sequence a counter belongs to: counters are only comparable
// for the same artifact type, architecture and board.
func Lineage(m Manifest) string {
	return string(m.Type) + "/" + m.Architecture + "/" + m.Board
}

// MemCounters is an in-memory CounterStore.
type MemCounters struct {
	mu sync.Mutex
	m  map[string]Floor
}

func (s *MemCounters) Get(l string) (Floor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.m[l]
	return f, ok, nil
}

func (s *MemCounters) Set(l string, f Floor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Floor{}
	}
	s.m[l] = f
	return nil
}

// FileCounters is a CounterStore persisted to one JSON file with an atomic
// write. A corrupt file is an error, never an implicit reset to zero.
type FileCounters struct {
	mu   sync.Mutex
	path string
	m    map[string]Floor
}

const maxCounterFile = 1 << 20

// OpenFileCounters loads (or creates lazily) the floor file at path.
func OpenFileCounters(path string) (*FileCounters, error) {
	s := &FileCounters{path: path, m: map[string]Floor{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	case len(b) > maxCounterFile:
		return nil, errors.New("ota: counter file too large")
	}
	if err = json.Unmarshal(b, &s.m); err != nil {
		return nil, fmt.Errorf("ota: counter file corrupt: %w", err)
	}
	return s, nil
}

func (s *FileCounters) Get(l string) (Floor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.m[l]
	return f, ok, nil
}

func (s *FileCounters) Set(l string, f Floor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]Floor, len(s.m)+1)
	for k, v := range s.m {
		next[k] = v
	}
	next[l] = f
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = atomicWrite(s.path, b); err != nil {
		return err
	}
	s.m = next
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ota-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Verifier checks signed manifests and artifacts on the device. It never
// executes anything: it only reads bytes and compares.
type Verifier struct {
	// Keys is the device-held trust root: key id -> Ed25519 public key.
	Keys map[string]ed25519.PublicKey
	// Now is the clock (injectable). Defaults to time.Now.
	Now func() time.Time
	// MaxSkew is the tolerated clock error in both directions. Default 5m.
	MaxSkew time.Duration
	// MaxValidity caps ExpiresAt-IssuedAt (0 = unlimited).
	MaxValidity time.Duration
	// Counters holds anti-rollback floors. Required.
	Counters CounterStore
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) skew() time.Duration {
	if v.MaxSkew > 0 {
		return v.MaxSkew
	}
	return 5 * time.Minute
}

// VerifyManifest checks structure, the Ed25519 signature against the trusted
// key named by KeyID, and the validity window (with clock-skew tolerance).
// It does not consult anti-rollback state; see CheckCounter / Accept.
func (v *Verifier) VerifyManifest(m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	key, ok := v.Keys[m.KeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return ErrUnknownKey
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(key, m.SigningBytes(), sig) {
		return ErrBadSignature
	}
	if m.IssuedAt.IsZero() || m.ExpiresAt.IsZero() || !m.ExpiresAt.After(m.IssuedAt) {
		return ErrBadValidity
	}
	if v.MaxValidity > 0 && m.ExpiresAt.Sub(m.IssuedAt) > v.MaxValidity {
		return ErrBadValidity
	}
	now := v.now()
	if m.IssuedAt.After(now.Add(v.skew())) {
		return ErrNotYetValid
	}
	if m.ExpiresAt.Before(now.Add(-v.skew())) {
		return ErrExpired
	}
	return nil
}

// CheckCounter enforces anti-rollback: the manifest counter must be strictly
// greater than the last committed one for its lineage, and the device clock
// must not be behind a manifest that was already accepted.
func (v *Verifier) CheckCounter(m Manifest) error {
	if m.Counter == 0 {
		return ErrCounterMissing
	}
	f, ok, err := v.Counters.Get(Lineage(m))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	switch {
	case m.Counter < f.Counter:
		return ErrRollback
	case m.Counter == f.Counter:
		return ErrReplay
	}
	if v.now().Add(v.skew()).Before(f.IssuedAt) {
		return ErrClockRegression
	}
	return nil
}

// VerifyArtifact streams r, which must contain exactly m.SizeBytes bytes
// hashing to m.SHA256. At most SizeBytes+1 bytes are read, so a hostile
// stream cannot make the verifier read unbounded data.
func (v *Verifier) VerifyArtifact(m Manifest, r io.Reader) error {
	want, err := hex.DecodeString(strings.ToLower(m.SHA256))
	if err != nil || len(want) != sha256.Size || m.SizeBytes <= 0 {
		return errors.New("ota: manifest digest or size invalid")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, m.SizeBytes+1))
	if err != nil {
		return fmt.Errorf("ota: reading artifact: %w", err)
	}
	switch {
	case n < m.SizeBytes:
		return ErrTruncated
	case n > m.SizeBytes:
		return ErrOversized
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		return ErrDigestMismatch
	}
	return nil
}

// Accept runs the whole pre-install gate in order: signature and validity,
// anti-rollback, artifact digest. The floor is NOT advanced here.
func (v *Verifier) Accept(m Manifest, artifact io.Reader) error {
	if err := v.VerifyManifest(m); err != nil {
		return err
	}
	if err := v.CheckCounter(m); err != nil {
		return err
	}
	return v.VerifyArtifact(m, artifact)
}

// Commit advances the anti-rollback floor. Call it only once the update has
// passed its health check (state committed); a failed or rolled-back update
// must not raise the floor or the device could never retry. The floor never
// moves backwards.
func (v *Verifier) Commit(m Manifest) error {
	if err := v.CheckCounter(m); err != nil {
		return err
	}
	return v.Counters.Set(Lineage(m), Floor{Counter: m.Counter, SHA256: strings.ToLower(m.SHA256), IssuedAt: m.IssuedAt.UTC()})
}
