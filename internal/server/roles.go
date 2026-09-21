// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/auth"
)

// CustomRole is an operator-defined role beyond the built-in admin/viewer pair.
// Write=true may mutate; Write=false is read-only like viewer.
type CustomRole struct {
	Name      string    `json:"name"`
	Token     string    `json:"token,omitempty"` // only on create response
	Write     bool      `json:"write"`
	CreatedAt time.Time `json:"created_at"`
}

type storedRole struct {
	Name      string    `json:"name"`
	TokenHash string    `json:"token_hash"`
	Write     bool      `json:"write"`
	CreatedAt time.Time `json:"created_at"`
}

type roleStore struct {
	mu    sync.RWMutex
	path  string
	roles []storedRole
}

func newRoleStore(path string) *roleStore {
	r := &roleStore{path: path}
	_ = r.load()
	return r
}

func (r *roleStore) load() error {
	if r.path == "" {
		return nil
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var roles []storedRole
	if err := json.Unmarshal(b, &roles); err != nil {
		return err
	}
	r.mu.Lock()
	r.roles = roles
	r.mu.Unlock()
	return nil
}

func (r *roleStore) saveLocked() error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r.roles, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func (r *roleStore) roleForToken(tok string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, role := range r.roles {
		if auth.EqualHash(role.TokenHash, tok) {
			if role.Write {
				return "admin", true
			}
			return "viewer", true
		}
	}
	return "", false
}

func (r *roleStore) list() []CustomRole {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CustomRole, 0, len(r.roles))
	for _, role := range r.roles {
		out = append(out, CustomRole{Name: role.Name, Write: role.Write, CreatedAt: role.CreatedAt})
	}
	return out
}

func (r *roleStore) create(name string, write bool) (CustomRole, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CustomRole{}, errRoleName
	}
	tok, err := auth.NewToken(24)
	if err != nil {
		return CustomRole{}, err
	}
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, role := range r.roles {
		if strings.EqualFold(role.Name, name) {
			return CustomRole{}, errRoleExists
		}
	}
	r.roles = append(r.roles, storedRole{Name: name, TokenHash: auth.Hash(tok), Write: write, CreatedAt: now})
	if err := r.saveLocked(); err != nil {
		r.roles = r.roles[:len(r.roles)-1]
		return CustomRole{}, err
	}
	return CustomRole{Name: name, Token: tok, Write: write, CreatedAt: now}, nil
}

func (r *roleStore) delete(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, role := range r.roles {
		if role.Name == name {
			r.roles = append(r.roles[:i], r.roles[i+1:]...)
			_ = r.saveLocked()
			return true
		}
	}
	return false
}

var (
	errRoleName   = errString("role name is required")
	errRoleExists = errString("role already exists")
)

type errString string

func (e errString) Error() string { return string(e) }

func (s *Server) rolesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, asJSONList(s.customRoles.list()))
}

func (s *Server) roleCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Write bool   `json:"write"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	role, err := s.customRoles.create(in.Name, in.Write)
	if err != nil {
		errorJSON(w, 400, err.Error())
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", role.Name, "role.create", "custom role created: "+role.Name, map[string]any{"write": role.Write})
	writeJSON(w, 201, role)
}

func (s *Server) roleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.customRoles.delete(name) {
		errorJSON(w, 404, "role not found")
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", name, "role.delete", "custom role deleted: "+name, nil)
	w.WriteHeader(204)
}

func (s *Server) enrollRotate(w http.ResponseWriter, r *http.Request) {
	tok, err := auth.NewToken(24)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	s.cfg.EnrollmentToken = tok
	s.enrollMu.Lock()
	if s.cfg.EnrollmentTokenTTL > 0 {
		s.enrollExpires = time.Now().UTC().Add(s.cfg.EnrollmentTokenTTL)
	} else {
		s.enrollExpires = time.Time{}
	}
	exp := s.enrollExpires
	s.enrollMu.Unlock()
	out := map[string]any{"enrollment_token": tok}
	if !exp.IsZero() {
		out["expires_at"] = exp
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", "", "enroll.rotate", "enrollment token rotated", nil)
	writeJSON(w, 200, out)
}
