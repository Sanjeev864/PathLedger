package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/api"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	apiKey string
}

func newHarness(t *testing.T) (*harness, func(name string) string) {
	t.Helper()
	st := pgtest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.New(st, log).Handler())
	t.Cleanup(srv.Close)
	newKey := func(name string) string {
		tenant, err := st.CreateTenant(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		_, key, err := st.CreateAPIKey(context.Background(), tenant.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	return &harness{t: t, srv: srv, apiKey: newKey("Acme")}, newKey
}

// call performs a request and decodes a JSON response into out (if non-nil).
func (h *harness) call(method, path, token string, body any, out any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type idResp struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func TestFullAgentLifecycle(t *testing.T) {
	h, _ := newHarness(t)

	var site idResp
	if code := h.call("POST", "/api/v1/sites", h.apiKey, map[string]string{"name": "Jayanagar", "location": "Bengaluru"}, &site); code != 201 {
		t.Fatalf("create site: %d", code)
	}
	var tok idResp
	if code := h.call("POST", "/api/v1/sites/"+site.ID+"/enrollment-tokens", h.apiKey, map[string]int{"ttl_hours": 2}, &tok); code != 201 || !strings.HasPrefix(tok.Token, "ple_") {
		t.Fatalf("create token: %d %+v", code, tok)
	}

	var test idResp
	code := h.call("POST", "/api/v1/tests", h.apiKey, map[string]any{
		"site_id": site.ID, "kind": "http", "target": "https://billing.example.com/health",
		"interval_seconds": 60, "timeout_millis": 5000,
	}, &test)
	if code != 201 {
		t.Fatalf("create test: %d", code)
	}

	var enrolled wire.EnrollResponse
	if code := h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: tok.Token, Hostname: "frontdesk-pc"}, &enrolled); code != 201 {
		t.Fatalf("enroll: %d", code)
	}
	if code := h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: tok.Token}, nil); code != 401 {
		t.Fatalf("reused token must fail, got %d", code)
	}

	// Plan with ETag caching.
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/v1/agent/plan", nil)
	req.Header.Set("Authorization", "Bearer "+enrolled.AgentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var plan wire.Plan
	_ = json.NewDecoder(resp.Body).Decode(&plan)
	resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != 200 || len(plan.Tests) != 1 || plan.Tests[0].ID != test.ID || etag == "" {
		t.Fatalf("plan: %d %+v etag=%q", resp.StatusCode, plan, etag)
	}
	req.Header.Set("If-None-Match", etag)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304 for unchanged plan, got %d", resp.StatusCode)
	}

	// Gzip-compressed upload.
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := wire.ResultBatch{Results: []wire.Result{
		{TestID: test.ID, Time: now, Kind: wire.KindHTTP, Target: "https://billing.example.com/health", Success: true, LatencyMS: wire.Float(84.2)},
		{TestID: "00000000-0000-0000-0000-000000000000", Time: now, Kind: wire.KindHTTP, Target: "x", Success: true},
	}}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_ = json.NewEncoder(zw).Encode(batch)
	_ = zw.Close()
	up, _ := http.NewRequest("POST", h.srv.URL+"/api/v1/agent/results", &buf)
	up.Header.Set("Authorization", "Bearer "+enrolled.AgentToken)
	up.Header.Set("Content-Encoding", "gzip")
	resp, err = http.DefaultClient.Do(up)
	if err != nil {
		t.Fatal(err)
	}
	var ing wire.IngestResponse
	_ = json.NewDecoder(resp.Body).Decode(&ing)
	resp.Body.Close()
	if resp.StatusCode != 200 || ing.Accepted != 1 || ing.Rejected != 1 {
		t.Fatalf("ingest: %d %+v", resp.StatusCode, ing)
	}

	var results struct {
		Items []struct {
			TestID    string   `json:"test_id"`
			LatencyMS *float64 `json:"latency_ms"`
			AgentID   string   `json:"agent_id"`
		} `json:"items"`
	}
	q := url.Values{"test_id": {test.ID}, "from": {now.Add(-time.Minute).Format(time.RFC3339)}}
	if code := h.call("GET", "/api/v1/results?"+q.Encode(), h.apiKey, nil, &results); code != 200 || len(results.Items) != 1 || *results.Items[0].LatencyMS != 84.2 || results.Items[0].AgentID != enrolled.AgentID {
		t.Fatalf("results query: %d %+v", code, results)
	}

	var agents struct {
		Items []struct {
			ID         string     `json:"id"`
			Hostname   string     `json:"hostname"`
			LastSeenAt *time.Time `json:"last_seen_at"`
		} `json:"items"`
	}
	if code := h.call("GET", "/api/v1/agents", h.apiKey, nil, &agents); code != 200 || len(agents.Items) != 1 || agents.Items[0].Hostname != "frontdesk-pc" || agents.Items[0].LastSeenAt == nil {
		t.Fatalf("agents: %d %+v", code, agents)
	}

	// Revocation locks the agent out immediately.
	if code := h.call("DELETE", "/api/v1/agents/"+enrolled.AgentID, h.apiKey, nil, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code := h.call("GET", "/api/v1/agent/plan", enrolled.AgentToken, nil, nil); code != 401 {
		t.Fatalf("revoked agent got %d", code)
	}
}

