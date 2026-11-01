package report

import (
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
)

func ptr[T any](v T) *T { return &v }

func TestBuild(t *testing.T) {
	ist, _ := time.LoadLocation("Asia/Kolkata")
	from, to := MonthBounds(2026, time.September, ist)
	if from.Format(time.RFC3339) != "2026-09-01T00:00:00+05:30" || to.Format(time.RFC3339) != "2026-10-01T00:00:00+05:30" {
		t.Fatalf("bounds %v %v", from, to)
	}
	mid := from.Add(15 * 24 * time.Hour) // HQ2 created halfway through
	d := store.ReportData{
		TenantName: "Acme",
		Sites: []store.ReportSite{
			{ID: "s1", Name: "HQ", CreatedAt: from.AddDate(0, -1, 0)},
			{ID: "s2", Name: "HQ2", CreatedAt: mid},
		},
		Incidents: []store.ReportIncident{
			{SiteID: "s1", Verdict: "isp", Minutes: 90},
			{SiteID: "s1", Verdict: "isp", Minutes: 30},
			{SiteID: "s1", Verdict: "app", Minutes: 600},  // a single service: not impairment
			{SiteID: "s1", Verdict: "local", Minutes: 12}, // the customer's own LAN
			{SiteID: "zz", Verdict: "isp", Minutes: 5},    // unknown site ignored
		},
		Tests: []store.ReportTest{
			{SiteID: "s1", Kind: "icmp", Target: "1.1.1.1", Samples: 86_400, Successes: 86_000,
				LatencyP50: ptr(12.345), IntervalSeconds: ptr(30), TestCreatedAt: from.AddDate(0, -1, 0)},
			{SiteID: "s2", Kind: "dns", Target: "example.com", Samples: 100, Successes: 100,
				IntervalSeconds: ptr(60), TestCreatedAt: mid},
		},
	}
	r := Build(d, "2026-09", ist, from, to, to.Add(time.Hour), 2000)
	if r.Partial || len(r.Sites) != 2 {
		t.Fatalf("report %+v", r)
	}
	hq := r.Sites[0]
	if hq.MonitoredMinutes != 30*24*60 || hq.ImpairedMinutes != 132 || hq.ISPMinutes != 120 || hq.Incidents != 4 || hq.LongestMinutes != 600 {
		t.Fatalf("hq %+v", hq)
	}
	if got := hq.ByVerdict["isp"]; got.Count != 2 || got.Minutes != 120 {
		t.Fatalf("isp %+v", got)
	}
	// 1 - 132/43200 = 0.996944… → floored, never rounded up.
	if *hq.AvailabilityPct != 99.694 {
		t.Fatalf("availability %v", *hq.AvailabilityPct)
	}
	tst := hq.Tests[0]
	if *tst.ExpectedSamples != 86_400 || *tst.CoveragePct != 100 || tst.SuccessPct != 99.54 || *tst.LatencyP50 != 12.35 {
		t.Fatalf("test %+v", tst)
	}
	hq2 := r.Sites[1]
	if hq2.MonitoredMinutes != 15*24*60 || *hq2.AvailabilityPct != 100 || *hq2.Tests[0].ExpectedSamples != 21_600 {
		t.Fatalf("hq2 %+v %+v", hq2, hq2.Tests[0])
	}

	// A month in progress is reported up to now and marked partial.
	now := from.Add(24 * time.Hour)
	r = Build(d, "2026-09", ist, from, to, now, 2000)
	if !r.Partial || r.Sites[0].MonitoredMinutes != 1440 || r.Sites[1].MonitoredFrom != nil || r.Sites[1].AvailabilityPct != nil {
		t.Fatalf("partial %+v", r.Sites)
	}
	// Impaired time never exceeds monitored time.
	if r.Sites[0].ImpairedMinutes != 132 || *r.Sites[0].AvailabilityPct != 90.833 {
		t.Fatalf("partial hq %+v %v", r.Sites[0], *r.Sites[0].AvailabilityPct)
	}
}
