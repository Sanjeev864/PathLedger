package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReportIncident is an event that overlaps a reporting period, clipped to it.
type ReportIncident struct {
	ID        string     `json:"id"`
	SiteID    string     `json:"site_id"`
	Verdict   string     `json:"verdict"`
	Summary   string     `json:"summary"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"resolved_at"`
	// Minutes is the part of the incident inside the period.
	Minutes float64 `json:"minutes"`
}

// ReportTest summarises one test's measurements at one site.
type ReportTest struct {
	SiteID          string    `json:"-"`
	TestID          string    `json:"test_id"`
	Kind            string    `json:"kind"`
	Target          string    `json:"target"`
	Samples         int64     `json:"samples"`
	Successes       int64     `json:"successes"`
	LatencyP50      *float64  `json:"latency_p50_ms"`
	LatencyP95      *float64  `json:"latency_p95_ms"`
	LatencyP99      *float64  `json:"latency_p99_ms"`
	LossAvg         *float64  `json:"loss_avg_pct"`
	IntervalSeconds *int      `json:"-"`
	TestCreatedAt   time.Time `json:"-"`
}

// ReportSite is a site in a tenant with its creation time.
type ReportSite struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// ReportData is the raw material of an SLA report for [from, to).
type ReportData struct {
	TenantName string
	Sites      []ReportSite
	Incidents  []ReportIncident
	Tests      []ReportTest
}

// maxReportIncidents bounds the incident list of one report.
const maxReportIncidents = 2000

// SLAReportData gathers sites, overlapping incidents and per-test result
// statistics for one tenant and period. Analysis happens in the caller.
func (s *Store) SLAReportData(ctx context.Context, tenantID string, from, to time.Time) (ReportData, error) {
	var d ReportData
	if err := s.pool.QueryRow(ctx, "SELECT name FROM tenants WHERE id = $1", tenantID).Scan(&d.TenantName); err != nil {
		return d, mapErr(err)
	}

	rows, err := s.pool.Query(ctx, `SELECT id, name, created_at FROM sites
		WHERE tenant_id = $1 AND created_at < $2 ORDER BY name, id`, tenantID, to)
	if err != nil {
		return d, err
	}
	if d.Sites, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportSite, error) {
		var x ReportSite
		return x, r.Scan(&x.ID, &x.Name, &x.CreatedAt)
	}); err != nil {
		return d, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT id, site_id, verdict, summary, started_at, resolved_at,
		       extract(epoch FROM least(coalesce(resolved_at, $3), $3) - greatest(started_at, $2)) / 60
		FROM events
		WHERE tenant_id = $1 AND started_at < $3 AND coalesce(resolved_at, 'infinity') > $2
		ORDER BY started_at
		LIMIT $4`, tenantID, from, to, maxReportIncidents)
	if err != nil {
		return d, err
	}
	if d.Incidents, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportIncident, error) {
		var x ReportIncident
		return x, r.Scan(&x.ID, &x.SiteID, &x.Verdict, &x.Summary, &x.StartedAt, &x.EndedAt, &x.Minutes)
	}); err != nil {
		return d, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT r.site_id, r.test_id, r.kind, r.target, count(*), count(*) FILTER (WHERE r.success),
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY r.latency_ms) FILTER (WHERE r.success),
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY r.latency_ms) FILTER (WHERE r.success),
		       percentile_cont(0.99) WITHIN GROUP (ORDER BY r.latency_ms) FILTER (WHERE r.success),
		       avg(r.loss_pct), t.interval_seconds, coalesce(t.created_at, min(r.time))
		FROM results r
		LEFT JOIN tests t ON t.id = r.test_id
		WHERE r.tenant_id = $1 AND r.time >= $2 AND r.time < $3 AND r.kind <> 'traceroute'
		GROUP BY r.site_id, r.test_id, r.kind, r.target, t.interval_seconds, t.created_at
		ORDER BY r.site_id, r.kind, r.target`, tenantID, from, to)
	if err != nil {
		return d, err
	}
	d.Tests, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportTest, error) {
		var x ReportTest
		return x, r.Scan(&x.SiteID, &x.TestID, &x.Kind, &x.Target, &x.Samples, &x.Successes,
			&x.LatencyP50, &x.LatencyP95, &x.LatencyP99, &x.LossAvg, &x.IntervalSeconds, &x.TestCreatedAt)
	})
	return d, err
}
