// Package agent is the PathLedger branch agent runtime: enrolment, test
// scheduling, offline spooling and result upload.
package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/sanjeev864/pathledger/internal/probe"
	"github.com/sanjeev864/pathledger/internal/update"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Config controls an agent. Zero durations take the defaults below.
type Config struct {
	ControllerURL  string
	EnrollToken    string
	StateDir       string
	Version        string
	PlanRefresh    time.Duration
	UploadInterval time.Duration
	SpoolMaxBytes  int64
	MaxConcurrency int
	// HTTPClient is used to reach the controller; nil builds a default one.
	HTTPClient *http.Client
	// Runner executes tests; nil uses the built-in probes.
	Runner RunFunc
	// UpdateKeys are the trusted release-signing keys. Without any, the agent
	// never installs updates.
	UpdateKeys []ed25519.PublicKey
}

// Defaults.
const (
	DefaultPlanRefresh    = 60 * time.Second
	DefaultUploadInterval = 15 * time.Second
	DefaultSpoolMaxBytes  = 64 << 20
	DefaultMaxConcurrency = 8
	maxBackoff            = 5 * time.Minute
	updateRetry           = 10 * time.Minute
	shutdownFlushTimeout  = 5 * time.Second
)

// ErrRevoked is returned when the controller rejects the agent's credential.
var ErrRevoked = errors.New("controller rejected this agent's credential; it may have been revoked — re-enrol with a new token")

func (c *Config) setDefaults() {
	if c.PlanRefresh == 0 {
		c.PlanRefresh = DefaultPlanRefresh
	}
	if c.UploadInterval == 0 {
		c.UploadInterval = DefaultUploadInterval
	}
	if c.SpoolMaxBytes == 0 {
		c.SpoolMaxBytes = DefaultSpoolMaxBytes
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = DefaultMaxConcurrency
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Runner == nil {
		c.Runner = probe.NewRunner().Run
	}
	if c.Version == "" {
		c.Version = "dev"
	}
}

// Agent is a running PathLedger agent.
type Agent struct {
	cfg    Config
	log    *slog.Logger
	client atomic.Pointer[Client] // replaced when the credential rotates
	spool  *Spool
	sched  *Scheduler
	state  State
}

// statePath is where the agent's identity is stored.
func statePath(dir string) string { return filepath.Join(dir, "agent.json") }

// New prepares an agent, enrolling it first if it has no saved identity.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*Agent, error) {
	cfg.setDefaults()
	if cfg.StateDir == "" {
		return nil, errors.New("state directory is required")
	}
	state, err := LoadState(statePath(cfg.StateDir))
	switch {
	case errors.Is(err, ErrNotEnrolled):
		if cfg.EnrollToken == "" {
			return nil, errors.New("agent is not enrolled: provide an enrolment token")
		}
		state, err = enroll(ctx, cfg, log)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if cfg.ControllerURL != "" && cfg.ControllerURL != state.ControllerURL {
			return nil, fmt.Errorf("agent is enrolled with %s; remove %s to enrol with a different controller",
				state.ControllerURL, statePath(cfg.StateDir))
		}
	}

	client, err := NewClient(state.ControllerURL, cfg.HTTPClient, userAgent(cfg.Version))
	if err != nil {
		return nil, err
	}
	spool, err := OpenSpool(filepath.Join(cfg.StateDir, "spool"), cfg.SpoolMaxBytes, log)
	if err != nil {
		return nil, err
	}
	a := &Agent{cfg: cfg, log: log.With("agent_id", state.AgentID), spool: spool, state: state}
	a.client.Store(client.WithToken(state.AgentToken))
	a.sched = NewScheduler(cfg.Runner, a.store, cfg.MaxConcurrency, a.log)
	return a, nil
}

func userAgent(version string) string {
	return fmt.Sprintf("PathLedger-Agent/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
}

func enroll(ctx context.Context, cfg Config, log *slog.Logger) (State, error) {
	if cfg.ControllerURL == "" {
		return State{}, errors.New("controller URL is required to enrol")
	}
	client, err := NewClient(cfg.ControllerURL, cfg.HTTPClient, userAgent(cfg.Version))
	if err != nil {
		return State{}, err
	}
	host, _ := os.Hostname()
	resp, err := client.Enroll(ctx, wire.EnrollRequest{
		Token: cfg.EnrollToken, Hostname: host, AgentVersion: cfg.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
	})
	if err != nil {
		return State{}, fmt.Errorf("enrolment failed: %w", err)
	}
	st := State{
		AgentID: resp.AgentID, AgentToken: resp.AgentToken, TenantID: resp.TenantID, SiteID: resp.SiteID,
		ControllerURL: cfg.ControllerURL, EnrolledAt: time.Now().UTC(),
	}
	if err := SaveState(statePath(cfg.StateDir), st); err != nil {
		return State{}, err
	}
	log.Info("enrolled with controller", "agent_id", st.AgentID, "site_id", st.SiteID)
	return st, nil
}

// api returns the client with the current credential.
func (a *Agent) api() *Client { return a.client.Load() }

// rotateCredential obtains a new credential and saves it before using it.
// If saving fails the old credential stays in use (the controller keeps it
// valid until the new one is first presented).
func (a *Agent) rotateCredential(ctx context.Context) error {
	resp, err := a.api().RotateCredential(ctx)
	if err != nil {
		return err
	}
	next := a.state
	next.AgentToken = resp.AgentToken
	if err := SaveState(statePath(a.cfg.StateDir), next); err != nil {
		return fmt.Errorf("saving rotated credential: %w", err)
	}
	a.state = next
	a.client.Store(a.api().WithToken(resp.AgentToken))
	a.log.Info("agent credential rotated")
	return nil
}

// ID returns the agent's identifier.
func (a *Agent) ID() string { return a.state.AgentID }

// store is the scheduler's result sink.
func (a *Agent) store(r wire.Result) {
	if err := a.spool.Append([]wire.Result{r}); err != nil {
		a.log.Error("could not spool result; it is lost", "test_id", r.TestID, "error", err)
	}
}

// Run blocks until ctx is cancelled or the credential is revoked.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent started", "controller", a.state.ControllerURL, "site_id", a.state.SiteID, "version", a.cfg.Version)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- a.planLoop(runCtx) }()
	go func() { errc <- a.uploadLoop(runCtx) }()

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	cancel()
	a.sched.Stop()
	// Give buffered results one last chance to reach the controller.
	if !errors.Is(err, ErrRevoked) {
		flushCtx, done := context.WithTimeout(context.Background(), shutdownFlushTimeout)
		if ferr := a.flush(flushCtx); ferr != nil {
			a.log.Info("results left in spool for next start", "reason", ferr)
		}
		done()
	}
	if cerr := a.spool.Close(); cerr != nil && err == nil {
		err = cerr
	}
	a.log.Info("agent stopped")
	return err
}

