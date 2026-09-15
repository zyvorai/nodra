// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package nats implements a dependency-free, hand-rolled NATS core client
// (INFO/CONNECT/SUB/MSG/PING/PONG/-ERR only — no clustering, no JetStream,
// no TLS) and a subscribing bridge connector on top of it. NATS's core wire
// protocol is a simple text-prefixed line protocol, comparable in
// complexity to this project's own hand-rolled MQTT broker
// (internal/mqtt), which is why it's hand-rolled here rather than pulling
// in a client library — see docs/NATS_BRIDGE.md for why Zenoh, unlike
// NATS, is NOT hand-rolled and is explicitly deferred.
package nats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// maxPayloadBytes bounds a single MSG frame's advertised byte count so a
// corrupted or malicious length field can't make Next allocate unbounded
// memory.
const maxPayloadBytes = 64 << 20

// connectInfo is the JSON body of a NATS CONNECT protocol line. Fields are
// tagged omitempty so an unconfigured user/pass/auth_token is simply absent
// rather than sent as an empty string.
type connectInfo struct {
	Verbose     bool   `json:"verbose"`
	Pedantic    bool   `json:"pedantic"`
	TLSRequired bool   `json:"tls_required"`
	Name        string `json:"name"`
	Lang        string `json:"lang"`
	Version     string `json:"version"`
	User        string `json:"user,omitempty"`
	Pass        string `json:"pass,omitempty"`
	AuthToken   string `json:"auth_token,omitempty"`
}

// buildConnect renders a "CONNECT {json}\r\n" protocol line.
func buildConnect(name, user, pass, token, version string) []byte {
	ci := connectInfo{
		Verbose: false, Pedantic: false, TLSRequired: false,
		Name: name, Lang: "go", Version: version,
		User: user, Pass: pass, AuthToken: token,
	}
	b, _ := json.Marshal(ci)
	out := append([]byte("CONNECT "), b...)
	return append(out, '\r', '\n')
}

// buildSub renders a "SUB <subject> <sid>\r\n" protocol line. sid is a
// client-chosen subscription id; nodra assigns one sequentially per subject
// and never unsubscribes, so sid values are never reused within a
// connection.
func buildSub(subject string, sid int) []byte {
	return []byte(fmt.Sprintf("SUB %s %d\r\n", subject, sid))
}

// readLine reads one CRLF- or LF-terminated protocol line, with the
// terminator stripped. It works correctly even when the underlying reads
// are chunked arbitrarily by the network, since bufio.Reader buffers across
// calls until it sees the delimiter.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readInfo consumes the server's mandatory first line. v1 doesn't need any
// of INFO's fields (cluster URLs, max payload, tls_required, ...) — it only
// confirms the handshake started as expected. Notably this means a server
// requiring TLS is not detected here; TLS is not supported in v1 (see
// docs/NATS_BRIDGE.md).
func readInfo(r *bufio.Reader) error {
	line, err := readLine(r)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "INFO ") {
		return fmt.Errorf("nats: expected INFO, got %q", line)
	}
	return nil
}

// parseMSGHeader parses a "MSG <subject> <sid> [reply-to] <#bytes>" header
// (the "MSG " prefix and trailing CRLF already stripped) into the subject
// and the exact byte count of the payload that follows on the wire.
func parseMSGHeader(header string) (subject string, nbytes int, err error) {
	fields := strings.Fields(header)
	switch len(fields) {
	case 3:
		subject = fields[0]
		nbytes, err = strconv.Atoi(fields[2])
	case 4:
		subject = fields[0]
		nbytes, err = strconv.Atoi(fields[3])
	default:
		return "", 0, fmt.Errorf("nats: malformed MSG header %q", header)
	}
	if err != nil {
		return "", 0, fmt.Errorf("nats: invalid MSG byte count in %q: %w", header, err)
	}
	if nbytes < 0 || nbytes > maxPayloadBytes {
		return "", 0, fmt.Errorf("nats: implausible MSG byte count %d", nbytes)
	}
	return subject, nbytes, nil
}
