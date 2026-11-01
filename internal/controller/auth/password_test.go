package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if err := VerifyPassword("correct horse battery", h); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if err := VerifyPassword("wrong horse battery", h); err != ErrMismatch {
		t.Fatalf("wrong password: %v", err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
	if err := VerifyPassword("x", "$bcrypt$nope"); err == nil {
		t.Fatal("foreign hash accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if CheckPasswordPolicy("short") == nil {
		t.Fatal("short password accepted")
	}
	if CheckPasswordPolicy(strings.Repeat("a", 200)) == nil {
		t.Fatal("overlong password accepted")
	}
	if err := CheckPasswordPolicy("ten chars!"); err != nil {
		t.Fatal(err)
	}
}
