// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func init() { connector.Register("opcua", NewPoller) }

// nodeReader is satisfied by *Client; poller tests substitute a fake.
type nodeReader interface {
	Read(ctx context.Context, nodeIDs []NodeID) ([]DataValue, error)
}

// subscribeClient is satisfied by *Client; poller tests substitute a fake.
type subscribeClient interface {
	Subscribe(ctx context.Context, nodeIDs []NodeID, interval time.Duration, handler func(NodeID, DataValue)) error
}

// PollerConfig configures an OPC-UA poller. Default security is
// SecurityPolicy None with an anonymous session; set SecurityPolicy to
// Basic256Sha256 (plus all three cert paths) for a Sign or SignAndEncrypt
// channel. The user identity token stays anonymous either way — see
// docs/INDUSTRIAL_PROTOCOLS.md. Mode "poll" (the default) ticks Read on
// Interval; mode "subscribe" instead opens one long-lived
// Subscribe/MonitoredItems session and reconnects (waiting Interval between
// attempts) on any error — see Client.Subscribe's doc for why this is the
// least-verified part of the package.
type PollerConfig struct {
	Endpoint       string   `json:"endpoint"`
	NodeIDs        []string `json:"node_ids"`
	Topic          string   `json:"topic"`
	Mode           string   `json:"mode,omitempty"`
	Interval       string   `json:"interval,omitempty"`
	Timeout        string   `json:"timeout,omitempty"`
	SecurityPolicy string   `json:"security_policy,omitempty"`
	SecurityMode   string   `json:"security_mode,omitempty"`
	ClientCertPath string   `json:"client_cert_path,omitempty"`
	ClientKeyPath  string   `json:"client_key_path,omitempty"`
	ServerCertPath string   `json:"server_cert_path,omitempty"`
}

type Poller struct {
	name    string
	cfg     PollerConfig
	nodeIDs []NodeID
	cli     nodeReader
	subCli  subscribeClient
	ival    time.Duration
	cancel  context.CancelFunc
	last    string
}

func NewPoller(name string, raw json.RawMessage) (connector.Connector, error) {
	var cfg PollerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("opcua connector requires endpoint")
	}
	if cfg.Topic == "" {
		return nil, fmt.Errorf("opcua connector requires topic")
	}
	if len(cfg.NodeIDs) == 0 {
		return nil, fmt.Errorf("opcua connector requires at least one node_id")
	}
	nodeIDs := make([]NodeID, 0, len(cfg.NodeIDs))
	for _, s := range cfg.NodeIDs {
		id, err := ParseNodeID(s)
		if err != nil {
			return nil, fmt.Errorf("opcua node_id %q: %w", s, err)
		}
		nodeIDs = append(nodeIDs, id)
	}
	ival := 5 * time.Second
	if cfg.Interval != "" {
		d, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			return nil, fmt.Errorf("opcua interval: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("opcua interval must be positive")
		}
		ival = d
	}
	timeout := 5 * time.Second
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("opcua timeout: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("opcua timeout must be positive")
		}
		timeout = d
	}
	switch cfg.Mode {
	case "", "poll", "subscribe":
	default:
		return nil, fmt.Errorf("opcua mode must be poll or subscribe, got %q", cfg.Mode)
	}
	sec := SecurityConfig{
		SecurityPolicy: cfg.SecurityPolicy,
		SecurityMode:   cfg.SecurityMode,
		ClientCertPath: cfg.ClientCertPath,
		ClientKeyPath:  cfg.ClientKeyPath,
		ServerCertPath: cfg.ServerCertPath,
	}
	if _, err := resolveSecurity(sec); err != nil {
		return nil, err
	}
	if name == "" {
		name = "opcua"
	}
	client := &Client{Endpoint: cfg.Endpoint, Timeout: timeout, Security: sec}
	return &Poller{
		name: name, cfg: cfg, nodeIDs: nodeIDs, ival: ival,
		cli: client, subCli: client,
	}, nil
}

func (p *Poller) Name() string { return p.name }
func (p *Poller) Start(ctx context.Context, h connector.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	if p.cfg.Mode == "subscribe" {
		go p.subscribeLoop(ctx, h)
	} else {
		go p.loop(ctx, h)
	}
	return nil
}

