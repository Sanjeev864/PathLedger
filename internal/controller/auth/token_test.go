package auth

import (
	"bytes"
	"net/http"
	"testing"
)

func TestGenerateAndParse(t *testing.T) {
	tok, hash, err := Generate(PrefixAgent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(tok, PrefixAgent); err != nil {
		t.Fatalf("fresh token rejected: %v", err)
	}
	if !bytes.Equal(hash, Hash(tok)) {
		t.Fatal("hash mismatch")
	}
	if _, err := Parse(tok, PrefixAPIKey); err == nil {
		t.Fatal("token accepted with wrong prefix")
	}
	if _, err := Parse(PrefixAgent+"short", PrefixAgent); err == nil {
		t.Fatal("short token accepted")
	}
	other, _, _ := Generate(PrefixAgent)
	if other == tok {
		t.Fatal("tokens must be unique")
	}
}

func TestBearerToken(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	if _, ok := BearerToken(r); ok {
		t.Fatal("missing header accepted")
	}
	r.Header.Set("Authorization", "bearer abc")
	if tok, ok := BearerToken(r); !ok || tok != "abc" {
		t.Fatalf("got %q %v", tok, ok)
	}
	r.Header.Set("Authorization", "Basic abc")
	if _, ok := BearerToken(r); ok {
		t.Fatal("basic auth accepted")
	}
}
