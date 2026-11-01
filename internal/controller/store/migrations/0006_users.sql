-- People: users, their memberships in tenants (an MSP's staff belong to
-- many), browser sessions and invitations.
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    name          text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    disabled_at   timestamptz,
    UNIQUE (email)
);

CREATE TABLE memberships (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'admin', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tenant_id)
);
CREATE INDEX memberships_tenant_idx ON memberships (tenant_id);

CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   bytea NOT NULL UNIQUE,
    csrf_token   text NOT NULL,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE invitations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    email       text NOT NULL CHECK (email = lower(email)),
    role        text NOT NULL CHECK (role IN ('owner', 'admin', 'viewer')),
    token_hash  bytea NOT NULL UNIQUE,
    invited_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    accepted_at timestamptz
);
CREATE INDEX invitations_tenant_idx ON invitations (tenant_id);
