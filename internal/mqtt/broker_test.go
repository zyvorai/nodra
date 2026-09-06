package mqtt

import (
	"bufio"
	"context"
	"encoding/binary"
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
