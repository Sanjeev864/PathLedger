# Architecture

PathLedger has two binaries and one database.

| Component | Runs at | Role |
| --- | --- | --- |
| `pathledger-agent` | Each customer branch | Runs probes on schedule, buffers results, uploads them |
| `pathledger-controller` | PathLedger cloud (or customer-hosted) | Authenticates agents, serves test plans, stores results, evaluates sites, sends alerts, serves the dashboard |
| PostgreSQL 14+ | Next to the controller | Relational data and the results time series |

## Design principles

1. **The agent is a guest on someone else's network.** Outbound HTTPS only,
   no listening ports, no traffic inspection, bounded CPU, memory, disk and
   bandwidth. Probe intervals have a 10 s floor; HTTP bodies are capped at
   256 KiB; the spool has a size cap.
2. **A failed measurement is data.** Probes never return Go errors; they
   return a `wire.Result` with `success: false`, an `error_class` and timing
   details, so the controller can attribute faults.
3. **One protocol definition.** `pkg/wire` holds every type and validation
   rule shared by agent and controller, so they cannot drift.
4. **Tenant isolation is enforced twice.** Every query takes the tenant ID
   from the authenticated credential, and composite foreign keys on
   `(tenant_id, site_id)` make cross-tenant references impossible in the
   database itself.
5. **Few dependencies.** Runtime dependencies are `pgx` and the Go standard
   library. ICMP packets are built and parsed in-house.

## Agent

```
plan loop ──(every 60 s, ETag)──► Scheduler.Apply ──► one goroutine per test
                                                      │ jittered start, fixed cadence,
                                                      │ semaphore caps concurrent probes
                                                      ▼
                                              probe.Runner.Run
                                                      ▼
upload loop ◄──(every 15 s)── Spool (JSON-lines segments, fsync, size cap)
     │
     └─► POST /agent/results (gzip) ── ack ──► segment deleted
```

* **Enrolment**: first start exchanges `PATHLEDGER_ENROLL_TOKEN` for an
  agent credential saved to `<state-dir>/agent.json` with mode 0600.
* **Scheduler**: reconciles running tests with each new plan (start new, stop
  removed, restart changed). Start times are randomised within the interval so
  fleets do not fire in lockstep; an overrunning probe skips missed slots
  rather than firing back-to-back.
* **Spool**: append-only JSON-lines segment files of at most 500 results
  (one upload batch). Torn lines from a crash are skipped. When the size cap
  is hit, the oldest segments are dropped first and the loss is logged.
* **Uploader**: retries with exponential backoff and jitter (max 5 min). A
  batch the controller permanently rejects (4xx) is discarded so it cannot
  block newer data. A `401` stops the agent: its credential was revoked.

### Probes

| Kind | Measures | Key details reported |
| --- | --- | --- |
| `icmp` | Echo train (default 5): avg RTT, loss %, jitter | `sent`, `received`, `min_ms`, `max_ms`, `socket` |
| `dns` | Resolution time via system or a chosen resolver | `answers`, `resolver` |
| `tcp` | Three-way handshake time (DNS timed separately) | `dns_ms`, `connect_ms`, `remote_ip` |
| `tls` | Handshake time after connect | `tls_version`, `cipher`, `cert_days_left`, `cert_issuer` |
| `http` | Full GET with per-phase timing; redirects not followed | `status`, `dns_ms`, `connect_ms`, `tls_ms`, `ttfb_ms`, `bytes` |
| `traceroute` | Forward path, hop by hop | `hops[]` with per-hop loss and RTT |

**Traceroute** is Paris-style: every probe in a run shares one UDP 5-tuple
(source port, destination port 33434), so ECMP load balancers hash all
probes onto the same path and the result is one real path rather than a
blend. Probes are told apart by payload length, which routers quote back in
ICMP errors but which does not affect ECMP hashing. A run stops at the
destination's port-unreachable reply, at a router's destination-unreachable
(reported as `host`, `net` or `admin`), or after five silent hops. It needs
a raw ICMP socket (root or `CAP_NET_RAW`).

ICMP prefers unprivileged datagram ping sockets and falls back to raw
sockets (root or `CAP_NET_RAW`). Raw-socket replies are matched by ICMP ID,
sequence number, source address and a random 8-byte payload nonce.

## Controller

* `internal/controller/api` — Go 1.22 `net/http` routing. Middleware: request
  ID, panic recovery, access log (never logs headers), security headers,
  credential auth. Bodies are size-limited and strictly decoded.
* `internal/controller/store` — pgx pool, embedded SQL migrations applied
  under a PostgreSQL advisory lock (safe with several replicas), result
  ingest via `COPY`.
* `internal/controller/auth` — 256-bit random credentials; SHA-256 hashes
  stored and looked up by unique index.

### Data model

