package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Tenant is a customer account.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// APIKey is a management credential (without its secret).
type APIKey struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// Site is a branch or office where an agent runs.
type Site struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	Location  string    `json:"location"`
	CreatedAt time.Time `json:"created_at"`
}

// EnrollmentToken is a single-use credential for registering an agent.
type EnrollmentToken struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Agent is an enrolled agent.
type Agent struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	SiteID     string     `json:"site_id"`
	Hostname   string     `json:"hostname"`
	Version    string     `json:"version"`
	OS         string     `json:"os"`
	Arch       string     `json:"arch"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// Test is a stored test definition.
type Test struct {
	wire.Test
	TenantID  string    `json:"tenant_id"`
	SiteID    *string   `json:"site_id"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentIdentity is what an authenticated agent may act as.
type AgentIdentity struct {
	AgentID  string
	TenantID string
	SiteID   string
	// TokenRotatedAt is when the agent's current credential was issued.
	TokenRotatedAt time.Time
}

// CreateTenant creates a tenant.
func (s *Store) CreateTenant(ctx context.Context, name string) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		"INSERT INTO tenants (name) VALUES ($1) RETURNING id, name, created_at", name,
	).Scan(&t.ID, &t.Name, &t.CreatedAt)
	return t, mapErr(err)
}

// CreateAPIKey issues a management key. The plaintext is returned once.
func (s *Store) CreateAPIKey(ctx context.Context, tenantID, name string) (APIKey, string, error) {
	token, hash, err := auth.Generate(auth.PrefixAPIKey)
	if err != nil {
		return APIKey{}, "", err
	}
	k := APIKey{TenantID: tenantID, Name: name, Prefix: auth.DisplayPrefix(token)}
	err = s.pool.QueryRow(ctx,
		`INSERT INTO api_keys (tenant_id, name, prefix, key_hash) VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`, tenantID, name, k.Prefix, hash,
	).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return APIKey{}, "", mapErr(err)
	}
	return k, token, nil
}

// AuthenticateAPIKey resolves a key to its tenant. last_used_at is updated at
// most once a minute to avoid a write on every request.
func (s *Store) AuthenticateAPIKey(ctx context.Context, token string) (tenantID, keyID string, err error) {
	if _, err := auth.Parse(token, auth.PrefixAPIKey); err != nil {
		return "", "", ErrNotFound
	}
	err = s.pool.QueryRow(ctx,
		`UPDATE api_keys SET last_used_at = CASE
		     WHEN last_used_at IS NULL OR last_used_at < now() - interval '1 minute' THEN now()
		     ELSE last_used_at END
		 WHERE key_hash = $1 AND revoked_at IS NULL
		 RETURNING tenant_id, id`, auth.Hash(token),
	).Scan(&tenantID, &keyID)
	return tenantID, keyID, mapErr(err)
}

// CreateSite creates a site.
func (s *Store) CreateSite(ctx context.Context, tenantID, name, location string) (Site, error) {
	site := Site{TenantID: tenantID, Name: name, Location: location}
	err := s.pool.QueryRow(ctx,
		"INSERT INTO sites (tenant_id, name, location) VALUES ($1, $2, $3) RETURNING id, created_at",
		tenantID, name, location,
	).Scan(&site.ID, &site.CreatedAt)
	return site, mapErr(err)
}

// ListSites lists a tenant's sites by name.
func (s *Store) ListSites(ctx context.Context, tenantID string) ([]Site, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT id, tenant_id, name, location, created_at FROM sites WHERE tenant_id = $1 ORDER BY name", tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Site, error) {
		var x Site
		err := r.Scan(&x.ID, &x.TenantID, &x.Name, &x.Location, &x.CreatedAt)
		return x, err
	})
}

// GetSite returns one site.
func (s *Store) GetSite(ctx context.Context, tenantID, siteID string) (Site, error) {
	var x Site
	err := s.pool.QueryRow(ctx,
		"SELECT id, tenant_id, name, location, created_at FROM sites WHERE tenant_id = $1 AND id = $2",
		tenantID, siteID,
	).Scan(&x.ID, &x.TenantID, &x.Name, &x.Location, &x.CreatedAt)
	return x, mapErr(err)
}

// CreateEnrollmentToken issues a single-use token for a site.
func (s *Store) CreateEnrollmentToken(ctx context.Context, tenantID, siteID string, ttl time.Duration) (EnrollmentToken, string, error) {
	token, hash, err := auth.Generate(auth.PrefixEnrollment)
	if err != nil {
		return EnrollmentToken{}, "", err
	}
	et := EnrollmentToken{SiteID: siteID, ExpiresAt: s.now().Add(ttl).UTC().Truncate(time.Microsecond)}
	err = s.pool.QueryRow(ctx,
		`INSERT INTO enrollment_tokens (tenant_id, site_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4) RETURNING id`, tenantID, siteID, hash, et.ExpiresAt,
	).Scan(&et.ID)
	if err != nil {
		return EnrollmentToken{}, "", mapErr(err)
	}
	return et, token, nil
}