func TestTenantIsolation(t *testing.T) {
	h, newKey := newHarness(t)
	var site idResp
	h.call("POST", "/api/v1/sites", h.apiKey, map[string]string{"name": "HQ"}, &site)
	otherKey := newKey("Rival")

	if code := h.call("GET", "/api/v1/sites/"+site.ID, otherKey, nil, nil); code != 404 {
		t.Fatalf("other tenant read site: %d", code)
	}
	if code := h.call("POST", "/api/v1/sites/"+site.ID+"/enrollment-tokens", otherKey, nil, nil); code != 404 {
		t.Fatalf("other tenant issued token: %d", code)
	}
	if code := h.call("POST", "/api/v1/tests", otherKey, map[string]any{
		"site_id": site.ID, "kind": "icmp", "target": "1.1.1.1", "interval_seconds": 30, "timeout_millis": 1000,
	}, nil); code != 404 {
		t.Fatalf("other tenant attached a test to the site: %d", code)
	}
	var list struct{ Items []any }
	if h.call("GET", "/api/v1/sites", otherKey, nil, &list); len(list.Items) != 0 {
		t.Fatal("site listing leaked across tenants")
	}
}

func TestAuthAndValidationErrors(t *testing.T) {
	h, _ := newHarness(t)
	cases := []struct {
		name, method, path, token string
		body                      any
		want                      int
	}{
		{"no key", "GET", "/api/v1/sites", "", nil, 401},
		{"bad key", "GET", "/api/v1/sites", "plk_nope", nil, 401},
		{"agent token on management API", "GET", "/api/v1/sites", "pla_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil, 401},
		{"empty site name", "POST", "/api/v1/sites", h.apiKey, map[string]string{"name": " "}, 422},
		{"unknown field", "POST", "/api/v1/sites", h.apiKey, map[string]string{"name": "x", "evil": "1"}, 400},
		{"invalid test", "POST", "/api/v1/tests", h.apiKey, map[string]any{"kind": "tcp", "target": "no-port", "interval_seconds": 30, "timeout_millis": 1000}, 422},
		{"interval too short", "POST", "/api/v1/tests", h.apiKey, map[string]any{"kind": "icmp", "target": "1.1.1.1", "interval_seconds": 1, "timeout_millis": 500}, 422},
		{"bad site id", "GET", "/api/v1/sites/not-a-uuid", h.apiKey, nil, 404},
		{"bad results window", "GET", "/api/v1/results?from=2020-01-01T00:00:00Z", h.apiKey, nil, 400},
		{"bad limit", "GET", "/api/v1/results?limit=0", h.apiKey, nil, 400},
		{"bad enrolment token", "POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: "ple_x"}, 401},
		{"unknown route", "GET", "/api/v2/anything", "", nil, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var eb wire.ErrorBody
			if got := h.call(c.method, c.path, c.token, c.body, &eb); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
			if eb.Error.Code == "" {
				t.Fatal("error response missing code")
			}
		})
	}
}

func TestOversizedBatchRejected(t *testing.T) {
	h, _ := newHarness(t)
	var site, tok idResp
	h.call("POST", "/api/v1/sites", h.apiKey, map[string]string{"name": "S"}, &site)
	h.call("POST", "/api/v1/sites/"+site.ID+"/enrollment-tokens", h.apiKey, nil, &tok)
	var enrolled wire.EnrollResponse
	h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: tok.Token}, &enrolled)

	results := make([]wire.Result, wire.MaxResultsPerBatch+1)
	for i := range results {
		results[i] = wire.Result{TestID: fmt.Sprint(i), Time: time.Now(), Kind: wire.KindTCP, Target: "x:1"}
	}
	if code := h.call("POST", "/api/v1/agent/results", enrolled.AgentToken, wire.ResultBatch{Results: results}, nil); code != 413 {
		t.Fatalf("expected 413, got %d", code)
	}
}

