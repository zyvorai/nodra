// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package mqtt implements the small MQTT 3.1.1 edge-ingress subset Nodra needs:
// CONNECT (now parsing CleanSession and ClientID, previously silently
// ignored), SUBSCRIBE, PUBLISH, PUBACK/PUBREC/PUBREL/PUBCOMP, PINGREQ and
// DISCONNECT. It is intentionally not a replacement for a full broker.
// Persistent sessions (CleanSession=0) are supported for QoS0/1/2 subscribers
// when EnableSessions is used — see its doc for the one honesty gap
// (in-memory subscription lists don't survive a broker restart).
//
// QoS2 is supported in both directions. INBOUND (publisher -> broker): a
// publishing client gets the full exactly-once PUBLISH/PUBREC/PUBREL/PUBCOMP
// handshake, with delivery to the Handler/local subscribers deferred to
// PUBREL so a retransmitted PUBLISH never double-delivers. OUTBOUND
// (broker -> subscriber): SUBSCRIBE grants up to QoS2; Broker.Publish and
// session replay send PUBLISH QoS2 with a broker-allocated packet ID,
// register the message in an in-flight map, and complete PUBREC→PUBREL→
// PUBCOMP asynchronously in the client's serve loop without blocking the
// Publish caller. QoS1 outbound remains fire-and-forget (no PUBACK wait).
// The broker is sufficient for sensors/PLCs to publish locally and for
// local applications to subscribe while the cloud is unavailable.
package mqtt

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/queue"
	"github.com/zyvorai/nodra/internal/router"
)

type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}
type Handler func(context.Context, Message) error

type Broker struct {
	Addr        string
	Handler     Handler
	ln          net.Listener
	mu          sync.RWMutex
	clients     map[*client]struct{}
	sessionDir  string
	sessionOpts queue.Options
	sessMu      sync.Mutex
	sessions    map[string]*persistentSession
}
type subscription struct {
	filter string
	qos    byte
}

// outQoS2 phases for broker→subscriber exactly-once delivery.
const (
	outAwaitPubrec  byte = iota // PUBLISH sent; waiting for PUBREC
	outAwaitPubcomp             // PUBREL sent; waiting for PUBCOMP
)

type client struct {
	c           net.Conn
	wmu         sync.Mutex
	subsMu      sync.Mutex
	subs        []subscription
	closed      chan struct{}
	session     *persistentSession
	qos2Mu      sync.Mutex
	qos2Pending map[uint16]Message // inbound publisher→broker, keyed by client pid
	outMu       sync.Mutex
	nextOutID   uint16
	qos2Out     map[uint16]byte // outbound broker→subscriber inflight state
}