func (a *Agent) planLoop(ctx context.Context) error {
	var (
		etag          string
		failures      int
		healthy       bool
		rotate        bool   // from the last full plan
		announced     string // release version from the last full plan
		nextRotate    time.Time
		nextUpdate    time.Time
		updatesWarned bool
	)
	for {
		plan, newETag, notModified, err := a.api().Plan(ctx, etag)
		wait := a.cfg.PlanRefresh
		switch {
		case err == nil:
			failures = 0
			if !healthy {
				markHealthy(a.cfg.StateDir)
				healthy = true
			}
			if !notModified {
				etag = newETag
				a.sched.Apply(ctx, plan.Tests)
				a.log.Info("test plan applied", "tests", a.sched.Active())
				rotate, announced = plan.RotateCredential, ""
				if plan.Update != nil {
					announced = plan.Update.Version
				}
			}
			now := time.Now()
			if rotate && !now.Before(nextRotate) {
				if err := a.rotateCredential(ctx); err != nil {
					a.log.Warn("credential rotation failed; will retry", "error", err)
					nextRotate = now.Add(updateRetry)
				} else {
					rotate, etag = false, "" // refetch the plan with the new credential
				}
			}
			if announced != "" && update.Newer(announced, a.cfg.Version) && !now.Before(nextUpdate) {
				if len(a.cfg.UpdateKeys) == 0 {
					if !updatesWarned {
						a.log.Info("a newer agent release is available; automatic updates are off (no update public key configured)", "version", announced)
						updatesWarned = true
					}
				} else {
					ok, err := a.installUpdate(ctx, announced)
					switch {
					case err != nil:
						a.log.Warn("installing agent update failed; will retry", "version", announced, "error", err)
						nextUpdate = now.Add(updateRetry)
					case ok:
						return ErrUpdateInstalled
					default:
						nextUpdate = now.Add(updateRetry)
					}
				}
			}
		case IsUnauthorized(err):
			return ErrRevoked
		case ctx.Err() != nil:
			return nil
		default:
			failures++
			wait = backoff(failures)
			a.log.Warn("fetching test plan failed; keeping current plan", "error", err, "retry_in", wait)
		}
		if !sleep(ctx, wait) {
			return nil
		}
	}
}

func (a *Agent) uploadLoop(ctx context.Context) error {
	failures := 0
	for {
		wait := a.cfg.UploadInterval
		if err := a.flush(ctx); err != nil {
			if errors.Is(err, ErrRevoked) {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
			failures++
			wait = backoff(failures)
			a.log.Warn("uploading results failed; will retry", "error", err, "retry_in", wait)
		} else {
			failures = 0
		}
		if !sleep(ctx, wait) {
			return nil
		}
	}
}

// flush uploads every spooled segment, oldest first.
func (a *Agent) flush(ctx context.Context) error {
	for {
		seg, ok, err := a.spool.Next()
		if err != nil || !ok {
			return err
		}
		if len(seg.Results) > 0 {
			resp, err := a.api().Upload(ctx, seg.Results)
			switch {
			case IsUnauthorized(err):
				return ErrRevoked
			case IsPermanent(err):
				// The controller will never accept this batch; keeping it would
				// block every later result behind it.
				a.log.Error("controller rejected a result batch; discarding it", "segment", seg.Name, "results", len(seg.Results), "error", err)
			case err != nil:
				return err
			case resp.Rejected > 0:
				a.log.Warn("controller rejected some results", "accepted", resp.Accepted, "rejected", resp.Rejected)
			}
		}
		if err := a.spool.Ack(seg.Name); err != nil {
			return err
		}
	}
}

// backoff returns an exponential delay with "equal jitter" (half fixed, half
// random), capped at maxBackoff, so many agents recovering from the same
// outage do not reconnect in lockstep.
func backoff(failures int) time.Duration {
	ceiling := 2 * time.Second << min(failures, 10)
	if ceiling > maxBackoff || ceiling <= 0 {
		ceiling = maxBackoff
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
