package durable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "value")
	if err := AtomicWrite(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
}
