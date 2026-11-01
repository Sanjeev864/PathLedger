package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/enrich"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

var ctx = context.Background()

type fixture struct {
	s      *store.Store
	tenant store.Tenant
	site   store.Site
}

func setup(t *testing.T) fixture {
	t.Helper()
	s := pgtest.New(t)
	tenant, err := s.CreateTenant(ctx, "Acme Clinics")
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.CreateSite(ctx, tenant.ID, "Koramangala", "Bengaluru")
	if err != nil {
		t.Fatal(err)
	}
	return fixture{s: s, tenant: tenant, site: site}
}

func enroll(t *testing.T, f fixture, siteID string) wire.EnrollResponse {
	t.Helper()
	_, tok, err := f.s.CreateEnrollmentToken(ctx, f.tenant.ID, siteID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.s.EnrollAgent(ctx, wire.EnrollRequest{Token: tok, Hostname: "pi", OS: "linux", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := pgtest.New(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
}

func TestAPIKeys(t *testing.T) {
	f := setup(t)
	key, token, err := f.s.CreateAPIKey(ctx, f.tenant.ID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	tenantID, keyID, err := f.s.AuthenticateAPIKey(ctx, token)
	if err != nil || tenantID != f.tenant.ID || keyID != key.ID {
		t.Fatalf("authenticate: %v %v %v", tenantID, keyID, err)
	}
	if _, _, err := f.s.AuthenticateAPIKey(ctx, token+"x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tampered key accepted: %v", err)
	}
}

func TestDuplicateSiteName(t *testing.T) {
	f := setup(t)
	if _, err := f.s.CreateSite(ctx, f.tenant.ID, "Koramangala", ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestEnrollmentTokenIsSingleUse(t *testing.T) {
	f := setup(t)
	_, tok, err := f.s.CreateEnrollmentToken(ctx, f.tenant.ID, f.site.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Two concurrent enrolments with the same token: exactly one wins.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.s.EnrollAgent(ctx, wire.EnrollRequest{Token: tok})
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrInvalidToken) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("expected exactly one successful enrolment, got %d", ok)
	}
}

func TestExpiredEnrollmentToken(t *testing.T) {
	f := setup(t)
	_, tok, err := f.s.CreateEnrollmentToken(ctx, f.tenant.ID, f.site.ID, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.EnrollAgent(ctx, wire.EnrollRequest{Token: tok}); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

func TestEnrollmentTokenForOtherTenantsSite(t *testing.T) {
	f := setup(t)
	other, _ := f.s.CreateTenant(ctx, "Other")
	if _, _, err := f.s.CreateEnrollmentToken(ctx, other.ID, f.site.ID, time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token for another tenant's site must fail, got %v", err)
	}
}

func TestAgentAuthAndRevocation(t *testing.T) {
	f := setup(t)
	resp := enroll(t, f, f.site.ID)
	id, err := f.s.AuthenticateAgent(ctx, resp.AgentToken)
	if err != nil || id.AgentID != resp.AgentID || id.SiteID != f.site.ID || id.TenantID != f.tenant.ID {
		t.Fatalf("authenticate agent: %+v %v", id, err)
	}
	if err := f.s.RevokeAgent(ctx, f.tenant.ID, resp.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AuthenticateAgent(ctx, resp.AgentToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked agent still authenticates: %v", err)
	}
}

func TestPlanScopingAndIngest(t *testing.T) {
	f := setup(t)
	otherSite, _ := f.s.CreateSite(ctx, f.tenant.ID, "Indiranagar", "")
	siteTest, err := f.s.CreateTest(ctx, f.tenant.ID, &f.site.ID,
		wire.Test{Kind: wire.KindTCP, Target: "upi.example:443", IntervalSeconds: 30, TimeoutMillis: 2000})
	if err != nil {
		t.Fatal(err)
	}
	globalTest, err := f.s.CreateTest(ctx, f.tenant.ID, nil,
		wire.Test{Kind: wire.KindDNS, Target: "example.com", IntervalSeconds: 60, TimeoutMillis: 2000,
			Params: wire.TestParams{Resolver: "1.1.1.1:53"}})
	if err != nil {
		t.Fatal(err)
	}
	otherTest, _ := f.s.CreateTest(ctx, f.tenant.ID, &otherSite.ID,
		wire.Test{Kind: wire.KindICMP, Target: "1.1.1.1", IntervalSeconds: 30, TimeoutMillis: 1000})

	plan, err := f.s.PlanFor(ctx, f.tenant.ID, f.site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("expected site + global test, got %d", len(plan))
	}
	for _, p := range plan {
		if p.ID == globalTest.ID && p.Params.Resolver != "1.1.1.1:53" {
			t.Fatalf("params not round-tripped: %+v", p.Params)
		}
	}

	agent := enroll(t, f, f.site.ID)
	id, _ := f.s.AuthenticateAgent(ctx, agent.AgentToken)
	now := time.Now().UTC()
	results := []wire.Result{
		{TestID: siteTest.ID, Time: now, Kind: wire.KindTCP, Target: "upi.example:443", Success: true, LatencyMS: wire.Float(21.5), Details: map[string]string{"remote_ip": "203.0.113.5"}},
		{TestID: globalTest.ID, Time: now, Kind: wire.KindDNS, Target: "example.com", Success: false, ErrorClass: wire.ErrClassTimeout, Error: "i/o timeout"},
		{TestID: otherTest.ID, Time: now, Kind: wire.KindICMP, Target: "1.1.1.1", Success: true},          // other site's test
		{TestID: siteTest.ID, Time: now.Add(time.Hour), Kind: wire.KindTCP, Target: "x:1", Success: true}, // future timestamp
		{TestID: siteTest.ID, Time: now, Kind: wire.KindTCP, Target: "x:1", LossPct: wire.Float(500)},     // invalid
	}
	accepted, rejected, err := f.s.InsertResults(ctx, id, results)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != 2 || rejected != 3 {
		t.Fatalf("accepted=%d rejected=%d, want 2 and 3", accepted, rejected)
	}

	got, err := f.s.QueryResults(ctx, f.tenant.ID, store.ResultFilter{From: now.Add(-time.Minute), To: now.Add(time.Minute), Limit: 100})
	if err != nil || len(got) != 2 {
		t.Fatalf("query: %d results, err %v", len(got), err)
	}
	byTest, _ := f.s.QueryResults(ctx, f.tenant.ID, store.ResultFilter{TestID: siteTest.ID, From: now.Add(-time.Minute), To: now.Add(time.Minute), Limit: 100})
	if len(byTest) != 1 || *byTest[0].LatencyMS != 21.5 || byTest[0].Details["remote_ip"] != "203.0.113.5" || byTest[0].AgentID != agent.AgentID {
		t.Fatalf("filtered query wrong: %+v", byTest)
	}

	other, _ := f.s.CreateTenant(ctx, "Other")
	leaked, _ := f.s.QueryResults(ctx, other.ID, store.ResultFilter{From: now.Add(-time.Minute), To: now.Add(time.Minute), Limit: 100})
	if len(leaked) != 0 {
		t.Fatal("results leaked across tenants")
	}

	if err := f.s.DeleteTest(ctx, other.ID, siteTest.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another tenant deleted a test: %v", err)
	}
	if err := f.s.DeleteTest(ctx, f.tenant.ID, siteTest.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTracerouteIngestAnnotatesHops(t *testing.T) {
	f := setup(t)
	db, err := enrich.Load(strings.NewReader("49.204.0.0\t49.207.255.255\t24309\tIN\tACT Fibernet\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.s.SetASNData(db)
	tr, err := f.s.CreateTest(ctx, f.tenant.ID, nil, wire.Test{Kind: wire.KindTraceroute, Target: "1.1.1.1", IntervalSeconds: 300, TimeoutMillis: 1000})
	if err != nil {
		t.Fatal(err)
	}
	agent := enroll(t, f, f.site.ID)
	id, _ := f.s.AuthenticateAgent(ctx, agent.AgentToken)
	now := time.Now().UTC()
	res := wire.Result{TestID: tr.ID, Time: now, Kind: wire.KindTraceroute, Target: "1.1.1.1", Success: true,
		Hops: []wire.Hop{
			{TTL: 1, Addr: "192.168.1.1", Sent: 3, Received: 3, AvgMS: wire.Float(1.1)},
			{TTL: 2, Addr: "49.205.0.1", Sent: 3, Received: 2, AvgMS: wire.Float(9.4), ASN: 1}, // agent-sent ASN is overwritten
			{TTL: 3, Sent: 3},
		}}
	if acc, rej, err := f.s.InsertResults(ctx, id, []wire.Result{res}); err != nil || acc != 1 || rej != 0 {
		t.Fatalf("insert: %d %d %v", acc, rej, err)
	}
	got, err := f.s.QueryResults(ctx, f.tenant.ID, store.ResultFilter{TestID: tr.ID, From: now.Add(-time.Minute), To: now.Add(time.Minute), Limit: 10})
	if err != nil || len(got) != 1 || len(got[0].Hops) != 3 {
		t.Fatalf("query: %+v %v", got, err)
	}
	h := got[0].Hops
	if h[0].Scope != "lan" || h[1].ASN != 24309 || h[1].ASName != "ACT Fibernet" || h[1].Scope != "public" || h[2].Addr != "" {
		t.Fatalf("hops not annotated: %+v", h)
	}
}
