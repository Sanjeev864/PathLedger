// Package report turns stored incidents and measurements into a monthly
// service-level report per site: how long the internet was impaired, where
// the fault sat, and what the probes measured.
package report

import (
	"math"
	"sort"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
)

// impairing lists the verdicts that count against a site's availability.
// "app" (one destination failing) does not: the connection itself worked.
var impairing = map[string]bool{
	"local": true, "isp": true, "upstream": true, "dns": true, "offline": true, "unknown": true,
}

// VerdictTotal is the incident count and minutes for one verdict.
type VerdictTotal struct {
	Count   int     `json:"count"`
	Minutes float64 `json:"minutes"`
}

// Test is one test's measurements in the period.
type Test struct {
	store.ReportTest
	ExpectedSamples *int64   `json:"expected_samples"`
	CoveragePct     *float64 `json:"coverage_pct"`
	SuccessPct      float64  `json:"success_pct"`
}

// Site is one site's report.
type Site struct {
	SiteID           string                  `json:"site_id"`
	Name             string                  `json:"name"`
	MonitoredFrom    *time.Time              `json:"monitored_from"`
	MonitoredMinutes float64                 `json:"monitored_minutes"`
	ImpairedMinutes  float64                 `json:"impaired_minutes"`
	ISPMinutes       float64                 `json:"isp_minutes"`
	AvailabilityPct  *float64                `json:"availability_pct"`
	Incidents        int                     `json:"incidents"`
	LongestMinutes   float64                 `json:"longest_incident_minutes"`
	ByVerdict        map[string]VerdictTotal `json:"by_verdict"`
	Tests            []Test                  `json:"tests"`
}

// Report is the whole tenant's report for one period.
type Report struct {
	TenantName  string                 `json:"tenant_name"`
	Period      string                 `json:"period"`
	TimeZone    string                 `json:"time_zone"`
	From        time.Time              `json:"from"`
	To          time.Time              `json:"to"`
	Partial     bool                   `json:"partial"`
	GeneratedAt time.Time              `json:"generated_at"`
	Sites       []Site                 `json:"sites"`
	Incidents   []store.ReportIncident `json:"incidents"`
	Truncated   bool                   `json:"incidents_truncated"`
}

// MonthBounds returns [first instant of month, first instant of next month)
// in loc.
func MonthBounds(year int, month time.Month, loc *time.Location) (time.Time, time.Time) {
	from := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 1, 0)
}

// Build computes the report. now caps the period so a month in progress is
// reported up to the present (and marked partial).
func Build(d store.ReportData, period string, loc *time.Location, from, to, now time.Time, maxIncidents int) Report {
	end := to
	if now.Before(end) {
		end = now
	}
	rep := Report{
		TenantName: d.TenantName, Period: period, TimeZone: loc.String(),
		From: from, To: to, Partial: end.Before(to), GeneratedAt: now,
		Sites: []Site{}, Incidents: d.Incidents,
	}
	if rep.Incidents == nil {
		rep.Incidents = []store.ReportIncident{}
	}
	rep.Truncated = len(d.Incidents) >= maxIncidents

	bySite := map[string]*Site{}
	for _, s := range d.Sites {
		start := from
		if s.CreatedAt.After(start) {
			start = s.CreatedAt
		}
		site := Site{SiteID: s.ID, Name: s.Name, ByVerdict: map[string]VerdictTotal{}, Tests: []Test{}}
		if end.After(start) {
			st := start
			site.MonitoredFrom = &st
			site.MonitoredMinutes = end.Sub(start).Minutes()
		}
		rep.Sites = append(rep.Sites, site)
	}
	for i := range rep.Sites {
		bySite[rep.Sites[i].SiteID] = &rep.Sites[i]
	}

	for _, inc := range d.Incidents {
		site := bySite[inc.SiteID]
		if site == nil {
			continue
		}
		m := math.Max(0, inc.Minutes)
		vt := site.ByVerdict[inc.Verdict]
		vt.Count++
		vt.Minutes += m
		site.ByVerdict[inc.Verdict] = vt
		site.Incidents++
		site.LongestMinutes = math.Max(site.LongestMinutes, m)
		if impairing[inc.Verdict] {
			site.ImpairedMinutes += m
		}
		if inc.Verdict == "isp" {
			site.ISPMinutes += m
		}
	}

	for _, t := range d.Tests {
		site := bySite[t.SiteID]
		if site == nil {
			continue
		}
		rt := Test{ReportTest: t}
		if t.Samples > 0 {
			rt.SuccessPct = round(100 * float64(t.Successes) / float64(t.Samples))
		}
		if t.IntervalSeconds != nil && *t.IntervalSeconds > 0 && site.MonitoredFrom != nil {
			start := *site.MonitoredFrom
			if t.TestCreatedAt.After(start) {
				start = t.TestCreatedAt
			}
			if end.After(start) {
				exp := int64(end.Sub(start).Seconds()) / int64(*t.IntervalSeconds)
				if exp > 0 {
					cov := round(math.Min(100, 100*float64(t.Samples)/float64(exp)))
					rt.ExpectedSamples, rt.CoveragePct = &exp, &cov
				}
			}
		}
		roundPtr(rt.LatencyP50)
		roundPtr(rt.LatencyP95)
		roundPtr(rt.LatencyP99)
		roundPtr(rt.LossAvg)
		site.Tests = append(site.Tests, rt)
	}

	for i := range rep.Sites {
		s := &rep.Sites[i]
		s.ImpairedMinutes = math.Min(round(s.ImpairedMinutes), s.MonitoredMinutes)
		s.ISPMinutes = round(s.ISPMinutes)
		s.LongestMinutes = round(s.LongestMinutes)
		s.MonitoredMinutes = round(s.MonitoredMinutes)
		for k, v := range s.ByVerdict {
			v.Minutes = round(v.Minutes)
			s.ByVerdict[k] = v
		}
		if s.MonitoredMinutes > 0 {
			a := math.Floor(100000*(1-s.ImpairedMinutes/s.MonitoredMinutes)) / 1000 // never round up to 100%
			s.AvailabilityPct = &a
		}
		sort.SliceStable(s.Tests, func(a, b int) bool {
			if s.Tests[a].Kind != s.Tests[b].Kind {
				return s.Tests[a].Kind < s.Tests[b].Kind
			}
			return s.Tests[a].Target < s.Tests[b].Target
		})
	}
	return rep
}

func round(v float64) float64 { return math.Round(v*100) / 100 }

func roundPtr(p *float64) {
	if p != nil {
		*p = round(*p)
	}
}