func TestHealth(t *testing.T) {
	h, _ := newHarness(t)
	if code := h.call("GET", "/healthz", "", nil, nil); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	if code := h.call("GET", "/readyz", "", nil, nil); code != 200 {
		t.Fatalf("readyz %d", code)
	}
}

func TestEventsEndpoints(t *testing.T) {
	h, _ := newHarness(t)
	var list struct{ Items []any }
	if code := h.call("GET", "/api/v1/events?state=open", h.apiKey, nil, &list); code != 200 || list.Items == nil {
		t.Fatalf("list events: %d %+v", code, list)
	}
	if code := h.call("GET", "/api/v1/events?state=bogus", h.apiKey, nil, nil); code != 400 {
		t.Fatalf("bad state accepted: %d", code)
	}
	if code := h.call("GET", "/api/v1/events/00000000-0000-0000-0000-000000000000", h.apiKey, nil, nil); code != 404 {
		t.Fatalf("missing event: %d", code)
	}
}

func TestChannelsEndpoints(t *testing.T) {
	h, otherKey := newHarness(t)
	var created struct {
		ID     string         `json:"id"`
		Config map[string]any `json:"config"`
	}
	if code := h.call("POST", "/api/v1/channels", h.apiKey, map[string]any{"kind": "webhook", "name": "ops", "url": "https://hooks.example.com/pl"}, &created); code != 201 {
		t.Fatalf("create webhook: %d", code)
	}
	if secret, _ := created.Config["secret"].(string); len(secret) != 64 {
		t.Fatalf("secret not returned at creation: %+v", created.Config)
	}
	var list struct {
		Items []struct {
			Config map[string]any `json:"config"`
		} `json:"items"`
	}
	h.call("GET", "/api/v1/channels", h.apiKey, nil, &list)
	if len(list.Items) != 1 || list.Items[0].Config["secret"] != "********" {
		t.Fatalf("secret not masked in listing: %+v", list)
	}
	for _, bad := range []map[string]any{
		{"kind": "webhook", "name": "x", "url": "http://insecure.example.com"},
		{"kind": "email", "name": "x", "to": []string{"not-an-address"}},
		{"kind": "telegram", "name": "x", "chat_id": "drop table"},
		{"kind": "sms", "name": "x"},
	} {
		if code := h.call("POST", "/api/v1/channels", h.apiKey, bad, nil); code != 422 {
			t.Fatalf("invalid channel %v accepted: %d", bad, code)
		}
	}
	var queued map[string]int
	if code := h.call("POST", "/api/v1/channels/"+created.ID+"/test", h.apiKey, nil, &queued); code != 202 || queued["queued"] != 1 {
		t.Fatalf("test channel: %d %v", code, queued)
	}
	if code := h.call("POST", "/api/v1/channels/"+created.ID+"/test", otherKey("Rival"), nil, nil); code != 404 {
		t.Fatalf("other tenant tested channel: %d", code)
	}
	var deliveries struct{ Items []any }
	if h.call("GET", "/api/v1/deliveries", h.apiKey, nil, &deliveries); len(deliveries.Items) != 1 {
		t.Fatalf("deliveries: %+v", deliveries)
	}
	if code := h.call("DELETE", "/api/v1/channels/"+created.ID, h.apiKey, nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
}

func TestPatchTestAndAPIKeys(t *testing.T) {
	h, _ := newHarness(t)
	var test idResp
	h.call("POST", "/api/v1/tests", h.apiKey, map[string]any{"kind": "icmp", "target": "1.1.1.1", "interval_seconds": 30, "timeout_millis": 1000}, &test)
	var updated struct {
		IntervalSeconds int  `json:"interval_seconds"`
		Enabled         bool `json:"enabled"`
	}
	if code := h.call("PATCH", "/api/v1/tests/"+test.ID, h.apiKey, map[string]any{"interval_seconds": 120, "enabled": false}, &updated); code != 200 || updated.IntervalSeconds != 120 || updated.Enabled {
		t.Fatalf("patch: %d %+v", code, updated)
	}
	if code := h.call("PATCH", "/api/v1/tests/"+test.ID, h.apiKey, map[string]any{"interval_seconds": 1}, nil); code != 422 {
		t.Fatalf("invalid patch accepted: %d", code)
	}

	var created struct {
		Key   struct{ ID string } `json:"key"`
		Token string              `json:"token"`
	}
	if code := h.call("POST", "/api/v1/api-keys", h.apiKey, map[string]string{"name": "dashboard"}, &created); code != 201 || !strings.HasPrefix(created.Token, "plk_") {
		t.Fatalf("create key: %d %+v", code, created)
	}
	var keys struct{ Items []map[string]any }
	if h.call("GET", "/api/v1/api-keys", created.Token, nil, &keys); len(keys.Items) != 2 {
		t.Fatalf("list keys: %+v", keys)
	}
	if code := h.call("DELETE", "/api/v1/api-keys/"+created.Key.ID, created.Token, nil, nil); code != 409 {
		t.Fatalf("self-revocation must be refused: %d", code)
	}
	if code := h.call("DELETE", "/api/v1/api-keys/"+created.Key.ID, h.apiKey, nil, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code := h.call("GET", "/api/v1/sites", created.Token, nil, nil); code != 401 {
		t.Fatalf("revoked key still works: %d", code)
	}
}

func TestRateLimits(t *testing.T) {
	st := pgtest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.New(st, log, api.WithLimits(
		api.Limit{Rate: 0.001, Burst: 2}, api.Limit{Rate: 0.001, Burst: 3}, api.Limit{Rate: 0.001, Burst: 1},
	)).Handler())
	defer srv.Close()
	tenant, _ := st.CreateTenant(context.Background(), "T")
	_, key, _ := st.CreateAPIKey(context.Background(), tenant.ID, "k")
	h := &harness{t: t, srv: srv, apiKey: key}
	for i := 0; i < 3; i++ {
		if code := h.call("GET", "/api/v1/sites", key, nil, nil); code != 200 {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/sites", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d", resp.StatusCode)
	}
	h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: "ple_x"}, nil)
	if code := h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: "ple_x"}, nil); code != 429 {
		t.Fatalf("enrol flood not limited: %d", code)
	}
}

