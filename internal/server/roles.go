// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

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

// roleStore is file-backed (roles.json) in file mode, or Postgres-backed when
// StoreDriver=postgres so live replicas share custom role bearers.
type roleStore struct {
	mu    sync.RWMutex
	path  string
	db    *sql.DB
	roles []storedRole
}

const pgRoleSchema = `
CREATE TABLE IF NOT EXISTS nodra_custom_roles (
	name TEXT PRIMARY KEY,
	token_hash TEXT NOT NULL,
	write_access BOOLEAN NOT NULL DEFAULT false,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS nodra_custom_roles_token_hash_idx ON nodra_custom_roles (token_hash);
`

func openRoleStore(driver, path, databaseURL string) (*roleStore, error) {
	if driver == "postgres" && databaseURL != "" {
		return newPostgresRoleStore(databaseURL)
	}
	return newRoleStore(path), nil
}

func newRoleStore(path string) *roleStore {
	r := &roleStore{path: path}
	_ = r.load()
	return r
}

func newPostgresRoleStore(databaseURL string) (*roleStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres roles ping: %w", err)
	}
	if _, err = db.ExecContext(ctx, pgRoleSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres roles migrate: %w", err)
	}
	return &roleStore{db: db}, nil
}

func (r *roleStore) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

func (r *roleStore) load() error {
	if r.path == "" || r.db != nil {
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
	if r.path == "" || r.db != nil {
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

func (r *roleStore) listAll() ([]storedRole, error) {
	if r.db != nil {
		rows, err := r.db.Query(`SELECT name, token_hash, write_access, created_at FROM nodra_custom_roles ORDER BY name`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []storedRole
		for rows.Next() {
			var role storedRole
			if err := rows.Scan(&role.Name, &role.TokenHash, &role.Write, &role.CreatedAt); err != nil {
				return nil, err
			}
			out = append(out, role)
		}
		return out, rows.Err()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]storedRole, len(r.roles))
	copy(out, r.roles)
	return out, nil
}

func (r *roleStore) roleForToken(tok string) (string, bool) {
	roles, err := r.listAll()
	if err != nil {
		return "", false
	}
	for _, role := range roles {
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
	roles, err := r.listAll()
	if err != nil {
		return nil
	}
	out := make([]CustomRole, 0, len(roles))
	for _, role := range roles {
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
	hash := auth.Hash(tok)
	if r.db != nil {
		_, err = r.db.Exec(`INSERT INTO nodra_custom_roles (name, token_hash, write_access, created_at) VALUES ($1,$2,$3,$4)`,
			name, hash, write, now)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
				return CustomRole{}, errRoleExists
			}
			return CustomRole{}, err
		}
		return CustomRole{Name: name, Token: tok, Write: write, CreatedAt: now}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, role := range r.roles {
		if strings.EqualFold(role.Name, name) {
			return CustomRole{}, errRoleExists
		}
	}
	r.roles = append(r.roles, storedRole{Name: name, TokenHash: hash, Write: write, CreatedAt: now})
	if err := r.saveLocked(); err != nil {
		r.roles = r.roles[:len(r.roles)-1]
		return CustomRole{}, err
	}
	return CustomRole{Name: name, Token: tok, Write: write, CreatedAt: now}, nil
}

func (r *roleStore) delete(name string) bool {
	if r.db != nil {
		res, err := r.db.Exec(`DELETE FROM nodra_custom_roles WHERE name = $1`, name)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n > 0
	}
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
