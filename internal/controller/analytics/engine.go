package analytics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Tunables. They are conservative on purpose: a false alarm costs more
// customer trust than a minute of detection delay.
const (
	Window            = 5 * time.Minute // evaluation window
	BaselineSpan      = 7 * 24 * time.Hour
	BaselineRefresh   = time.Hour
	SilentAfter       = 3 * time.Minute // no agent check-in → site offline
	ResolveAfter      = 2               // consecutive healthy evaluations to resolve
	MinSamples        = 2               // per test per window
	MinBaseline       = 30              // samples before latency anomalies count
	DownFailureRatio  = 0.5
	LossFloorPct      = 5.0
	LatencyFloorMS    = 20.0
	LatencyMADFactor  = 4.0
	LatencyRatioFloor = 0.5 // at least 50% above normal
	pathFreshness     = 15 * time.Minute
)

// VerdictOffline is used when a site's agents stop reporting entirely.
const VerdictOffline Verdict = "offline"

// Store is the persistence the engine needs.
type Store interface {
	WithAnalyticsLock(ctx context.Context, fn func(context.Context) error) error
	BaselinesRefreshedAt(ctx context.Context) (time.Time, error)
	RefreshBaselines(ctx context.Context, from, to time.Time) (int, error)
	WindowHealth(ctx context.Context, from, to time.Time) ([]store.WindowRow, error)
	LatestPaths(ctx context.Context, since time.Time, successfulOnly bool) (map[string]store.SitePath, error)
	SilentSites(ctx context.Context, cutoff time.Time) ([]store.SiteRef, error)
	OpenEvents(ctx context.Context) (map[string]store.Event, error)
	OpenEvent(ctx context.Context, e store.Event) (store.Event, error)
	RefreshEvent(ctx context.Context, e store.Event, at time.Time) error
	ClearEvent(ctx context.Context, id string, at time.Time, resolveAfter int) (bool, error)
}

// Change describes what happened to an event during an evaluation.
type Change string

// Event changes delivered to notifiers.
const (
	ChangeOpened         Change = "opened"
	ChangeVerdictChanged Change = "verdict_changed"
	ChangeResolved       Change = "resolved"
)

// Notifier is told about event changes. Implementations must not block for
// long; delivery should be queued.
type Notifier interface {
	Notify(ctx context.Context, change Change, e store.Event)
}

// Engine periodically evaluates every site.
type Engine struct {
	store    Store
	notifier Notifier
	log      *slog.Logger
	now      func() time.Time
}

// NewEngine returns an engine; notifier may be nil.
func NewEngine(st Store, n Notifier, log *slog.Logger) *Engine {
	return &Engine{store: st, notifier: n, log: log, now: time.Now}
}

// Run evaluates every interval until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := e.Evaluate(ctx); err != nil && !errors.Is(err, store.ErrLocked) && ctx.Err() == nil {
			e.log.Error("analytics evaluation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Evaluate runs one evaluation cycle if this replica holds the lock.
func (e *Engine) Evaluate(ctx context.Context) error {
	return e.store.WithAnalyticsLock(ctx, e.evaluate)
}

func (e *Engine) evaluate(ctx context.Context) error {
	now := e.now().UTC()
	if err := e.maybeRefreshBaselines(ctx, now); err != nil {
		return fmt.Errorf("refreshing baselines: %w", err)
	}
	rows, err := e.store.WindowHealth(ctx, now.Add(-Window), now)
	if err != nil {
		return fmt.Errorf("window health: %w", err)
	}
	paths, err := e.store.LatestPaths(ctx, now.Add(-pathFreshness), false)
	if err != nil {
		return fmt.Errorf("latest paths: %w", err)
	}
	healthyPaths, err := e.store.LatestPaths(ctx, now.Add(-BaselineSpan), true)
	if err != nil {
		return fmt.Errorf("healthy paths: %w", err)
	}
	silent, err := e.store.SilentSites(ctx, now.Add(-SilentAfter))
	if err != nil {
		return fmt.Errorf("silent sites: %w", err)
	}
	open, err := e.store.OpenEvents(ctx)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}

	snapshots, tenants := buildSnapshots(rows, paths, healthyPaths)
	verdicts := map[string]Attribution{}
	for site, snap := range snapshots {
		if a, ok := Attribute(snap); ok {
			verdicts[site] = a
		}
	}
	for _, s := range silent {
		tenants[s.SiteID] = s.TenantID
		verdicts[s.SiteID] = Attribution{
			Verdict:  VerdictOffline,
			Summary:  fmt.Sprintf("The agent at %s has not reported for over %d minutes; the site has likely lost its internet connection or power. Buffered results will be analysed when it reconnects.", s.Name, int(SilentAfter.Minutes())),
			Evidence: []string{fmt.Sprintf("no agent check-in since %s", now.Add(-SilentAfter).Format(time.RFC3339))},
		}
	}

	for site, a := range verdicts {
		ev, isOpen := open[site]
		switch {
		case !isOpen:
			created, err := e.store.OpenEvent(ctx, store.Event{
				TenantID: tenants[site], SiteID: site, Verdict: string(a.Verdict), Summary: a.Summary,
				Evidence: a.Evidence, AffectedTests: a.Affected, StartedAt: now,
			})
			if err != nil {
				return fmt.Errorf("opening event: %w", err)
			}
			e.log.Info("event opened", "event_id", created.ID, "site_id", site, "verdict", a.Verdict)
			e.notify(ctx, ChangeOpened, created)
		default:
			changed := ev.Verdict != string(a.Verdict)
			ev.Verdict, ev.Summary, ev.Evidence, ev.AffectedTests, ev.UpdatedAt = string(a.Verdict), a.Summary, a.Evidence, a.Affected, now
			if err := e.store.RefreshEvent(ctx, ev, now); err != nil {
				return fmt.Errorf("refreshing event: %w", err)
			}
			if changed {
				e.log.Info("event verdict changed", "event_id", ev.ID, "verdict", a.Verdict)
				e.notify(ctx, ChangeVerdictChanged, ev)
			}
		}
	}
	for site, ev := range open {
		if _, still := verdicts[site]; still {
			continue
		}
		resolved, err := e.store.ClearEvent(ctx, ev.ID, now, ResolveAfter)
		if err != nil {
			return fmt.Errorf("clearing event: %w", err)
		}
		if resolved {
			ev.State, ev.ResolvedAt, ev.UpdatedAt = store.EventResolved, &now, now
			e.log.Info("event resolved", "event_id", ev.ID, "site_id", site)
			e.notify(ctx, ChangeResolved, ev)
		}
	}
	return nil
}

