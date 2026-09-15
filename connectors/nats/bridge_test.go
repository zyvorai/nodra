// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

// fakeConn/fakeClient substitute for *Conn/*Client in bridge tests,
// avoiding any real network I/O — mirrors connectors/opcua/poller_test.go's
// fakeReader-substitution pattern.
type fakeMsg struct {
	subject string
	payload []byte
}
type fakeConn struct {
	mu   sync.Mutex
	msgs []fakeMsg
	idx  int
	err  error
}

func (f *fakeConn) Next(ctx context.Context) (string, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx < len(f.msgs) {
		m := f.msgs[f.idx]
		f.idx++
		return m.subject, m.payload, nil
	}
	if f.err != nil {
		return "", nil, f.err
	}
	<-ctx.Done()
	return "", nil, ctx.Err()
}
func (f *fakeConn) Close() error { return nil }

type fakeClient struct {
	conn *fakeConn
	err  error
}

func (f *fakeClient) Connect(ctx context.Context) (natsConn, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

func TestNewBridgeRejectsMissingFields(t *testing.T) {
	cases := []string{
		`{"subjects":["a"]}`,           // missing address
		`{"address":"127.0.0.1:4222"}`, // missing subjects
		`{"address":"127.0.0.1:4222","subjects":["a"],"reconnect":"nope"}`, // bad reconnect
	}
	for _, raw := range cases {
		if _, err := NewBridge("b", json.RawMessage(raw)); err == nil {
			t.Fatalf("expected error for config %s", raw)
		}
	}
}

func TestNewBridgeDefaults(t *testing.T) {
	raw, _ := json.Marshal(BridgeConfig{Address: "127.0.0.1:4222", Subjects: []string{"a", "b"}})
	c, err := NewBridge("plant", raw)
	if err != nil {
		t.Fatal(err)
	}
	b := c.(*Bridge)
	if b.reconnect != 2*time.Second {
		t.Fatalf("reconnect default=%v", b.reconnect)
	}
	if b.Name() != "plant" {
		t.Fatalf("name=%q", b.Name())
	}
}

func TestBridgeConsumeEmitsExpectedEventShape(t *testing.T) {
	fc := &fakeConn{msgs: []fakeMsg{{"factory.line1", []byte("hello")}}, err: errors.New("stream ended")}
	b := &Bridge{name: "plant", cfg: BridgeConfig{TopicPrefix: "vendor/nats"}, cli: &fakeClient{conn: fc}}

	var got connector.Event
	var calls int
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = b.consume(ctx, func(_ context.Context, ev connector.Event) error {
		calls++
		got = ev
		return nil
	})

	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
	if got.Topic != "vendor/nats/factory.line1" {
		t.Fatalf("topic=%q", got.Topic)
	}
	if string(got.Payload) != "hello" {
		t.Fatalf("payload=%q", got.Payload)
	}
	if got.Headers["x-nodra-ingress"] != "nats" || got.Headers["x-nodra-connector"] != "plant" || got.Headers["x-nats-subject"] != "factory.line1" {
		t.Fatalf("headers=%v", got.Headers)
	}
}

func TestBridgeTopicWithoutPrefixUsesBareSubject(t *testing.T) {
	fc := &fakeConn{msgs: []fakeMsg{{"a.b", []byte("x")}}, err: errors.New("done")}
	b := &Bridge{name: "n", cli: &fakeClient{conn: fc}}
	var got connector.Event
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = b.consume(ctx, func(_ context.Context, ev connector.Event) error {
		got = ev
		return nil
	})
	if got.Topic != "a.b" {
		t.Fatalf("topic=%q", got.Topic)
	}
}

func TestBridgeReconnectsOnConsumeError(t *testing.T) {
	b := &Bridge{name: "n", cli: &fakeClient{err: errors.New("dial refused")}, reconnect: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	b.loop(ctx, func(context.Context, connector.Event) error { return nil })
	if h := b.Health(context.Background()); h.Healthy {
		t.Fatalf("expected unhealthy after repeated connect failures, got %+v", h)
	}
}

func TestBridgeStopsOnHandlerError(t *testing.T) {
	fc := &fakeConn{msgs: []fakeMsg{{"a", []byte("1")}, {"a", []byte("2")}}}
	b := &Bridge{name: "n", cli: &fakeClient{conn: fc}}
	var calls int
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := b.consume(ctx, func(context.Context, connector.Event) error {
		calls++
		return errors.New("handler failed")
	})
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1 (consume should stop on first error)", calls)
	}
	if err == nil {
		t.Fatal("expected consume to return the handler error")
	}
}
