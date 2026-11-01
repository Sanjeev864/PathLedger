package fsperm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestrict(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secret")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RestrictDir(dir); err != nil {
		t.Fatal(err)
	}
	if ok, err := Private(dir); err != nil || !ok {
		t.Fatalf("dir not private: %v %v", ok, err)
	}
	f := filepath.Join(dir, "key")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestrictFile(f); err != nil {
		t.Fatal(err)
	}
	if ok, err := Private(f); err != nil || !ok {
		t.Fatalf("file not private: %v %v", ok, err)
	}
}
