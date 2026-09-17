// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/zyvorai/nodra/internal/router"

	"github.com/zyvorai/nodra/internal/queue"
)

// storedMsg is one durably-queued message awaiting delivery to an offline
// persistent session. ID is a queue key, not part of the wire protocol.
type storedMsg struct {
	ID      string
	Topic   string
	Payload []byte
	QoS     byte
}

// persistentSession is one MQTT ClientID's durable state: its subscriptions
// (in-memory only — lost on a broker restart, see EnableSessions's doc) and
// a durable queue of messages matched while no live connection held them.
type persistentSession struct {
	mu   sync.Mutex
	subs []subscription
	live *client
	q    *queue.Queue[storedMsg]
	seq  uint64
}

func (s *persistentSession) attach(cl *client) {
	s.mu.Lock()
	s.live = cl
	s.mu.Unlock()
}

// detach clears live only if it is still exactly this connection — a session
// must not be marked offline because an older connection for the same
// ClientID is finally unwinding after a newer one already replaced it.
func (s *persistentSession) detach(cl *client) {
	s.mu.Lock()
	if s.live == cl {
		s.live = nil
	}
	s.mu.Unlock()
}

func (s *persistentSession) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live != nil
}

func (s *persistentSession) setSubs(subs []subscription) {
	s.mu.Lock()
	s.subs = append([]subscription(nil), subs...)
	s.mu.Unlock()
}

func (s *persistentSession) snapshotSubs() []subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]subscription(nil), s.subs...)
}

// matches reports the effective QoS if topic matches one of this session's
// subscriptions, reusing the same filter matcher Broker.Publish already uses
// for live subscribers.
func (s *persistentSession) matches(topic string) (byte, bool) {
	s.mu.Lock()
	subs := s.subs
	s.mu.Unlock()
	for _, sub := range subs {
		if router.Match(sub.filter, topic) {
			return sub.qos, true
		}
	}
	return 0, false
}

func (s *persistentSession) enqueue(topic string, payload []byte, qos byte) {
	s.mu.Lock()
	s.seq++
	id := "m" + itoa(s.seq)
	s.mu.Unlock()
	_ = s.q.Put(id, storedMsg{ID: id, Topic: topic, Payload: append([]byte(nil), payload...), QoS: qos})
}

// replay drains the durable queue in order, sending each message to cl with
// DUP set (qos>=1) and deleting it from the queue only after a successful
// write — a write failure leaves the message queued for a future replay.
func (s *persistentSession) replay(cl *client) error {
	msgs, err := s.q.List()
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if err := cl.sendPublish(m.Topic, m.Payload, m.QoS, m.QoS > 0); err != nil {
			return err
		}
		_ = s.q.Delete(m.ID)
	}
	return nil
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// EnableSessions turns on persistent-session support (CleanSession=0) for
// QoS0/1/2 subscribers, durably queuing messages matched while no live
// connection holds the session and replaying them (with DUP set) on
// reconnect. QoS2 replays go through the full outbound PUBLISH/PUBREC/
// PUBREL/PUBCOMP handshake via sendPublish.
//
// Known limitation: subscription lists are in-memory only and are lost on a
// broker/process restart. The durable message queue itself survives a
// restart (it's a WAL, replayed on next open), but a client's subscriptions
// won't be there to match against until it re-subscribes after a restart —
// messages published in that narrow window won't be captured for it even
// though its already-matched backlog is fully durable.
func (b *Broker) EnableSessions(dir string, opts queue.Options) *Broker {
	b.sessionDir = dir
	b.sessionOpts = opts
	b.sessions = map[string]*persistentSession{}
	return b
}

func sessionDirFor(base, clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return filepath.Join(base, hex.EncodeToString(sum[:]))
}

// resumeSession returns the persistent session for clientID, opening its
// durable queue (creating it if none existed) and remembering it in memory.
// existed reflects whether this ClientID had a session before this call —
// checked on disk too, so a process restart doesn't lose that signal even
// though the in-memory map starts empty.
func (b *Broker) resumeSession(clientID string) (sess *persistentSession, existed bool, err error) {
	b.sessMu.Lock()
	defer b.sessMu.Unlock()
	if s, ok := b.sessions[clientID]; ok {
		return s, true, nil
	}
	dir := sessionDirFor(b.sessionDir, clientID)
	_, statErr := os.Stat(dir)
	existed = statErr == nil
	q, err := queue.OpenWithOptions[storedMsg](dir, b.sessionOpts)
	if err != nil {
		return nil, false, err
	}
	s := &persistentSession{q: q}
	b.sessions[clientID] = s
	return s, existed, nil
}

// discardSession drops clientID's session entirely (CleanSession=1):
// closes and deletes its durable queue and forgets its subscriptions.
func (b *Broker) discardSession(clientID string) {
	b.sessMu.Lock()
	s, ok := b.sessions[clientID]
	delete(b.sessions, clientID)
	b.sessMu.Unlock()
	if ok {
		_ = s.q.Close()
	}
	_ = os.RemoveAll(sessionDirFor(b.sessionDir, clientID))
}
