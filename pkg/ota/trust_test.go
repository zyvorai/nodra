// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package ota

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"testing/iotest"
	"time"
)

var clock0 = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

type fixture struct {
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	now      time.Time
	artifact []byte
	v        *Verifier
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize) // deterministic test key
	priv := ed25519.NewKeyFromSeed(seed)
	f := &fixture{pub: priv.Public().(ed25519.PublicKey), priv: priv, now: clock0, artifact: bytes.Repeat([]byte("firmware-bytes-"), 100)}
	f.v = &Verifier{Keys: map[string]ed25519.PublicKey{"rel-1": f.pub}, Now: func() time.Time { return f.now }, Counters: &MemCounters{}}
	return f
}

func (f *fixture) manifest(counter uint64) Manifest {
	sum := sha256.Sum256(f.artifact)
	m := Manifest{ArtifactID: "fw", Version: "1.2.3", Type: ArtifactFirmware, Architecture: "arm64", Board: "b1",
		SizeBytes: int64(len(f.artifact)), SHA256: hex.EncodeToString(sum[:]), KeyID: "rel-1", Counter: counter,
		IssuedAt: f.now.Add(-time.Hour), ExpiresAt: f.now.Add(24 * time.Hour), RollbackSupported: true}
	Sign(f.priv, &m)
	return m
}

func TestAcceptHappyPath(t *testing.T) {
	f := newFixture(t)
	m := f.manifest(5)
	if err := f.v.Accept(m, bytes.NewReader(f.artifact)); err != nil {
		t.Fatal(err)
	}
	if err := f.v.Commit(m); err != nil {
		t.Fatal(err)
	}
}

func TestManifestTamperMatrix(t *testing.T) {
	f := newFixture(t)
	base := f.manifest(5)
	mut := map[string]func(*Manifest){
		"artifact id":   func(m *Manifest) { m.ArtifactID = "other" },
		"version":       func(m *Manifest) { m.Version = "9.9.9" },
		"type":          func(m *Manifest) { m.Type = ArtifactBootloader },
		"arch":          func(m *Manifest) { m.Architecture = "amd64" },
		"board":         func(m *Manifest) { m.Board = "b2" },
		"size":          func(m *Manifest) { m.SizeBytes++ },
		"sha":           func(m *Manifest) { m.SHA256 = hex.EncodeToString(bytes.Repeat([]byte{1}, 32)) },
		"counter":       func(m *Manifest) { m.Counter = 99 },
		"issued":        func(m *Manifest) { m.IssuedAt = m.IssuedAt.Add(time.Minute) },
		"expires":       func(m *Manifest) { m.ExpiresAt = m.ExpiresAt.Add(24 * time.Hour) },
		"rollback flag": func(m *Manifest) { m.RollbackSupported = false },
		"sbom":          func(m *Manifest) { m.SBOM = "x" },
		"key id swap": func(m *Manifest) {
			f.v.Keys["rel-2"] = f.pub // same key under another name must still fail: key id is signed
			m.KeyID = "rel-2"
		},
	}
	for name, fn := range mut {
		m := base
		fn(&m)
		if err := f.v.VerifyManifest(m); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err=%v, want bad signature", name, err)
		}
	}
	// signature mangling
	raw, _ := base64.StdEncoding.DecodeString(base.Signature)
	cases := map[string]string{
		"empty":     "",
		"not b64":   "!!!!",
		"short":     base64.StdEncoding.EncodeToString(raw[:10]),
		"long":      base64.StdEncoding.EncodeToString(append(append([]byte{}, raw...), 0)),
		"bit flip":  base64.StdEncoding.EncodeToString(append([]byte{raw[0] ^ 1}, raw[1:]...)),
		"all zeros": base64.StdEncoding.EncodeToString(make([]byte, 64)),
	}
	for name, sig := range cases {
		m := base
		m.Signature = sig
		if err := f.v.VerifyManifest(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// unknown key and wrong key
	m := base
	m.KeyID = "nobody"
	if err := f.v.VerifyManifest(m); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{9}, 64)))
	m = f.manifest(5)
	Sign(other, &m)
	if err := f.v.VerifyManifest(m); !errors.Is(err, ErrBadSignature) {
		t.Errorf("attacker key: %v", err)
	}
	// malformed trust root entry
	f.v.Keys["short"] = ed25519.PublicKey{1, 2}
	m = base
	m.KeyID = "short"
	if err := f.v.VerifyManifest(m); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("short key: %v", err)
	}
}

