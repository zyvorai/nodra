//go:build linux

package modbus

import "testing"

func TestCRC16KnownRequest(t *testing.T) {
	request := []byte{0x01, 0x03, 0x00, 0x00, 0x00, 0x0a}
	withCRC := appendCRC(append([]byte(nil), request...))
	if got := withCRC[len(withCRC)-2:]; got[0] != 0xc5 || got[1] != 0xcd {
		t.Fatalf("CRC bytes = %02x %02x, want c5 cd", got[0], got[1])
	}
	if !validCRC(withCRC) {
		t.Fatal("known-good frame failed CRC validation")
	}
}

func TestCRCRejectsMutation(t *testing.T) {
	frame := appendCRC([]byte{0x11, 0x03, 0x00, 0x6b, 0x00, 0x03})
	frame[3] ^= 0x01
	if validCRC(frame) {
		t.Fatal("mutated frame unexpectedly passed CRC")
	}
}

func TestBaudValidation(t *testing.T) {
	if _, err := baudConstant(115200); err != nil {
		t.Fatal(err)
	}
	if _, err := baudConstant(12345); err == nil {
		t.Fatal("unsupported baud unexpectedly accepted")
	}
}
