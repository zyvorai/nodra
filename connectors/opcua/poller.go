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

// PollerConfig configures a v1 OPC-UA poller: SecurityPolicy None, anonymous
// session, Read-only, polling only — see docs/INDUSTRIAL_PROTOCOLS.md.
type PollerConfig struct {
	Endpoint string   `json:"endpoint"`
	NodeIDs  []string `json:"node_ids"`
	Topic    string   `json:"topic"`
	Interval string   `json:"interval,omitempty"`
	Timeout  string   `json:"timeout,omitempty"`
}

type Poller struct {
	name    string
	cfg     PollerConfig
	nodeIDs []NodeID
	cli     nodeReader
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
	if name == "" {
		name = "opcua"
	}
	return &Poller{
		name: name, cfg: cfg, nodeIDs: nodeIDs, ival: ival,
		cli: &Client{Endpoint: cfg.Endpoint, Timeout: timeout},
	}, nil
}

func (p *Poller) Name() string { return p.name }
func (p *Poller) Start(ctx context.Context, h connector.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.loop(ctx, h)
	return nil
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