// EnrollAgent consumes an enrolment token and registers an agent. The token
// row is locked so two agents cannot enrol with the same token concurrently.
func (s *Store) EnrollAgent(ctx context.Context, req wire.EnrollRequest) (wire.EnrollResponse, error) {
	var resp wire.EnrollResponse
	if _, err := auth.Parse(req.Token, auth.PrefixEnrollment); err != nil {
		return resp, ErrInvalidToken
	}
	agentToken, agentHash, err := auth.Generate(auth.PrefixAgent)
	if err != nil {
		return resp, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tokenID string
		err := tx.QueryRow(ctx,
			`SELECT id, tenant_id, site_id FROM enrollment_tokens
			 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
			 FOR UPDATE`, auth.Hash(req.Token),
		).Scan(&tokenID, &resp.TenantID, &resp.SiteID)
		if err != nil {
			if mapErr(err) == ErrNotFound {
				return ErrInvalidToken
			}
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE enrollment_tokens SET used_at = now() WHERE id = $1", tokenID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO agents (tenant_id, site_id, hostname, version, os, arch, token_hash, last_seen_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, now()) RETURNING id`,
			resp.TenantID, resp.SiteID, req.Hostname, req.AgentVersion, req.OS, req.Arch, agentHash,
		).Scan(&resp.AgentID)
	})
	if err != nil {
		return wire.EnrollResponse{}, err
	}
	resp.AgentToken = agentToken
	return resp, nil
}

// AuthenticateAgent resolves an agent credential and records that the agent
// was seen (at most once every 30 seconds).
func (s *Store) AuthenticateAgent(ctx context.Context, token string) (AgentIdentity, error) {
	var id AgentIdentity
	if _, err := auth.Parse(token, auth.PrefixAgent); err != nil {
		return id, ErrNotFound
	}
	// The first use of a pending (rotated) token promotes it and retires
	// the old one. All CASE expressions see the row's old values.
	err := s.pool.QueryRow(ctx,
		`UPDATE agents SET
		     last_seen_at = CASE
		         WHEN last_seen_at IS NULL OR last_seen_at < now() - interval '30 seconds' THEN now()
		         ELSE last_seen_at END,
		     token_hash = CASE WHEN pending_token_hash = $1 THEN pending_token_hash ELSE token_hash END,
		     token_rotated_at = CASE WHEN pending_token_hash = $1 THEN now() ELSE token_rotated_at END,
		     pending_token_hash = CASE WHEN pending_token_hash = $1 THEN NULL ELSE pending_token_hash END
		 WHERE (token_hash = $1 OR pending_token_hash = $1) AND revoked_at IS NULL
		 RETURNING id, tenant_id, site_id, token_rotated_at`, auth.Hash(token),
	).Scan(&id.AgentID, &id.TenantID, &id.SiteID, &id.TokenRotatedAt)
	return id, mapErr(err)
}

// RotateAgentToken issues a new credential for an agent. It becomes the
// agent's only credential the first time it is used; until then the current
// one keeps working. Calling it again replaces an unused pending token.
func (s *Store) RotateAgentToken(ctx context.Context, agentID string) (string, error) {
	token, hash, err := auth.Generate(auth.PrefixAgent)
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx,
		"UPDATE agents SET pending_token_hash = $2 WHERE id = $1 AND revoked_at IS NULL", agentID, hash)
	if err != nil {
		return "", mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

// RequestAgentRotation makes the agent rotate its credential at its next
// plan fetch (for example after a device was handled by a third party).
func (s *Store) RequestAgentRotation(ctx context.Context, tenantID, agentID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE agents SET token_rotated_at = 'epoch' WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL", tenantID, agentID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAgentVersion records the version an agent reports (it changes after a
// self-update). It writes only when the value differs.
func (s *Store) SetAgentVersion(ctx context.Context, agentID, version string) error {
	_, err := s.pool.Exec(ctx, "UPDATE agents SET version = $2 WHERE id = $1 AND version <> $2", agentID, version)
	return err
}

// ListAgents lists a tenant's agents.
func (s *Store) ListAgents(ctx context.Context, tenantID string) ([]Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, site_id, hostname, version, os, arch, created_at, last_seen_at, revoked_at
		 FROM agents WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Agent, error) {
		var a Agent
		err := r.Scan(&a.ID, &a.TenantID, &a.SiteID, &a.Hostname, &a.Version, &a.OS, &a.Arch, &a.CreatedAt, &a.LastSeenAt, &a.RevokedAt)
		return a, err
	})
}

