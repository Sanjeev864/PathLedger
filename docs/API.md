# PathLedger API v1

All endpoints speak JSON over HTTPS. Times are RFC 3339 in UTC. Every
response carries an `X-Request-ID` header; quote it when reporting problems.

## Authentication

| Credential | Prefix | Used by | Header |
| --- | --- | --- | --- |
| Session cookie | `pls_` | People using the dashboard | `pathledger_session` cookie (HttpOnly, Secure, SameSite=Strict) plus `X-CSRF-Token` on every non-GET request |
| API key | `plk_` | Scripts and integrations (acts as `admin`) | `Authorization: Bearer plk_...` |
| Enrolment token | `ple_` | Agent, once, to enrol | Sent in the enrol request body |
| Agent token | `pla_` | Agent, for every call after enrolment | `Authorization: Bearer pla_...` |

API keys are created with `pathledger-controller tenant create` or
`pathledger-controller apikey create`. Every credential is shown once; the
controller stores only its SHA-256 hash.

### Roles

Every management endpoint requires a minimum role in the current
organisation (tenant):

| Role | Can |
| --- | --- |
| `viewer` | Read everything: sites, agents, tests, results, events, channels, members |
| `admin` | Also create, change and delete sites, tests, channels, API keys, agents; invite and manage viewers and admins |
| `owner` | Also invite, promote, demote and remove owners. An organisation always keeps at least one owner |

A request below the required role gets `403 forbidden`.

### Sessions and people

| Method | Path | Auth | Body | Notes |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/auth/login` | none | `{"email", "password", "tenant_id"?}` | Sets the session cookie; returns the `me` object including `csrf_token`. Rate-limited per IP and per email |
| `GET` | `/api/v1/auth/me` | any | | `{"auth": "session"|"api_key", "user", "tenant_id", "role", "csrf_token", "memberships": [{"tenant_id", "tenant_name", "role"}]}` |
| `POST` | `/api/v1/auth/logout` | session | | `204` |
| `POST` | `/api/v1/auth/switch` | session | `{"tenant_id"}` | Moves the session to another organisation the user belongs to (MSPs) |
| `POST` | `/api/v1/auth/password` | session | `{"current_password", "new_password"}` | Ends all the user's sessions |
| `GET` | `/api/v1/members` | viewer | | `{"items": [{"user_id", "email", "name", "role", "last_login_at"}]}` |
| `PATCH` | `/api/v1/members/{user_id}` | admin | `{"role"}` | `409 last_owner` if it would leave no owner |
| `DELETE` | `/api/v1/members/{user_id}` | admin | | Ends their sessions in this organisation |
| `GET` | `/api/v1/invitations` | admin | | Pending invitations |
| `POST` | `/api/v1/invitations` | admin | `{"email", "role"}` | `201 {"token", "link", "emailed"}`; valid 7 days; emailed when SMTP and `PATHLEDGER_PUBLIC_URL` are set |
| `DELETE` | `/api/v1/invitations/{id}` | admin | | `204` |
| `GET` | `/api/v1/invitations/token/{token}` | none | | What the invitation is for, and whether the email already has an account |
| `POST` | `/api/v1/invitations/token/{token}/accept` | none | `{"name", "password"}` | New users choose a password (10–128 characters); existing users enter theirs. The account email must match the invitation. Signs the user in |

Passwords are stored as argon2id hashes (19 MiB, 2 iterations). Sign-in
failures return the same message and take the same time whether or not
the email exists.

## Errors

Non-2xx responses share one shape:

```json
{"error": {"code": "validation_failed", "message": "target: must be host:port"}}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_json`, `invalid_query`, `invalid_encoding` |
| 401 | `unauthorized`, `invalid_token`, `invalid_credentials` |
| 403 | `forbidden` (role too low), `csrf_failed`, `no_tenant` |
| 429 | `rate_limited` (see `Retry-After`) |
| 404 | `not_found` (also returned for resources owned by another tenant) |
| 409 | `conflict` |
| 413 | `body_too_large`, `batch_too_large` |
| 415 | `unsupported_encoding` |
| 422 | `validation_failed` |
| 500 | `internal_error` (message includes the request ID) |
| 503 | `not_ready` |

Request bodies are decoded strictly: unknown fields are rejected.

## Test definitions

| Field | Type | Rules |
| --- | --- | --- |
| `kind` | string | `icmp`, `dns`, `tcp`, `tls`, `http`, `traceroute` |
| `target` | string | icmp/traceroute: host or IP · dns: domain name · tcp/tls: `host:port` · http: `http(s)://` URL without credentials |
| `interval_seconds` | int | 10–3600 |
| `timeout_millis` | int | 100–30000, and shorter than the interval |
| `params.count` | int | icmp: 1–20 echo requests per run (default 5) · traceroute: 1–5 probes per hop (default 3) |
| `params.max_hops` | int | traceroute only, 1–32 (default 30) |
| `params.resolver` | string | dns only, `ip:port` of the server to query (default: system resolver) |
| `params.expect_status` | int | http only; exact status treated as success (default: any 2xx/3xx) |

