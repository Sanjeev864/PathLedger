-- Rolling per-test, per-site baselines (refreshed hourly from 7 days of data).
CREATE TABLE baselines (
    tenant_id   uuid NOT NULL,
    site_id     uuid NOT NULL,
    test_id     uuid NOT NULL,
    samples     integer NOT NULL,
    latency_p50 double precision,
    latency_mad double precision,
    loss_avg    double precision NOT NULL DEFAULT 0,
    computed_at timestamptz NOT NULL,
    PRIMARY KEY (site_id, test_id)
);

-- Site-level incidents. At most one open event per site; its verdict and
-- evidence are refined while it is open.
CREATE TABLE events (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL,
    site_id        uuid NOT NULL,
    state          text NOT NULL CHECK (state IN ('open', 'resolved')),
    verdict        text NOT NULL,
    summary        text NOT NULL,
    evidence       jsonb NOT NULL DEFAULT '[]',
    affected_tests jsonb NOT NULL DEFAULT '[]',
    started_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    resolved_at    timestamptz,
    clear_count    integer NOT NULL DEFAULT 0,
    FOREIGN KEY (tenant_id, site_id) REFERENCES sites (tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX events_one_open_per_site ON events (site_id) WHERE state = 'open';
CREATE INDEX events_tenant_started_idx ON events (tenant_id, started_at DESC);

CREATE TABLE analytics_state (
    id                    boolean PRIMARY KEY DEFAULT true CHECK (id),
    baselines_refreshed_at timestamptz
);
INSERT INTO analytics_state DEFAULT VALUES;
