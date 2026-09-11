// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
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
	file, err := os.OpenFile(c.Device, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err = configureSerial(file.Fd(), c); err != nil {
		return nil, err
	}
	if err = syscall.SetNonblock(int(file.Fd()), true); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(c.timeout())
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = writeAll(ctx, file, request, deadline); err != nil {
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
		n, readErr := file.Read(buf[:min(len(buf), expected-len(response))])
		if n > 0 {
			response = append(response, buf[:n]...)
			if len(response) >= 3 && response[1]&0x80 != 0 {
				expected = 5 // unit + exception-function + code + CRC
			}
		}
		// A termios line configured with VMIN=0/VTIME=0 (set in configureSerial)
		// returns a 0-byte read - which Go's os.File surfaces as io.EOF - the
		// moment no data happens to be buffered yet. That is normal non-canonical
		// "nothing available right now" behavior for a character device, not an
		// actual end-of-file condition, so it must be treated the same as
		// EAGAIN/EWOULDBLOCK: keep polling until data arrives or the deadline
		// above fires. Treating it as fatal here made any live slave response
		// that wasn't already queued before the very first read() attempt fail
		// with a spurious "EOF" - i.e. effectively all of them.
		if readErr != nil && !errors.Is(readErr, syscall.EAGAIN) && !errors.Is(readErr, syscall.EWOULDBLOCK) && !errors.Is(readErr, io.EOF) {
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

func writeAll(ctx context.Context, file *os.File, data []byte, deadline time.Time) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("Modbus RTU write timeout")
		}
		n, err := file.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
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

func configureSerial(fd uintptr, c *RTUClient) error {
	var term syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&term)))
	if errno != 0 {
		return errno
	}
	term.Iflag = 0
	term.Oflag = 0
	term.Lflag = 0
	term.Cflag |= syscall.CREAD | syscall.CLOCAL
	term.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.PARODD | syscall.CSTOPB

	switch c.DataBits {
	case 0, 8:
		term.Cflag |= syscall.CS8
	case 7:
		term.Cflag |= syscall.CS7
	default:
		return fmt.Errorf("unsupported Modbus RTU data bits %d", c.DataBits)
	}
	switch c.Parity {
	case "", "none", "N", "n":
	case "even", "E", "e":
		term.Cflag |= syscall.PARENB
	case "odd", "O", "o":
		term.Cflag |= syscall.PARENB | syscall.PARODD
	default:
		return fmt.Errorf("unsupported Modbus RTU parity %q", c.Parity)
	}
	if c.StopBits == 2 {
		term.Cflag |= syscall.CSTOPB
	} else if c.StopBits != 0 && c.StopBits != 1 {
		return fmt.Errorf("unsupported Modbus RTU stop bits %d", c.StopBits)
	}

	speed, err := baudConstant(c.Baud)
	if err != nil {
		return err
	}
	term.Cflag &^= 0x100f // Linux asm-generic CBAUD mask
	term.Cflag |= speed
	term.Ispeed = speed
	term.Ospeed = speed
	term.Cc[syscall.VMIN] = 0
	term.Cc[syscall.VTIME] = 0
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&term)))
	if errno != 0 {
		return errno
	}
	return nil
}

func baudConstant(baud int) (uint32, error) {
	if baud == 0 {
		baud = 9600
	}
	values := map[int]uint32{
		1200: syscall.B1200, 2400: syscall.B2400, 4800: syscall.B4800,
		9600: syscall.B9600, 19200: syscall.B19200, 38400: syscall.B38400,
		57600: syscall.B57600, 115200: syscall.B115200, 230400: syscall.B230400,
	}
	value, ok := values[baud]
	if !ok {
		return 0, fmt.Errorf("unsupported Modbus RTU baud %d", baud)
	}
	return value, nil
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
