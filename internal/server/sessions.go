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
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/zyvorai/nodra/internal/auth"
)

// consoleSession is a short-lived bearer minted by password or OIDC login.
// Static AdminToken/ViewerToken remain valid for automation and do not expire.
type consoleSession struct {
	Token     string    `json:"token"`
	Role      string    `json:"role"`
	Actor     string    `json:"actor"`
	OrgID     string    `json:"org_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// sessionStore is file-backed (sessions.json) in file mode, or Postgres-backed
// when StoreDriver=postgres so live replicas share minted console sessions.
type sessionStore struct {
	mu    sync.Mutex
	path  string
	db    *sql.DB
	byTok map[string]consoleSession
}

const pgSessionSchema = `
CREATE TABLE IF NOT EXISTS nodra_console_sessions (
	token TEXT PRIMARY KEY,
	role TEXT NOT NULL,
	actor TEXT NOT NULL,
	org_id TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS nodra_console_sessions_exp_idx ON nodra_console_sessions (expires_at);
`

func openSessionStore(driver, path, databaseURL string) (*sessionStore, error) {
	if driver == "postgres" && databaseURL != "" {
		return newPostgresSessionStore(databaseURL)
	}
	return newSessionStore(path), nil
}

func newSessionStore(path string) *sessionStore {
	s := &sessionStore{path: path, byTok: map[string]consoleSession{}}
	_ = s.load()
	return s
}

func newPostgresSessionStore(databaseURL string) (*sessionStore, error) {
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
		return nil, fmt.Errorf("postgres sessions ping: %w", err)
	}
	if _, err = db.ExecContext(ctx, pgSessionSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres sessions migrate: %w", err)
	}
	return &sessionStore{db: db}, nil
}

func (s *sessionStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *sessionStore) load() error {
	if s.path == "" || s.db != nil {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []consoleSession
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range list {
		if now.Before(sess.ExpiresAt) {
			s.byTok[sess.Token] = sess
		}
	}
	return nil
}

func (s *sessionStore) saveLocked() error {
	if s.path == "" || s.db != nil {
		return nil
	}
	list := make([]consoleSession, 0, len(s.byTok))
	for _, sess := range s.byTok {
		list = append(list, sess)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *sessionStore) mint(role, actor, orgID string, ttl time.Duration) (consoleSession, error) {
	tok, err := auth.NewToken(24)
	if err != nil {
		return consoleSession{}, err
	}
	sess := consoleSession{
		Token:     tok,
		Role:      role,
		Actor:     actor,
		OrgID:     orgID,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if s.db != nil {
		_, err = s.db.Exec(`INSERT INTO nodra_console_sessions (token, role, actor, org_id, expires_at) VALUES ($1,$2,$3,$4,$5)`,
			sess.Token, sess.Role, sess.Actor, sess.OrgID, sess.ExpiresAt)
		if err != nil {
			return consoleSession{}, err
		}
		_, _ = s.db.Exec(`DELETE FROM nodra_console_sessions WHERE expires_at < $1`, time.Now().UTC())
		return sess, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.byTok[tok] = sess
	_ = s.saveLocked()
	return sess, nil
}

func (s *sessionStore) get(tok string) (consoleSession, bool) {
	if s.db != nil {
		var sess consoleSession
		err := s.db.QueryRow(`SELECT token, role, actor, org_id, expires_at FROM nodra_console_sessions WHERE token = $1`, tok).
			Scan(&sess.Token, &sess.Role, &sess.Actor, &sess.OrgID, &sess.ExpiresAt)
		if err != nil {
			return consoleSession{}, false
		}
		if time.Now().UTC().After(sess.ExpiresAt) {
			_, _ = s.db.Exec(`DELETE FROM nodra_console_sessions WHERE token = $1`, tok)
			return consoleSession{}, false
		}
		return sess, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	sess, ok := s.byTok[tok]
	if !ok || time.Now().UTC().After(sess.ExpiresAt) {
		delete(s.byTok, tok)
		_ = s.saveLocked()
		return consoleSession{}, false
	}
	return sess, true
}

func (s *sessionStore) revoke(tok string) {
	if s.db != nil {
		_, _ = s.db.Exec(`DELETE FROM nodra_console_sessions WHERE token = $1`, tok)
		return
	}
	s.mu.Lock()
	delete(s.byTok, tok)
	_ = s.saveLocked()
	s.mu.Unlock()
}

func (s *sessionStore) rotate(tok string, ttl time.Duration) (consoleSession, bool, error) {
	if s.db != nil {
		tx, err := s.db.Begin()
		if err != nil {
			return consoleSession{}, false, err
		}
		defer tx.Rollback()
		var old consoleSession
		err = tx.QueryRow(`SELECT token, role, actor, org_id, expires_at FROM nodra_console_sessions WHERE token = $1 FOR UPDATE`, tok).
			Scan(&old.Token, &old.Role, &old.Actor, &old.OrgID, &old.ExpiresAt)
		if err != nil {
			return consoleSession{}, false, nil
		}
		if time.Now().UTC().After(old.ExpiresAt) {
			_, _ = tx.Exec(`DELETE FROM nodra_console_sessions WHERE token = $1`, tok)
			_ = tx.Commit()
			return consoleSession{}, false, nil
		}
		newTok, err := auth.NewToken(24)
		if err != nil {
			return consoleSession{}, false, err
		}
		sess := consoleSession{
			Token:     newTok,
			Role:      old.Role,
			Actor:     old.Actor,
			OrgID:     old.OrgID,
			ExpiresAt: time.Now().UTC().Add(ttl),
		}
		if _, err = tx.Exec(`DELETE FROM nodra_console_sessions WHERE token = $1`, tok); err != nil {
			return consoleSession{}, false, err
		}
		if _, err = tx.Exec(`INSERT INTO nodra_console_sessions (token, role, actor, org_id, expires_at) VALUES ($1,$2,$3,$4,$5)`,
			sess.Token, sess.Role, sess.Actor, sess.OrgID, sess.ExpiresAt); err != nil {
			return consoleSession{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return consoleSession{}, false, err
		}
		return sess, true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	old, ok := s.byTok[tok]
	if !ok || time.Now().UTC().After(old.ExpiresAt) {
		delete(s.byTok, tok)
		_ = s.saveLocked()
		return consoleSession{}, false, nil
	}
	delete(s.byTok, tok)
	newTok, err := auth.NewToken(24)
	if err != nil {
		return consoleSession{}, false, err
	}
	sess := consoleSession{
		Token:     newTok,
		Role:      old.Role,
		Actor:     old.Actor,
		OrgID:     old.OrgID,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	s.byTok[newTok] = sess
	_ = s.saveLocked()
	return sess, true, nil
}

func (s *sessionStore) pruneLocked() {
	now := time.Now().UTC()
	changed := false
	for k, v := range s.byTok {
		if now.After(v.ExpiresAt) {
			delete(s.byTok, k)
			changed = true
		}
	}
	if changed {
		_ = s.saveLocked()
	}
}

func (s *Server) sessionTTL() time.Duration {
	if s.cfg.SessionTTL > 0 {
		return s.cfg.SessionTTL
	}
	return time.Hour
}

func (s *Server) issueSession(w http.ResponseWriter, role, actor, orgID string) {
	sess, err := s.sessions.mint(role, actor, orgID, s.sessionTTL())
	if err != nil {
		errorJSON(w, 500, "session mint failed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"token":      sess.Token,
		"expires_at": sess.ExpiresAt,
		"user":       map[string]string{"username": actor, "role": role},
	})
}

func (s *Server) authRefresh(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		errorJSON(w, 401, "unauthorized")
		return
	}
	sess, ok, err := s.sessions.rotate(tok, s.sessionTTL())
	if err != nil {
		errorJSON(w, 500, "session rotate failed")
		return
	}
	if !ok {
		errorJSON(w, 401, "session expired or unknown; static tokens cannot be refreshed")
		return
	}
	writeJSON(w, 200, map[string]any{
		"token":      sess.Token,
		"expires_at": sess.ExpiresAt,
		"user":       map[string]string{"username": sess.Actor, "role": sess.Role},
	})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		errorJSON(w, 401, "unauthorized")
		return
	}
	if _, ok := s.sessions.get(tok); !ok {
		w.WriteHeader(204)
		return
	}
	s.sessions.revoke(tok)
	w.WriteHeader(204)
}
