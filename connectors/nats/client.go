// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/version"
)

// Client dials a NATS server and subscribes to a fixed set of subjects for
// the lifetime of one connection. v1 scope: no TLS, no clustering, no
// JetStream, no queue groups — a plain subscribing client.
type Client struct {
	Addr     string
	Subjects []string
	Name     string
	User     string
	Pass     string
	Token    string
	Timeout  time.Duration
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 5 * time.Second
	}
	return c.Timeout
}

// Connect dials Addr, performs the INFO/CONNECT/SUB handshake, and returns a
// Conn ready for Next. The underlying connection is closed automatically
// when ctx is done, so a Next blocked on a read unblocks with an error
// rather than hanging past the caller's cancellation.
func (c *Client) Connect(ctx context.Context) (*Conn, error) {
	addr := strings.TrimPrefix(c.Addr, "nats://")
	d := net.Dialer{Timeout: c.timeout()}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	cn := &Conn{conn: conn, r: bufio.NewReaderSize(conn, 4096)}
	if err := cn.handshake(c); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	return cn, nil
}

// Conn is one live NATS connection, positioned to read MSG frames via Next.
type Conn struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
}

func (cn *Conn) handshake(c *Client) error {
	_ = cn.conn.SetDeadline(time.Now().Add(c.timeout()))
	if err := readInfo(cn.r); err != nil {
		return fmt.Errorf("nats: read INFO: %w", err)
	}
	name := c.Name
	if name == "" {
		name = "nodra"
	}
	if err := cn.writeRaw(buildConnect(name, c.User, c.Pass, c.Token, version.Version)); err != nil {
		return fmt.Errorf("nats: write CONNECT: %w", err)
	}
	for i, subj := range c.Subjects {
		if err := cn.writeRaw(buildSub(subj, i+1)); err != nil {
			return fmt.Errorf("nats: write SUB %s: %w", subj, err)
		}
	}
	// Handshake writes are fire-and-forget (verbose:false means no ack is
	// expected); clear the deadline so Next can block indefinitely,
	// governed only by ctx cancellation closing the connection.
	return cn.conn.SetDeadline(time.Time{})
}

func (cn *Conn) writeRaw(b []byte) error {
	cn.wmu.Lock()
	defer cn.wmu.Unlock()
	_, err := cn.conn.Write(b)
	return err
}

func (cn *Conn) writeLine(s string) error {
	return cn.writeRaw([]byte(s + "\r\n"))
}

// Next blocks until the next MSG frame arrives, transparently answering
// server PINGs and skipping protocol lines the caller doesn't need to see
// (PONG, +OK, INFO updates). It returns an error on -ERR, a malformed
// frame, or when the underlying connection is closed — including by ctx
// cancellation, via Connect's watcher goroutine closing the connection.
func (cn *Conn) Next(ctx context.Context) (string, []byte, error) {
	for {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		line, err := readLine(cn.r)
		if err != nil {
			return "", nil, err
		}
		switch {
		case line == "":
		case strings.HasPrefix(line, "MSG "):
			subject, nbytes, err := parseMSGHeader(strings.TrimPrefix(line, "MSG "))
			if err != nil {
				return "", nil, err
			}
			buf := make([]byte, nbytes+2) // payload + trailing CRLF
			if _, err := io.ReadFull(cn.r, buf); err != nil {
				return "", nil, err
			}
			return subject, buf[:nbytes], nil
		case line == "PING":
			if err := cn.writeLine("PONG"); err != nil {
				return "", nil, err
			}
		case line == "PONG", strings.HasPrefix(line, "+OK"), strings.HasPrefix(line, "INFO "):
			// no-op: ack for our own PING, protocol ack, or a cluster-info
			// update — nothing for the caller to see.
		case strings.HasPrefix(line, "-ERR"):
			return "", nil, fmt.Errorf("nats: server error: %s", strings.TrimSpace(strings.TrimPrefix(line, "-ERR")))
		default:
			slog.Debug("nats: ignoring unrecognized protocol line", "line", line)
		}
	}
}

func (cn *Conn) Close() error { return cn.conn.Close() }