// subscribeLoop keeps a long-lived Subscribe session open (see
// Client.Subscribe), reconnecting with a p.ival backoff on any error —
// mirroring the reconnect-on-error shape connectors/nats and connectors/
// j1939 already use for their own persistent-connection connectors, since
// this is fundamentally that same push-driven model, not the ticker-driven
// polling connectors/modbus/connectors/opcua's own Read mode use.
func (p *Poller) subscribeLoop(ctx context.Context, h connector.Handler) {
	for ctx.Err() == nil {
		err := p.subCli.Subscribe(ctx, p.nodeIDs, p.ival, func(id NodeID, dv DataValue) {
			p.emitOne(ctx, h, id, dv)
		})
		if ctx.Err() != nil {
			return
		}
		p.last = fmt.Sprintf("subscribe error, reconnecting: %v", err)
		slog.Warn("opcua subscribe disconnected", "connector", p.name, "endpoint", p.cfg.Endpoint, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.ival):
		}
	}
}

// emitOne publishes a single Subscribe-mode data-change notification, using
// the same event shape (one values[] array) as poll() does for Read mode so
// downstream local routes/transforms don't need to special-case which mode
// produced an event.
func (p *Poller) emitOne(ctx context.Context, h connector.Handler, id NodeID, dv DataValue) {
	ov := opcuaValue{NodeID: id.String(), Value: dv.Value, StatusCode: dv.StatusCode}
	if dv.SourceTimestamp != nil {
		ov.SourceTimestamp = dv.SourceTimestamp.Format(time.RFC3339Nano)
	}
	payload, _ := json.Marshal(map[string]any{"endpoint": p.cfg.Endpoint, "values": []opcuaValue{ov}})
	if err := h(ctx, connector.Event{
		Topic: p.cfg.Topic, Payload: payload,
		Headers: map[string]string{"x-nodra-ingress": "opcua", "x-nodra-connector": p.name},
	}); err != nil {
		p.last = err.Error()
		slog.Warn("opcua ingest failed", "connector", p.name, "error", err)
		return
	}
	p.last = "ok"
}
func (p *Poller) loop(ctx context.Context, h connector.Handler) {
	t := time.NewTicker(p.ival)
	defer t.Stop()
	p.poll(ctx, h)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll(ctx, h)
		}
	}
}

type opcuaValue struct {
	NodeID          string `json:"node_id"`
	Value           any    `json:"value,omitempty"`
	StatusCode      uint32 `json:"status_code,omitempty"`
	SourceTimestamp string `json:"source_timestamp,omitempty"`
}

func (p *Poller) poll(ctx context.Context, h connector.Handler) {
	values, err := p.cli.Read(ctx, p.nodeIDs)
	if err != nil {
		p.last = err.Error()
		slog.Warn("opcua poll failed", "connector", p.name, "endpoint", p.cfg.Endpoint, "error", err)
		return
	}
	out := make([]opcuaValue, len(values))
	for i, v := range values {
		ov := opcuaValue{NodeID: p.cfg.NodeIDs[i], Value: v.Value, StatusCode: v.StatusCode}
		if v.SourceTimestamp != nil {
			ov.SourceTimestamp = v.SourceTimestamp.Format(time.RFC3339Nano)
		}
		out[i] = ov
	}
	payload, _ := json.Marshal(map[string]any{"endpoint": p.cfg.Endpoint, "values": out})
	if err = h(ctx, connector.Event{
		Topic: p.cfg.Topic, Payload: payload,
		Headers: map[string]string{"x-nodra-ingress": "opcua", "x-nodra-connector": p.name},
	}); err != nil {
		p.last = err.Error()
		slog.Warn("opcua ingest failed", "connector", p.name, "error", err)
		return
	}
	p.last = "ok"
}

func (p *Poller) Health(context.Context) connector.Health {
	ok := p.last == "" || p.last == "ok"
	return connector.Health{Healthy: ok, Message: p.last}
}
func (p *Poller) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
