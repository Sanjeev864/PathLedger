package demo

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/report"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

func results(f Fault) map[wire.ProbeKind]wire.Result {
	rng := rand.New(rand.NewPCG(1, 2))
	out := map[wire.ProbeKind]wire.Result{}
	for _, t := range Tests {
		t.ID = string(t.Kind)
		if r, ok := Result(t, 0, time.Now(), f, rng); ok {
			out[t.Kind] = r
		}
	}
	return out
}

func TestFaultsLookRight(t *testing.T) {
	for kind, r := range results(Healthy) {
		if !r.Success || r.Validate() != nil {
			t.Fatalf("healthy %s: %+v", kind, r)
		}
	}
	if len(results(Offline)) != 0 {
		t.Fatal("an offline site reported results")
	}
	answering := func(r wire.Result) int {
		n := 0
		for _, h := range r.Hops {
			if h.Received > 0 {
				n++
			}
		}
		return n
	}
	for f, hops := range map[Fault]int{ISP: 1, Local: 0, Upstream: 4} {
		rs := results(f)
		for kind, r := range rs {
			if r.Success {
				t.Fatalf("%s: %s still succeeds", f, kind)
			}
		}
		if got := answering(rs[wire.KindTraceroute]); got != hops {
			t.Fatalf("%s: %d hops answer, want %d", f, got, hops)
		}
	}
	dns := results(DNS)
	if !dns[wire.KindICMP].Success || dns[wire.KindDNS].ErrorClass != wire.ErrClassDNS || dns[wire.KindHTTP].ErrorClass != wire.ErrClassDNS {
		t.Fatalf("dns fault: %+v", dns)
	}
	app := results(App)
	if app[wire.KindHTTP].Success || !app[wire.KindTCP].Success || !app[wire.KindDNS].Success {
		t.Fatalf("app fault: %+v", app)
	}
	if _, err := ParseFault("router"); err == nil {
		t.Fatal("unknown fault accepted")
	}
}

func TestSeedAndLive(t *testing.T) {
	st := pgtest.New(t)
	if err := PrepareStore(st); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	s, err := Seed(ctx, st, Options{Days: 3, Email: "Demo@Example.com", Password: "demo password 123"}, log)
	if err != nil {
		t.Fatal(err)
	}
	// 3 sites x (4 tests every minute + traceroute every 2 minutes) x 3 days,
	// minus the scripted offline time (none in the last 3 days).
	if s.Results < 3*3*24*60*4 || s.Events != 3 || s.Password != "demo password 123" || s.Email != "demo@example.com" {
		t.Fatalf("seeded %+v", s)
	}
	if u, err := st.UserByEmail(ctx, "demo@example.com"); err != nil {
		t.Fatal(err)
	} else if role, err := st.MemberRole(ctx, s.TenantID, u.ID); err != nil || role != store.RoleOwner {
		t.Fatalf("membership: %v %v", role, err)
	}

	// The month's report sees whole days of monitoring and the incidents.
	now := time.Now()
	data, err := st.SLAReportData(ctx, s.TenantID, now.Add(-3*24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	rep := report.Build(data, "x", time.UTC, now.Add(-3*24*time.Hour), now, now, 2000)
	if len(rep.Sites) != 3 || len(rep.Incidents) != 3 || *rep.Sites[0].Tests[0].CoveragePct < 99 {
		t.Fatalf("report: %+v", rep.Sites[0])
	}

	// A second seed reuses the account and leaves its password alone.
	s2, err := Seed(ctx, st, Options{Days: 1, Email: "demo@example.com"}, log)
	if err != nil || !s2.ExistingAccount || s2.Password != "" {
		t.Fatalf("second seed: %+v %v", s2, err)
	}

	// The live feed reports with fresh credentials and keeps agents seen.
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := Live(lctx, st, s.TenantID, LiveOptions{Fault: ISP, Site: 0, For: time.Minute}, log); err != nil {
		t.Fatal(err)
	}
	rs, err := st.QueryResults(ctx, s.TenantID, store.ResultFilter{From: now, To: time.Now().Add(time.Minute), Limit: 100})
	if err != nil || len(rs) != 3*len(Tests) {
		t.Fatalf("live results: %d %v", len(rs), err)
	}
	failing := 0
	for _, r := range rs {
		if !r.Success {
			failing++
		}
	}
	if failing != len(Tests) {
		t.Fatalf("expected one site's %d tests to fail, got %d failures", len(Tests), failing)
	}
	if err := Live(ctx, st, "00000000-0000-0000-0000-000000000000", LiveOptions{}, log); err == nil {
		t.Fatal("live feed for a tenant without demo agents")
	}
}
