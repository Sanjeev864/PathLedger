package analytics

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) Notify(_ context.Context, c Change, e store.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, string(c)+":"+e.Verdict)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func TestClassify(t *testing.T) {
	base := &store.Baseline{Samples: 100, P50: wire.Float(20), MAD: wire.Float(2), LossAvg: 0.5}
	cases := []struct {
		name string
		row  store.WindowRow
		want Status
	}{
		{"too few samples", store.WindowRow{Samples: 1}, StatusUnknown},
		{"mostly failing", store.WindowRow{Samples: 10, Failures: 6}, StatusDown},
		{"lossy", store.WindowRow{Samples: 10, LossAvg: wire.Float(12), P50: wire.Float(21), Baseline: base}, StatusDegraded},
		{"normal", store.WindowRow{Samples: 10, LossAvg: wire.Float(1), P50: wire.Float(24), Baseline: base}, StatusOK},
		// margin = max(4×1.4826×2 ≈ 11.9, max(0.5×20, 20)) = 20 → slow above 40 ms
		{"slow", store.WindowRow{Samples: 10, P50: wire.Float(45), Baseline: base}, StatusDegraded},
		{"slow but no baseline yet", store.WindowRow{Samples: 10, P50: wire.Float(500)}, StatusOK},
	}
	for _, c := range cases {
		if got := Classify(c.row); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

type world struct {
	s       *store.Store
	tenant  store.Tenant
	sites   []store.Site
	agents  []store.AgentIdentity
	tests   []store.Test
	engine  *Engine
	rec     *recorder
	current time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	s := pgtest.New(t)
	// Start in the past so advancing the clock never produces results the
	// store would reject as coming from the future.
	w := &world{s: s, rec: &recorder{}, current: time.Now().UTC().Add(-30 * time.Minute)}
	w.tenant, _ = s.CreateTenant(ctx, "Chain")
	for _, name := range []string{"Branch A", "Branch B"} {
		site, err := s.CreateSite(ctx, w.tenant.ID, name, "")
		if err != nil {
			t.Fatal(err)
		}
		_, tok, _ := s.CreateEnrollmentToken(ctx, w.tenant.ID, site.ID, time.Hour)
		resp, err := s.EnrollAgent(ctx, wire.EnrollRequest{Token: tok})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := s.AuthenticateAgent(ctx, resp.AgentToken)
		w.sites, w.agents = append(w.sites, site), append(w.agents, id)
	}
	for _, def := range []wire.Test{
		{Kind: wire.KindHTTP, Target: "https://erp.example.com/health", IntervalSeconds: 30, TimeoutMillis: 5000},
		{Kind: wire.KindICMP, Target: "1.1.1.1", IntervalSeconds: 30, TimeoutMillis: 1000},
	} {
		tt, err := s.CreateTest(ctx, w.tenant.ID, nil, def)
		if err != nil {
			t.Fatal(err)
		}
		w.tests = append(w.tests, tt)
	}
	w.engine = NewEngine(s, w.rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.engine.now = func() time.Time { return w.current }
	return w
}

// report inserts n results per test for a site in the minute before now.
func (w *world) report(t *testing.T, site int, ok map[string]bool, n int) {
	t.Helper()
	var rs []wire.Result
	for _, tt := range w.tests {
		for i := 0; i < n; i++ {
			r := wire.Result{TestID: tt.ID, Time: w.current.Add(-time.Duration(i+1) * 10 * time.Second), Kind: tt.Kind, Target: tt.Target, Success: ok[tt.ID]}
			if r.Success {
				r.LatencyMS = wire.Float(20)
			} else {
				r.ErrorClass = wire.ErrClassTimeout
			}
			rs = append(rs, r)
		}
	}
	if _, rej, err := w.s.InsertResults(context.Background(), w.agents[site], rs); err != nil || rej != 0 {
		t.Fatalf("insert: rejected=%d err=%v", rej, err)
	}
}

func (w *world) allOK() map[string]bool {
	m := map[string]bool{}
	for _, tt := range w.tests {
		m[tt.ID] = true
	}
	return m
}

func TestEngineOpensAttributesAndResolves(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// Branch A loses everything; Branch B is healthy.
	w.report(t, 0, map[string]bool{}, 4)
	w.report(t, 1, w.allOK(), 4)
	if err := w.engine.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	events, _ := w.s.ListEvents(ctx, w.tenant.ID, store.EventFilter{Limit: 10})
	if len(events) != 1 || events[0].SiteID != w.sites[0].ID || events[0].Verdict != string(VerdictISP) || events[0].State != "open" {
		t.Fatalf("expected one open ISP event for Branch A, got %+v", events)
	}
	if got := w.rec.list(); len(got) != 1 || got[0] != "opened:isp" {
		t.Fatalf("notifications: %v", got)
	}

	// Re-evaluating the same window refreshes rather than duplicates.
	if err := w.engine.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if events, _ = w.s.ListEvents(ctx, w.tenant.ID, store.EventFilter{Limit: 10}); len(events) != 1 {
		t.Fatalf("event duplicated: %d", len(events))
	}

	// Recovery: two healthy windows resolve the event.
	for i := 0; i < ResolveAfter; i++ {
		w.current = w.current.Add(Window)
		w.report(t, 0, w.allOK(), 4)
		w.report(t, 1, w.allOK(), 4)
		if err := w.engine.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := w.s.GetEvent(ctx, w.tenant.ID, events[0].ID)
	if err != nil || ev.State != "resolved" || ev.ResolvedAt == nil {
		t.Fatalf("expected resolved event, got %+v %v", ev, err)
	}
	if got := w.rec.list(); got[len(got)-1] != "resolved:isp" {
		t.Fatalf("notifications: %v", got)
	}
}

func TestEngineAppVerdictWhenOneServiceFailsEverywhere(t *testing.T) {
	w := newWorld(t)
	ok := w.allOK()
	ok[w.tests[0].ID] = false // ERP fails, ICMP fine
	w.report(t, 0, ok, 4)
	w.report(t, 1, ok, 4)
	if err := w.engine.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, _ := w.s.ListEvents(context.Background(), w.tenant.ID, store.EventFilter{Limit: 10})
	if len(events) != 2 {
		t.Fatalf("expected an event per site, got %d", len(events))
	}
	for _, e := range events {
		if e.Verdict != string(VerdictApp) {
			t.Fatalf("expected app verdict, got %s: %s", e.Verdict, e.Summary)
		}
	}
}

func TestEngineFlagsSilentSites(t *testing.T) {
	w := newWorld(t)
	w.current = time.Now().UTC().Add(10 * time.Minute) // agents last checked in "10 minutes ago"
	if err := w.engine.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, _ := w.s.ListEvents(context.Background(), w.tenant.ID, store.EventFilter{State: "open", Limit: 10})
	if len(events) != 2 || events[0].Verdict != string(VerdictOffline) {
		t.Fatalf("expected two offline events, got %+v", events)
	}
}

func TestEngineRefreshesBaselines(t *testing.T) {
	w := newWorld(t)
	w.report(t, 0, w.allOK(), 4)
	w.current = w.current.Add(Window + time.Minute) // results now fall inside the baseline span
	if err := w.engine.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	at, err := w.s.BaselinesRefreshedAt(context.Background())
	if err != nil || at.IsZero() {
		t.Fatalf("baselines not refreshed: %v %v", at, err)
	}
	rows, _ := w.s.WindowHealth(context.Background(), w.current.Add(-time.Hour), w.current)
	found := false
	for _, r := range rows {
		if r.Baseline != nil && r.Baseline.Samples == 4 && r.Baseline.P50 != nil && *r.Baseline.P50 == 20 {
			found = true
		}
	}
	if !found {
		t.Fatalf("baseline not attached to window rows: %+v", rows)
	}
}
