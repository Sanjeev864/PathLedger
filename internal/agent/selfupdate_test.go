package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sanjeev864/pathledger/internal/update"
)

func installFake(t *testing.T, stateDir, version string, priv ed25519.PrivateKey, tamper bool) string {
	t.Helper()
	dir := binDir(stateDir)
	_ = os.MkdirAll(dir, 0o700)
	bin := []byte("binary " + version)
	sum := sha256.Sum256(bin)
	p := update.Current()
	m := update.Manifest{Version: version, Artifacts: []update.Artifact{{
		OS: p.OS, Arch: p.Arch, Variant: p.Variant, File: "pathledger-agent-x", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin)),
	}}}
	raw, _ := json.Marshal(m)
	path := filepath.Join(dir, "pathledger-agent-"+version)
	if tamper {
		bin = []byte("binary evil")
	}
	_ = os.WriteFile(path, bin, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json.sig"), []byte(update.Sign(raw, priv)), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600)
	return path
}

func TestLaunch(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := []ed25519.PublicKey{pub}
	state := t.TempDir()

	if got := Launch(state, "v0.1.0", keys, log); got != "" {
		t.Fatalf("nothing installed: %q", got)
	}
	path := installFake(t, state, "v0.2.0", priv, false)
	if got := Launch(state, "v0.1.0", nil, log); got != "" {
		t.Fatal("launched without trusted keys")
	}
	if got := Launch(state, "v0.2.0", keys, log); got != "" {
		t.Fatal("launched the same version")
	}
	if got := Launch(state, "dev", keys, log); got != "" {
		t.Fatal("dev build launched an update")
	}
	if got := Launch(state, "v0.1.0", keys, log); got != path {
		t.Fatalf("launch: %q", got)
	}
	markHealthy(state)
	// Crash loop: after maxStartAttempts unhealthy starts the version is
	// marked bad and the launcher keeps running itself.
	for i := 0; i < maxStartAttempts; i++ {
		if got := Launch(state, "v0.1.0", keys, log); got != path {
			t.Fatalf("attempt %d: %q", i, got)
		}
	}
	if got := Launch(state, "v0.1.0", keys, log); got != "" {
		t.Fatal("crash-looping update launched again")
	}
	if badVersion(binDir(state)) != "v0.2.0" {
		t.Fatal("bad version not recorded")
	}

	// Tampered binary or foreign signature: never launched.
	state2 := t.TempDir()
	installFake(t, state2, "v0.2.0", priv, true)
	if got := Launch(state2, "v0.1.0", keys, log); got != "" {
		t.Fatal("tampered binary launched")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	state3 := t.TempDir()
	installFake(t, state3, "v0.2.0", other, false)
	if got := Launch(state3, "v0.1.0", keys, log); got != "" {
		t.Fatal("binary signed by an untrusted key launched")
	}
}
