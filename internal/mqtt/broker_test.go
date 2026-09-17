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
)

func freeAddr(t *testing.T) string {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a := l.Addr().String()
	_ = l.Close()
	return a
}
func pkt(h byte, b []byte) []byte { return append([]byte{h, byte(len(b))}, b...) }
func TestMQTTPublishQoS1(t *testing.T) {
	addr := freeAddr(t)
	got := make(chan Message, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, func(_ context.Context, m Message) error { got <- m; return nil })
	go b.Start(ctx)
	var c net.Conn
	var e error
	for i := 0; i < 20; i++ {
		c, e = net.Dial("tcp", addr)
		if e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	conn := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 2, 0, 30, 0, 1, 'x'}
	_, _ = c.Write(pkt(0x10, conn))
	ack := make([]byte, 4)
	if _, e = r.Read(ack); e != nil || ack[0] != 0x20 {
		t.Fatalf("connack %v %v", ack, e)
	}
	pub := []byte{0, 3, 'a', '/', 'b', 0, 7, '{', '"', 'x', '"', ':', '1', '}'}
	_, _ = c.Write(pkt(0x32, pub))
	pa := make([]byte, 4)
	if _, e = r.Read(pa); e != nil || pa[0] != 0x40 || binary.BigEndian.Uint16(pa[2:]) != 7 {
		t.Fatalf("puback %v %v", pa, e)
	}
	select {
	case m := <-got:
		if m.Topic != "a/b" {
			t.Fatal(m.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("no message")
	}
}

// TestOutboundQoS2LiveDelivery is the broker_test entry for outbound QoS2:
// subscribe at QoS2, Publish fans out, then complete PUBREC/PUBREL/PUBCOMP.
func TestOutboundQoS2LiveDelivery(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, nil)
	go func() { _ = b.Start(ctx) }()

	c, r := dial(t, addr)
	defer c.Close()
	writeAndReadConnack(t, c, r, connectPacket("outqos2", true))

	if _, err := c.Write(subscribePacket(9, "sensors/#", 2)); err != nil {
		t.Fatal(err)
	}
	suback := make([]byte, 5)
	if _, err := io.ReadFull(r, suback); err != nil {
		t.Fatal(err)
	}
	if suback[0] != 0x90 || binary.BigEndian.Uint16(suback[2:4]) != 9 || suback[4] != 2 {
		t.Fatalf("SUBACK %v, want pid=9 granted=2", suback)
	}

	b.Publish(Message{Topic: "sensors/temp", Payload: []byte("21.5"), QoS: 2})

	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	typ, flags, payload, err := readPacket(r)
	if err != nil {
		t.Fatal(err)
	}
	if typ != 3 || (flags>>1)&3 != 2 {
		t.Fatalf("got type=%d flags=%#x, want PUBLISH QoS2", typ, flags)
	}
	topic, body, qos, pid, err := parsePublish(flags, payload)
	if err != nil {
		t.Fatal(err)
	}
	if topic != "sensors/temp" || string(body) != "21.5" || qos != 2 || pid == 0 {
		t.Fatalf("publish topic=%q body=%q qos=%d pid=%d", topic, body, qos, pid)
	}

	if _, err := c.Write(pkt(0x50, []byte{byte(pid >> 8), byte(pid)})); err != nil {
		t.Fatal(err)
	}
	pubrel := make([]byte, 4)
	if _, err := io.ReadFull(r, pubrel); err != nil {
		t.Fatal(err)
	}
	if pubrel[0] != 0x62 || binary.BigEndian.Uint16(pubrel[2:]) != pid {
		t.Fatalf("PUBREL %v, want pid %d", pubrel, pid)
	}
	if _, err := c.Write(pkt(0x70, []byte{byte(pid >> 8), byte(pid)})); err != nil {
		t.Fatal(err)
	}
}
