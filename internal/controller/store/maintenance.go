package store

import (
	"context"
	"time"
)

// Retention says how long each kind of data is kept.
type Retention struct {
	Results    time.Duration // raw probe results
	Events     time.Duration // resolved incidents
	Deliveries time.Duration // finished notification deliveries
}

// PurgeReport counts what a purge removed.
type PurgeReport struct {
	Results, Events, Deliveries, Sessions, Invitations, EnrollmentTokens int64
}

const purgeBatch = 10_000

// Purge deletes expired data. Results are deleted in batches so a large
// backlog never holds long locks; with TimescaleDB, whole chunks are dropped.
func (s *Store) Purge(ctx context.Context, r Retention) (PurgeReport, error) {
	var rep PurgeReport
	now := s.now()

	var hyper bool
	_ = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`).Scan(&hyper)
	if hyper {
		if _, err := s.pool.Exec(ctx, "SELECT drop_chunks('results', older_than => $1::timestamptz)", now.Add(-r.Results)); err != nil {
			return rep, err
		}
	} else {
		for {
			tag, err := s.pool.Exec(ctx, `DELETE FROM results WHERE ctid = ANY (ARRAY(
				SELECT ctid FROM results WHERE time < $1 LIMIT $2))`, now.Add(-r.Results), purgeBatch)
			if err != nil {
				return rep, err
			}
			rep.Results += tag.RowsAffected()
			if tag.RowsAffected() < purgeBatch || ctx.Err() != nil {
				break
			}
		}
	}
	steps := []struct {
		n   *int64
		sql string
		arg any
	}{
		{&rep.Events, "DELETE FROM events WHERE state = 'resolved' AND resolved_at < $1", now.Add(-r.Events)},
		{&rep.Deliveries, "DELETE FROM deliveries WHERE (delivered_at < $1 OR failed_at < $1)", now.Add(-r.Deliveries)},
		{&rep.Sessions, "DELETE FROM sessions WHERE expires_at < $1", now},
		{&rep.Invitations, "DELETE FROM invitations WHERE expires_at < $1 OR accepted_at < $1", now.Add(-30 * 24 * time.Hour)},
		{&rep.EnrollmentTokens, "DELETE FROM enrollment_tokens WHERE expires_at < $1", now.Add(-30 * 24 * time.Hour)},
	}
	for _, st := range steps {
		tag, err := s.pool.Exec(ctx, st.sql, st.arg)
		if err != nil {
			return rep, err
		}
		*st.n = tag.RowsAffected()
	}
	return rep, nil
}

// Gauges returns operational counts for /metrics.
func (s *Store) Gauges(ctx context.Context) (map[string]float64, error) {
	var agents, stale, open, pending, failed, results float64
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE revoked_at IS NULL),
		(SELECT count(*) FROM agents WHERE revoked_at IS NULL AND (last_seen_at IS NULL OR last_seen_at < now() - interval '5 minutes')),
		(SELECT count(*) FROM events WHERE state = 'open'),
		(SELECT count(*) FROM deliveries WHERE delivered_at IS NULL AND failed_at IS NULL),
		(SELECT count(*) FROM deliveries WHERE failed_at > now() - interval '1 day'),
		(SELECT greatest(reltuples, 0) FROM pg_class WHERE relname = 'results')`).
		Scan(&agents, &stale, &open, &pending, &failed, &results)
	if err != nil {
		return nil, err
	}
	return map[string]float64{
		"pathledger_agents":                     agents,
		"pathledger_agents_stale":               stale,
		"pathledger_events_open":                open,
		"pathledger_deliveries_pending":         pending,
		"pathledger_deliveries_failed_last_day": failed,
		"pathledger_results_rows_estimate":      results,
	}, nil
}

const maintenanceLockID = 727_365_003

// WithMaintenanceLock runs fn only if this replica wins the maintenance lock.
func (s *Store) WithMaintenanceLock(ctx context.Context, fn func(context.Context) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", maintenanceLockID).Scan(&got); err != nil {
		return err
	}
	if !got {
		return ErrLocked
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", maintenanceLockID) }()
	return fn(ctx)
}
