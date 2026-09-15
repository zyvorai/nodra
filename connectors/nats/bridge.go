// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func init() { connector.Register("nats", NewBridge) }

// BridgeConfig configures a subscribing NATS bridge: dial Address,
// subscribe to Subjects, and publish each received message into the
// ingest pipeline under TopicPrefix/<subject> (or bare <subject> when
// TopicPrefix is empty).
type BridgeConfig struct {
	Address       string   `json:"address"`
	Subjects      []string `json:"subjects"`
	TopicPrefix   string   `json:"topic_prefix,omitempty"`
	User          string   `json:"user,omitempty"`
	Pass          string   `json:"pass,omitempty"`
	Token         string   `json:"token,omitempty"`
	Name          string   `json:"name,omitempty"`
	ReconnectText string   `json:"reconnect,omitempty"`
}

// natsConn is satisfied by *Conn; bridge tests substitute a fake to avoid
// any real network I/O.
type natsConn interface {
	Next(ctx context.Context) (subject string, payload []byte, err error)
	Close() error
}

// natsClient is satisfied by *Client (via clientAdapter); bridge tests
// substitute a fake.
type natsClient interface {
	Connect(ctx context.Context) (natsConn, error)
}

// clientAdapter adapts *Client — whose Connect returns the concrete *Conn
// — to the natsClient interface Bridge depends on.
type clientAdapter struct{ cli *Client }

func (a clientAdapter) Connect(ctx context.Context) (natsConn, error) { return a.cli.Connect(ctx) }

// Bridge is a persistent-connection, push-driven connector: unlike the
// ticker-driven Modbus/OPC-UA pollers, NATS SUB is inherently push, so it
// reconnects and blocks on Conn.Next rather than polling on an interval.
type Bridge struct {
	name      string
	cfg       BridgeConfig
	cli       natsClient
	reconnect time.Duration
	cancel    context.CancelFunc
	mu        sync.RWMutex
	last      string
}

func NewBridge(name string, raw json.RawMessage) (connector.Connector, error) {
	var cfg BridgeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Address == "" {
		return nil, errors.New("nats connector requires address")
	}
	if len(cfg.Subjects) == 0 {
		return nil, errors.New("nats connector requires at least one subject")
	}
	reconnect := 2 * time.Second
	if cfg.ReconnectText != "" {
		d, err := time.ParseDuration(cfg.ReconnectText)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid nats reconnect duration %q", cfg.ReconnectText)
		}
		reconnect = d
	}
	if name == "" {
		name = "nats"
	}
	cli := &Client{Addr: cfg.Address, Subjects: cfg.Subjects, Name: cfg.Name, User: cfg.User, Pass: cfg.Pass, Token: cfg.Token}
	return &Bridge{name: name, cfg: cfg, cli: clientAdapter{cli}, reconnect: reconnect}, nil
}

func (b *Bridge) Name() string { return b.name }

func (b *Bridge) Start(ctx context.Context, h connector.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	go b.loop(ctx, h)
	return nil
}

func (b *Bridge) Close() error {
	if b.cancel != nil {
		b.cancel()
	}
	return nil
}

func (b *Bridge) Health(context.Context) connector.Health {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return connector.Health{Healthy: b.last == "" || b.last == "ok", Message: b.last}
}

func (b *Bridge) setHealth(v string) {
	b.mu.Lock()
	b.last = v
	b.mu.Unlock()
}

func (b *Bridge) loop(ctx context.Context, h connector.Handler) {
	for ctx.Err() == nil {
		if err := b.consume(ctx, h); err != nil && ctx.Err() == nil {
			b.setHealth(err.Error())
			slog.Warn("NATS bridge disconnected", "connector", b.name, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(b.reconnect):
			}
		}
	}
}

func (b *Bridge) consume(ctx context.Context, h connector.Handler) error {
	conn, err := b.cli.Connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	b.setHealth("ok")
	for {
		subject, payload, err := conn.Next(ctx)
		if err != nil {
			return err
		}
		topic := subject
		if b.cfg.TopicPrefix != "" {
			topic = strings.TrimRight(b.cfg.TopicPrefix, "/") + "/" + subject
		}
		ev := connector.Event{
			Topic:   topic,
			Payload: payload,
			Headers: map[string]string{
				"x-nodra-ingress":   "nats",
				"x-nodra-connector": b.name,
				"x-nats-subject":    subject,
			},
		}
		if err := h(ctx, ev); err != nil {
			return err
		}
	}
}
