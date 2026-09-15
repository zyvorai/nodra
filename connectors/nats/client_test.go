// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// runMockNATSServer accepts exactly one connection and plays the server
// side of the handshake: send INFO, read CONNECT plus one SUB per expected
// subject, then hand off to script. Mirrors
// connectors/opcua/client_test.go's runMockServer approach — NATS is TCP
// end-to-end like OPC-UA, so a mock listener exercises the real wire
// format rather than needing a pty/socat harness.
func runMockNATSServer(t *testing.T, ln net.Listener, wantSubs int, script func(conn net.Conn, r *bufio.Reader)) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		t.Errorf("accept: %v", err)
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("INFO {\"server_id\":\"mock\",\"version\":\"2.10.0\"}\r\n")); err != nil {
		t.Errorf("write INFO: %v", err)
		return
	}
	r := bufio.NewReader(conn)
	connectLine, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(connectLine, "CONNECT ") {
		t.Errorf("expected CONNECT, got %q err=%v", connectLine, err)
		return
	}
	for i := 0; i < wantSubs; i++ {
		subLine, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(subLine, "SUB ") {
			t.Errorf("expected SUB, got %q err=%v", subLine, err)
			return
		}
	}
	script(conn, r)
}

func TestClientConnectAndReceiveMessage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockNATSServer(t, ln, 1, func(conn net.Conn, r *bufio.Reader) {
			if _, err := conn.Write([]byte("MSG factory.line1 1 11\r\nhello world\r\n")); err != nil {
				t.Errorf("write MSG: %v", err)
			}
		})
	}()

	cli := &Client{Addr: ln.Addr().String(), Subjects: []string{"factory.line1"}, Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := cli.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	subject, payload, err := conn.Next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if subject != "factory.line1" || string(payload) != "hello world" {
		t.Fatalf("subject=%q payload=%q", subject, payload)
	}
	<-done
}

func TestClientPingTransparentlyAnswered(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	pongCh := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockNATSServer(t, ln, 1, func(conn net.Conn, r *bufio.Reader) {
			// MSG, then a server PING mid-stream, then another MSG — Next()
			// must reply PONG on the wire and never surface PING/PONG to
			// the caller.
			if _, err := conn.Write([]byte("MSG s 1 3\r\none\r\n")); err != nil {
				t.Errorf("write MSG1: %v", err)
				return
			}
			if _, err := conn.Write([]byte("PING\r\n")); err != nil {
				t.Errorf("write PING: %v", err)
				return
			}
			line, err := r.ReadString('\n')
			if err != nil || strings.TrimRight(line, "\r\n") != "PONG" {
				t.Errorf("expected client PONG, got %q err=%v", line, err)
				return
			}
			pongCh <- struct{}{}
			if _, err := conn.Write([]byte("MSG s 1 3\r\ntwo\r\n")); err != nil {
				t.Errorf("write MSG2: %v", err)
			}
		})
	}()

	cli := &Client{Addr: ln.Addr().String(), Subjects: []string{"s"}, Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := cli.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	_, p1, err := conn.Next(ctx)
	if err != nil || string(p1) != "one" {
		t.Fatalf("first next: payload=%q err=%v", p1, err)
	}
	// PING/PONG handling happens inside an active Next() call, not between
	// calls — so the second Next() is what actually drives the client to
	// read the PING and reply PONG, before it continues on to read "two".
	_, p2, err := conn.Next(ctx)
	if err != nil || string(p2) != "two" {
		t.Fatalf("second next: payload=%q err=%v (PING/PONG must not surface to caller)", p2, err)
	}
	select {
	case <-pongCh:
	case <-time.After(2 * time.Second):
		t.Fatal("client never sent PONG")
	}
	<-done
}

func TestClientSurfacesServerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runMockNATSServer(t, ln, 1, func(conn net.Conn, r *bufio.Reader) {
			_, _ = conn.Write([]byte("-ERR 'Authorization Violation'\r\n"))
		})
	}()

	cli := &Client{Addr: ln.Addr().String(), Subjects: []string{"s"}, Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := cli.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if _, _, err = conn.Next(ctx); err == nil || !strings.Contains(err.Error(), "Authorization Violation") {
		t.Fatalf("expected server error surfaced, got %v", err)
	}
	<-done
}

func TestClientRejectsNonInfoFirstLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("NOT-INFO\r\n"))
	}()

	cli := &Client{Addr: ln.Addr().String(), Subjects: []string{"s"}, Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Connect(ctx); err == nil {
		t.Fatal("expected handshake failure for non-INFO first line")
	}
}