Traceroute tests run at most once every 60 seconds, and for them
`timeout_millis` is the wait per probe: `max_hops × count × timeout_millis`
must be shorter than the interval.

## Rate limits

Token buckets per credential: agents 1 request/s (burst 20), API keys
10 requests/s (burst 50), enrolment 1 request per 5 s per client IP
(burst 10). Exceeding a limit returns `429` with `Retry-After` in seconds.

## Agent API

### `POST /api/v1/agent/enroll`

Exchanges a single-use enrolment token for an agent credential. No auth header.

```json
{"token": "ple_...", "hostname": "branch-pi", "agent_version": "v0.1.0", "os": "linux", "arch": "arm64"}
```

`201 Created`:

```json
{"agent_id": "…", "agent_token": "pla_…", "tenant_id": "…", "site_id": "…"}
```

`401 invalid_token` if the token is unknown, expired or already used.

### `GET /api/v1/agent/plan`

Returns the enabled tests for the agent's site plus tenant-wide tests. Send
`If-None-Match` with the previous `ETag` to receive `304 Not Modified` when
nothing changed.

```json
{"tests": [{"id": "…", "kind": "tcp", "target": "upi.example:443", "interval_seconds": 30,
            "timeout_millis": 2000, "params": {}}],
 "generated_at": "2026-11-01T10:00:00Z",
 "rotate_credential": true,
 "update": {"version": "v0.3.0"}}
```

`rotate_credential` appears when the agent's credential is older than
`PATHLEDGER_AGENT_TOKEN_MAX_AGE` (default 30 days) or an admin asked for a
rotation. `update` appears when the controller distributes a signed release;
it is only a hint — the agent verifies the signed manifest itself. The
controller records the version from the agent's `User-Agent`
(`PathLedger-Agent/<version> (os/arch)`).

### `POST /api/v1/agent/credential`

Returns a new credential, `{"agent_token": "pla_…"}`. The agent saves it,
then uses it; the first request made with it retires the old credential. Until
then the old credential keeps working, so an agent that crashes before saving
the new one is not locked out. Calling again replaces an unused new
credential.

### Agent updates

| Path | Response |
| --- | --- |
| `GET /api/v1/agent/update/manifest.json` | The signed release manifest, byte for byte |
| `GET /api/v1/agent/update/manifest.json.sig` | Base64 ed25519 signature over those bytes |
| `GET /api/v1/agent/update/files/{name}` | A binary listed in the manifest |

