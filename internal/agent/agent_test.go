package agent

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// fakeController implements the agent API in memory.
type fakeController struct {
	mu        sync.Mutex
	results   []wire.Result
	planCalls int
	revoked   atomic.Bool
	failNext  atomic.Int32 // number of upload requests to fail with 503
	plan      wire.Plan
}

func (f *fakeController) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if f.revoked.Load() || r.Header.Get("Authorization") != "Bearer pla_token" {
			writeJSON(w, http.StatusUnauthorized, wire.ErrorBody{Error: wire.ErrorDetail{Code: "unauthorized", Message: "bad token"}})
			return false
		}
		return true
	}
	mux.HandleFunc("POST /api/v1/agent/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req wire.EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token != "ple_good" {
			writeJSON(w, http.StatusUnauthorized, wire.ErrorBody{Error: wire.ErrorDetail{Code: "invalid_token", Message: "nope"}})
			return
		}
		writeJSON(w, http.StatusCreated, wire.EnrollResponse{AgentID: "agent-1", AgentToken: "pla_token", TenantID: "t", SiteID: "s"})
	})
	mux.HandleFunc("GET /api/v1/agent/plan", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		f.mu.Lock()
		f.planCalls++
		f.mu.Unlock()
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		writeJSON(w, http.StatusOK, f.plan)
	})
	mux.HandleFunc("POST /api/v1/agent/results", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		if f.failNext.Load() > 0 {
			f.failNext.Add(-1)
			writeJSON(w, http.StatusServiceUnavailable, wire.ErrorBody{Error: wire.ErrorDetail{Code: "unavailable", Message: "try later"}})
			return
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("upload not gzip-encoded")
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("bad gzip: %v", err)
			return
		}
		var batch wire.ResultBatch
		if err := json.NewDecoder(zr).Decode(&batch); err != nil {
			t.Errorf("bad batch: %v", err)
			return
		}
		f.mu.Lock()
		f.results = append(f.results, batch.Results...)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, wire.IngestResponse{Accepted: len(batch.Results)})
	})
	return mux
}

func (f *fakeController) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.results)
}

func newFake(t *testing.T) (*fakeController, *httptest.Server) {
	f := &fakeController{plan: wire.Plan{Tests: []wire.Test{
		{ID: "t1", Kind: wire.KindTCP, Target: "example.com:443", IntervalSeconds: 10, TimeoutMillis: 1000},
	}}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return f, srv
}

func fastConfig(url, stateDir string) Config {
	return Config{
		ControllerURL: url, EnrollToken: "ple_good", StateDir: stateDir,
		PlanRefresh: 50 * time.Millisecond, UploadInterval: 20 * time.Millisecond,
		Runner: func(_ context.Context, t wire.Test) wire.Result {
			return wire.Result{TestID: t.ID, Time: time.Now().UTC(), Kind: t.Kind, Target: t.Target, Success: true}
		},
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgentEnrolsRunsAndUploads(t *testing.T) {
	fake, srv := newFake(t)
	dir := t.TempDir()
	a, err := New(context.Background(), fastConfig(srv.URL, dir), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() != "agent-1" {
		t.Fatalf("unexpected agent id %q", a.ID())
	}
	// Bypass the 10s minimum interval by feeding results straight in, then
	// check the full spool → upload path.
	for i := 0; i < 3; i++ {
		a.store(wire.Result{TestID: "t1", Time: time.Now(), Kind: wire.KindTCP, Target: "x:1", Success: true})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitFor(t, func() bool { return fake.count() >= 3 && a.sched.Active() == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}

	// A second start reuses the saved identity without the token.
	cfg := fastConfig(srv.URL, dir)
	cfg.EnrollToken = ""
	if _, err := New(context.Background(), cfg, quietLog()); err != nil {
		t.Fatalf("restart without token failed: %v", err)
	}
}

func TestAgentBuffersThroughOutage(t *testing.T) {
	fake, srv := newFake(t)
	fake.failNext.Store(2)
	a, err := New(context.Background(), fastConfig(srv.URL, t.TempDir()), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		a.store(wire.Result{TestID: "t1", Time: time.Now(), Kind: wire.KindTCP, Target: "x:1", Success: true})
	}
	// First flush hits the outage and keeps the data.
	if err := a.flush(context.Background()); err == nil {
		t.Fatal("expected upload failure during outage")
	}
	_ = a.flush(context.Background())
	if err := a.flush(context.Background()); err != nil {
		t.Fatalf("upload after recovery failed: %v", err)
	}
	if fake.count() != 5 {
		t.Fatalf("expected all 5 buffered results delivered, got %d", fake.count())
	}
}

func TestAgentStopsWhenRevoked(t *testing.T) {
	fake, srv := newFake(t)
	a, err := New(context.Background(), fastConfig(srv.URL, t.TempDir()), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	fake.revoked.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Run(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected ErrRevoked, got %v", err)
	}
}

func TestEnrolRejectsBadToken(t *testing.T) {
	_, srv := newFake(t)
	cfg := fastConfig(srv.URL, t.TempDir())
	cfg.EnrollToken = "ple_bad"
	if _, err := New(context.Background(), cfg, quietLog()); err == nil {
		t.Fatal("enrolment with a bad token succeeded")
	}
}

func TestClientRequiresHTTPS(t *testing.T) {
	if _, err := NewClient("http://controller.example.com", http.DefaultClient, "ua"); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
	for _, ok := range []string{"https://controller.example.com", "http://127.0.0.1:8080", "http://localhost:8080"} {
		if _, err := NewClient(ok, http.DefaultClient, "ua"); err != nil {
			t.Fatalf("%s rejected: %v", ok, err)
		}
	}
}

func TestSchedulerReconciles(t *testing.T) {
	var mu sync.Mutex
	runs := map[string]int{}
	s := NewScheduler(func(_ context.Context, t wire.Test) wire.Result {
		mu.Lock()
		runs[t.ID]++
		mu.Unlock()
		return wire.Result{TestID: t.ID}
	}, func(wire.Result) {}, 2, quietLog())
	mk := func(id string) wire.Test {
		return wire.Test{ID: id, Kind: wire.KindTCP, Target: "x.io:1", IntervalSeconds: 10, TimeoutMillis: 500}
	}
	invalid := wire.Test{ID: "bad", Kind: wire.KindTCP, Target: "no-port", IntervalSeconds: 10, TimeoutMillis: 500}
	s.Apply(context.Background(), []wire.Test{mk("a"), mk("b"), invalid})
	if s.Active() != 2 {
		t.Fatalf("expected 2 active tests (invalid skipped), got %d", s.Active())
	}
	s.Apply(context.Background(), []wire.Test{mk("b")})
	if s.Active() != 1 {
		t.Fatalf("expected 1 active test after removal, got %d", s.Active())
	}
	s.Stop()
	if s.Active() != 0 {
		t.Fatal("stop left tasks running")
	}
}

func TestBackoffBounds(t *testing.T) {
	for f := 1; f < 30; f++ {
		d := backoff(f)
		if d <= 0 || d > maxBackoff {
			t.Fatalf("backoff(%d) = %v out of bounds", f, d)
		}
	}
}
