// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/auth"
)

// consoleSession is a short-lived bearer minted by password or OIDC login.
// Static AdminToken/ViewerToken remain valid for automation and do not expire.
type consoleSession struct {
	Token     string
	Role      string
	Actor     string
	OrgID     string
	ExpiresAt time.Time
}

type sessionStore struct {
	mu    sync.Mutex
	byTok map[string]consoleSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{byTok: map[string]consoleSession{}}
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
	s.mu.Lock()
	s.pruneLocked()
	s.byTok[tok] = sess
	s.mu.Unlock()
	return sess, nil
}

func (s *sessionStore) get(tok string) (consoleSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	sess, ok := s.byTok[tok]
	if !ok || time.Now().UTC().After(sess.ExpiresAt) {
		delete(s.byTok, tok)
		return consoleSession{}, false
	}
	return sess, true
}

func (s *sessionStore) revoke(tok string) {
	s.mu.Lock()
	delete(s.byTok, tok)
	s.mu.Unlock()
}

func (s *sessionStore) rotate(tok string, ttl time.Duration) (consoleSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	old, ok := s.byTok[tok]
	if !ok || time.Now().UTC().After(old.ExpiresAt) {
		delete(s.byTok, tok)
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
	return sess, true, nil
}

func (s *sessionStore) pruneLocked() {
	now := time.Now().UTC()
	for k, v := range s.byTok {
		if now.After(v.ExpiresAt) {
			delete(s.byTok, k)
		}
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
		// Static long-lived tokens are not revoked here.
		w.WriteHeader(204)
		return
	}
	s.sessions.revoke(tok)
	w.WriteHeader(204)
}
