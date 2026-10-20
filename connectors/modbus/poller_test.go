package modbus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestNewPollerRTUDefaults(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{Transport: "rtu", Device: "/dev/ttyS1", UnitID: 1, Topic: "factory/plc", Quantity: 2})
	c, err := NewPoller("plc", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*Poller)
	if p.transport != "rtu" || p.cfg.Baud != 9600 || p.cfg.DataBits != 8 || p.cfg.StopBits != 1 {
		t.Fatalf("unexpected defaults: %+v", p.cfg)
	}
}

func TestNewPollerRejectsMissingRTUDevice(t *testing.T) {
	raw := json.RawMessage(`{"transport":"rtu","topic":"factory/plc"}`)
	if _, err := NewPoller("plc", raw); err == nil {
		t.Fatal("missing RTU device accepted")
	}
}

func TestNewPollerRejectsOversizedQuantity(t *testing.T) {
	raw := json.RawMessage(`{"address":"127.0.0.1:502","topic":"factory/plc","quantity":126}`)
	if _, err := NewPoller("plc", raw); err == nil {
		t.Fatal("quantity 126 accepted at config time")
	}
}

type errReader struct{}

func (errReader) ReadHoldingRegisters(context.Context, uint16, uint16) ([]uint16, error) {
	return nil, errors.New("boom")
}

// Run with -race: Health must be safe against a concurrent poll.
func TestPollerHealthConcurrentWithPoll(t *testing.T) {
	p := &Poller{name: "m", cli: errReader{}, transport: "tcp", cfg: PollerConfig{Quantity: 1}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.poll(context.Background(), nil)
		}
	}()
	for i := 0; i < 200; i++ {
		p.Health(context.Background())
	}
	<-done
}
