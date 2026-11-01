package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// maxClockSkew bounds how far in the future a result timestamp may be; a
// badly set agent clock must not create data that sorts ahead of real data.
const maxClockSkew = 5 * time.Minute

// maxResultAge bounds how old a buffered result may be when it arrives.
const maxResultAge = 30 * 24 * time.Hour

// InsertResults stores results from an agent. A result is accepted only if
// it references a test currently assigned to the agent's site and its
// timestamp is plausible; everything else is counted as rejected.
func (s *Store) InsertResults(ctx context.Context, agent AgentIdentity, results []wire.Result) (accepted, rejected int, err error) {
	plan, err := s.PlanFor(ctx, agent.TenantID, agent.SiteID)
	if err != nil {
		return 0, 0, err
	}
	allowed := make(map[string]bool, len(plan))
	for _, t := range plan {
		allowed[t.ID] = true
	}
	now := s.now()
	rows := make([][]any, 0, len(results))
	for _, r := range results {
		if !allowed[r.TestID] || r.Validate() != nil || r.Time.After(now.Add(maxClockSkew)) || r.Time.Before(now.Add(-maxResultAge)) {
			rejected++
			continue
		}
		details := r.Details
		if details == nil {
			details = map[string]string{}
		}
		var hops any // NULL for non-traceroute results
		if len(r.Hops) > 0 {
			s.asn.AnnotateHops(r.Hops)
			hops = r.Hops
		}
		rows = append(rows, []any{
			r.Time, now, agent.TenantID, agent.SiteID, agent.AgentID, r.TestID, string(r.Kind), r.Target,
			r.Success, r.LatencyMS, r.LossPct, r.JitterMS, string(r.ErrorClass), r.Error, details, hops,
		})
	}
	if len(rows) == 0 {
		return 0, rejected, nil
	}
	n, err := s.pool.CopyFrom(ctx, pgx.Identifier{"results"},
		[]string{"time", "received_at", "tenant_id", "site_id", "agent_id", "test_id", "kind", "target",
			"success", "latency_ms", "loss_pct", "jitter_ms", "error_class", "error", "details", "hops"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return 0, 0, err
	}
	return int(n), rejected, nil
}

// ResultFilter narrows a results query. From and To are required.
type ResultFilter struct {
	SiteID string
	TestID string
	From   time.Time
	To     time.Time
	Limit  int
}

// StoredResult is a result with its storage context.
type StoredResult struct {
	wire.Result
	SiteID  string `json:"site_id"`
	AgentID string `json:"agent_id"`
}

// QueryResults returns a tenant's results, newest first.
func (s *Store) QueryResults(ctx context.Context, tenantID string, f ResultFilter) ([]StoredResult, error) {
	q := `SELECT time, site_id, agent_id, test_id, kind, target, success, latency_ms, loss_pct, jitter_ms, error_class, error, details, hops
	      FROM results
	      WHERE tenant_id = $1 AND time >= $2 AND time < $3
	        AND ($4::uuid IS NULL OR site_id = $4::uuid)
	        AND ($5::uuid IS NULL OR test_id = $5::uuid)
	      ORDER BY time DESC
	      LIMIT $6`
	rows, err := s.pool.Query(ctx, q, tenantID, f.From, f.To, nullIfEmpty(f.SiteID), nullIfEmpty(f.TestID), f.Limit)
	if err != nil {
		return nil, mapErr(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StoredResult, error) {
		var x StoredResult
		var kind, class string
		err := r.Scan(&x.Time, &x.SiteID, &x.AgentID, &x.TestID, &kind, &x.Target, &x.Success,
			&x.LatencyMS, &x.LossPct, &x.JitterMS, &class, &x.Error, &x.Details, &x.Hops)
		x.Kind, x.ErrorClass = wire.ProbeKind(kind), wire.ErrorClass(class)
		return x, err
	})
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