| Table | Purpose |
| --- | --- |
| `tenants` | Customer accounts |
| `api_keys` | Management credentials (hash, prefix, last used, revoked) |
| `sites` | Branches |
| `enrollment_tokens` | Single-use, expiring, site-bound |
| `agents` | Enrolled agents (hash, version, last seen, revoked) |
| `tests` | Test definitions; `site_id NULL` means every site |
| `results` | Time series, indexed by tenant, site and test over time |

`results` has no foreign keys so ingest stays cheap and the table can become
a TimescaleDB hypertable (migration `0002` does this automatically when the
extension is installed and preloaded).

### Ingest rules

A result is stored only if its test is currently assigned to the agent's
site, it passes `wire.Result.Validate`, and its timestamp is no more than
5 minutes in the future or 30 days in the past. Others are counted as
rejected and reported back.

## Analysis: from results to verdicts

`internal/controller/analytics` runs in the controller every minute. With
several replicas, a PostgreSQL advisory lock makes exactly one evaluate.

1. **Baselines** (hourly): for each site and test, the median and median
   absolute deviation (MAD) of successful latencies and the average loss
   over the last 7 days, excluding the current window.
2. **Status** per test over the last 5 minutes: `down` if at least half
   the runs failed; `degraded` if loss exceeds max(5 %, baseline + 5 %) or
   median latency exceeds the baseline median by max(4 × 1.4826 × MAD,
   50 % of baseline, 20 ms) once at least 30 baseline samples exist.
   Traceroute tests are evidence, never a status (firewalls often drop them).
3. **Verdict** per site (`analytics.Attribute`, pure and unit-tested):

| Verdict | Rule |
| --- | --- |
| `local` | The latest traceroute gets no reply from hop 1, the site's own gateway |
| `dns` | Only name resolution fails while other destinations work |
| `isp` | Two or more distinct destinations fail and none is healthy, and the path dies at the LAN gateway or inside the ISP (or no path is known) |
| `upstream` | As above, but the path's last answering hop belongs to an AS other than the site's ISP (learned from the first public hop of a healthy traceroute) |
| `app` | Some destinations fail while others from the same site are healthy; stronger when the same test fails at most other sites |
| `offline` | No agent at the site has checked in for 3 minutes |
| `unknown` | Not enough healthy reference tests to decide |

4. **Events**: at most one open event per site, refreshed each evaluation;
   resolved after two consecutive healthy evaluations. Every verdict carries
   its evidence so a customer can forward it to their ISP.

Destinations are compared by host, so an HTTP and a TCP test against the
same server count as one destination.

## Alerting

Event changes (`opened`, `verdict_changed`, `resolved`) are written to a
`deliveries` outbox in the database, one row per enabled channel. A
dispatcher claims due rows with `FOR UPDATE SKIP LOCKED` (safe across
replicas), sends them, and retries failures after 1, 5, 15, 60 and 180
minutes; 4xx-style failures are permanent. Channels: SMTP email (STARTTLS
or implicit TLS on 465), Telegram via an operator-owned bot, and webhooks
signed with per-channel HMAC-SHA256 secrets. Webhook connections are
refused at dial time if the resolved address is not public (SSRF and DNS
rebinding protection).

## Users, roles and sessions

People sign in with email and password (argon2id). A session is a random
256-bit cookie (`HttpOnly`, `Secure`, `SameSite=Strict`) whose hash is stored
with a per-session CSRF token; every state-changing request must echo the
token in `X-CSRF-Token`. Sessions slide forward while used (24 h idle) and
end on sign-out, password change or removal from the organisation.

A user can belong to many tenants with a role in each (`owner`, `admin`,
`viewer`), which is how an MSP's staff manage several customers from one
account. A session is scoped to one tenant at a time; switching re-checks
membership. API keys remain for automation and act as `admin`. People join
through single-use, 7-day invitations bound to an email address.

## Dashboard

`web/` is a Svelte 5 + TypeScript single-page app with ECharts, built by
Vite into `internal/controller/webui/dist` and embedded in the controller
binary, so a deployment is still one binary. It talks only to the same-origin
API using the session cookie (or, for automation, an API key held in
`sessionStorage`), and is served with a strict Content Security Policy. Charts use `/api/v1/series`, which aggregates in
PostgreSQL rather than shipping raw results to the browser.

## Fault-injection lab

`lab/faultlab.py` builds a five-namespace network (branch, LAN router, ISP,
transit, servers) with real routing and a lab ASN table, runs a real
controller and agent, injects faults at each segment, and records whether
and how quickly PathLedger attributes them correctly. It runs in CI and
writes `lab/results.md`.

## Scaling notes

* 1,000 agents × 10 tests every 60 s ≈ 170 inserts/s — comfortably within a
  single PostgreSQL instance using `COPY`.
* The controller holds no state; run several replicas behind a load
  balancer. Migrations are serialised by an advisory lock.
* Next steps for scale: TimescaleDB compression and retention policies,
  continuous aggregates for dashboards, and a queue in front of ingest if
  write bursts outgrow the database.
