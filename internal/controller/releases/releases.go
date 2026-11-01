// Package releases serves signed agent releases from a directory prepared
// with pathledger-release (manifest.json, manifest.json.sig and binaries).
// The controller never holds the signing key; agents verify signatures
// themselves, so this package only relays files.
package releases

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sanjeev864/pathledger/internal/update"
)

// Release is the currently published manifest.
type Release struct {
	Raw       []byte
	Signature string
	Manifest  update.Manifest
}

// Source reads the release directory, reloading when the manifest changes.
type Source struct {
	dir  string
	keys []ed25519.PublicKey

	mu      sync.Mutex
	modTime time.Time
	size    int64
	current *Release
	lastErr error
}

// New returns a source for dir. When keys are given, a manifest that no key
// signed is refused, so a broken release is caught before agents see it.
func New(dir string, keys []ed25519.PublicKey) (*Source, error) {
	s := &Source{dir: dir, keys: keys}
	if _, err := s.Current(); err != nil {
		return nil, err
	}
	return s, nil
}

// Current returns the published release, or nil when the directory has no
// manifest yet.
func (s *Source) Current() (*Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "manifest.json")
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		s.current, s.lastErr, s.modTime = nil, nil, time.Time{}
		return nil, nil
	}
	if err != nil {
		return s.current, err
	}
	if fi.ModTime().Equal(s.modTime) && fi.Size() == s.size {
		return s.current, s.lastErr
	}
	s.modTime, s.size = fi.ModTime(), fi.Size()
	s.current, s.lastErr = s.load(path)
	return s.current, s.lastErr
}

func (s *Source) load(path string) (*Release, error) {
	raw, err := readLimited(path, update.MaxManifestSize)
	if err != nil {
		return nil, err
	}
	sig, err := readLimited(path+".sig", 1024)
	if err != nil {
		return nil, fmt.Errorf("release manifest has no signature: %w", err)
	}
	var m update.Manifest
	if len(s.keys) > 0 {
		if m, err = update.Verify(raw, string(sig), s.keys); err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("decoding release manifest: %w", err)
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
	}
	return &Release{Raw: raw, Signature: string(sig), Manifest: m}, nil
}

// Open opens an artifact listed in the current manifest.
func (s *Source) Open(name string) (*os.File, int64, error) {
	rel, err := s.Current()
	if err != nil || rel == nil {
		return nil, 0, os.ErrNotExist
	}
	for _, a := range rel.Manifest.Artifacts {
		if a.File == name && update.ValidFile(name) {
			f, err := os.Open(filepath.Join(s.dir, name))
			return f, a.Size, err
		}
	}
	return nil, 0, os.ErrNotExist
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, nil
}
