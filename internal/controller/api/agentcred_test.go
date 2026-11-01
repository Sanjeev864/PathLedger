package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/api"
	"github.com/sanjeev864/pathledger/internal/controller/releases"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
	"github.com/sanjeev864/pathledger/internal/update"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// publishRelease writes a signed release with one linux/amd64 binary.
func publishRelease(t *testing.T, dir, version string, priv ed25519.PrivateKey) {
	t.Helper()
	bin := []byte("#!/bin/sh\necho " + version + "\n")
	_ = os.WriteFile(filepath.Join(dir, "pathledger-agent-linux-amd64"), bin, 0o755)
	m := update.Manifest{Version: version, Artifacts: []update.Artifact{{
		OS: "linux", Arch: "amd64", File: "pathledger-agent-linux-amd64", SHA256: sha(bin), Size: int64(len(bin)),
	}}}
	raw, _ := json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json.sig"), []byte(update.Sign(raw, priv)), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
	// Make the change visible to the mtime-based reload even within one tick.
	later := time.Now().Add(time.Duration(len(version)) * time.Second)
	_ = os.Chtimes(filepath.Join(dir, "manifest.json"), later, later)
}

func TestAgentCredentialRotationAndUpdates(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	src, err := releases.New(dir, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatal(err)
	}
	st := pgtest.New(t)
	srv := httptest.NewServer(api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		api.WithReleases(src), api.WithAgentTokenMaxAge(time.Hour)).Handler())
	t.Cleanup(srv.Close)
	tenant, _ := st.CreateTenant(context.Background(), "Acme")
	_, key, _ := st.CreateAPIKey(context.Background(), tenant.ID, "k")
	h := &harness{t: t, srv: srv, apiKey: key}

	var site, tok idResp
	h.call("POST", "/api/v1/sites", key, map[string]string{"name": "HQ"}, &site)
	h.call("POST", "/api/v1/sites/"+site.ID+"/enrollment-tokens", key, nil, &tok)
	var enrolled wire.EnrollResponse
	h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: tok.Token}, &enrolled)

	plan := func(token string) (wire.Plan, int) {
		var p wire.Plan
		code := h.call("GET", "/api/v1/agent/plan", token, nil, &p)
		return p, code
	}
	p, code := plan(enrolled.AgentToken)
	if code != 200 || p.RotateCredential || p.Update != nil {
		t.Fatalf("fresh agent plan: %d %+v", code, p)
	}

	// An admin asks for rotation; the agent sees it in its next plan.
	if code := h.call("POST", "/api/v1/agents/"+enrolled.AgentID+"/rotate-credential", key, nil, nil); code != 202 {
		t.Fatalf("request rotation: %d", code)
	}
	if p, _ := plan(enrolled.AgentToken); !p.RotateCredential {
		t.Fatal("plan does not ask for rotation")
	}
	var cred wire.CredentialResponse
	if code := h.call("POST", "/api/v1/agent/credential", enrolled.AgentToken, nil, &cred); code != 200 || !strings.HasPrefix(cred.AgentToken, "pla_") {
		t.Fatalf("rotate: %d", code)
	}
	if p, code := plan(cred.AgentToken); code != 200 || p.RotateCredential {
		t.Fatalf("plan with new token: %d %+v", code, p)
	}
	if _, code := plan(enrolled.AgentToken); code != 401 {
		t.Fatalf("old token still accepted after promotion: %d", code)
	}
	if code := h.call("POST", "/api/v1/agent/credential", key, nil, nil); code != 401 {
		t.Fatalf("API key rotated an agent credential: %d", code)
	}

	// No release published yet.
	if code := h.call("GET", "/api/v1/agent/update/manifest.json", cred.AgentToken, nil, nil); code != 404 {
		t.Fatalf("manifest without release: %d", code)
	}
	publishRelease(t, dir, "v0.3.0", priv)
	if p, _ := plan(cred.AgentToken); p.Update == nil || p.Update.Version != "v0.3.0" {
		t.Fatalf("plan does not announce the release: %+v", p.Update)
	}
	get := func(path string) (int, []byte) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+cred.AgentToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	_, raw := get("/api/v1/agent/update/manifest.json")
	_, sig := get("/api/v1/agent/update/manifest.json.sig")
	m, err := update.Verify(raw, string(sig), []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("served manifest does not verify: %v", err)
	}
	code, bin := get("/api/v1/agent/update/files/" + m.Artifacts[0].File)
	if code != 200 || sha(bin) != m.Artifacts[0].SHA256 {
		t.Fatalf("binary: %d", code)
	}
	for _, name := range []string{"manifest.json", "..%2Fmanifest.json", "other"} {
		if code, _ := get("/api/v1/agent/update/files/" + name); code != 404 {
			t.Fatalf("unlisted file %s served: %d", name, code)
		}
	}

	// A release signed with another key is refused, and not announced.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	publishRelease(t, dir, "v0.4.0", otherPriv)
	if p, _ := plan(cred.AgentToken); p.Update != nil {
		t.Fatalf("badly signed release announced: %+v", p.Update)
	}

	// The agent's reported version is recorded.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/agent/plan", nil)
	req.Header.Set("Authorization", "Bearer "+cred.AgentToken)
	req.Header.Set("User-Agent", "PathLedger-Agent/v0.3.0 (linux/amd64)")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	var agents struct {
		Items []struct {
			Version string `json:"version"`
		} `json:"items"`
	}
	h.call("GET", "/api/v1/agents", key, nil, &agents)
	if len(agents.Items) != 1 || agents.Items[0].Version != "v0.3.0" {
		t.Fatalf("agent version: %+v", agents)
	}
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
