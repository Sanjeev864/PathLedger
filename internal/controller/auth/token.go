// Package auth generates and verifies PathLedger's bearer credentials.
//
// Every credential is 256 bits of randomness encoded as base64url behind a
// readable prefix. Only a SHA-256 hash is stored: because the secrets are
// high-entropy, a fast hash is sufficient (unlike passwords) and lets lookups
// use an index.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// Credential kinds, identified by prefix.
const (
	PrefixAPIKey     = "plk_"
	PrefixEnrollment = "ple_"
	PrefixAgent      = "pla_"
	PrefixSession    = "pls_"
	PrefixInvite     = "pli_"
)

const secretBytes = 32

// ErrMalformed means a presented credential does not have the expected shape.
var ErrMalformed = errors.New("malformed credential")

// Generate returns a new credential with prefix and its hash.
func Generate(prefix string) (token string, hash []byte, err error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = prefix + base64.RawURLEncoding.EncodeToString(b)
	return token, Hash(token), nil
}

// Hash returns the stored form of a credential.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// DisplayPrefix returns a short, non-secret identifier for a credential, safe
// to show in listings and logs.
func DisplayPrefix(token string) string {
	if len(token) < 12 {
		return token
	}
	return token[:12]
}

// Parse checks that token has the expected prefix and length.
func Parse(token, prefix string) (string, error) {
	if !strings.HasPrefix(token, prefix) {
		return "", ErrMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[len(prefix):])
	if err != nil || len(raw) != secretBytes {
		return "", ErrMalformed
	}
	return token, nil
}

// BearerToken extracts the token from an Authorization header.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const scheme = "Bearer "
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(scheme):]), true
}
