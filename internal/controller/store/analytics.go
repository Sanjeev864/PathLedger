package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// analyticsLockID makes sure only one controller replica evaluates at a time.
const analyticsLockID = 727_365_002

// ErrLocked means another replica holds the analytics lock.
var ErrLocked = errors.New("analytics lock held by another replica")

// WithAnalyticsLock runs fn only if this process wins the analytics lock.
func (s *Store) WithAnalyticsLock(ctx context.Context, fn func(context.Context) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", analyticsLockID).Scan(&got); err != nil {
		return err
	}
	if !got {
		return ErrLocked
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", analyticsLockID) }()
	return fn(ctx)
}

// BaselinesRefreshedAt reports when baselines were last recomputed.
func (s *Store) BaselinesRefreshedAt(ctx context.Context) (time.Time, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, "SELECT baselines_refreshed_at FROM analytics_state").Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}

// RefreshBaselines recomputes the median and median absolute deviation of
// successful latencies, and the average loss, per site and test over
// [from, to). Median/MAD resist the outliers that make a mean useless here.
func (s *Store) RefreshBaselines(ctx context.Context, from, to time.Time) (int, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			WITH w AS (
				SELECT tenant_id, site_id, test_id, latency_ms, loss_pct, success
				FROM results WHERE time >= $1 AND time < $2 AND kind <> 'traceroute'
			), m AS (
				SELECT tenant_id, site_id, test_id,
				       count(*) FILTER (WHERE success AND latency_ms IS NOT NULL) AS samples,
				       percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE success) AS p50,
				       coalesce(avg(loss_pct), 0) AS loss
				FROM w GROUP BY 1, 2, 3
			), d AS (
				SELECT w.site_id, w.test_id,
				       percentile_cont(0.5) WITHIN GROUP (ORDER BY abs(w.latency_ms - m.p50)) AS mad
				FROM w JOIN m USING (site_id, test_id)
				WHERE w.success AND w.latency_ms IS NOT NULL
				GROUP BY 1, 2
			)
			INSERT INTO baselines (tenant_id, site_id, test_id, samples, latency_p50, latency_mad, loss_avg, computed_at)
			SELECT m.tenant_id, m.site_id, m.test_id, m.samples, m.p50, d.mad, m.loss, $2
			FROM m LEFT JOIN d USING (site_id, test_id)
			ON CONFLICT (site_id, test_id) DO UPDATE SET
				samples = EXCLUDED.samples, latency_p50 = EXCLUDED.latency_p50,
				latency_mad = EXCLUDED.latency_mad, loss_avg = EXCLUDED.loss_avg,
				computed_at = EXCLUDED.computed_at`, from, to)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		_, err = tx.Exec(ctx, "UPDATE analytics_state SET baselines_refreshed_at = $1", to)
		return err
	})
	return int(n), err
}

// WindowRow summarises one test at one site over an evaluation window,
// with its baseline.
type WindowRow struct {
	TenantID   string
	SiteID     string
	TestID     string
	Kind       wire.ProbeKind
	Target     string
	SiteScoped bool // the test is assigned to this site only
	Samples    int
	Failures   int
	P50        *float64
	LossAvg    *float64
	ErrorClass wire.ErrorClass
	Baseline   *Baseline
}

// Baseline is a test's normal behaviour at a site.
type Baseline struct {
	Samples int
	P50     *float64
	MAD     *float64
	LossAvg float64
}

// WindowHealth summarises results in [from, to) for enabled, non-traceroute
// tests.
func (s *Store) WindowHealth(ctx context.Context, from, to time.Time) ([]WindowRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.tenant_id, r.site_id, r.test_id, t.kind, t.target, t.site_id IS NOT NULL,
		       count(*), count(*) FILTER (WHERE NOT r.success),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY r.latency_ms) FILTER (WHERE r.success),
		       avg(r.loss_pct),
		       coalesce(mode() WITHIN GROUP (ORDER BY r.error_class) FILTER (WHERE NOT r.success), ''),
		       b.samples, b.latency_p50, b.latency_mad, b.loss_avg
		FROM results r
		JOIN tests t ON t.id = r.test_id AND t.enabled AND t.kind <> 'traceroute'
		LEFT JOIN baselines b ON b.site_id = r.site_id AND b.test_id = r.test_id
		WHERE r.time >= $1 AND r.time < $2
		GROUP BY r.tenant_id, r.site_id, r.test_id, t.kind, t.target, t.site_id, b.samples, b.latency_p50, b.latency_mad, b.loss_avg`,
		from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WindowRow, error) {
		var w WindowRow
		var kind, class string
		var bSamples *int
		var bP50, bMAD, bLoss *float64
		err := r.Scan(&w.TenantID, &w.SiteID, &w.TestID, &kind, &w.Target, &w.SiteScoped,
			&w.Samples, &w.Failures, &w.P50, &w.LossAvg, &class, &bSamples, &bP50, &bMAD, &bLoss)
		w.Kind, w.ErrorClass = wire.ProbeKind(kind), wire.ErrorClass(class)
		if bSamples != nil {
			w.Baseline = &Baseline{Samples: *bSamples, P50: bP50, MAD: bMAD}
			if bLoss != nil {
				w.Baseline.LossAvg = *bLoss
			}
		}
		return w, err
	})
}

// SitePath is a site's traceroute.
type SitePath struct {
	SiteID  string
	Target  string
	Success bool
	Hops    []wire.Hop
}

// LatestPaths returns each site's most recent traceroute since a time,
// optionally only successful ones.
func (s *Store) LatestPaths(ctx context.Context, since time.Time, successfulOnly bool) (map[string]SitePath, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (site_id) site_id, target, success, coalesce(hops, '[]'::jsonb)
		FROM results
		WHERE kind = 'traceroute' AND time >= $1 AND (success OR NOT $2)
		ORDER BY site_id, time DESC`, since, successfulOnly)
	if err != nil {
		return nil, err
	}
	paths, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SitePath, error) {
		var p SitePath
		err := r.Scan(&p.SiteID, &p.Target, &p.Success, &p.Hops)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]SitePath, len(paths))
	for _, p := range paths {
		out[p.SiteID] = p
	}
	return out, nil
}

// SiteRef identifies a site within its tenant.
type SiteRef struct {
	TenantID string
	SiteID   string
	Name     string
}

// SilentSites returns sites that have active agents but none reported since
// the cutoff — the usual sign that the site lost power or connectivity.
func (s *Store) SilentSites(ctx context.Context, cutoff time.Time) ([]SiteRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.tenant_id, s.id, s.name FROM sites s
		WHERE EXISTS (SELECT 1 FROM agents a WHERE a.site_id = s.id AND a.revoked_at IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.site_id = s.id AND a.revoked_at IS NULL AND a.last_seen_at >= $1)`, cutoff)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SiteRef, error) {
		var x SiteRef
		err := r.Scan(&x.TenantID, &x.SiteID, &x.Name)
		return x, err
	})
}

// SiteNames returns names for the given site IDs.
func (s *Store) SiteNames(ctx context.Context, ids []string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, name FROM sites WHERE id = ANY($1::uuid[])", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