func TestSeries(t *testing.T) {
	h, _ := newHarness(t)
	var site, tok, test idResp
	h.call("POST", "/api/v1/sites", h.apiKey, map[string]string{"name": "S"}, &site)
	h.call("POST", "/api/v1/sites/"+site.ID+"/enrollment-tokens", h.apiKey, nil, &tok)
	h.call("POST", "/api/v1/tests", h.apiKey, map[string]any{"kind": "tcp", "target": "x.example:443", "interval_seconds": 10, "timeout_millis": 1000}, &test)
	var enrolled wire.EnrollResponse
	h.call("POST", "/api/v1/agent/enroll", "", wire.EnrollRequest{Token: tok.Token}, &enrolled)
	now := time.Now().UTC().Add(-2 * time.Minute)
	var rs []wire.Result
	for i, ms := range []float64{10, 20, 30} {
		rs = append(rs, wire.Result{TestID: test.ID, Time: now.Add(time.Duration(i) * time.Second), Kind: wire.KindTCP, Target: "x.example:443", Success: true, LatencyMS: wire.Float(ms)})
	}
	rs = append(rs, wire.Result{TestID: test.ID, Time: now, Kind: wire.KindTCP, Target: "x.example:443", Success: false, ErrorClass: wire.ErrClassTimeout})
	h.call("POST", "/api/v1/agent/results", enrolled.AgentToken, wire.ResultBatch{Results: rs}, nil)

	var series struct {
		Step  int `json:"step_seconds"`
		Items []struct {
			N, Fail   int
			LatencyMS *float64 `json:"latency_ms"`
		} `json:"items"`
	}
	code := h.call("GET", "/api/v1/series?site_id="+site.ID+"&test_id="+test.ID+"&window=3h", h.apiKey, nil, &series)
	if code != 200 || series.Step != 60 {
		t.Fatalf("series: %d %+v", code, series)
	}
	total, fails := 0, 0
	for _, p := range series.Items {
		total, fails = total+p.N, fails+p.Fail
	}
	if total != 4 || fails != 1 {
		t.Fatalf("expected 4 samples with 1 failure, got %+v", series.Items)
	}
	if code := h.call("GET", "/api/v1/series?site_id="+site.ID+"&test_id="+test.ID+"&window=5m", h.apiKey, nil, nil); code != 400 {
		t.Fatalf("short window accepted: %d", code)
	}
}