func New(addr string, h Handler) *Broker {
	return &Broker{Addr: addr, Handler: h, clients: map[*client]struct{}{}}
}
func (b *Broker) Start(ctx context.Context) error {
	if b.Addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", b.Addr)
	if err != nil {
		return err
	}
	b.ln = ln
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		cl := &client{c: c, closed: make(chan struct{})}
		b.mu.Lock()
		b.clients[cl] = struct{}{}
		b.mu.Unlock()
		go b.serve(ctx, cl)
	}
}
func (b *Broker) Close() error {
	b.sessMu.Lock()
	for _, s := range b.sessions {
		_ = s.q.Close()
	}
	b.sessMu.Unlock()
	if b.ln != nil {
		return b.ln.Close()
	}
	return nil
}
func (b *Broker) Publish(msg Message) {
	b.mu.RLock()
	cs := make([]*client, 0, len(b.clients))
	for c := range b.clients {
		cs = append(cs, c)
	}
	b.mu.RUnlock()
	for _, c := range cs {
		c.subsMu.Lock()
		subs := c.subs
		c.subsMu.Unlock()
		for _, s := range subs {
			if router.Match(s.filter, msg.Topic) {
				_ = c.sendPublish(msg.Topic, msg.Payload, minQoS(msg.QoS, s.qos), false)
				break
			}
		}
	}
	if b.sessionDir == "" {
		return
	}
	b.sessMu.Lock()
	sessions := make([]*persistentSession, 0, len(b.sessions))
	for _, s := range b.sessions {
		sessions = append(sessions, s)
	}
	b.sessMu.Unlock()
	for _, s := range sessions {
		// A live session already received this message via the loop above —
		// enqueueing here too would double-deliver it once it reconnects.
		if s.isLive() {
			continue
		}
		if qos, ok := s.matches(msg.Topic); ok {
			s.enqueue(msg.Topic, msg.Payload, minQoS(msg.QoS, qos))
		}
	}
}
func minQoS(a, b byte) byte {
	if a < b {
		return a
	}
	return b
}
func (b *Broker) serve(ctx context.Context, cl *client) {
	defer func() {
		_ = cl.c.Close()
		b.mu.Lock()
		delete(b.clients, cl)
		b.mu.Unlock()
		if cl.session != nil {
			cl.session.detach(cl)
		}
		close(cl.closed)
	}()
	r := bufio.NewReader(cl.c)
	connected := false
	for {
		_ = cl.c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		typ, flags, payload, err := readPacket(r)
		if err != nil {
			return
		}
		switch typ {
		case 1:
			if connected {
				return
			}
			info, err := parseConnect(payload)
			if err != nil {
				return
			}
			sessionPresent := byte(0)
			if b.sessionDir != "" {
				if info.cleanSession {
					b.discardSession(info.clientID)
				} else {
					sess, existed, err := b.resumeSession(info.clientID)
					if err != nil {
						return
					}
					cl.session = sess
					sess.attach(cl)
					cl.subsMu.Lock()
					cl.subs = sess.snapshotSubs()
					cl.subsMu.Unlock()
					if existed {
						sessionPresent = 1
					}
				}
			}
			if err = cl.write([]byte{0x20, 0x02, 0x00, sessionPresent}); err != nil {
				return
			}
			connected = true
			if cl.session != nil {
				if err := cl.session.replay(cl); err != nil {
					return
				}
			}
		case 3:
			if !connected {
				return
			}
			topic, body, qos, pid, er := parsePublish(flags, payload)
			if er != nil {
				return
			}
			msg := Message{Topic: topic, Payload: body, QoS: qos, Retain: flags&1 != 0}
			switch qos {
			case 0, 1:
				if b.Handler != nil {
					_ = b.Handler(ctx, msg)
				}
				b.Publish(msg)
				if qos == 1 {
					_ = cl.write([]byte{0x40, 0x02, byte(pid >> 8), byte(pid)})
				}
			case 2:
				// Exactly-once handoff is deferred to PUBREL (case 6 below) —
				// storing here and delivering on PUBLISH would double-deliver
				// if the sender retransmits PUBLISH after a lost PUBREC.
				cl.qos2Mu.Lock()
				if cl.qos2Pending == nil {
					cl.qos2Pending = map[uint16]Message{}
				}
				cl.qos2Pending[pid] = msg
				cl.qos2Mu.Unlock()
				if err = cl.write([]byte{0x50, 0x02, byte(pid >> 8), byte(pid)}); err != nil { // PUBREC
					return
				}
			}
		case 5: // PUBREC (outbound QoS2: subscriber ack of our PUBLISH)
			if !connected {
				return
			}
			if len(payload) < 2 {
				return
			}
			pid := binary.BigEndian.Uint16(payload[:2])
			cl.outMu.Lock()
			st, ok := cl.qos2Out[pid]
			if ok && (st == outAwaitPubrec || st == outAwaitPubcomp) {
				cl.qos2Out[pid] = outAwaitPubcomp
				cl.outMu.Unlock()
				// Retransmit PUBREL if the subscriber never saw the first one.
				if err = cl.write([]byte{0x62, 0x02, byte(pid >> 8), byte(pid)}); err != nil {
					return
				}
			} else {
				cl.outMu.Unlock()
			}
		case 6: // PUBREL (inbound QoS2)
			if !connected {
				return
			}
			if len(payload) < 2 {
				return
			}
			pid := binary.BigEndian.Uint16(payload[:2])
			cl.qos2Mu.Lock()
			msg, ok := cl.qos2Pending[pid]
			delete(cl.qos2Pending, pid)
			cl.qos2Mu.Unlock()
			if ok {
				if b.Handler != nil {
					_ = b.Handler(ctx, msg)
				}
				b.Publish(msg)
			}
			// PUBCOMP unconditionally, even for an unrecognized pid, so a
			// retransmitted PUBREL (sender never saw our PUBCOMP) is
			// idempotent rather than hanging the sender.
			if err = cl.write([]byte{0x70, 0x02, byte(pid >> 8), byte(pid)}); err != nil {
				return
			}
		case 7: // PUBCOMP (outbound QoS2: subscriber finished handshake)
			if !connected {
				return
			}
			if len(payload) < 2 {
				return
			}
			pid := binary.BigEndian.Uint16(payload[:2])
			cl.outMu.Lock()
			delete(cl.qos2Out, pid)
			cl.outMu.Unlock()
		case 8:
			if !connected {
				return
			}
			pid, subs, er := parseSubscribe(payload)
			if er != nil {
				return
			}
			cl.subsMu.Lock()
			cl.subs = append(cl.subs, subs...)
			all := append([]subscription(nil), cl.subs...)
			cl.subsMu.Unlock()
			if cl.session != nil {
				cl.session.setSubs(all)
			}
			ack := []byte{0x90, byte(2 + len(subs)), byte(pid >> 8), byte(pid)}
			for _, s := range subs {
				ack = append(ack, s.qos)
			}
			if err = cl.write(ack); err != nil {
				return
			}
		case 12:
			if err = cl.write([]byte{0xd0, 0x00}); err != nil {
				return
			}
		case 14:
			return
		default: /* ignore unsupported packets */
		}
	}
}
func (c *client) write(p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.c.Write(p)
	return err
}

