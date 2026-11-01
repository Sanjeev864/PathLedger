-- Notification channels and a transactional outbox of deliveries.
CREATE TABLE channels (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('email', 'telegram', 'webhook')),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    config     jsonb NOT NULL,
    enabled    boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX channels_tenant_idx ON channels (tenant_id);

CREATE TABLE deliveries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    channel_id      uuid NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    payload         jsonb NOT NULL,
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    delivered_at    timestamptz,
    failed_at       timestamptz,
    last_error      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deliveries_due_idx ON deliveries (next_attempt_at) WHERE delivered_at IS NULL AND failed_at IS NULL;
