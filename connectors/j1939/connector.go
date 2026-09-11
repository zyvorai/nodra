// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package j1939 consumes the Zyvor Device Agent read-only CAN SSE stream and
// translates 29-bit J1939 identifiers into Nodra events. Device Agent remains
// protocol-agnostic; PGN/source/destination semantics live here.
package j1939

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func init() { connector.Register("j1939-device-agent", New) }

type Config struct {
	URL         string `json:"url"`
	TopicPrefix string `json:"topic_prefix,omitempty"`
	Reconnect   string `json:"reconnect,omitempty"`
}

type RawFrame struct {
	Sequence            uint64 `json:"sequence"`
	Interface           string `json:"interface"`
	CapturedAtUnixMS    uint64 `json:"captured_at_unix_ms"`
	CANID               uint32 `json:"can_id"`
	Extended            bool   `json:"extended"`
	Remote              bool   `json:"remote"`
	Error               bool   `json:"error"`
	FD                  bool   `json:"fd"`
	BitrateSwitch       bool   `json:"bitrate_switch"`
	ErrorStateIndicator bool   `json:"error_state_indicator"`
	DLC                 uint8  `json:"dlc"`
	Data                []byte `json:"data"`
	DataHex             string `json:"data_hex"`
}

type Identifier struct {
	Priority    uint8  `json:"priority"`
	PGN         uint32 `json:"pgn"`
	Source      uint8  `json:"source"`
	Destination *uint8 `json:"destination,omitempty"`
	PDUFormat   uint8  `json:"pdu_format"`
	PDUSpecific uint8  `json:"pdu_specific"`
	DataPage    bool   `json:"data_page"`
}

type EventPayload struct {
	Interface        string     `json:"interface"`
	Sequence         uint64     `json:"sequence"`
	CapturedAtUnixMS uint64     `json:"captured_at_unix_ms"`
	CANID            uint32     `json:"can_id"`
	Identifier       Identifier `json:"identifier"`
	FD               bool       `json:"fd"`
	DLC              uint8      `json:"dlc"`
	Data             []byte     `json:"data"`
	DataHex          string     `json:"data_hex"`
}

type Connector struct {
	name      string
	cfg       Config
	client    *http.Client
	reconnect time.Duration
	cancel    context.CancelFunc
	mu        sync.RWMutex
	last      string
}

func New(name string, raw json.RawMessage) (connector.Connector, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, errors.New("j1939-device-agent connector requires url")
	}
	if cfg.TopicPrefix == "" {
		cfg.TopicPrefix = "j1939"
	}
	reconnect := 2 * time.Second
	if cfg.Reconnect != "" {
		d, err := time.ParseDuration(cfg.Reconnect)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid j1939 reconnect duration %q", cfg.Reconnect)
		}
		reconnect = d
	}
	if name == "" {
		name = "j1939-device-agent"
	}
	return &Connector{name: name, cfg: cfg, client: &http.Client{}, reconnect: reconnect}, nil
}

func (c *Connector) Name() string { return c.name }
func (c *Connector) Start(ctx context.Context, h connector.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go c.loop(ctx, h)
	return nil
}
func (c *Connector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}
func (c *Connector) Health(context.Context) connector.Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return connector.Health{Healthy: c.last == "" || c.last == "ok", Message: c.last}
}
func (c *Connector) setHealth(v string) { c.mu.Lock(); c.last = v; c.mu.Unlock() }

func (c *Connector) loop(ctx context.Context, h connector.Handler) {
	for ctx.Err() == nil {
		if err := c.consume(ctx, h); err != nil && ctx.Err() == nil {
			c.setHealth(err.Error())
			slog.Warn("J1939 Device Agent stream disconnected", "connector", c.name, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.reconnect):
			}
		}
	}
}

func (c *Connector) consume(ctx context.Context, h connector.Handler) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("device agent returned %s", resp.Status)
	}
	c.setHealth("ok")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var frame RawFrame
		if err := json.Unmarshal([]byte(data), &frame); err != nil {
			continue
		}
		ev, ok := c.translate(frame)
		if !ok {
			continue
		}
		if err := h(ctx, ev); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("device agent SSE stream ended")
}

func (c *Connector) translate(frame RawFrame) (connector.Event, bool) {
	if !frame.Extended || frame.Error || frame.Remote || frame.CANID > 0x1fffffff {
		return connector.Event{}, false
	}
	id := DecodeIdentifier(frame.CANID)
	payload, _ := json.Marshal(EventPayload{
		Interface: frame.Interface, Sequence: frame.Sequence, CapturedAtUnixMS: frame.CapturedAtUnixMS,
		CANID: frame.CANID, Identifier: id, FD: frame.FD, DLC: frame.DLC, Data: frame.Data, DataHex: frame.DataHex,
	})
	topic := fmt.Sprintf("%s/pgn/%06X", strings.TrimRight(c.cfg.TopicPrefix, "/"), id.PGN)
	return connector.Event{Topic: topic, Payload: payload, Headers: map[string]string{
		"x-nodra-ingress": "j1939-device-agent", "x-nodra-connector": c.name,
		"x-j1939-pgn": strconv.FormatUint(uint64(id.PGN), 10), "x-can-interface": frame.Interface,
	}}, true
}

func DecodeIdentifier(id uint32) Identifier {
	id &= 0x1fffffff
	priority := uint8((id >> 26) & 0x7)
	dataPage := ((id >> 24) & 0x1) != 0
	pf := uint8((id >> 16) & 0xff)
	ps := uint8((id >> 8) & 0xff)
	source := uint8(id & 0xff)
	pgn := (id >> 8) & 0x3ffff
	var destination *uint8
	if pf < 240 {
		pgn &= 0x3ff00
		d := ps
		destination = &d
	}
	return Identifier{Priority: priority, PGN: pgn, Source: source, Destination: destination, PDUFormat: pf, PDUSpecific: ps, DataPage: dataPage}
}
