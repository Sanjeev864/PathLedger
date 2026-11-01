package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.2.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v0.3.0", "dev", false},
		{"v0.3.0-rc1", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"v01.0.0", "v0.2.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func manifest() Manifest {
	return Manifest{Version: "v0.3.0", Artifacts: []Artifact{
		{OS: "linux", Arch: "arm", Variant: "7", File: "pathledger-agent-linux-arm-7", SHA256: strings.Repeat("a", 64), Size: 10},
		{OS: "linux", Arch: "amd64", File: "pathledger-agent-linux-amd64", SHA256: strings.Repeat("b", 64), Size: 10},
	}}
}

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	raw, _ := json.Marshal(manifest())
	sig := Sign(raw, priv)

	keys, err := ParsePublicKeys(base64.StdEncoding.EncodeToString(other) + ", " + base64.StdEncoding.EncodeToString(pub))
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	m, err := Verify(raw, sig, keys)
	if err != nil || m.Version != "v0.3.0" {
		t.Fatal(m, err)
	}
	if _, err := Verify(raw, sig, keys[:1]); err != ErrBadSignature {
		t.Fatalf("wrong key accepted: %v", err)
	}
	tampered := []byte(strings.Replace(string(raw), "v0.3.0", "v0.3.1", 1))
	if _, err := Verify(tampered, sig, keys); err != ErrBadSignature {
		t.Fatalf("tampered manifest accepted: %v", err)
	}
	if _, err := Verify(raw, "not base64!", keys); err != ErrBadSignature {
		t.Fatal(err)
	}
	if _, err := ParsePublicKeys("abc"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestValidateAndFor(t *testing.T) {
	m := manifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if a, ok := m.For(Platform{"linux", "arm", "7"}); !ok || a.Variant != "7" {
		t.Fatal(a)
	}
	if _, ok := m.For(Platform{"linux", "arm", "6"}); ok {
		t.Fatal("wrong ARM variant matched")
	}
	if a, ok := m.For(Platform{"linux", "amd64", "v1"}); !ok || a.Arch != "amd64" {
		t.Fatal("variant-less artifact should match")
	}
	for _, bad := range []func(*Manifest){
		func(m *Manifest) { m.Version = "latest" },
		func(m *Manifest) { m.Artifacts = nil },
		func(m *Manifest) { m.Artifacts[0].File = "../../etc/passwd" },
		func(m *Manifest) { m.Artifacts[0].SHA256 = "XYZ" },
		func(m *Manifest) { m.Artifacts[0].Size = MaxBinarySize + 1 },
	} {
		m := manifest()
		bad(&m)
		if m.Validate() == nil {
			t.Fatalf("accepted %+v", m)
		}
	}
}
