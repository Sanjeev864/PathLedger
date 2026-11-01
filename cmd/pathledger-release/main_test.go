package main

import (
	"github.com/sanjeev864/pathledger/internal/fsperm"

	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "release.key")
	var out bytes.Buffer
	if err := run([]string{"keygen", "-out", key}, &out); err != nil {
		t.Fatal(err)
	}
	if ok, err := fsperm.Private(key); err != nil || !ok {
		t.Fatalf("key file is readable by others: %v", err)
	}
	if err := run([]string{"keygen", "-out", key}, &out); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	pub := lines[len(lines)-1]

	dist := filepath.Join(dir, "dist")
	_ = os.Mkdir(dist, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "pathledger-agent-linux-amd64"), []byte("binary-a"), 0o755)
	_ = os.WriteFile(filepath.Join(dist, "pathledger-agent-linux-arm-7"), []byte("binary-b"), 0o755)
	_ = os.WriteFile(filepath.Join(dist, "README.txt"), []byte("ignored"), 0o644)

	if err := run([]string{"sign", "-key", key, "-version", "v0.3.0", "-dir", dist}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "-key", key, "-version", "latest", "-dir", dist}, &out); err == nil {
		t.Fatal("non-release version signed")
	}
	if err := run([]string{"verify", "-pub", pub, "-dir", dist}, &out); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dist, "pathledger-agent-linux-amd64"), []byte("evil"), 0o755)
	if err := run([]string{"verify", "-pub", pub, "-dir", dist}, &out); err == nil {
		t.Fatal("tampered binary verified")
	}
}