All three require an agent credential and return 404 when no release is
published. See [DEPLOYMENT.md](DEPLOYMENT.md#agent-updates) for the trust model.

### `POST /api/v1/agent/results`

Uploads up to 500 results. Send `Content-Encoding: gzip` to save bandwidth
(compressed limit 2 MiB, decompressed 8 MiB).

```json
{"results": [{
  "test_id": "…", "time": "2026-11-01T10:00:03Z", "kind": "icmp", "target": "1.1.1.1",
  "success": true, "latency_ms": 18.4, "loss_pct": 0, "jitter_ms": 1.2,
  "details": {"sent": "5", "received": "5", "min_ms": "16.900", "max_ms": "21.300", "remote_ip": "1.1.1.1"}
}]}
```

Traceroute results also carry `hops`: `[{"ttl", "addr", "sent", "received",
"avg_ms", "min_ms", "max_ms", "unreachable"}]`. The controller adds `scope`
(`lan`, `cgnat`, `public`, …), `asn` and `as_name` to each hop when it stores
the result.

Failed measurements set `"success": false`, an `error_class` (`timeout`, `dns`,
`refused`, `unreachable`, `tls`, `http_status`, `loss`, `config`, `other`) and
an `error` message.

Response: `{"accepted": 1, "rejected": 0}`. A result is rejected (not retried)
if its test is not assigned to the agent's site, it fails validation, or its
timestamp is more than 5 minutes in the future or 30 days in the past.

## Management API (session or API key)

### Sites

| Method | Path | Body / query | Response |
| --- | --- | --- | --- |
| `GET` | `/api/v1/sites` | | `{"items": [Site]}` |
| `POST` | `/api/v1/sites` | `{"name": "Jayanagar", "location": "Bengaluru"}` | `201` Site; `409` if the name exists |
| `GET` | `/api/v1/sites/{id}` | | Site |
| `POST` | `/api/v1/sites/{id}/enrollment-tokens` | optional `{"ttl_hours": 24}` (1–168) | `201 {"id", "site_id", "expires_at", "token"}` |

### Agents

| Method | Path | Response |
| --- | --- | --- |
| `GET` | `/api/v1/agents` | `{"items": [{"id", "site_id", "hostname", "version", "os", "arch", "created_at", "last_seen_at", "revoked_at"}]}` |
| `DELETE` | `/api/v1/agents/{id}` | `204`; the agent's credential stops working immediately |
| `POST` | `/api/v1/agents/{id}/rotate-credential` | `202`; the agent replaces its credential at its next plan fetch (within about a minute) |

### Tests

| Method | Path | Body | Response |
| --- | --- | --- | --- |
| `GET` | `/api/v1/tests` | | `{"items": [Test]}` |
| `POST` | `/api/v1/tests` | Test definition plus optional `"site_id"` (omit or `null` for every site) | `201` Test |
| `PATCH` | `/api/v1/tests/{id}` | any of `target`, `interval_seconds`, `timeout_millis`, `params`, `enabled` | Test |
| `DELETE` | `/api/v1/tests/{id}` | | `204`; historical results are kept |

### Results

`GET /api/v1/results?site_id=&test_id=&from=&to=&limit=`

* `from` / `to`: RFC 3339; default is the last hour; maximum window 31 days.
* `limit`: 1–10000, default 1000. Newest first.

```json
{"items": [{"test_id": "…", "site_id": "…", "agent_id": "…", "time": "…", "kind": "http",
            "target": "https://billing.example.com/health", "success": true, "latency_ms": 84.2,
            "details": {"status": "200", "dns_ms": "3.100", "connect_ms": "12.400", "tls_ms": "31.000", "ttfb_ms": "70.200"}}]}
```

### Series (for charts)

`GET /api/v1/series?site_id=&test_id=&window=24h` — results aggregated into
buckets (1 min up to 3 h, 5 min up to 24 h, 30 min up to 7 days, 2 h beyond):
`{"step_seconds": 300, "from", "to", "items": [{"t", "n", "fail", "latency_ms", "latency_p95_ms", "loss_pct"}]}`.

### Events

PathLedger evaluates every site each minute and keeps at most one open
event per site. `verdict` is one of `local`, `isp`, `upstream`, `dns`, `app`,
`offline` or `unknown`.

| Method | Path | Query | Response |
| --- | --- | --- | --- |
| `GET` | `/api/v1/events` | `state` (`open`/`resolved`), `site_id`, `limit` (≤1000) | `{"items": [Event]}` newest first |
| `GET` | `/api/v1/events/{id}` | | `{"id", "site_id", "state", "verdict", "summary", "evidence": [], "affected_tests": [], "started_at", "updated_at", "resolved_at"}` |

### Service reports

`GET /api/v1/reports/sla?month=2026-10&tz=Asia/Kolkata` — the monthly
service-level report the dashboard prints under **Reports**. `month` defaults
to the current month (reported up to now, with `"partial": true`); `tz` is an
IANA zone (default `UTC`) that sets the month boundaries. Months in the
future or more than five years back return 400.

Per site: `monitored_minutes` (from the later of the month start and the
site's creation), `impaired_minutes` (incidents with verdict `local`, `isp`,
`upstream`, `dns`, `offline` or `unknown`, clipped to the month — `app`
incidents do not count), `isp_minutes`, `availability_pct` (1 − impaired /
monitored, floored to three decimals), `incidents`, `longest_incident_minutes`,
`by_verdict` and `tests[]` with `samples`, `success_pct`, latency
`p50/p95/p99` of successful probes, `loss_avg_pct` and `coverage_pct`
(received ÷ expected samples). Traceroutes are excluded from `tests`.
`incidents[]` lists every overlapping incident (first 2000;
`incidents_truncated` says when more exist) with its `minutes` inside the
month.

### Notification channels

| Method | Path | Body | Response |
| --- | --- | --- | --- |
| `GET` | `/api/v1/channels` | | `{"items": [Channel]}` (webhook secrets masked) |
| `POST` | `/api/v1/channels` | `{"kind": "email", "name", "to": ["a@b.c"]}` · `{"kind": "telegram", "name", "chat_id"}` · `{"kind": "webhook", "name", "url": "https://…"}` | `201` Channel; for webhooks `config.secret` is shown this once |
| `POST` | `/api/v1/channels/{id}/test` | | `202 {"queued": 1}` |
| `DELETE` | `/api/v1/channels/{id}` | | `204` |
| `GET` | `/api/v1/deliveries` | | last 100 deliveries with attempts, errors and outcome |

Webhooks receive `{"type": "event.opened" | "event.verdict_changed" | "event.resolved" | "test", "site_name", "event", "link", "created_at"}`
with header `X-PathLedger-Signature: sha256=<hex HMAC-SHA256 of the body using the channel secret>`.
Failed deliveries are retried after 1, 5, 15, 60 and 180 minutes; 4xx
responses fail immediately. Webhook URLs that resolve to private or
reserved addresses are refused.

### API keys

| Method | Path | Body | Response |
| --- | --- | --- | --- |
| `GET` | `/api/v1/api-keys` | | `{"items": [{"id", "name", "prefix", "created_at", "last_used_at", "revoked_at"}]}` |
| `POST` | `/api/v1/api-keys` | `{"name": "dashboard"}` | `201 {"key": {...}, "token": "plk_…"}` (shown once) |
| `DELETE` | `/api/v1/api-keys/{id}` | | `204`; `409` if it is the key making the request |

## Operations

| Method | Path | Meaning |
| --- | --- | --- |
| `GET` | `/healthz` | Process is up |
| `GET` | `/readyz` | Database reachable; use for load-balancer health checks |
