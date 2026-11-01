package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Event is a site-level incident.
type Event struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	SiteID        string     `json:"site_id"`
	State         string     `json:"state"`
	Verdict       string     `json:"verdict"`
	Summary       string     `json:"summary"`
	Evidence      []string   `json:"evidence"`
	AffectedTests []string   `json:"affected_tests"`
	StartedAt     time.Time  `json:"started_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
	ClearCount    int        `json:"-"`
}

// Event states.
const (
	EventOpen     = "open"
	EventResolved = "resolved"
)

const eventColumns = "id, tenant_id, site_id, state, verdict, summary, evidence, affected_tests, started_at, updated_at, resolved_at, clear_count"

func scanEvent(r pgx.CollectableRow) (Event, error) {
	var e Event
	err := r.Scan(&e.ID, &e.TenantID, &e.SiteID, &e.State, &e.Verdict, &e.Summary, &e.Evidence,
		&e.AffectedTests, &e.StartedAt, &e.UpdatedAt, &e.ResolvedAt, &e.ClearCount)
	return e, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// OpenEvents returns every open event keyed by site.
func (s *Store) OpenEvents(ctx context.Context) (map[string]Event, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+eventColumns+" FROM events WHERE state = 'open'")
	if err != nil {
		return nil, err
	}
	evs, err := pgx.CollectRows(rows, scanEvent)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Event, len(evs))
	for _, e := range evs {
		out[e.SiteID] = e
	}
	return out, nil
}

// OpenEvent records a new incident.
func (s *Store) OpenEvent(ctx context.Context, e Event) (Event, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO events (tenant_id, site_id, state, verdict, summary, evidence, affected_tests, started_at, updated_at)
		VALUES ($1, $2, 'open', $3, $4, $5, $6, $7, $7) RETURNING `+eventColumns,
		e.TenantID, e.SiteID, e.Verdict, e.Summary, nonNil(e.Evidence), nonNil(e.AffectedTests), e.StartedAt)
	if err != nil {
		return Event{}, mapErr(err)
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanEvent)
	return created, mapErr(err)
}

// RefreshEvent updates an open event's diagnosis and resets its clear count.
func (s *Store) RefreshEvent(ctx context.Context, e Event, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE events SET verdict = $2, summary = $3, evidence = $4, affected_tests = $5, updated_at = $6, clear_count = 0
		WHERE id = $1 AND state = 'open'`,
		e.ID, e.Verdict, e.Summary, nonNil(e.Evidence), nonNil(e.AffectedTests), at)
	return err
}

// ClearEvent records a healthy evaluation for an open event and resolves it
// once enough consecutive healthy evaluations have been seen.
func (s *Store) ClearEvent(ctx context.Context, id string, at time.Time, resolveAfter int) (resolved bool, err error) {
	var state string
	err = s.pool.QueryRow(ctx, `
		UPDATE events SET clear_count = clear_count + 1, updated_at = $2,
		       state = CASE WHEN clear_count + 1 >= $3 THEN 'resolved' ELSE state END,
		       resolved_at = CASE WHEN clear_count + 1 >= $3 THEN $2 ELSE resolved_at END
		WHERE id = $1 AND state = 'open' RETURNING state`, id, at, resolveAfter).Scan(&state)
	if err != nil {
		return false, mapErr(err)
	}
	return state == EventResolved, nil
}

// EventFilter narrows an events listing.
type EventFilter struct {
	State  string // "", "open" or "resolved"
	SiteID string
	Limit  int
}

// ListEvents returns a tenant's events, newest first.
func (s *Store) ListEvents(ctx context.Context, tenantID string, f EventFilter) ([]Event, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+eventColumns+` FROM events
		WHERE tenant_id = $1 AND ($2 = '' OR state = $2) AND ($3::uuid IS NULL OR site_id = $3::uuid)
		ORDER BY started_at DESC LIMIT $4`, tenantID, f.State, nullIfEmpty(f.SiteID), f.Limit)
	if err != nil {
		return nil, mapErr(err)
	}
	return pgx.CollectRows(rows, scanEvent)
}

// GetEvent returns one event.
func (s *Store) GetEvent(ctx context.Context, tenantID, id string) (Event, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+eventColumns+" FROM events WHERE tenant_id = $1 AND id = $2", tenantID, id)
	if err != nil {
		return Event{}, mapErr(err)
	}
	e, err := pgx.CollectExactlyOneRow(rows, scanEvent)
	return e, mapErr(err)
}
