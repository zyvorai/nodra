package modbus

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestReadHoldingRegisters(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, _ := ln.Accept()
		defer c.Close()
		h := make([]byte, 12)
		_, _ = io.ReadFull(c, h)
		tx := binary.BigEndian.Uint16(h[:2])
		resp := []byte{byte(tx >> 8), byte(tx), 0, 0, 0, 7, 1, 3, 4, 0, 10, 0, 20}
		_, _ = c.Write(resp)
	}()
	cl := Client{Address: ln.Addr().String(), UnitID: 1, Timeout: time.Second}
	v, err := cl.ReadHoldingRegisters(context.Background(), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || v[0] != 10 || v[1] != 20 {
		t.Fatalf("%v", v)
	}
}
