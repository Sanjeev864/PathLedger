// Package maintenance runs periodic housekeeping: data retention.
package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
)

// Store is what the purge job needs.
type Store interface {
	WithMaintenanceLock(ctx context.Context, fn func(context.Context) error) error
	Purge(ctx context.Context, r store.Retention) (store.PurgeReport, error)
}

// Run purges expired data every interval until ctx ends. Only one replica
// works at a time.
func Run(ctx context.Context, st Store, r store.Retention, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		err := st.WithMaintenanceLock(ctx, func(ctx context.Context) error {
			rep, err := st.Purge(ctx, r)
			if err == nil && (rep.Results+rep.Events+rep.Deliveries+rep.Sessions+rep.Invitations+rep.EnrollmentTokens) > 0 {
				log.Info("expired data purged", "results", rep.Results, "events", rep.Events, "deliveries", rep.Deliveries,
					"sessions", rep.Sessions, "invitations", rep.Invitations, "enrollment_tokens", rep.EnrollmentTokens)
			}
			return err
		})
		if err != nil && !errors.Is(err, store.ErrLocked) && ctx.Err() == nil {
			log.Error("purging expired data failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
