// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/queue"
)

func connectPacket(clientID string, cleanSession bool) []byte {
	flags := byte(0x00)
	if cleanSession {
		flags = 0x02
	}
	body := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, flags, 0, 30}
	body = appendString(body, clientID)
	return pkt(0x10, body)
}
func subscribePacket(pid uint16, filter string, qos byte) []byte {
	body := []byte{byte(pid >> 8), byte(pid)}
	body = appendString(body, filter)
	body = append(body, qos)
	return pkt(0x82, body)
}
func publishPacket(topic string, payload []byte, qos byte, pid uint16) []byte {
	body := appendString(nil, topic)
	if qos > 0 {
		body = append(body, byte(pid>>8), byte(pid))
	}
	body = append(body, payload...)
	return pkt(0x30|(qos<<1), body)
}
func disconnectPacket() []byte { return pkt(0xE0, nil) }

func dial(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	var c net.Conn
	var err error
	for i := 0; i < 20; i++ {
		c, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	return c, bufio.NewReader(c)
}
func writeAndReadConnack(t *testing.T, c net.Conn, r *bufio.Reader, p []byte) []byte {
	t.Helper()
	if _, err := c.Write(p); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(r, ack); err != nil {
		t.Fatal(err)
	}
	if ack[0] != 0x20 {
		t.Fatalf("expected CONNACK, got %v", ack)
	}
	return ack
}
func waitDetached(t *testing.T, b *Broker, clientID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.sessMu.Lock()
		s, ok := b.sessions[clientID]
		b.sessMu.Unlock()
		if ok && !s.isLive() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session %q did not detach in time", clientID)
}

// TestPersistentSessionReplaysQueuedMessageWithDup exercises the full
// resume/subscribe/disconnect/offline-publish/reconnect/replay cycle.
func TestPersistentSessionReplaysQueuedMessageWithDup(t *testing.T) {
	addr := freeAddr(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, func(context.Context, Message) error { return nil }).EnableSessions(dir, queue.Options{})
	go func() { _ = b.Start(ctx) }()

	c1, r1 := dial(t, addr)
	ack1 := writeAndReadConnack(t, c1, r1, connectPacket("sub1", false))
	if ack1[3] != 0x00 {
		t.Fatalf("first connect session-present = %d, want 0 (no prior session)", ack1[3])
	}
	if _, err := c1.Write(subscribePacket(1, "a/b", 1)); err != nil {
		t.Fatal(err)
	}
	suback := make([]byte, 5)
	if _, err := io.ReadFull(r1, suback); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Write(disconnectPacket()); err != nil {
		t.Fatal(err)
	}
	_ = c1.Close()
	waitDetached(t, b, "sub1")

	c2, r2 := dial(t, addr)
	writeAndReadConnack(t, c2, r2, connectPacket("pub1", true))
	if _, err := c2.Write(publishPacket("a/b", []byte("hello"), 1, 42)); err != nil {
		t.Fatal(err)
	}
	puback := make([]byte, 4)
	if _, err := io.ReadFull(r2, puback); err != nil || puback[0] != 0x40 {
		t.Fatalf("puback %v %v", puback, err)
	}
	_ = c2.Close()

	c3, r3 := dial(t, addr)
	defer c3.Close()
	ack3 := writeAndReadConnack(t, c3, r3, connectPacket("sub1", false))
	if ack3[3] != 0x01 {
		t.Fatalf("reconnect session-present = %d, want 1 (prior session existed)", ack3[3])
	}
	typ, flags, payload, err := readPacket(r3)
	if err != nil {
		t.Fatal(err)
	}
	if typ != 3 || flags != 0x0A {
		t.Fatalf("replayed publish type=%d flags=%#x, want type=3 flags=0x0a (QoS1+DUP)", typ, flags)
	}
	topic, body, qos, _, err := parsePublish(flags, payload)
	if err != nil {
		t.Fatal(err)
	}
	if topic != "a/b" || string(body) != "hello" || qos != 1 {
		t.Fatalf("replayed publish topic=%q body=%q qos=%d", topic, body, qos)
	}
}

// TestCleanSessionDiscardsPriorState proves CleanSession=1 actually deletes
// the prior session and its queued backlog, not merely hides them.
func TestCleanSessionDiscardsPriorState(t *testing.T) {
	addr := freeAddr(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, func(context.Context, Message) error { return nil }).EnableSessions(dir, queue.Options{})
	go func() { _ = b.Start(ctx) }()

	c1, r1 := dial(t, addr)
	writeAndReadConnack(t, c1, r1, connectPacket("sub2", false))
	if _, err := c1.Write(subscribePacket(1, "x/y", 1)); err != nil {
		t.Fatal(err)
	}
	suback := make([]byte, 5)
	if _, err := io.ReadFull(r1, suback); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Write(disconnectPacket()); err != nil {
		t.Fatal(err)
	}
	_ = c1.Close()
	waitDetached(t, b, "sub2")

	c2, r2 := dial(t, addr)
	writeAndReadConnack(t, c2, r2, connectPacket("pub2", true))
	if _, err := c2.Write(publishPacket("x/y", []byte("msg"), 1, 7)); err != nil {
		t.Fatal(err)
	}
	puback := make([]byte, 4)
	if _, err := io.ReadFull(r2, puback); err != nil {
		t.Fatal(err)
	}
	_ = c2.Close()

	c3, r3 := dial(t, addr)
	ack3 := writeAndReadConnack(t, c3, r3, connectPacket("sub2", true))
	if ack3[3] != 0x00 {
		t.Fatalf("clean-session connect session-present = %d, want 0", ack3[3])
	}
	_ = c3.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := r3.Peek(1); err == nil {
		t.Fatal("unexpected data after clean-session connect (stale replay?)")
	}
	_ = c3.Close()

	c4, r4 := dial(t, addr)
	defer c4.Close()
	ack4 := writeAndReadConnack(t, c4, r4, connectPacket("sub2", false))
	if ack4[3] != 0x00 {
		t.Fatalf("post-discard reconnect session-present = %d, want 0 (proves the old session/queue were actually discarded)", ack4[3])
	}
	_ = c4.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := r4.Peek(1); err == nil {
		t.Fatal("unexpected replayed data after supposed session discard")
	}
}

// TestOutboundQoS2SubscribeStillRejected is a regression check: inbound
// QoS2 PUBLISH support (TestInboundQoS2ExactlyOnceHandshake) must not loosen
// SUBSCRIBE's existing QoS2 rejection — outbound QoS2 remains a separate,
// unimplemented piece of work (see the package doc).
func TestOutboundQoS2SubscribeStillRejected(t *testing.T) {
	addr := freeAddr(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, func(context.Context, Message) error { return nil }).EnableSessions(dir, queue.Options{})
	go func() { _ = b.Start(ctx) }()

	c, r := dial(t, addr)
	defer c.Close()
	writeAndReadConnack(t, c, r, connectPacket("qos2subclient", true))

	if _, err := c.Write(subscribePacket(1, "a/b", 2)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err == nil {
		t.Fatal("expected connection to close after a QoS2 subscribe, got data instead")
	}
}

// TestInboundQoS2ExactlyOnceHandshake drives the full PUBLISH/PUBREC/PUBREL/
// PUBCOMP handshake and asserts the Handler is invoked only once, only after
// PUBREL — not on the initial PUBLISH, which would double-deliver if the
// sender retransmits PUBLISH after a lost PUBREC.
func TestInboundQoS2ExactlyOnceHandshake(t *testing.T) {
	addr := freeAddr(t)
	got := make(chan Message, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, func(_ context.Context, m Message) error { got <- m; return nil })
	go func() { _ = b.Start(ctx) }()

	c, r := dial(t, addr)
	defer c.Close()
	writeAndReadConnack(t, c, r, connectPacket("qos2pub", true))

	const pid uint16 = 77
	if _, err := c.Write(publishPacket("a/b", []byte("hello"), 2, pid)); err != nil {
		t.Fatal(err)
	}
	pubrec := make([]byte, 4)
	if _, err := io.ReadFull(r, pubrec); err != nil {
		t.Fatal(err)
	}
	if pubrec[0] != 0x50 || binary.BigEndian.Uint16(pubrec[2:]) != pid {
		t.Fatalf("expected PUBREC for pid %d, got %v", pid, pubrec)
	}

	select {
	case <-got:
		t.Fatal("handler must not fire before PUBREL")
	case <-time.After(100 * time.Millisecond):
	}

	// A retransmitted PUBLISH (sender never saw our PUBREC) must be
	// idempotent: another PUBREC, still no handler delivery.
	if _, err := c.Write(publishPacket("a/b", []byte("hello"), 2, pid)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, pubrec); err != nil {
		t.Fatal(err)
	}
	if pubrec[0] != 0x50 || binary.BigEndian.Uint16(pubrec[2:]) != pid {
		t.Fatalf("expected PUBREC again for pid %d, got %v", pid, pubrec)
	}

	if _, err := c.Write(pkt(0x62, []byte{byte(pid >> 8), byte(pid)})); err != nil { // PUBREL
		t.Fatal(err)
	}
	pubcomp := make([]byte, 4)
	if _, err := io.ReadFull(r, pubcomp); err != nil {
		t.Fatal(err)
	}
	if pubcomp[0] != 0x70 || binary.BigEndian.Uint16(pubcomp[2:]) != pid {
		t.Fatalf("expected PUBCOMP for pid %d, got %v", pid, pubcomp)
	}

	select {
	case m := <-got:
		if m.Topic != "a/b" || string(m.Payload) != "hello" {
			t.Fatalf("unexpected message: %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("handler never fired after PUBREL")
	}
	select {
	case m := <-got:
		t.Fatalf("handler fired twice: second delivery %+v", m)
	case <-time.After(100 * time.Millisecond):
	}

	// A retransmitted PUBREL (sender never saw our first PUBCOMP) must still
	// get a PUBCOMP, without re-delivering to the handler.
	if _, err := c.Write(pkt(0x62, []byte{byte(pid >> 8), byte(pid)})); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, pubcomp); err != nil {
		t.Fatal(err)
	}
	if pubcomp[0] != 0x70 || binary.BigEndian.Uint16(pubcomp[2:]) != pid {
		t.Fatalf("expected PUBCOMP again for pid %d, got %v", pid, pubcomp)
	}
	select {
	case m := <-got:
		t.Fatalf("handler fired again on duplicate PUBREL: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}
