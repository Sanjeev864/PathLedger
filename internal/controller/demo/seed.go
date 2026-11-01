package demo

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/auth"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/enrich"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// TenantName marks the simulated customer everywhere it appears.
const TenantName = "Demo: Sunrise Clinics (simulated data)"

// MaxDays is the longest history the controller accepts (results older than
// 30 days are rejected at ingest).
const MaxDays = 29

// Options control seeding.
type Options struct {
	Days     int
	Email    string
	Password string // empty: generate one
}

// Seeded is what was created.
type Seeded struct {
	TenantID        string
	Email           string
	Password        string // empty when an existing user was reused
	Results         int
	Events          int
	ExistingAccount bool
}

// agent is a simulated agent and the credential it reports with.
type agent struct {
	id    store.AgentIdentity
	token string
	site  int
}

// PrepareStore lets the store attribute the demo addresses to demo networks.
func PrepareStore(st *store.Store) error {
	db, err := enrich.Load(strings.NewReader(ASNData))
	if err != nil {
		return err
	}
	st.SetASNData(db)
	return nil
}

// Seed creates the demo tenant, its sites, tests, agents, a login and the
// scripted history up to now.
func Seed(ctx context.Context, st *store.Store, o Options, log *slog.Logger) (Seeded, error) {
	if o.Days < 1 || o.Days > MaxDays {
		return Seeded{}, fmt.Errorf("days must be between 1 and %d", MaxDays)
	}
	out := Seeded{Email: strings.ToLower(strings.TrimSpace(o.Email))}
	tenant, err := st.CreateTenant(ctx, TenantName)
	if err != nil {
		return out, err
	}
	out.TenantID = tenant.ID
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-time.Duration(o.Days) * 24 * time.Hour)

	// Sites must predate their history for reports to count it.
	var sites []store.Site
	for _, s := range Sites {
		site, err := st.CreateSite(ctx, tenant.ID, s.Name, s.Location)
		if err != nil {
			return out, err
		}
		sites = append(sites, site)
	}
	if err := st.Backdate(ctx, tenant.ID, start.Add(-time.Hour)); err != nil {
		return out, err
	}

	var tests []wire.Test
	for _, t := range Tests {
		created, err := st.CreateTest(ctx, tenant.ID, nil, t)
		if err != nil {
			return out, fmt.Errorf("creating demo test %s %s: %w", t.Kind, t.Target, err)
		}
		t.ID = created.ID
		tests = append(tests, t)
	}
	if err := st.Backdate(ctx, tenant.ID, start.Add(-time.Hour)); err != nil {
		return out, err
	}

	agents, err := enrollAgents(ctx, st, tenant.ID, sites)
	if err != nil {
		return out, err
	}

	// History, one site and test at a time, in ingest-sized batches.
	rng := mrand.New(mrand.NewPCG(uint64(now.Unix()), 42))
	for _, a := range agents {
		for _, t := range tests {
			var batch []wire.Result
			step := time.Duration(t.IntervalSeconds) * time.Second
			for at := start.Add(time.Duration(rng.IntN(t.IntervalSeconds)) * time.Second); at.Before(now); at = at.Add(step) {
				r, ok := Result(t, a.site, at, faultAt(a.site, at, now, start), rng)
				if !ok {
					continue
				}
				if batch = append(batch, r); len(batch) == 500 {
					if out.Results, err = insert(ctx, st, a.id, batch, out.Results); err != nil {
						return out, err
					}
					batch = batch[:0]
				}
			}
			if out.Results, err = insert(ctx, st, a.id, batch, out.Results); err != nil {
				return out, err
			}
		}
	}

	for _, inc := range History {
		begin := now.Add(-inc.Ago)
		if begin.Before(start) {
			continue
		}
		summary, evidence := Summary(inc.Fault)
		ev, err := st.OpenEvent(ctx, store.Event{TenantID: tenant.ID, SiteID: sites[inc.Site].ID, Verdict: string(inc.Fault),
			Summary: summary, Evidence: evidence, StartedAt: begin})
		if err != nil {
			return out, err
		}
		if _, err := st.ClearEvent(ctx, ev.ID, begin.Add(inc.Duration), 1); err != nil {
			return out, err
		}
		out.Events++
	}

	if err := addLogin(ctx, st, tenant.ID, o, &out); err != nil {
		return out, err
	}
	log.Info("demo tenant seeded", "tenant_id", tenant.ID, "results", out.Results, "events", out.Events)
	return out, nil
}

// faultAt returns the scripted fault at a site and time.
func faultAt(site int, at, now, start time.Time) Fault {
	for _, inc := range History {
		begin := now.Add(-inc.Ago)
		if inc.Site == site && !begin.Before(start) && !at.Before(begin) && at.Before(begin.Add(inc.Duration)) {
			return inc.Fault
		}
	}
	return Healthy
}

