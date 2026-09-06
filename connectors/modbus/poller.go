package modbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func init() {
	connector.Register("modbus", NewPoller)
}

// PollerConfig drives a periodic Modbus TCP holding-register poller.
type PollerConfig struct {
	Address  string `json:"address"`
	UnitID   uint8  `json:"unit_id"`
	Start    uint16 `json:"start"`
	Quantity uint16 `json:"quantity"`
	Topic    string `json:"topic"`
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
}

// Poller implements connector.Connector for Modbus TCP.
type Poller struct {
	name   string
	cfg    PollerConfig
	cli    Client
	ival   time.Duration
	cancel context.CancelFunc
	last   string
}

func NewPoller(name string, raw json.RawMessage) (connector.Connector, error) {
	var cfg PollerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Address == "" || cfg.Topic == "" {
		return nil, fmt.Errorf("modbus connector requires address and topic")
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
		ival = d
	}
	timeout := 3 * time.Second
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("modbus timeout: %w", err)
		}
		timeout = d
	}
	if name == "" {
		name = "modbus"
	}
	return &Poller{
		name: name,
		cfg:  cfg,
		ival: ival,
		cli:  Client{Address: cfg.Address, UnitID: cfg.UnitID, Timeout: timeout},
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

func (p *Poller) poll(ctx context.Context, h connector.Handler) {
	regs, err := p.cli.ReadHoldingRegisters(ctx, p.cfg.Start, p.cfg.Quantity)
	if err != nil {
		p.last = err.Error()
		slog.Warn("modbus poll failed", "connector", p.name, "error", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"unit_id":   p.cfg.UnitID,
		"start":     p.cfg.Start,
		"quantity":  p.cfg.Quantity,
		"registers": regs,
	})
	if err = h(ctx, connector.Event{
		Topic:   p.cfg.Topic,
		Payload: payload,
		Headers: map[string]string{"x-nodra-ingress": "modbus", "x-nodra-connector": p.name},
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
