# PathLedger API v1

All endpoints speak JSON over HTTPS. Times are RFC 3339 in UTC. Every
response carries an `X-Request-ID` header; quote it when reporting problems.

## Authentication

| Credential | Prefix | Used by | Header |
| --- | --- | --- | --- |
| API key | `plk_` | Dashboard, scripts, CLI tools | `Authorization: Bearer plk_...` |
| Enrolment token | `ple_` | Agent, once, to enrol | Sent in the enrol request body |
| Agent token | `pla_` | Agent, for every call after enrolment | `Authorization: Bearer pla_...` |

API keys are created with `pathledger-controller tenant create` or
`pathledger-controller apikey create`. Every credential is shown once; the
controller stores only its SHA-256 hash.

## Errors

Non-2xx responses share one shape:

```json
{"error": {"code": "validation_failed", "message": "target: must be host:port"}}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_json`, `invalid_query`, `invalid_encoding` |
| 401 | `unauthorized`, `invalid_token` |
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
| `kind` | string | `icmp`, `dns`, `tcp`, `tls`, `http` |
| `target` | string | icmp: host or IP · dns: domain name · tcp/tls: `host:port` · http: `http(s)://` URL without credentials |
| `interval_seconds` | int | 10–3600 |
| `timeout_millis` | int | 100–30000, and shorter than the interval |
| `params.count` | int | icmp only, 1–20 echo requests per run (default 5) |
| `params.resolver` | string | dns only, `ip:port` of the server to query (default: system resolver) |
| `params.expect_status` | int | http only; exact status treated as success (default: any 2xx/3xx) |

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
 "generated_at": "2026-11-01T10:00:00Z"}
```

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

Failed measurements set `"success": false`, an `error_class` (`timeout`, `dns`,
`refused`, `unreachable`, `tls`, `http_status`, `loss`, `config`, `other`) and
an `error` message.

Response: `{"accepted": 1, "rejected": 0}`. A result is rejected (not retried)
if its test is not assigned to the agent's site, it fails validation, or its
timestamp is more than 5 minutes in the future or 30 days in the past.

## Management API (API key)

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

### Tests

| Method | Path | Body | Response |
| --- | --- | --- | --- |
| `GET` | `/api/v1/tests` | | `{"items": [Test]}` |
| `POST` | `/api/v1/tests` | Test definition plus optional `"site_id"` (omit or `null` for every site) | `201` Test |
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

## Operations

| Method | Path | Meaning |
| --- | --- | --- |
| `GET` | `/healthz` | Process is up |
| `GET` | `/readyz` | Database reachable; use for load-balancer health checks |
