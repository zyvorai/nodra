// Package mqtt implements the small MQTT 3.1.1 edge-ingress subset Nodra needs:
// CONNECT, SUBSCRIBE, PUBLISH QoS 0/1, PUBACK, PINGREQ and DISCONNECT.
// It is intentionally not a replacement for a full broker; persistent sessions and QoS2
// are out of scope. The broker is sufficient for sensors/PLCs to publish locally and for
// local applications to subscribe while the cloud is unavailable.
package mqtt

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/router"
)

type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}
type Handler func(context.Context, Message) error

type Broker struct {
	Addr    string
	Handler Handler
	ln      net.Listener
	mu      sync.RWMutex
	clients map[*client]struct{}
}
type subscription struct {
	filter string
	qos    byte
}
type client struct {
	c      net.Conn
	wmu    sync.Mutex
	subs   []subscription
	closed chan struct{}
}

func New(addr string, h Handler) *Broker {
	return &Broker{Addr: addr, Handler: h, clients: map[*client]struct{}{}}
}
func (b *Broker) Start(ctx context.Context) error {
	if b.Addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", b.Addr)
	if err != nil {
		return err
	}
	b.ln = ln
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		cl := &client{c: c, closed: make(chan struct{})}
		b.mu.Lock()
		b.clients[cl] = struct{}{}
		b.mu.Unlock()
		go b.serve(ctx, cl)
	}
}
func (b *Broker) Close() error {
	if b.ln != nil {
		return b.ln.Close()
	}
	return nil
}
func (b *Broker) Publish(msg Message) {
	b.mu.RLock()
	cs := make([]*client, 0, len(b.clients))
	for c := range b.clients {
		cs = append(cs, c)
	}
	b.mu.RUnlock()
	for _, c := range cs {
		for _, s := range c.subs {
			if router.Match(s.filter, msg.Topic) {
				_ = c.sendPublish(msg.Topic, msg.Payload, minQoS(msg.QoS, s.qos))
				break
			}
		}
	}
}
func minQoS(a, b byte) byte {
	if a < b {
		return a
	}
	return b
}
func (b *Broker) serve(ctx context.Context, cl *client) {
	defer func() { _ = cl.c.Close(); b.mu.Lock(); delete(b.clients, cl); b.mu.Unlock(); close(cl.closed) }()
	r := bufio.NewReader(cl.c)
	connected := false
	for {
		_ = cl.c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		typ, flags, payload, err := readPacket(r)
		if err != nil {
			return
		}
		switch typ {
		case 1:
			if connected {
				return
			}
			if err = validateConnect(payload); err != nil {
				return
			}
			if err = cl.write([]byte{0x20, 0x02, 0x00, 0x00}); err != nil {
				return
			}
			connected = true
		case 3:
			if !connected {
				return
			}
			topic, body, qos, pid, er := parsePublish(flags, payload)
			if er != nil {
				return
			}
			msg := Message{Topic: topic, Payload: body, QoS: qos, Retain: flags&1 != 0}
			if b.Handler != nil {
				_ = b.Handler(ctx, msg)
			}
			b.Publish(msg)
			if qos == 1 {
				_ = cl.write([]byte{0x40, 0x02, byte(pid >> 8), byte(pid)})
			}
		case 8:
			if !connected {
				return
			}
			pid, subs, er := parseSubscribe(payload)
			if er != nil {
				return
			}
			cl.subs = append(cl.subs, subs...)
			ack := []byte{0x90, byte(2 + len(subs)), byte(pid >> 8), byte(pid)}
			for _, s := range subs {
				ack = append(ack, s.qos)
			}
			if err = cl.write(ack); err != nil {
				return
			}
		case 12:
			if err = cl.write([]byte{0xd0, 0x00}); err != nil {
				return
			}
		case 14:
			return
		default: /* ignore unsupported packets */
		}
	}
}
func (c *client) write(p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.c.Write(p)
	return err
}
func (c *client) sendPublish(topic string, payload []byte, qos byte) error {
	vh := appendString(nil, topic)
	if qos > 0 {
		vh = append(vh, 0, 1)
	}
	body := append(vh, payload...)
	h := byte(0x30)
	if qos == 1 {
		h |= 0x02
	}
	pkt := []byte{h}
	pkt = append(pkt, encodeRemaining(len(body))...)
	pkt = append(pkt, body...)
	return c.write(pkt)
}
func readPacket(r *bufio.Reader) (byte, byte, []byte, error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, 0, nil, err
	}
	n, mul := 0, 1
	for i := 0; i < 4; i++ {
		b, er := r.ReadByte()
		if er != nil {
			return 0, 0, nil, er
		}
		n += int(b&127) * mul
		if b&128 == 0 {
			break
		}
		mul *= 128
		if i == 3 {
			return 0, 0, nil, errors.New("malformed remaining length")
		}
	}
	if n > 16<<20 {
		return 0, 0, nil, errors.New("packet too large")
	}
	p := make([]byte, n)
	_, err = io.ReadFull(r, p)
	return h >> 4, h & 15, p, err
}
func encodeRemaining(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 128
		}
		out = append(out, d)
		if n == 0 {
			return out
		}
	}
}
func readString(p []byte) (string, []byte, error) {
	if len(p) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(p[:2]))
	if len(p) < 2+n {
		return "", nil, io.ErrUnexpectedEOF
	}
	return string(p[2 : 2+n]), p[2+n:], nil
}
func appendString(dst []byte, s string) []byte {
	dst = append(dst, byte(len(s)>>8), byte(len(s)))
	return append(dst, []byte(s)...)
}
func validateConnect(p []byte) error {
	proto, rest, err := readString(p)
	if err != nil {
		return err
	}
	if proto != "MQTT" || len(rest) < 4 || rest[0] != 4 {
		return errors.New("only MQTT 3.1.1 supported")
	}
	return nil
}
func parsePublish(flags byte, p []byte) (string, []byte, byte, uint16, error) {
	topic, rest, err := readString(p)
	if err != nil || topic == "" {
		return "", nil, 0, 0, errors.New("invalid publish topic")
	}
	qos := (flags >> 1) & 3
	if qos > 1 {
		return "", nil, qos, 0, errors.New("QoS2 unsupported")
	}
	var pid uint16
	if qos == 1 {
		if len(rest) < 2 {
			return "", nil, qos, 0, io.ErrUnexpectedEOF
		}
		pid = binary.BigEndian.Uint16(rest[:2])
		rest = rest[2:]
	}
	return topic, append([]byte(nil), rest...), qos, pid, nil
}
func parseSubscribe(p []byte) (uint16, []subscription, error) {
	if len(p) < 2 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	pid := binary.BigEndian.Uint16(p[:2])
	p = p[2:]
	var out []subscription
	for len(p) > 0 {
		f, rest, err := readString(p)
		if err != nil || len(rest) < 1 {
			return 0, nil, errors.New("invalid subscribe")
		}
		q := rest[0]
		if q > 1 {
			return 0, nil, fmt.Errorf("QoS %d unsupported", q)
		}
		out = append(out, subscription{f, q})
		p = rest[1:]
	}
	if len(out) == 0 {
		return 0, nil, errors.New("empty subscribe")
	}
	return pid, out, nil
}
