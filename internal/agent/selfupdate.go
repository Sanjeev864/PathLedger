package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sanjeev864/pathledger/internal/update"
)

// Self-update layout inside the state directory (which is writable, unlike
// the installed binary under ProtectSystem=strict):
//
//	bin/pathledger-agent-<version>  verified binary
//	bin/manifest.json(.sig)         the signed manifest it came from; written
//	                                last, so it is the commit point
//	bin/attempts                    starts of the new version not yet healthy
//	bin/bad-version                 a version that failed to start; skipped
//
// The installed binary acts as a launcher: at start it re-verifies the
// signature and hash and execs the newer binary. A version that fails to
// become healthy within maxStartAttempts starts is marked bad, and the
// launcher falls back to itself.

// ErrUpdateInstalled is returned by Run after a newer release was installed;
// the caller should exec the path returned by Launch.
var ErrUpdateInstalled = errors.New("a newer agent release was installed")

// LaunchedEnv marks a process started by the launcher, so it does not try
// to launch again.
const LaunchedEnv = "PATHLEDGER_AGENT_LAUNCHED"

const maxStartAttempts = 3

func binDir(stateDir string) string { return filepath.Join(stateDir, "bin") }

// Launch returns the path of a verified agent binary newer than current, or
// "" when the running binary should continue. It counts the start attempt.
func Launch(stateDir, current string, keys []ed25519.PublicKey, log *slog.Logger) string {
	if len(keys) == 0 {
		return ""
	}
	dir := binDir(stateDir)
	m, path, err := installed(dir, keys)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warn("ignoring installed agent update", "error", err)
		}
		return ""
	}
	if !update.Newer(m.Version, current) || badVersion(dir) == m.Version {
		return ""
	}
	attempts := readInt(filepath.Join(dir, "attempts"))
	if attempts >= maxStartAttempts {
		log.Error("agent update failed to start repeatedly; rolling back", "version", m.Version, "attempts", attempts)
		_ = writeFileAtomic(filepath.Join(dir, "bad-version"), []byte(m.Version), 0o600)
		return ""
	}
	if err := writeFileAtomic(filepath.Join(dir, "attempts"), []byte(strconv.Itoa(attempts+1)), 0o600); err != nil {
		log.Warn("cannot record update start attempt; not launching update", "error", err)
		return ""
	}
	return path
}

// markHealthy records that the running version works.
func markHealthy(stateDir string) {
	p := filepath.Join(binDir(stateDir), "attempts")
	if readInt(p) != 0 {
		_ = writeFileAtomic(p, []byte("0"), 0o600)
	}
}

// installed verifies bin/manifest.json and the binary it names.
func installed(dir string, keys []ed25519.PublicKey) (update.Manifest, string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return update.Manifest{}, "", err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		return update.Manifest{}, "", err
	}
	m, err := update.Verify(raw, string(sig), keys)
	if err != nil {
		return m, "", err
	}
	art, ok := m.For(update.Current())
	if !ok {
		return m, "", fmt.Errorf("release %s has no binary for this platform", m.Version)
	}
	path := filepath.Join(dir, "pathledger-agent-"+m.Version)
	sum, size, err := hashFile(path)
	if err != nil {
		return m, "", err
	}
	if sum != art.SHA256 || size != art.Size {
		return m, "", fmt.Errorf("%s does not match its signed manifest", path)
	}
	return m, path, nil
}

// installUpdate downloads, verifies and commits the release the controller
// announces. It returns true when a newer version is ready to launch.
func (a *Agent) installUpdate(ctx context.Context, announced string) (bool, error) {
	dir := binDir(a.cfg.StateDir)
	if badVersion(dir) == announced {
		return false, nil
	}
	cl := a.api()
	var raw, sig bytes.Buffer
	if _, err := cl.Download(ctx, "/api/v1/agent/update/manifest.json", &raw, update.MaxManifestSize); err != nil {
		return false, err
	}
	if _, err := cl.Download(ctx, "/api/v1/agent/update/manifest.json.sig", &sig, 1024); err != nil {
		return false, err
	}
	m, err := update.Verify(raw.Bytes(), sig.String(), a.cfg.UpdateKeys)
	if err != nil {
		return false, err
	}
	// Decide only on signed data: the announcement is just a hint.
	if !update.Newer(m.Version, a.cfg.Version) || badVersion(dir) == m.Version {
		return false, nil
	}
	art, ok := m.For(update.Current())
	if !ok {
		return false, fmt.Errorf("release %s has no binary for this platform", m.Version)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := cl.Download(ctx, "/api/v1/agent/update/files/"+art.File, io.MultiWriter(tmp, h), art.Size)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if n != art.Size || hex.EncodeToString(h.Sum(nil)) != art.SHA256 {
		return false, fmt.Errorf("downloaded %s does not match its signed hash", art.File)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return false, err
	}
	target := filepath.Join(dir, "pathledger-agent-"+m.Version)
	if err := os.Rename(tmp.Name(), target); err != nil {
		return false, err
	}
	// Commit: attempts reset, signature, then the manifest itself.
	if err := writeFileAtomic(filepath.Join(dir, "attempts"), []byte("0"), 0o600); err != nil {
		return false, err
	}
	if err := writeFileAtomic(filepath.Join(dir, "manifest.json.sig"), sig.Bytes(), 0o600); err != nil {
		return false, err
	}
	if err := writeFileAtomic(filepath.Join(dir, "manifest.json"), raw.Bytes(), 0o600); err != nil {
		return false, err
	}
	removeOldBinaries(dir, filepath.Base(target), a.log)
	a.log.Info("agent update installed", "version", m.Version)
	return true, nil
}

func removeOldBinaries(dir, keep string, log *slog.Logger) {
	entries, _ := os.ReadDir(dir)
	self, _ := os.Executable()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "pathledger-agent-") || name == keep {
			continue
		}
		p := filepath.Join(dir, name)
		if p == self {
			continue // still running; removed after the next update
		}
		if err := os.Remove(p); err != nil {
			log.Debug("could not remove old agent binary", "path", p, "error", err)
		}
	}
}

func badVersion(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "bad-version"))
	return strings.TrimSpace(string(b))
}

func readInt(path string) int {
	b, _ := os.ReadFile(path)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, update.MaxBinarySize+1))
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// writeFileAtomic writes via a temporary file and rename.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
