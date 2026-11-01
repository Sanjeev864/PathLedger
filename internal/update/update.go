// Package update defines signed agent release manifests.
//
// A release is a manifest (JSON) listing one binary per platform with its
// SHA-256, plus an ed25519 signature over the manifest's exact bytes, made
// with a key that never leaves the release machine. Agents pin the public
// key, so a compromised controller or mirror cannot push a binary.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// MaxBinarySize bounds a downloaded agent binary.
const MaxBinarySize = 64 << 20

// MaxManifestSize bounds a manifest.
const MaxManifestSize = 64 << 10

// Artifact is one platform's binary.
type Artifact struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Variant string `json:"variant,omitempty"` // GOARM or GOMIPS, e.g. "7", "softfloat"
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// Manifest describes one release.
type Manifest struct {
	Version   string     `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	Artifacts []Artifact `json:"artifacts"`
}

// Platform identifies the binary this process needs.
type Platform struct{ OS, Arch, Variant string }

// Current returns the running binary's platform.
func Current() Platform {
	p := Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if (s.Key == "GOARM" && p.Arch == "arm") || (s.Key == "GOMIPS" && strings.HasPrefix(p.Arch, "mips")) {
				p.Variant = s.Value
			}
		}
	}
	return p
}

// For returns the artifact for p. A variant-less artifact matches any
// variant; an exact variant match is preferred.
func (m Manifest) For(p Platform) (Artifact, bool) {
	var fallback *Artifact
	for i, a := range m.Artifacts {
		if a.OS != p.OS || a.Arch != p.Arch {
			continue
		}
		if a.Variant == p.Variant {
			return a, true
		}
		if a.Variant == "" {
			fallback = &m.Artifacts[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return Artifact{}, false
}

// Validate checks the manifest's shape.
func (m Manifest) Validate() error {
	if _, ok := ParseVersion(m.Version); !ok {
		return fmt.Errorf("manifest version %q is not vMAJOR.MINOR.PATCH", m.Version)
	}
	if len(m.Artifacts) == 0 {
		return errors.New("manifest lists no artifacts")
	}
	for _, a := range m.Artifacts {
		if a.OS == "" || a.Arch == "" || !validFile(a.File) {
			return fmt.Errorf("artifact %+v: os, arch and a plain file name are required", a)
		}
		if len(a.SHA256) != 64 || strings.Trim(a.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("artifact %s: sha256 must be 64 lowercase hex characters", a.File)
		}
		if a.Size <= 0 || a.Size > MaxBinarySize {
			return fmt.Errorf("artifact %s: size %d out of range", a.File, a.Size)
		}
	}
	return nil
}

func validFile(name string) bool {
	if name == "" || len(name) > 128 || name[0] == '.' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// ValidFile reports whether name is a safe artifact file name.
func ValidFile(name string) bool { return validFile(name) }

// ParsePublicKeys parses a comma-separated list of base64 ed25519 public
// keys. Several keys allow the signing key to be rotated.
func ParsePublicKeys(s string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(part)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("update public key %q is not a base64 ed25519 public key", part)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	return keys, nil
}

// ErrBadSignature means no trusted key signed the manifest.
var ErrBadSignature = errors.New("release manifest signature is not valid for any trusted key")

// Verify checks sig (base64) over raw with any of keys and decodes the
// manifest. Nothing in an unverified manifest is trusted.
func Verify(raw []byte, sig string, keys []ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(s) != ed25519.SignatureSize {
		return m, ErrBadSignature
	}
	ok := false
	for _, k := range keys {
		if ed25519.Verify(k, raw, s) {
			ok = true
			break
		}
	}
	if !ok {
		return m, ErrBadSignature
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("decoding release manifest: %w", err)
	}
	return m, m.Validate()
}

// Sign returns the base64 signature of raw.
func Sign(raw []byte, key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))
}

// ParseVersion parses "vMAJOR.MINOR.PATCH" (pre-release and build suffixes
// are not accepted: only final releases are rolled out automatically).
func ParseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 1_000_000 || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Newer reports whether candidate is a strictly newer release than current.
// A current version that is not a release (e.g. "dev") never updates.
func Newer(candidate, current string) bool {
	c, ok1 := ParseVersion(candidate)
	r, ok2 := ParseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range 3 {
		if c[i] != r[i] {
			return c[i] > r[i]
		}
	}
	return false
}
