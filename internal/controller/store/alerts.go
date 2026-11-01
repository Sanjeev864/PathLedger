package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Channel is a destination for event notifications. Config holds
// kind-specific settings: {"to": ["a@b.c"]} for email, {"chat_id": "..."}
// for Telegram, {"url": "...", "secret": "..."} for webhooks.
type Channel struct {
	ID        string         `json:"id"`
	TenantID  string         `json:"tenant_id"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Config    map[string]any `json:"config"`
	Enabled   bool           `json:"enabled"`
	CreatedAt time.Time      `json:"created_at"`
}

const channelColumns = "id, tenant_id, kind, name, config, enabled, created_at"

func scanChannel(r pgx.CollectableRow) (Channel, error) {
	var c Channel
	err := r.Scan(&c.ID, &c.TenantID, &c.Kind, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt)
	return c, err
}

// CreateChannel stores a validated channel.
func (s *Store) CreateChannel(ctx context.Context, c Channel) (Channel, error) {
	rows, err := s.pool.Query(ctx,
		"INSERT INTO channels (tenant_id, kind, name, config) VALUES ($1, $2, $3, $4) RETURNING "+channelColumns,
		c.TenantID, c.Kind, c.Name, c.Config)
	if err != nil {
		return Channel{}, mapErr(err)
	}
	out, err := pgx.CollectExactlyOneRow(rows, scanChannel)
	return out, mapErr(err)
}

// ListChannels lists a tenant's channels.
func (s *Store) ListChannels(ctx context.Context, tenantID string) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+channelColumns+" FROM channels WHERE tenant_id = $1 ORDER BY created_at", tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanChannel)
}

// GetChannel returns one channel.
func (s *Store) GetChannel(ctx context.Context, tenantID, id string) (Channel, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+channelColumns+" FROM channels WHERE tenant_id = $1 AND id = $2", tenantID, id)
	if err != nil {
		return Channel{}, mapErr(err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanChannel)
	return c, mapErr(err)
}

// DeleteChannel removes a channel and its pending deliveries.
func (s *Store) DeleteChannel(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM channels WHERE tenant_id = $1 AND id = $2", tenantID, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnqueueDeliveries queues payload for every enabled channel of a tenant,
// or only for channelID when it is non-empty. It returns the number queued.
func (s *Store) EnqueueDeliveries(ctx context.Context, tenantID, channelID string, payload any) (int, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO deliveries (tenant_id, channel_id, payload)
		SELECT tenant_id, id, $3 FROM channels
		WHERE tenant_id = $1 AND enabled AND ($2::uuid IS NULL OR id = $2::uuid)`,
		tenantID, nullIfEmpty(channelID), b)
	if err != nil {
		return 0, mapErr(err)
	}
	return int(tag.RowsAffected()), nil
}

// Delivery is a queued notification with its channel.
type Delivery struct {
	ID       string
	Attempts int
	Payload  json.RawMessage
	Channel  Channel
}

// ClaimDeliveries locks up to limit due deliveries and pushes their next
// attempt time forward by lease, so a crashed sender's work is retried and
// two replicas never send the same message at once.
func (s *Store) ClaimDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM deliveries
			WHERE delivered_at IS NULL AND failed_at IS NULL AND next_attempt_at <= $1
			ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE deliveries d SET next_attempt_at = $1::timestamptz + $2::interval, attempts = d.attempts + 1
		FROM due, channels c WHERE d.id = due.id AND c.id = d.channel_id
		RETURNING d.id, d.attempts, d.payload, c.id, c.tenant_id, c.kind, c.name, c.config, c.enabled, c.created_at`,
		now, lease, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		c := &d.Channel
		err := r.Scan(&d.ID, &d.Attempts, &d.Payload, &c.ID, &c.TenantID, &c.Kind, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt)
		return d, err
	})
}

// CompleteDelivery records the outcome of an attempt. On failure, retryAt
// schedules the next attempt; a zero retryAt marks it permanently failed.
func (s *Store) CompleteDelivery(ctx context.Context, id string, now time.Time, sendErr error, retryAt time.Time) error {
	switch {
	case sendErr == nil:
		_, err := s.pool.Exec(ctx, "UPDATE deliveries SET delivered_at = $2, last_error = '' WHERE id = $1", id, now)
		return err
	case retryAt.IsZero():
		_, err := s.pool.Exec(ctx, "UPDATE deliveries SET failed_at = $2, last_error = $3 WHERE id = $1", id, now, clipErr(sendErr))
		return err
	default:
		_, err := s.pool.Exec(ctx, "UPDATE deliveries SET next_attempt_at = $2, last_error = $3 WHERE id = $1", id, retryAt, clipErr(sendErr))
		return err
	}
}

// DeliveryStatus is a delivery as shown to users.
type DeliveryStatus struct {
	ID          string     `json:"id"`
	ChannelID   string     `json:"channel_id"`
	Attempts    int        `json:"attempts"`
	DeliveredAt *time.Time `json:"delivered_at"`
	FailedAt    *time.Time `json:"failed_at"`
	LastError   string     `json:"last_error"`
	CreatedAt   time.Time  `json:"created_at"`
}

// ListDeliveries shows a tenant's recent deliveries, newest first.
func (s *Store) ListDeliveries(ctx context.Context, tenantID string, limit int) ([]DeliveryStatus, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, channel_id, attempts, delivered_at, failed_at, last_error, created_at
		FROM deliveries WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeliveryStatus, error) {
		var d DeliveryStatus
		err := r.Scan(&d.ID, &d.ChannelID, &d.Attempts, &d.DeliveredAt, &d.FailedAt, &d.LastError, &d.CreatedAt)
		return d, err
	})
}

func clipErr(err error) string {
	s := err.Error()
	if len(s) > 1000 {
		s = s[:1000]
	}
	return s
}