// nextOutPacketID returns a free broker→client packet identifier (1..65535),
// skipping any id currently in the outbound QoS2 inflight map.
func (c *client) nextOutPacketID() uint16 {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	for {
		c.nextOutID++
		if c.nextOutID == 0 {
			c.nextOutID = 1
		}
		if _, busy := c.qos2Out[c.nextOutID]; !busy {
			return c.nextOutID
		}
	}
}

func (c *client) sendPublish(topic string, payload []byte, qos byte, dup bool) error {
	vh := appendString(nil, topic)
	var pid uint16
	if qos > 0 {
		pid = c.nextOutPacketID()
		vh = append(vh, byte(pid>>8), byte(pid))
	}
	body := append(vh, payload...)
	h := byte(0x30)
	switch qos {
	case 1:
		h |= 0x02
	case 2:
		h |= 0x04
	}
	if qos > 0 && dup {
		h |= 0x08
	}
	// Register outbound QoS2 before the write so a fast PUBREC cannot race
	// the serve loop past an empty inflight map.
	if qos == 2 {
		c.outMu.Lock()
		if c.qos2Out == nil {
			c.qos2Out = map[uint16]byte{}
		}
		c.qos2Out[pid] = outAwaitPubrec
		c.outMu.Unlock()
	}
	pkt := []byte{h}
	pkt = append(pkt, encodeRemaining(len(body))...)
	pkt = append(pkt, body...)
	if err := c.write(pkt); err != nil {
		if qos == 2 {
			c.outMu.Lock()
			delete(c.qos2Out, pid)
			c.outMu.Unlock()
		}
		return err
	}
	return nil
}
func readPacket(r *bufio.Reader) (byte, byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, 0, nil, err
	}
	n, mul := 0, 1
	for i := 0; i < 4; i++ {
		b, er := r.ReadByte()
		if er != nil {
			return 0, 0, nil, er
		}
		n += int(b&127) * mul
		if b&128 == 0 {
			break
		}
		mul *= 128
		if i == 3 {
			return 0, 0, nil, errors.New("malformed remaining length")
		}
	}
	if n > 16<<20 {
		return 0, 0, nil, errors.New("packet too large")
	}
	p := make([]byte, n)
	_, err = io.ReadFull(r, p)
	return h >> 4, h & 15, p, err
}
func encodeRemaining(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 128
		}
		out = append(out, d)
		if n == 0 {
			return out
		}
	}
}
func readString(p []byte) (string, []byte, error) {
	if len(p) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	if len(p) < 2+n {
		return "", nil, io.ErrUnexpectedEOF
	}
	return string(p[2 : 2+n]), p[2+n:], nil
}
func appendString(dst []byte, s string) []byte {
	dst = append(dst, byte(len(s)>>8), byte(len(s)))
	return append(dst, []byte(s)...)
}

