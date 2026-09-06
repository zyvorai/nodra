package auth

import "testing"

func TestTokenAndHash(t *testing.T) {
	tok, err := NewToken(24)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 48 {
		t.Fatalf("token length=%d", len(tok))
	}
	h := Hash(tok)
	if !EqualHash(h, tok) {
		t.Fatal("hash should match")
	}
	if EqualHash(h, tok+"x") {
		t.Fatal("hash should not match wrong token")
	}
}
func TestEqualToken(t *testing.T) {
	if !EqualToken("same", "same") {
		t.Fatal("expected equal")
	}
	if EqualToken("same", "different") {
		t.Fatal("expected different")
	}
}
func TestMalformedHash(t *testing.T) {
	if EqualHash("not-hex", "x") {
		t.Fatal("malformed hash matched")
	}
}
