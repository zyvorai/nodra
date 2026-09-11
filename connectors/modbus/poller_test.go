package modbus

import (
	"encoding/json"
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
