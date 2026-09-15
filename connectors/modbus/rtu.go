// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/zyvorai/nodra/internal/serialport"
)

// RTUClient is a dependency-free Modbus RTU client for Linux serial ports.
// It opens the configured device for each transaction so reconnect/recovery is
// deterministic on unplug/replug. RS485 direction control is expected to be
// handled by the kernel/driver/device-tree unless ConfigureRS485 is explicitly
// enabled in a future transport revision.
type RTUClient struct {
	Device   string
	UnitID   byte
	Baud     int
	DataBits int
	Parity   string
	StopBits int
	Timeout  time.Duration
}

func (c *RTUClient) ReadHoldingRegisters(ctx context.Context, address, quantity uint16) ([]uint16, error) {
	if quantity == 0 || quantity > 125 {
		return nil, errors.New("quantity must be 1..125")
	}
	request := appendCRC([]byte{
		c.UnitID, 0x03,
		byte(address >> 8), byte(address),
		byte(quantity >> 8), byte(quantity),
	})
	response, err := c.roundTrip(ctx, request, 5+int(quantity)*2)
	if err != nil {
		return nil, err
	}
	if len(response) < 5 || response[0] != c.UnitID {
		return nil, errors.New("malformed Modbus RTU response")
	}
	if response[1]&0x80 != 0 {
		return nil, fmt.Errorf("Modbus exception %d", response[2])
	}
	if response[1] != 0x03 || int(response[2]) != int(quantity)*2 {
		return nil, errors.New("unexpected Modbus RTU read response")
	}
	out := make([]uint16, quantity)
	for i := range out {
		start := 3 + i*2
		out[i] = binary.BigEndian.Uint16(response[start : start+2])
	}
	return out, nil
}

func (c *RTUClient) WriteSingleRegister(ctx context.Context, address, value uint16) error {
	request := appendCRC([]byte{
		c.UnitID, 0x06,
		byte(address >> 8), byte(address),
		byte(value >> 8), byte(value),
	})
	response, err := c.roundTrip(ctx, request, 8)
	if err != nil {
		return err
	}
	if len(response) != 8 || response[0] != c.UnitID {
		return errors.New("malformed Modbus RTU write response")
	}
	if response[1]&0x80 != 0 {
		return fmt.Errorf("Modbus exception %d", response[2])
	}
	if response[1] != 0x06 || binary.BigEndian.Uint16(response[2:4]) != address || binary.BigEndian.Uint16(response[4:6]) != value {
		return errors.New("write echo mismatch")
	}
	return nil
}

func (c *RTUClient) roundTrip(ctx context.Context, request []byte, expected int) ([]byte, error) {
	if c.Device == "" {
		return nil, errors.New("RTU device is required")
	}
	port, err := serialport.Open(c.Device, serialport.Config{
		Baud: c.Baud, DataBits: c.DataBits, Parity: c.Parity, StopBits: c.StopBits,
	})
	if err != nil {
		return nil, err
	}
	defer port.Close()
	deadline := time.Now().Add(c.timeout())
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = writeAll(ctx, port, request, deadline); err != nil {
		return nil, err
	}

	response := make([]byte, 0, expected)
	buf := make([]byte, expected)
	for len(response) < expected {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("Modbus RTU timeout after %s", c.timeout())
		}
		// port.Read already treats EAGAIN/EWOULDBLOCK and the spurious 0-byte
		// io.EOF a VMIN=0/VTIME=0 termios line produces when nothing is
		// buffered yet (normal non-canonical "nothing available right now"
		// behavior for a character device, not an actual end-of-file) as
		// (0, nil) rather than an error - see internal/serialport.Port.Read.
		// Treating that condition as fatal here made any live slave response
		// that wasn't already queued before the very first read() attempt
		// fail with a spurious "EOF" - i.e. effectively all of them.
		n, readErr := port.Read(buf[:min(len(buf), expected-len(response))])
		if n > 0 {
			response = append(response, buf[:n]...)
			if len(response) >= 3 && response[1]&0x80 != 0 {
				expected = 5 // unit + exception-function + code + CRC
			}
		}
		if readErr != nil {
			return nil, readErr
		}
		if n == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !validCRC(response) {
		return nil, errors.New("Modbus RTU CRC mismatch")
	}
	return response, nil
}

func writeAll(ctx context.Context, port *serialport.Port, data []byte, deadline time.Time) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("Modbus RTU write timeout")
		}
		n, err := port.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return nil
}

func (c *RTUClient) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 3 * time.Second
	}
	return c.Timeout
}

func appendCRC(frame []byte) []byte {
	crc := crc16(frame)
	return append(frame, byte(crc), byte(crc>>8))
}

func validCRC(frame []byte) bool {
	if len(frame) < 3 {
		return false
	}
	want := uint16(frame[len(frame)-2]) | uint16(frame[len(frame)-1])<<8
	return crc16(frame[:len(frame)-2]) == want
}

func crc16(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, b := range data {
		crc ^= uint16(b)
		for range 8 {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