func insert(ctx context.Context, st *store.Store, id store.AgentIdentity, rs []wire.Result, total int) (int, error) {
	if len(rs) == 0 {
		return total, nil
	}
	acc, rej, err := st.InsertResults(ctx, id, rs)
	if err != nil {
		return total, err
	}
	if rej > 0 {
		return total, fmt.Errorf("controller rejected %d demo results", rej)
	}
	return total + acc, nil
}

func enrollAgents(ctx context.Context, st *store.Store, tenantID string, sites []store.Site) ([]agent, error) {
	var out []agent
	for i, site := range sites {
		_, tok, err := st.CreateEnrollmentToken(ctx, tenantID, site.ID, time.Hour)
		if err != nil {
			return nil, err
		}
		resp, err := st.EnrollAgent(ctx, wire.EnrollRequest{Token: tok, Hostname: Sites[i].Hostname,
			AgentVersion: "demo", OS: "simulated", Arch: "-"})
		if err != nil {
			return nil, err
		}
		out = append(out, agent{id: store.AgentIdentity{AgentID: resp.AgentID, TenantID: tenantID, SiteID: site.ID}, token: resp.AgentToken, site: i})
	}
	return out, nil
}

// addLogin gives the demo login owner access to the demo tenant.
func addLogin(ctx context.Context, st *store.Store, tenantID string, o Options, out *Seeded) error {
	u, err := st.UserByEmail(ctx, out.Email)
	switch {
	case err == nil:
		out.ExistingAccount = true
	case errors.Is(err, store.ErrNotFound):
		pw := o.Password
		if pw == "" {
			b := make([]byte, 12)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			pw = base64.RawURLEncoding.EncodeToString(b)
		}
		if err := auth.CheckPasswordPolicy(pw); err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if u, err = st.CreateUser(ctx, out.Email, "Demo owner", hash); err != nil {
			return err
		}
		out.Password = pw
	default:
		return err
	}
	return st.AddMembership(ctx, u.ID, tenantID, store.RoleOwner)
}

// LiveOptions configure the live feed.
type LiveOptions struct {
	Fault Fault // injected fault, or Healthy for none
	Site  int   // index into Sites
	After time.Duration
	For   time.Duration
}

// Live reports fresh measurements for every demo site until ctx ends,
// injecting the configured fault, so the running controller analyses and
// alerts on it exactly as it would for real agents.
func Live(ctx context.Context, st *store.Store, tenantID string, o LiveOptions, log *slog.Logger) error {
	agents, err := liveAgents(ctx, st, tenantID)
	if err != nil {
		return err
	}
	all, err := st.ListTests(ctx, tenantID)
	if err != nil {
		return err
	}
	var tests []wire.Test
	for _, t := range all {
		if t.Enabled {
			tests = append(tests, t.Test)
		}
	}
	if len(tests) == 0 {
		return errors.New("the demo tenant has no enabled tests")
	}
	rng := mrand.New(mrand.NewPCG(uint64(time.Now().UnixNano()), 7))
	began := time.Now()
	next := map[string]time.Time{}
	active := false
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		now := time.Now().UTC()
		inject := o.Fault != Healthy && now.After(began.Add(o.After)) && now.Before(began.Add(o.After+o.For))
		if inject != active {
			active = inject
			if active {
				log.Info("injecting fault", "fault", o.Fault, "site", Sites[o.Site].Name, "for", o.For)
			} else if o.Fault != Healthy {
				log.Info("fault cleared", "fault", o.Fault, "site", Sites[o.Site].Name)
			}
		}
		for _, a := range agents {
			f := Healthy
			if inject && a.site == o.Site {
				f = o.Fault
			}
			if f != Offline {
				// Reporting is what keeps an agent "seen"; an offline site goes silent.
				if _, err := st.AuthenticateAgent(ctx, a.token); err != nil {
					return fmt.Errorf("demo agent %s: %w", Sites[a.site].Hostname, err)
				}
			}
			var batch []wire.Result
			for _, t := range tests {
				key := a.id.AgentID + t.ID
				if now.Before(next[key]) {
					continue
				}
				next[key] = now.Add(time.Duration(t.IntervalSeconds) * time.Second)
				if r, ok := Result(t, a.site, now, f, rng); ok {
					batch = append(batch, r)
				}
			}
			if _, err := insert(ctx, st, a.id, batch, 0); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// liveAgents finds the demo agents of a tenant and gives each a fresh
// credential (the original ones were never stored).
func liveAgents(ctx context.Context, st *store.Store, tenantID string) ([]agent, error) {
	list, err := st.ListAgents(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var out []agent
	for _, a := range list {
		if a.RevokedAt != nil {
			continue
		}
		for i, s := range Sites {
			if a.Hostname != s.Hostname {
				continue
			}
			tok, err := st.RotateAgentToken(ctx, a.ID)
			if err != nil {
				return nil, err
			}
			id, err := st.AuthenticateAgent(ctx, tok) // promotes the new credential
			if err != nil {
				return nil, err
			}
			out = append(out, agent{id: id, token: tok, site: i})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no demo agents in this tenant; create one with `demo` first")
	}
	return out, nil
}
