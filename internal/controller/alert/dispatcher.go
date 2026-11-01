package alert

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// Dispatcher sends queued deliveries with retries.
type Dispatcher struct {
	Store  Store
	Sender Sender
	Log    *slog.Logger
	now    func() time.Time
}

// retrySchedule is the wait before each retry; after the last one the
// delivery is marked failed.
var retrySchedule = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour}

const (
	claimLease  = 2 * time.Minute
	claimBatch  = 20
	sendTimeout = 30 * time.Second
)

// Run polls the outbox until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := d.Drain(ctx); err != nil && ctx.Err() == nil {
			d.Log.Error("delivering notifications", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Drain sends every due delivery and returns how many were attempted.
func (d *Dispatcher) Drain(ctx context.Context) (int, error) {
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	total := 0
	for {
		batch, err := d.Store.ClaimDeliveries(ctx, now().UTC(), claimLease, claimBatch)
		if err != nil || len(batch) == 0 {
			return total, err
		}
		for _, del := range batch {
			total++
			var p Payload
			sendErr := json.Unmarshal(del.Payload, &p)
			if sendErr == nil {
				sctx, cancel := context.WithTimeout(ctx, sendTimeout)
				sendErr = d.Sender.Send(sctx, del.Channel, p)
				cancel()
			}
			var retryAt time.Time
			if sendErr != nil && !errors.Is(sendErr, ErrPermanent) && del.Attempts <= len(retrySchedule) {
				retryAt = now().UTC().Add(retrySchedule[del.Attempts-1])
			}
			if sendErr != nil {
				d.Log.Warn("notification delivery failed", "delivery_id", del.ID, "channel", del.Channel.Name,
					"kind", del.Channel.Kind, "attempt", del.Attempts, "will_retry", !retryAt.IsZero(), "error", sendErr)
			}
			if err := d.Store.CompleteDelivery(ctx, del.ID, now().UTC(), sendErr, retryAt); err != nil {
				return total, err
			}
		}
	}
}
