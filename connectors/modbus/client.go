// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package modbus implements a dependency-free Modbus TCP client suitable for Nodra adapters.
// It supports function 0x03 (Read Holding Registers) and 0x06 (Write Single Register).
package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

type Client struct {
	Address string
	UnitID  byte
	Timeout time.Duration
	tx      atomic.Uint32
}

func (c *Client) deadline(ctx context.Context) (time.Time, error) {
	d := time.Now().Add(c.Timeout)
	if c.Timeout <= 0 {
		d = time.Now().Add(5 * time.Second)
	}
	if cd, ok := ctx.Deadline(); ok && cd.Before(d) {
		d = cd
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	return d, nil
}
func (c *Client) roundTrip(ctx context.Context, pdu []byte) ([]byte, error) {
	d, err := c.deadline(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", c.Address, time.Until(d))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(d)
	tx := uint16(c.tx.Add(1))
	req := make([]byte, 7+len(pdu))
	binary.BigEndian.PutUint16(req[0:2], tx)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint16(req[4:6], uint16(1+len(pdu)))
	req[6] = c.UnitID
	copy(req[7:], pdu)
	if _, err = conn.Write(req); err != nil {
		return nil, err
	}
	hdr := make([]byte, 7)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint16(hdr[:2]) != tx || binary.BigEndian.Uint16(hdr[2:4]) != 0 {
		return nil, errors.New("invalid Modbus transaction response")
	}
	n := int(binary.BigEndian.Uint16(hdr[4:6]))
	if n < 2 || n > 260 {
		return nil, fmt.Errorf("invalid Modbus response length %d", n)
	}
	body := make([]byte, n-1)
	if _, err = io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	if body[0]&0x80 != 0 {
		if len(body) < 2 {
			return nil, errors.New("malformed Modbus exception")
		}
		return nil, fmt.Errorf("Modbus exception %d", body[1])
	}
	return body, nil
}
func (c *Client) ReadHoldingRegisters(ctx context.Context, address, quantity uint16) ([]uint16, error) {
	if quantity == 0 || quantity > 125 {
		return nil, errors.New("quantity must be 1..125")
	}
	pdu := []byte{0x03, byte(address >> 8), byte(address), byte(quantity >> 8), byte(quantity)}
	body, err := c.roundTrip(ctx, pdu)
	if err != nil {
		return nil, err
	}
	if len(body) < 2 || body[0] != 0x03 || int(body[1]) != int(quantity)*2 || len(body) != 2+int(body[1]) {
		return nil, errors.New("malformed read response")
	}
	out := make([]uint16, quantity)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(body[2+i*2 : 4+i*2])
	}
	return out, nil
}
func (c *Client) WriteSingleRegister(ctx context.Context, address, value uint16) error {
	pdu := []byte{0x06, byte(address >> 8), byte(address), byte(value >> 8), byte(value)}
	body, err := c.roundTrip(ctx, pdu)
	if err != nil {
		return err
	}
	if len(body) != 5 || body[0] != 0x06 {
		return errors.New("malformed write response")
	}
	if binary.BigEndian.Uint16(body[1:3]) != address || binary.BigEndian.Uint16(body[3:5]) != value {
		return errors.New("write echo mismatch")
	}
	return nil
}
