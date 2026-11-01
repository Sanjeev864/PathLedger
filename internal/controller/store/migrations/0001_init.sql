-- Core schema. Every tenant-owned row carries tenant_id, and composite
-- foreign keys on (tenant_id, site_id) make cross-tenant references
-- impossible at the database level.

CREATE TABLE tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    prefix       text NOT NULL,
    key_hash     bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_keys_tenant_idx ON api_keys (tenant_id);

CREATE TABLE sites (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    location   text NOT NULL DEFAULT '' CHECK (length(location) <= 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, id)
);

CREATE TABLE enrollment_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    site_id    uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    FOREIGN KEY (tenant_id, site_id) REFERENCES sites (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE agents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL,
    site_id      uuid NOT NULL,
    hostname     text NOT NULL DEFAULT '',
    version      text NOT NULL DEFAULT '',
    os           text NOT NULL DEFAULT '',
    arch         text NOT NULL DEFAULT '',
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    revoked_at   timestamptz,
    FOREIGN KEY (tenant_id, site_id) REFERENCES sites (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX agents_tenant_idx ON agents (tenant_id, site_id);

CREATE TABLE tests (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    site_id          uuid,
    kind             text NOT NULL CHECK (kind IN ('icmp', 'dns', 'tcp', 'tls', 'http')),
    target           text NOT NULL CHECK (length(target) BETWEEN 1 AND 512),
    interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 10 AND 3600),
    timeout_millis   integer NOT NULL CHECK (timeout_millis BETWEEN 100 AND 30000),
    params           jsonb NOT NULL DEFAULT '{}',
    enabled          boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, site_id) REFERENCES sites (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX tests_tenant_idx ON tests (tenant_id, site_id);

-- Time series of probe results. No foreign keys, to keep ingest fast and to
-- allow conversion to a TimescaleDB hypertable; tenant_id scopes every read.
CREATE TABLE results (
    time        timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    tenant_id   uuid NOT NULL,
    site_id     uuid NOT NULL,
    agent_id    uuid NOT NULL,
    test_id     uuid NOT NULL,
    kind        text NOT NULL,
    target      text NOT NULL,
    success     boolean NOT NULL,
    latency_ms  double precision,
    loss_pct    double precision,
    jitter_ms   double precision,
    error_class text NOT NULL DEFAULT '',
    error       text NOT NULL DEFAULT '',
    details     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX results_tenant_time_idx ON results (tenant_id, time DESC);
CREATE INDEX results_test_time_idx ON results (test_id, time DESC);
CREATE INDEX results_site_time_idx ON results (site_id, time DESC);
