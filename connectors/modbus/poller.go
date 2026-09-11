// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package modbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func init() { connector.Register("modbus", NewPoller) }

type registerReader interface {
	ReadHoldingRegisters(context.Context, uint16, uint16) ([]uint16, error)
}

// PollerConfig supports both Modbus TCP and Modbus RTU. transport defaults to tcp.
type PollerConfig struct {
	Transport string `json:"transport,omitempty"`
	Address   string `json:"address,omitempty"`
	Device    string `json:"device,omitempty"`
	Baud      int    `json:"baud,omitempty"`
	DataBits  int    `json:"data_bits,omitempty"`
	Parity    string `json:"parity,omitempty"`
	StopBits  int    `json:"stop_bits,omitempty"`
	UnitID    uint8  `json:"unit_id"`
	Start     uint16 `json:"start"`
	Quantity  uint16 `json:"quantity"`
	Topic     string `json:"topic"`
	Interval  string `json:"interval"`
	Timeout   string `json:"timeout"`
}

type Poller struct {
	name      string
	cfg       PollerConfig
	cli       registerReader
	transport string
	ival      time.Duration
	cancel    context.CancelFunc
	last      string
}

func NewPoller(name string, raw json.RawMessage) (connector.Connector, error) {
	var cfg PollerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
	if transport == "" {
		transport = "tcp"
	}
	if cfg.Topic == "" {
		return nil, fmt.Errorf("modbus connector requires topic")
	}
	if cfg.Quantity == 0 {
		cfg.Quantity = 1
	}
	ival := 5 * time.Second
	if cfg.Interval != "" {
		d, err := time.ParseDuration(cfg.Interval)
		if err != nil {
			return nil, fmt.Errorf("modbus interval: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("modbus interval must be positive")
		}
		ival = d
	}
	timeout := 3 * time.Second
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("modbus timeout: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("modbus timeout must be positive")
		}
		timeout = d
	}

	var cli registerReader
	switch transport {
	case "tcp":
		if cfg.Address == "" {
			return nil, fmt.Errorf("modbus TCP connector requires address")
		}
		cli = &Client{Address: cfg.Address, UnitID: cfg.UnitID, Timeout: timeout}
	case "rtu":
		if cfg.Device == "" {
			return nil, fmt.Errorf("modbus RTU connector requires device")
		}
		if cfg.Baud == 0 {
			cfg.Baud = 9600
		}
		if cfg.DataBits == 0 {
			cfg.DataBits = 8
		}
		if cfg.Parity == "" {
			cfg.Parity = "none"
		}
		if cfg.StopBits == 0 {
			cfg.StopBits = 1
		}
		if _, err := baudConstant(cfg.Baud); err != nil {
			return nil, err
		}
		cli = &RTUClient{
			Device: cfg.Device, UnitID: cfg.UnitID, Baud: cfg.Baud,
			DataBits: cfg.DataBits, Parity: cfg.Parity, StopBits: cfg.StopBits,
			Timeout: timeout,
		}
	default:
		return nil, fmt.Errorf("unsupported modbus transport %q (want tcp or rtu)", transport)
	}
	if name == "" {
		name = "modbus"
	}
	return &Poller{name: name, cfg: cfg, cli: cli, transport: transport, ival: ival}, nil
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
func (p *Poller) poll(ctx context.Context, h connector.Handler) {
	regs, err := p.cli.ReadHoldingRegisters(ctx, p.cfg.Start, p.cfg.Quantity)
	if err != nil {
		p.last = err.Error()
		slog.Warn("modbus poll failed", "connector", p.name, "transport", p.transport, "error", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"transport": p.transport, "unit_id": p.cfg.UnitID, "start": p.cfg.Start,
		"quantity": p.cfg.Quantity, "registers": regs,
	})
	if err = h(ctx, connector.Event{
		Topic: p.cfg.Topic, Payload: payload,
		Headers: map[string]string{"x-nodra-ingress": "modbus", "x-nodra-connector": p.name, "x-modbus-transport": p.transport},
	}); err != nil {
		p.last = err.Error()
		slog.Warn("modbus ingest failed", "connector", p.name, "error", err)
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
