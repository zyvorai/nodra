// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
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

func TestMQTTRejectsBadPassword(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, nil)
	b.Authenticate = func(u, p string) bool { return u == "edge" && p == "secret" }
	go func() { _ = b.Start(ctx) }()
	c, r := dial(t, addr)
	defer c.Close()
	conn := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 0xC2, 0, 30, 0, 1, 'x', 0, 4, 'e', 'd', 'g', 'e', 0, 3, 'n', 'o', 'p'}
	if _, err := c.Write(pkt(0x10, conn)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(r, ack); err != nil {
		t.Fatal(err)
	}
	if ack[0] != 0x20 || ack[3] != 0x04 {
		t.Fatalf("connack %v", ack)
	}
}

func TestMQTTACLAndRate(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Message, 1)
	b := New(addr, func(_ context.Context, m Message) error { got <- m; return nil })
	b.Authorize = func(_, topic string, write bool) bool {
		return write && topic == "ok"
	}
	b.MaxPublishPerMinute = 1
	go func() { _ = b.Start(ctx) }()
	c, r := dial(t, addr)
	defer c.Close()
	conn := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 2, 0, 30, 0, 1, 'x'}
	if _, err := c.Write(pkt(0x10, conn)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 4)
	if _, err := io.ReadFull(r, ack); err != nil || ack[0] != 0x20 || ack[3] != 0 {
		t.Fatalf("connack %v %v", ack, err)
	}
	sub := []byte{0, 1, 0, 6, 's', 'e', 'c', 'r', 'e', 't', 0}
	if _, err := c.Write(pkt(0x82, sub)); err != nil {
		t.Fatal(err)
	}
	sack := make([]byte, 5)
	if _, err := io.ReadFull(r, sack); err != nil {
		t.Fatal(err)
	}
	if sack[0] != 0x90 || sack[4] != 0x80 {
		t.Fatalf("suback %v", sack)
	}
	okPub := []byte{0, 2, 'o', 'k', '1'}
	if _, err := c.Write(pkt(0x30, okPub)); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.Topic != "ok" || string(m.Payload) != "1" {
			t.Fatalf("%+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed publish dropped")
	}
	if _, err := c.Write(pkt(0x30, okPub)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("rate limit kept the connection open")
	}
}

func TestMQTTMaxClients(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, nil)
	b.MaxClients = 1
	go func() { _ = b.Start(ctx) }()
	c1, _ := dial(t, addr)
	defer c1.Close()
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	c2.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := c2.Read(buf); err == nil {
		t.Fatal("second client was accepted")
	}
}

func testPair(t *testing.T, usage x509.ExtKeyUsage) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(func() *x509.Certificate {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}())
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func TestMQTTTLS(t *testing.T) {
	addr := freeAddr(t)
	cert, pool := testPair(t, x509.ExtKeyUsageServerAuth)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, nil)
	b.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	go func() { _ = b.Start(ctx) }()
	var conn *tls.Conn
	var err error
	for i := 0; i < 20; i++ {
		conn, err = tls.Dial("tcp", addr, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: "127.0.0.1"})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write(connectPacket("tls", true)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 4)
	if _, err = io.ReadFull(conn, ack); err != nil || ack[0] != 0x20 || ack[3] != 0 {
		t.Fatalf("connack %v %v", ack, err)
	}
	plain, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	_, _ = plain.Write(connectPacket("plain", true))
	plain.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	n, err := plain.Read(buf)
	if err == nil && n >= 4 && buf[0] == 0x20 && buf[3] == 0 {
		t.Fatal("cleartext CONNECT completed on a TLS listener")
	}
}

func TestMQTTRequireClientCert(t *testing.T) {
	addr := freeAddr(t)
	serverCert, serverPool := testPair(t, x509.ExtKeyUsageServerAuth)
	clientCert, clientPool := testPair(t, x509.ExtKeyUsageClientAuth)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(addr, nil)
	b.TLSConfig = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	go func() { _ = b.Start(ctx) }()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: serverPool, Certificates: []tls.Certificate{clientCert}, ServerName: "127.0.0.1"}
	var conn *tls.Conn
	var err error
	for i := 0; i < 20; i++ {
		conn, err = tls.Dial("tcp", addr, cfg)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write(connectPacket("mtls", true)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 4)
	if _, err = io.ReadFull(conn, ack); err != nil || ack[0] != 0x20 || ack[3] != 0 {
		t.Fatalf("connack %v %v", ack, err)
	}
	bare := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: serverPool, ServerName: "127.0.0.1"}
	bad, err := tls.Dial("tcp", addr, bare)
	if err != nil {
		return
	}
	defer bad.Close()
	_, _ = bad.Write(connectPacket("none", true))
	bad.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 4)
	n, err := bad.Read(buf)
	if err == nil && n >= 4 && buf[0] == 0x20 && buf[3] == 0 {
		t.Fatal("client without a certificate was accepted")
	}
}