// RevokeAgent permanently disables an agent's credential.
func (s *Store) RevokeAgent(ctx context.Context, tenantID, agentID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE agents SET revoked_at = now() WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL", tenantID, agentID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const testColumns = "id, tenant_id, site_id, kind, target, interval_seconds, timeout_millis, params, enabled, created_at, updated_at"

func scanTest(r pgx.CollectableRow) (Test, error) {
	var t Test
	err := r.Scan(&t.ID, &t.TenantID, &t.SiteID, &t.Kind, &t.Target, &t.IntervalSeconds, &t.TimeoutMillis,
		&t.Params, &t.Enabled, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// CreateTest stores a validated test. siteID nil means every site.
func (s *Store) CreateTest(ctx context.Context, tenantID string, siteID *string, t wire.Test) (Test, error) {
	if err := t.Validate(); err != nil {
		return Test{}, err
	}
	rows, err := s.pool.Query(ctx,
		`INSERT INTO tests (tenant_id, site_id, kind, target, interval_seconds, timeout_millis, params)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+testColumns,
		tenantID, siteID, t.Kind, t.Target, t.IntervalSeconds, t.TimeoutMillis, t.Params)
	if err != nil {
		return Test{}, mapErr(err)
	}
	created, err := pgx.CollectExactlyOneRow(rows, scanTest)
	return created, mapErr(err)
}

// ListTests lists a tenant's tests.
func (s *Store) ListTests(ctx context.Context, tenantID string) ([]Test, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+testColumns+" FROM tests WHERE tenant_id = $1 ORDER BY created_at", tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTest)
}

// DeleteTest removes a test. Its historical results are kept.
func (s *Store) DeleteTest(ctx context.Context, tenantID, testID string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM tests WHERE tenant_id = $1 AND id = $2", tenantID, testID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PlanFor returns the enabled tests for a site: site-specific tests plus
// tenant-wide tests, in a stable order so plan ETags are deterministic.
func (s *Store) PlanFor(ctx context.Context, tenantID, siteID string) ([]wire.Test, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+testColumns+` FROM tests
		 WHERE tenant_id = $1 AND enabled AND (site_id = $2 OR site_id IS NULL)
		 ORDER BY id`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	tests, err := pgx.CollectRows(rows, scanTest)
	if err != nil {
		return nil, err
	}
	out := make([]wire.Test, len(tests))
	for i, t := range tests {
		out[i] = t.Test
	}
	return out, nil
}

// String implements fmt.Stringer for log output without secrets.
func (a AgentIdentity) String() string { return fmt.Sprintf("agent %s (site %s)", a.AgentID, a.SiteID) }

// GetTest returns one test.
func (s *Store) GetTest(ctx context.Context, tenantID, testID string) (Test, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+testColumns+" FROM tests WHERE tenant_id = $1 AND id = $2", tenantID, testID)
	if err != nil {
		return Test{}, mapErr(err)
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTest)
	return t, mapErr(err)
}

// UpdateTest replaces a test's definition and enabled flag after validation.
func (s *Store) UpdateTest(ctx context.Context, tenantID string, t Test) (Test, error) {
	if err := t.Test.Validate(); err != nil {
		return Test{}, err
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE tests SET target = $3, interval_seconds = $4, timeout_millis = $5, params = $6, enabled = $7, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 RETURNING `+testColumns,
		tenantID, t.ID, t.Target, t.IntervalSeconds, t.TimeoutMillis, t.Params, t.Enabled)
	if err != nil {
		return Test{}, mapErr(err)
	}
	out, err := pgx.CollectExactlyOneRow(rows, scanTest)
	return out, mapErr(err)
}

// ListAPIKeys lists a tenant's API keys (never their secrets).
func (s *Store) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, tenant_id, name, prefix, created_at, last_used_at, revoked_at
		FROM api_keys WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) {
		var k APIKey
		err := r.Scan(&k.ID, &k.TenantID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
		return k, err
	})
}

// RevokeAPIKey disables a key immediately.
func (s *Store) RevokeAPIKey(ctx context.Context, tenantID, keyID string) error {
	tag, err := s.pool.Exec(ctx, "UPDATE api_keys SET revoked_at = now() WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL", tenantID, keyID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Backdate sets the creation time of a tenant's sites and tests. It exists
// for simulated (demo) tenants, whose history starts before they were
// created; reports count a site only from its creation.
func (s *Store) Backdate(ctx context.Context, tenantID string, at time.Time) error {
	if _, err := s.pool.Exec(ctx, "UPDATE sites SET created_at = $2 WHERE tenant_id = $1", tenantID, at); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, "UPDATE tests SET created_at = $2 WHERE tenant_id = $1", tenantID, at)
	return err
}
