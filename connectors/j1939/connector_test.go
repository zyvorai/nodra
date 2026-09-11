package j1939

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func TestDecodePDU2Identifier(t *testing.T) {
	id := DecodeIdentifier(0x18FF50E5)
	if id.Priority != 6 || id.PGN != 0xFF50 || id.Source != 0xE5 || id.Destination != nil {
		t.Fatalf("unexpected decode: %+v", id)
	}
}

func TestDecodePDU1ZerosDestinationFromPGN(t *testing.T) {
	id := DecodeIdentifier(0x18EA17F9) // Request PGN, destination 0x17, source 0xF9
	if id.PGN != 0xEA00 || id.Destination == nil || *id.Destination != 0x17 || id.Source != 0xF9 {
		t.Fatalf("unexpected decode: %+v", id)
	}
}

func TestTranslateRejectsStandardFrame(t *testing.T) {
	c := &Connector{name: "test", cfg: Config{TopicPrefix: "factory/j1939"}}
	if _, ok := c.translate(RawFrame{CANID: 0x123, Extended: false}); ok {
		t.Fatal("standard frame accepted")
	}
}

func TestSSEConnectorEmitsJ1939Event(t *testing.T) {
	frame := RawFrame{Sequence: 7, Interface: "can0", CapturedAtUnixMS: 1234, CANID: 0x18FF50E5, Extended: true, DLC: 3, Data: []byte{1, 2, 3}, DataHex: "010203"}
	body, _ := json.Marshal(frame)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: can.frame\ndata: %s\n\n", body)
	}))
	defer server.Close()
	raw, _ := json.Marshal(Config{URL: server.URL, TopicPrefix: "minewing/j1939", Reconnect: "10s"})
	created, err := New("j1939", raw)
	if err != nil {
		t.Fatal(err)
	}
	c := created.(*Connector)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := make(chan connector.Event, 1)
	err = c.consume(ctx, func(_ context.Context, ev connector.Event) error { got <- ev; return context.Canceled })
	if err == nil {
		t.Fatal("expected handler cancellation")
	}
	select {
	case ev := <-got:
		if ev.Topic != "minewing/j1939/pgn/00FF50" {
			t.Fatalf("topic=%s", ev.Topic)
		}
		if ev.Headers["x-j1939-pgn"] != "65360" {
			t.Fatalf("headers=%v", ev.Headers)
		}
	default:
		t.Fatal("no event emitted")
	}
}
