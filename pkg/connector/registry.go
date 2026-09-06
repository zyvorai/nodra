package connector

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Factory builds a Connector from raw JSON config.
type Factory func(name string, raw json.RawMessage) (Connector, error)

var (
	regMu     sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a connector type factory (e.g. "modbus").
func Register(typ string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	factories[typ] = f
}

// New constructs a connector by type name.
func New(typ, name string, raw json.RawMessage) (Connector, error) {
	regMu.RLock()
	f := factories[typ]
	regMu.RUnlock()
	if f == nil {
		return nil, fmt.Errorf("unknown connector type %q", typ)
	}
	if name == "" {
		name = typ
	}
	return f(name, raw)
}

// Spec is the agent config entry for a connector instance.
type Spec struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Config json.RawMessage `json:"config"`
}