func (e *Engine) notify(ctx context.Context, c Change, ev store.Event) {
	if e.notifier != nil {
		e.notifier.Notify(ctx, c, ev)
	}
}

func (e *Engine) maybeRefreshBaselines(ctx context.Context, now time.Time) error {
	last, err := e.store.BaselinesRefreshedAt(ctx)
	if err != nil {
		return err
	}
	if now.Sub(last) < BaselineRefresh {
		return nil
	}
	// Exclude the current window so an ongoing incident does not become "normal".
	n, err := e.store.RefreshBaselines(ctx, now.Add(-BaselineSpan), now.Add(-Window))
	if err == nil {
		e.log.Info("baselines refreshed", "series", n)
	}
	return err
}

// Classify turns a window summary into a status.
func Classify(w store.WindowRow) Status {
	if w.Samples < MinSamples {
		return StatusUnknown
	}
	if float64(w.Failures)/float64(w.Samples) >= DownFailureRatio {
		return StatusDown
	}
	if w.LossAvg != nil {
		limit := LossFloorPct
		if w.Baseline != nil {
			limit = math.Max(limit, w.Baseline.LossAvg+LossFloorPct)
		}
		if *w.LossAvg >= limit {
			return StatusDegraded
		}
	}
	if b := w.Baseline; w.P50 != nil && b != nil && b.Samples >= MinBaseline && b.P50 != nil {
		mad := 0.0
		if b.MAD != nil {
			mad = *b.MAD
		}
		// 1.4826 × MAD estimates the standard deviation for normal data.
		margin := math.Max(LatencyMADFactor*1.4826*mad, math.Max(LatencyRatioFloor**b.P50, LatencyFloorMS))
		if *w.P50 > *b.P50+margin {
			return StatusDegraded
		}
	}
	return StatusOK
}

// buildSnapshots groups window rows into per-site snapshots and fills in
// cross-site agreement for tenant-wide tests.
func buildSnapshots(rows []store.WindowRow, paths, healthy map[string]store.SitePath) (map[string]SiteSnapshot, map[string]string) {
	type key struct{ tenant, test string }
	sitesPerTest := map[key][]Status{}
	statuses := make([]Status, len(rows))
	for i, r := range rows {
		statuses[i] = Classify(r)
		if !r.SiteScoped {
			k := key{r.TenantID, r.TestID}
			sitesPerTest[k] = append(sitesPerTest[k], statuses[i])
		}
	}
	snaps := map[string]SiteSnapshot{}
	tenants := map[string]string{}
	for i, r := range rows {
		th := TestHealth{TestID: r.TestID, Kind: r.Kind, Target: r.Target, Status: statuses[i], ErrorClass: r.ErrorClass}
		if !r.SiteScoped {
			all := sitesPerTest[key{r.TenantID, r.TestID}]
			th.OtherSitesTotal = len(all) - 1
			for _, st := range all {
				if affected(st) {
					th.OtherSitesAffected++
				}
			}
			if affected(statuses[i]) {
				th.OtherSitesAffected--
			}
		}
		s := snaps[r.SiteID]
		s.Tests = append(s.Tests, th)
		snaps[r.SiteID] = s
		tenants[r.SiteID] = r.TenantID
	}
	for site, s := range snaps {
		sort.Slice(s.Tests, func(i, j int) bool { return s.Tests[i].TestID < s.Tests[j].TestID })
		if p, ok := paths[site]; ok {
			s.Path = &Path{Target: p.Target, Reached: p.Success, Hops: p.Hops}
		}
		if p, ok := healthy[site]; ok {
			s.ISPASN = firstPublicASN(p.Hops)
		}
		snaps[site] = s
	}
	return snaps, tenants
}

func firstPublicASN(hops []wire.Hop) int {
	for _, h := range hops {
		if h.Scope == "public" && h.ASN != 0 {
			return h.ASN
		}
	}
	return 0
}
