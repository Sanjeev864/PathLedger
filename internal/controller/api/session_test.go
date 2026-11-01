package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/sanjeev864/pathledger/internal/controller/api"
	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
)

// browser is an HTTPS client with a cookie jar, like a signed-in dashboard.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func newBrowser(t *testing.T, srv *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	c := *srv.Client() // copy: srv.Client() returns one shared client
	c.Jar = jar
	return &browser{t: t, base: srv.URL, c: &c}
}

func (b *browser) do(method, path string, body any, out any) int {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	if b.csrf != "" {
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type me struct {
	Auth        string `json:"auth"`
	TenantID    string `json:"tenant_id"`
	Role        string `json:"role"`
	CSRFToken   string `json:"csrf_token"`
	Memberships []struct {
		TenantID string `json:"tenant_id"`
		Role     string `json:"role"`
	} `json:"memberships"`
}

func (b *browser) login(email, pw string) (me, int) {
	var m me
	code := b.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw}, &m)
	b.csrf = m.CSRFToken
	return m, code
}

func setupPeople(t *testing.T) (*httptest.Server, *store.Store, store.Tenant) {
	st := pgtest.New(t)
	srv := httptest.NewTLSServer(api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	tenant, _ := st.CreateTenant(ctx, "Acme")
	hash, _ := auth.HashPassword("owner password 123")
	owner, _ := st.CreateUser(ctx, "owner@acme.example", "Olivia", hash)
	_ = st.AddMembership(ctx, owner.ID, tenant.ID, store.RoleOwner)
	return srv, st, tenant
}

func TestLoginSessionAndCSRF(t *testing.T) {
	srv, st, tenant := setupPeople(t)
	b := newBrowser(t, srv)

	if _, code := b.login("owner@acme.example", "wrong password!!"); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	if _, code := b.login("nobody@acme.example", "owner password 123"); code != 401 {
		t.Fatalf("unknown user: %d", code)
	}
	m, code := b.login("OWNER@acme.example", "owner password 123")
	if code != 200 || m.Role != "owner" || m.TenantID != tenant.ID || m.CSRFToken == "" {
		t.Fatalf("login: %d %+v", code, m)
	}
	var got me
	if code := b.do("GET", "/api/v1/auth/me", nil, &got); code != 200 || got.Auth != "session" {
		t.Fatalf("me: %d %+v", code, got)
	}

	csrf := b.csrf
	b.csrf = ""
	if code := b.do("POST", "/api/v1/sites", map[string]string{"name": "HQ"}, nil); code != 403 {
		t.Fatalf("state change without CSRF token: %d", code)
	}
	b.csrf = csrf
	if code := b.do("POST", "/api/v1/sites", map[string]string{"name": "HQ"}, nil); code != 201 {
		t.Fatalf("create site with CSRF token: %d", code)
	}

	// An MSP user switches between customers.
	other, _ := st.CreateTenant(context.Background(), "Globex")
	u, _ := st.UserByEmail(context.Background(), "owner@acme.example")
	_ = st.AddMembership(context.Background(), u.ID, other.ID, store.RoleViewer)
	var switched me
	if code := b.do("POST", "/api/v1/auth/switch", map[string]string{"tenant_id": other.ID}, &switched); code != 200 || switched.Role != "viewer" {
		t.Fatalf("switch: %d %+v", code, switched)
	}
	var sites struct{ Items []any }
	if b.do("GET", "/api/v1/sites", nil, &sites); len(sites.Items) != 0 {
		t.Fatal("switched session still sees the previous tenant's sites")
	}
	if code := b.do("POST", "/api/v1/sites", map[string]string{"name": "X"}, nil); code != 403 {
		t.Fatalf("viewer created a site: %d", code)
	}

	if code := b.do("POST", "/api/v1/auth/logout", nil, nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code := b.do("GET", "/api/v1/auth/me", nil, nil); code != 401 {
		t.Fatalf("session survived logout: %d", code)
	}
}

func TestInvitationFlowAndRoles(t *testing.T) {
	srv, _, _ := setupPeople(t)
	owner := newBrowser(t, srv)
	owner.login("owner@acme.example", "owner password 123")

	var inv struct {
		Token string `json:"token"`
		Link  string `json:"link"`
	}
	if code := owner.do("POST", "/api/v1/invitations", map[string]string{"email": "Admin@Acme.example", "role": "admin"}, &inv); code != 201 || inv.Token == "" {
		t.Fatalf("invite admin: %d", code)
	}
	anon := newBrowser(t, srv)
	var info struct {
		Email        string `json:"email"`
		ExistingUser bool   `json:"existing_user"`
	}
	if code := anon.do("GET", "/api/v1/invitations/token/"+inv.Token, nil, &info); code != 200 || info.Email != "admin@acme.example" || info.ExistingUser {
		t.Fatalf("inspect: %d %+v", code, info)
	}
	if code := anon.do("POST", "/api/v1/invitations/token/"+inv.Token+"/accept", map[string]string{"name": "Ada", "password": "short"}, nil); code != 422 {
		t.Fatalf("weak password accepted: %d", code)
	}
	var adaMe me
	if code := anon.do("POST", "/api/v1/invitations/token/"+inv.Token+"/accept", map[string]string{"name": "Ada", "password": "ada's long password"}, &adaMe); code != 200 || adaMe.Role != "admin" {
		t.Fatalf("accept: %d %+v", code, adaMe)
	}
	anon.csrf = adaMe.CSRFToken
	if code := anon.do("POST", "/api/v1/invitations/token/"+inv.Token+"/accept", map[string]string{"password": "ada's long password"}, nil); code != 404 {
		t.Fatalf("invitation reused: %d", code)
	}

	// Admins manage viewers and admins, never owners.
	if code := anon.do("POST", "/api/v1/invitations", map[string]string{"email": "boss@acme.example", "role": "owner"}, nil); code != 403 {
		t.Fatalf("admin invited an owner: %d", code)
	}
	var members struct {
		Items []struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
			Role   string `json:"role"`
		} `json:"items"`
	}
	anon.do("GET", "/api/v1/members", nil, &members)
	var ownerID string
	for _, m := range members.Items {
		if m.Role == "owner" {
			ownerID = m.UserID
		}
	}
	if code := anon.do("PATCH", "/api/v1/members/"+ownerID, map[string]string{"role": "viewer"}, nil); code != 403 {
		t.Fatalf("admin demoted an owner: %d", code)
	}
	var eb map[string]any
	if code := owner.do("PATCH", "/api/v1/members/"+ownerID, map[string]string{"role": "admin"}, &eb); code != 409 {
		t.Fatalf("last owner demoted: %d %v", code, eb)
	}

	// Password change ends sessions.
	if code := anon.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "ada's long password", "new_password": "an even longer password"}, nil); code != 200 {
		t.Fatalf("change password: %d", code)
	}
	if code := anon.do("GET", "/api/v1/auth/me", nil, nil); code != 401 {
		t.Fatalf("session survived password change: %d", code)
	}
	if _, code := anon.login("admin@acme.example", "an even longer password"); code != 200 {
		t.Fatalf("login with new password: %d", code)
	}
}

func TestAPIKeysCannotUseSessionEndpoints(t *testing.T) {
	h, _ := newHarness(t)
	if code := h.call("POST", "/api/v1/auth/logout", h.apiKey, nil, nil); code != 400 {
		t.Fatalf("api key on session endpoint: %d", code)
	}
	var m me
	if code := h.call("GET", "/api/v1/auth/me", h.apiKey, nil, &m); code != 200 || m.Auth != "api_key" || m.Role != "admin" {
		t.Fatalf("me with api key: %d %+v", code, m)
	}
}
