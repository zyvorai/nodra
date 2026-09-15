// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package serial implements a generic, protocol-agnostic serial/USB
// passthrough connector: it opens a configured serial device and publishes
// each delimited (or idle-gap-framed) chunk of bytes as a Nodra event,
// without decoding any application protocol itself (unlike connectors/modbus
// or connectors/j1939, which do). It shares Linux termios configuration with
// Modbus RTU via internal/serialport rather than duplicating it.
package serial

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

// knownBauds mirrors internal/serialport.BaudConstant's supported set, kept
// as a plain, platform-independent literal here so config validation (this
// file, no build tag) behaves identically on every platform - only actually
// opening the device (connector_linux.go / connector_stub.go) is Linux-only.
var knownBauds = map[int]bool{
	1200: true, 2400: true, 4800: true, 9600: true, 19200: true,
	38400: true, 57600: true, 115200: true, 230400: true,
}

func init() { connector.Register("serial", NewPoller) }

// PollerConfig configures the generic serial connector. Framing is
// "delimiter" (default, split on Delimiter - one byte, default "\n") or
// "idle" (flush whatever's been read after IdleTimeout of silence).
type PollerConfig struct {
	Device      string `json:"device"`
	Baud        int    `json:"baud,omitempty"`
	DataBits    int    `json:"data_bits,omitempty"`
	StopBits    int    `json:"stop_bits,omitempty"`
	Parity      string `json:"parity,omitempty"`
	Framing     string `json:"framing,omitempty"`
	Delimiter   string `json:"delimiter,omitempty"`
	IdleTimeout string `json:"idle_timeout,omitempty"`
	MaxFrame    int    `json:"max_frame,omitempty"`
	Topic       string `json:"topic"`
	// Timeout is the reconnect backoff after an I/O error (e.g. device
	// unplugged), default 2s. It is not a per-transaction deadline like
	// modbus's Timeout - this connector is a continuous read loop, not a
	// request/response client.
	Timeout string `json:"timeout,omitempty"`
}

// serialReader is satisfied by *serialport.Port in real use (see
// connector_linux.go) and faked in tests. connector_stub.go's non-Linux
// dialPort never returns one.
type serialReader interface {
	Read(p []byte) (int, error)
	Close() error
}

// dialPort actually opens the configured device. Set by an init() in
// connector_linux.go (real serialport.Open) or connector_stub.go (always
// returns serialport's "requires Linux" error) - whichever the build
// includes - so this file stays platform-code-free.
var dialPort func(cfg PollerConfig) (serialReader, error)

type Poller struct {
	name      string
	cfg       PollerConfig
	delimiter byte
	idle      time.Duration
	maxFrame  int
	reconnect time.Duration
	cancel    context.CancelFunc
	mu        sync.RWMutex
	last      string
}

func NewPoller(name string, raw json.RawMessage) (connector.Connector, error) {
	var cfg PollerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Device == "" {
		return nil, fmt.Errorf("serial connector requires device")
	}
	if cfg.Topic == "" {
		return nil, fmt.Errorf("serial connector requires topic")
	}
	if cfg.Baud == 0 {
		cfg.Baud = 9600
	}
	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = 1
	}
	if cfg.Parity == "" {
		cfg.Parity = "none"
	}
	if !knownBauds[cfg.Baud] {
		return nil, fmt.Errorf("unsupported serial baud %d", cfg.Baud)
	}
	if cfg.Framing == "" {
		cfg.Framing = "delimiter"
	}
	if cfg.MaxFrame <= 0 {
		cfg.MaxFrame = 65536
	}
	var delim byte = '\n'
	var idle time.Duration
	switch cfg.Framing {
	case "delimiter":
		d := cfg.Delimiter
		if d == "" {
			d = "\n"
		}
		if len(d) != 1 {
			return nil, fmt.Errorf("serial connector delimiter must be exactly one byte, got %q", d)
		}
		delim = d[0]
	case "idle":
		if cfg.IdleTimeout == "" {
			cfg.IdleTimeout = "100ms"
		}
		dur, err := time.ParseDuration(cfg.IdleTimeout)
		if err != nil || dur <= 0 {
			return nil, fmt.Errorf("invalid serial idle_timeout %q", cfg.IdleTimeout)
		}
		idle = dur
	default:
		return nil, fmt.Errorf("unsupported serial framing %q (want delimiter or idle)", cfg.Framing)
	}
	reconnect := 2 * time.Second
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid serial timeout %q", cfg.Timeout)
		}
		reconnect = d
	}
	if name == "" {
		name = "serial"
	}
	return &Poller{name: name, cfg: cfg, delimiter: delim, idle: idle, maxFrame: cfg.MaxFrame, reconnect: reconnect}, nil
}