// connectInfo is the subset of a CONNECT packet's variable header/payload
// this broker acts on: whether the client wants a persistent session
// (CleanSession=0) and, if so, its ClientID.
type connectInfo struct {
	cleanSession bool
	clientID     string
}

// parseConnect validates the protocol name/level (as validateConnect always
// did) and additionally reads the connect-flags byte's CleanSession bit
// (0x02) and the ClientID from the payload — both previously parsed far
// enough to skip past, but never looked at. A persistent session (
// CleanSession=0) requires a non-empty ClientID, per the MQTT 3.1.1 spec.
func parseConnect(p []byte) (connectInfo, error) {
	proto, rest, err := readString(p)
	if err != nil {
		return connectInfo{}, err
	}
	if proto != "MQTT" || len(rest) < 4 || rest[0] != 4 {
		return connectInfo{}, errors.New("only MQTT 3.1.1 supported")
	}
	flags := rest[1]
	// rest[2:4] is Keep Alive — not enforced by this broker.
	clientID, _, err := readString(rest[4:])
	if err != nil {
		return connectInfo{}, err
	}
	info := connectInfo{cleanSession: flags&0x02 != 0, clientID: clientID}
	if !info.cleanSession && info.clientID == "" {
		return connectInfo{}, errors.New("persistent session requires a non-empty client id")
	}
	return info, nil
}

// parsePublish accepts QoS 0/1/2 on the inbound (publisher -> broker) side.
// QoS2 here is the receiver half of the exactly-once handshake (PUBLISH ->
// PUBREC -> PUBREL -> PUBCOMP) — see the PUBLISH/PUBREL cases in serve().
// Outbound QoS2 (broker -> subscriber) uses the same wire encoding when the
// broker sends; see sendPublish and the PUBREC/PUBCOMP cases in serve().
func parsePublish(flags byte, p []byte) (string, []byte, byte, uint16, error) {
	topic, rest, err := readString(p)
	if err != nil || topic == "" {
		return "", nil, 0, 0, errors.New("invalid publish topic")
	}
	qos := (flags >> 1) & 3
	if qos > 2 {
		return "", nil, qos, 0, errors.New("invalid QoS in PUBLISH")
	}
	var pid uint16
	if qos > 0 {
		if len(rest) < 2 {
			return "", nil, qos, 0, io.ErrUnexpectedEOF
		}
		pid = binary.BigEndian.Uint16(rest[:2])
		rest = rest[2:]
	}
	return topic, append([]byte(nil), rest...), qos, pid, nil
}
func parseSubscribe(p []byte) (uint16, []subscription, error) {
	if len(p) < 2 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	pid := binary.BigEndian.Uint16(p[:2])
	p = p[2:]
	var out []subscription
	for len(p) > 0 {
		f, rest, err := readString(p)
		if err != nil || len(rest) < 1 {
			return 0, nil, errors.New("invalid subscribe")
		}
		q := rest[0]
		if q > 2 {
			return 0, nil, fmt.Errorf("QoS %d unsupported", q)
		}
		out = append(out, subscription{f, q})
		p = rest[1:]
	}
	if len(out) == 0 {
		return 0, nil, errors.New("empty subscribe")
	}
	return pid, out, nil
}
