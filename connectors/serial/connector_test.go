// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package serial

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func TestNewPollerDefaults(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{Device: "/dev/ttyUSB0", Topic: "factory/serial"})
	c, err := NewPoller("s", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*Poller)
	if p.cfg.Baud != 9600 || p.cfg.DataBits != 8 || p.cfg.StopBits != 1 || p.cfg.Parity != "none" {
		t.Fatalf("unexpected defaults: %+v", p.cfg)
	}
	if p.cfg.Framing != "delimiter" || p.delimiter != '\n' {
		t.Fatalf("unexpected framing defaults: framing=%q delimiter=%q", p.cfg.Framing, p.delimiter)
	}
	if p.reconnect != 2*time.Second {
		t.Fatalf("reconnect default=%v", p.reconnect)
	}
}

func TestNewPollerRejectsMissingFields(t *testing.T) {
	cases := []string{
		`{"topic":"x"}`,             // missing device
		`{"device":"/dev/ttyUSB0"}`, // missing topic
		`{"device":"/dev/ttyUSB0","topic":"x","baud":12345}`,      // unsupported baud
		`{"device":"/dev/ttyUSB0","topic":"x","framing":"bogus"}`, // unsupported framing
		`{"device":"/dev/ttyUSB0","topic":"x","delimiter":"ab"}`,  // multi-byte delimiter
		`{"device":"/dev/ttyUSB0","topic":"x","framing":"idle","idle_timeout":"not-a-duration"}`,
		`{"device":"/dev/ttyUSB0","topic":"x","timeout":"not-a-duration"}`,
	}
	for _, raw := range cases {
		if _, err := NewPoller("s", json.RawMessage(raw)); err == nil {
			t.Fatalf("expected error for config %s", raw)
		}
	}
}

func TestNewPollerIdleFraming(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{Device: "/dev/ttyUSB0", Topic: "t", Framing: "idle"})
	c, err := NewPoller("s", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*Poller)
	if p.idle != 100*time.Millisecond {
		t.Fatalf("idle default=%v", p.idle)
	}
}

// fakeSerialReader substitutes for *serialport.Port in readFrames() tests,
// avoiding any real device or build-tag dependency.
type fakeSerialReader struct {
	chunks [][]byte
	i      int
	closed bool
}

func (f *fakeSerialReader) Read(buf []byte) (int, error) {
	if f.i >= len(f.chunks) {
		return 0, io.EOF
	}
	c := f.chunks[f.i]
	f.i++
	n := copy(buf, c)
	return n, nil
}
func (f *fakeSerialReader) Close() error { f.closed = true; return nil }

func TestReadFramesDelimiterSplitsAcrossChunks(t *testing.T) {
	fr := &fakeSerialReader{chunks: [][]byte{
		[]byte("frame1\nfra"),
		[]byte("me2\n"),
		[]byte("frame3\n"),
	}}
	p := &Poller{name: "s", cfg: PollerConfig{Framing: "delimiter", Topic: "t"}, delimiter: '\n', maxFrame: 65536}

	var got []string
	err := p.readFrames(context.Background(), fr, func(_ context.Context, ev connector.Event) error {
		got = append(got, string(ev.Payload))
		if ev.Topic != "t" || ev.Headers["x-nodra-ingress"] != "serial" || ev.Headers["x-nodra-connector"] != "s" {
			t.Fatalf("unexpected event shape: %+v", ev)
		}
		return nil
	})
	if err != io.EOF {
		t.Fatalf("expected io.EOF once the fake stream ends, got %v", err)
	}
	want := []string{"frame1", "frame2", "frame3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d = %q, want %q", i, got[i], want[i])
		}
	}
	// readFrames itself doesn't close the port - consume() does - so the
	// fake reader should still be unclosed at this point.
	if fr.closed {
		t.Fatal("readFrames should not close the port itself")
	}
}

// idleThenQuietReader emits one chunk, then behaves like the real
// *serialport.Port does when no data is available: (0, nil), not an error -
// see internal/serialport.Port.Read's doc comment. A real io.EOF from the
// fake would (correctly) make readFrames propagate it as a genuine
// disconnect before ever reaching the idle-flush check, so it can't be used
// to test the idle path.
type idleThenQuietReader struct {
	chunk []byte
	sent  bool
}

func (f *idleThenQuietReader) Read(buf []byte) (int, error) {
	if f.sent {
		return 0, nil
	}
	f.sent = true
	return copy(buf, f.chunk), nil
}
func (f *idleThenQuietReader) Close() error { return nil }

func TestReadFramesIdleFlushesOnGap(t *testing.T) {
	fr := &idleThenQuietReader{chunk: []byte("partial-frame-no-delimiter")}
	p := &Poller{name: "s", cfg: PollerConfig{Framing: "idle", Topic: "t"}, idle: 5 * time.Millisecond, maxFrame: 65536}

	var got []string
	done := make(chan error, 1)
	go func() {
		done <- p.readFrames(context.Background(), fr, func(_ context.Context, ev connector.Event) error {
			got = append(got, string(ev.Payload))
			return io.EOF // stop the loop once we've captured the flushed frame
		})
	}()

	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("expected the handler's io.EOF to propagate, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle framing never flushed the buffered partial frame")
	}
	if len(got) != 1 || got[0] != "partial-frame-no-delimiter" {
		t.Fatalf("got=%v", got)
	}
}

func TestReadFramesDropsOversizedBufferWithoutDelimiter(t *testing.T) {
	fr := &fakeSerialReader{chunks: [][]byte{
		[]byte("aaaaaaaaaa"), // 10 bytes, no delimiter, exceeds maxFrame=4
		[]byte("b\n"),        // after the drop, a fresh frame should still be captured
	}}
	p := &Poller{name: "s", cfg: PollerConfig{Framing: "delimiter", Topic: "t"}, delimiter: '\n', maxFrame: 4}

	var got []string
	err := p.readFrames(context.Background(), fr, func(_ context.Context, ev connector.Event) error {
		got = append(got, string(ev.Payload))
		return nil
	})
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("expected the oversized buffer to be dropped and recovery to continue, got=%v", got)
	}
	if h := p.Health(context.Background()); h.Healthy {
		t.Fatalf("expected the max_frame drop to be reflected in health, got %+v", h)
	}
}

func TestReadFramesPropagatesHandlerError(t *testing.T) {
	fr := &fakeSerialReader{chunks: [][]byte{[]byte("x\n")}}
	p := &Poller{name: "s", cfg: PollerConfig{Framing: "delimiter", Topic: "t"}, delimiter: '\n', maxFrame: 65536}
	wantErr := io.ErrClosedPipe
	err := p.readFrames(context.Background(), fr, func(context.Context, connector.Event) error {
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
}