func (p *Poller) Name() string { return p.name }

func (p *Poller) Start(ctx context.Context, h connector.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.loop(ctx, h)
	return nil
}

func (p *Poller) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *Poller) Health(context.Context) connector.Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return connector.Health{Healthy: p.last == "" || p.last == "ok", Message: p.last}
}
func (p *Poller) setHealth(v string) { p.mu.Lock(); p.last = v; p.mu.Unlock() }

func (p *Poller) loop(ctx context.Context, h connector.Handler) {
	for ctx.Err() == nil {
		if err := p.consume(ctx, h); err != nil && ctx.Err() == nil {
			p.setHealth(err.Error())
			slog.Warn("serial connector disconnected", "connector", p.name, "device", p.cfg.Device, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.reconnect):
			}
		}
	}
}

func (p *Poller) consume(ctx context.Context, h connector.Handler) error {
	port, err := dialPort(p.cfg)
	if err != nil {
		return err
	}
	defer port.Close()
	p.setHealth("ok")
	return p.readFrames(ctx, port, h)
}

// readFrames is platform-code-free so it can be unit-tested directly against
// a fake serialReader (see connector_test.go) - the actual byte-framing
// logic has nothing to do with termios.
func (p *Poller) readFrames(ctx context.Context, port serialReader, h connector.Handler) error {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)
	lastData := time.Now()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := port.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			lastData = time.Now()
			if p.cfg.Framing == "delimiter" {
				for {
					idx := bytes.IndexByte(buf, p.delimiter)
					if idx < 0 {
						break
					}
					frame := append([]byte(nil), buf[:idx]...)
					buf = buf[idx+1:]
					if err := p.emit(ctx, h, frame); err != nil {
						return err
					}
				}
			}
			if len(buf) > p.maxFrame {
				// No delimiter (or idle gap) has shown up for maxFrame bytes -
				// almost certainly a misconfigured delimiter/baud rather than
				// a legitimately huge frame. Drop the buffer and keep reading
				// rather than growing it unboundedly or crashing the loop.
				p.setHealth(fmt.Sprintf("dropped %d buffered bytes: no frame boundary within max_frame=%d", len(buf), p.maxFrame))
				slog.Warn("serial connector frame exceeded max_frame, dropping buffer", "connector", p.name, "buffered", len(buf), "max_frame", p.maxFrame)
				buf = buf[:0]
			}
		}
		if err != nil {
			return err
		}
		if p.cfg.Framing == "idle" && len(buf) > 0 && time.Since(lastData) >= p.idle {
			frame := buf
			buf = make([]byte, 0, 4096)
			if err := p.emit(ctx, h, frame); err != nil {
				return err
			}
		}
		if n == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
}

func (p *Poller) emit(ctx context.Context, h connector.Handler, frame []byte) error {
	if len(frame) == 0 {
		return nil
	}
	return h(ctx, connector.Event{
		Topic:   p.cfg.Topic,
		Payload: frame,
		Headers: map[string]string{"x-nodra-ingress": "serial", "x-nodra-connector": p.name},
	})
}
