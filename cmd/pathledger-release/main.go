// Command pathledger-release creates the signing key for agent updates and
// signs release manifests. Run it on the release machine, never on the
// controller: whoever holds the private key can update every agent.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/internal/fsperm"
	"github.com/sanjeev864/pathledger/internal/update"
)

const usage = `usage:
  pathledger-release keygen -out release.key
      create a signing key (mode 0600) and print its public key
  pathledger-release sign -key release.key -version v0.3.0 [-dir dist]
      write dir/manifest.json and dir/manifest.json.sig for every
      pathledger-agent-<os>-<arch>[-<variant>] binary in dir
  pathledger-release verify -pub <base64> [-dir dist]
      check a signed manifest and the binaries it lists
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "keygen":
		path := fs.String("out", "release.key", "private key file to create")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("creating key file (it must not exist yet): %w", err)
		}
		if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := fsperm.RestrictFile(*path); err != nil {
			return fmt.Errorf("restricting access to the key file: %w", err)
		}
		fmt.Fprintf(out, "private key written to %s — keep it offline and backed up\npublic key (set PATHLEDGER_UPDATE_PUBLIC_KEY on agents):\n%s\n",
			*path, base64.StdEncoding.EncodeToString(pub))
		return nil

	case "sign":
		keyPath := fs.String("key", "release.key", "private key file")
		version := fs.String("version", "", "release version, e.g. v0.3.0")
		dir := fs.String("dir", "dist", "directory with the agent binaries")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		priv, err := loadKey(*keyPath)
		if err != nil {
			return err
		}
		m, err := buildManifest(*dir, *version)
		if err != nil {
			return err
		}
		raw, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*dir, "manifest.json"), raw, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*dir, "manifest.json.sig"), []byte(update.Sign(raw, priv)+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "signed %s with %d artifacts\n", m.Version, len(m.Artifacts))
		return nil

	case "verify":
		pub := fs.String("pub", "", "base64 public key(s), comma-separated")
		dir := fs.String("dir", "dist", "directory with manifest.json and binaries")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		keys, err := update.ParsePublicKeys(*pub)
		if err != nil || len(keys) == 0 {
			return fmt.Errorf("a public key is required: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(*dir, "manifest.json"))
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(filepath.Join(*dir, "manifest.json.sig"))
		if err != nil {
			return err
		}
		m, err := update.Verify(raw, string(sig), keys)
		if err != nil {
			return err
		}
		for _, a := range m.Artifacts {
			sum, size, err := hashFile(filepath.Join(*dir, a.File))
			if err != nil {
				return err
			}
			if sum != a.SHA256 || size != a.Size {
				return fmt.Errorf("%s does not match the manifest", a.File)
			}
		}
		fmt.Fprintf(out, "manifest %s and %d binaries verified\n", m.Version, len(m.Artifacts))
		return nil
	}
	return errors.New(usage)
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a pathledger-release key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// buildManifest lists pathledger-agent-<os>-<arch>[-<variant>] files.
func buildManifest(dir, version string) (update.Manifest, error) {
	m := update.Manifest{Version: version, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m, err
	}
	for _, e := range entries {
		name := e.Name()
		rest, ok := strings.CutPrefix(name, "pathledger-agent-")
		if !ok || e.IsDir() || !update.ValidFile(name) || strings.Contains(rest, ".") {
			continue
		}
		parts := strings.Split(rest, "-")
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		a := update.Artifact{OS: parts[0], Arch: parts[1], File: name}
		if len(parts) == 3 {
			a.Variant = parts[2]
		}
		if a.SHA256, a.Size, err = hashFile(filepath.Join(dir, name)); err != nil {
			return m, err
		}
		m.Artifacts = append(m.Artifacts, a)
	}
	sort.Slice(m.Artifacts, func(i, j int) bool { return m.Artifacts[i].File < m.Artifacts[j].File })
	return m, m.Validate()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