func TestArtifactMatrix(t *testing.T) {
	f := newFixture(t)
	m := f.manifest(1)
	flip := append([]byte(nil), f.artifact...)
	flip[len(flip)/2] ^= 0xff
	cases := []struct {
		name string
		r    io.Reader
		want error
	}{
		{"ok", bytes.NewReader(f.artifact), nil},
		{"bit flip", bytes.NewReader(flip), ErrDigestMismatch},
		{"truncated", bytes.NewReader(f.artifact[:len(f.artifact)-1]), ErrTruncated},
		{"empty", bytes.NewReader(nil), ErrTruncated},
		{"appended", bytes.NewReader(append(append([]byte(nil), f.artifact...), 'x')), ErrOversized},
		{"endless stream", iotest.OneByteReader(zeros{}), ErrOversized},
	}
	for _, c := range cases {
		if err := f.v.VerifyArtifact(m, c.r); !errors.Is(err, c.want) {
			t.Errorf("%s: %v want %v", c.name, err, c.want)
		}
	}
	if err := f.v.VerifyArtifact(m, iotest.ErrReader(errors.New("disk gone"))); err == nil {
		t.Error("read error accepted")
	}
	bad := m
	bad.SHA256 = "zz"
	if err := f.v.VerifyArtifact(bad, bytes.NewReader(f.artifact)); err == nil {
		t.Error("bad digest field accepted")
	}
	up := m
	up.SHA256 = bytesUpper(m.SHA256)
	if err := f.v.VerifyArtifact(up, bytes.NewReader(f.artifact)); err != nil {
		t.Errorf("uppercase hex digest: %v", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { return len(p), nil }

func bytesUpper(s string) string { return string(bytes.ToUpper([]byte(s))) }

func TestAntiRollbackDowngradeReplay(t *testing.T) {
	f := newFixture(t)
	m5 := f.manifest(5)
	if err := f.v.Accept(m5, bytes.NewReader(f.artifact)); err != nil {
		t.Fatal(err)
	}
	// not committed yet: a retry of the same update must still be accepted
	if err := f.v.Accept(m5, bytes.NewReader(f.artifact)); err != nil {
		t.Fatalf("retry before commit: %v", err)
	}
	if err := f.v.Commit(m5); err != nil {
		t.Fatal(err)
	}
	if err := f.v.Accept(m5, bytes.NewReader(f.artifact)); !errors.Is(err, ErrReplay) {
		t.Errorf("replay: %v", err)
	}
	if err := f.v.Accept(f.manifest(4), bytes.NewReader(f.artifact)); !errors.Is(err, ErrRollback) {
		t.Errorf("downgrade: %v", err)
	}
	if err := f.v.Accept(f.manifest(1), bytes.NewReader(f.artifact)); !errors.Is(err, ErrRollback) {
		t.Errorf("downgrade to 1: %v", err)
	}
	if err := f.v.Accept(f.manifest(0), bytes.NewReader(f.artifact)); !errors.Is(err, ErrCounterMissing) {
		t.Errorf("zero counter: %v", err)
	}
	if err := f.v.Commit(f.manifest(3)); !errors.Is(err, ErrRollback) {
		t.Errorf("commit must not lower the floor: %v", err)
	}
	if err := f.v.Accept(f.manifest(6), bytes.NewReader(f.artifact)); err != nil {
		t.Errorf("upgrade: %v", err)
	}
	// a different lineage has its own counter
	other := f.manifest(1)
	other.Board = "b2"
	Sign(f.priv, &other)
	if err := f.v.Accept(other, bytes.NewReader(f.artifact)); err != nil {
		t.Errorf("other lineage: %v", err)
	}
	// a downgrade that is correctly signed by the real key is still refused
	if err := f.v.CheckCounter(f.manifest(2)); !errors.Is(err, ErrRollback) {
		t.Error("signed downgrade accepted")
	}
}

func TestClockSkew(t *testing.T) {
	f := newFixture(t)
	mk := func(issued, expires time.Duration) Manifest {
		m := f.manifest(1)
		m.IssuedAt, m.ExpiresAt = f.now.Add(issued), f.now.Add(expires)
		Sign(f.priv, &m)
		return m
	}
	cases := []struct {
		name string
		m    Manifest
		want error
	}{
		{"valid", mk(-time.Hour, time.Hour), nil},
		{"issued slightly in future (within skew)", mk(4*time.Minute, time.Hour), nil},
		{"issued far in future", mk(10*time.Minute, time.Hour), ErrNotYetValid},
		{"expired within skew", mk(-time.Hour, -4*time.Minute), nil},
		{"expired", mk(-2*time.Hour, -10*time.Minute), ErrExpired},
		{"expires before issue", mk(time.Hour, -time.Hour), ErrBadValidity},
		{"no validity", func() Manifest {
			m := f.manifest(1)
			m.IssuedAt, m.ExpiresAt = time.Time{}, time.Time{}
			Sign(f.priv, &m)
			return m
		}(), ErrBadValidity},
	}
	for _, c := range cases {
		if err := f.v.VerifyManifest(c.m); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: %v want %v", c.name, err, c.want)
		}
	}
	// zone representation of the same instant must not matter
	m := f.manifest(1)
	m.IssuedAt = m.IssuedAt.In(time.FixedZone("x", 5*3600+1800))
	if err := f.v.VerifyManifest(m); err != nil {
		t.Errorf("tz normalisation: %v", err)
	}
	// max validity cap
	f.v.MaxValidity = time.Hour
	if err := f.v.VerifyManifest(f.manifest(1)); !errors.Is(err, ErrBadValidity) {
		t.Errorf("validity cap: %v", err)
	}
	f.v.MaxValidity = 0
	// clock rolled back after a commit: refuse older-than-accepted time
	if err := f.v.Commit(f.manifest(1)); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(-48 * time.Hour)
	m2 := f.manifest(2) // issued relative to the (rolled back) clock, so it passes validity
	if err := f.v.Accept(m2, bytes.NewReader(f.artifact)); !errors.Is(err, ErrClockRegression) {
		t.Errorf("clock rollback: %v", err)
	}
}

func TestFileCountersPersistAndFailClosed(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "sub", "ota-floor.json")
	fc, err := OpenFileCounters(path)
	if err != nil {
		t.Fatal(err)
	}
	f.v.Counters = fc
	if err = f.v.Commit(f.manifest(7)); err != nil {
		t.Fatal(err)
	}
	// "reboot"
	fc2, err := OpenFileCounters(path)
	if err != nil {
		t.Fatal(err)
	}
	f.v.Counters = fc2
	if err = f.v.CheckCounter(f.manifest(7)); !errors.Is(err, ErrReplay) {
		t.Errorf("floor lost across restart: %v", err)
	}
	if err = f.v.CheckCounter(f.manifest(3)); !errors.Is(err, ErrRollback) {
		t.Errorf("floor lost across restart: %v", err)
	}
	// corrupt file must not silently reset the floor
	if err = writeFile(path, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenFileCounters(path); err == nil {
		t.Error("corrupt floor file opened")
	}
	if err = writeFile(path, bytes.Repeat([]byte(" "), maxCounterFile+1)); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenFileCounters(path); err == nil {
		t.Error("oversized floor file opened")
	}
	// store failure surfaces as an error, not "no floor"
	f.v.Counters = failingStore{}
	if err = f.v.CheckCounter(f.manifest(9)); err == nil {
		t.Error("store failure ignored")
	}
}

type failingStore struct{}

func (failingStore) Get(string) (Floor, bool, error) { return Floor{}, false, errors.New("boom") }
func (failingStore) Set(string, Floor) error         { return errors.New("boom") }

func writeFile(path string, b []byte) error { return atomicWrite(path, b) }
