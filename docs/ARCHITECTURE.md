# Architecture

PathLedger has two binaries and one database.

| Component | Runs at | Role |
| --- | --- | --- |
| `pathledger-agent` | Each customer branch | Runs probes on schedule, buffers results, uploads them |
| `pathledger-controller` | PathLedger cloud (or customer-hosted) | Authenticates agents, serves test plans, stores and serves results |
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

## Scaling notes

* 1,000 agents × 10 tests every 60 s ≈ 170 inserts/s — comfortably within a
  single PostgreSQL instance using `COPY`.
* The controller holds no state; run several replicas behind a load
  balancer. Migrations are serialised by an advisory lock.
* Next steps for scale: TimescaleDB compression and retention policies,
  continuous aggregates for dashboards, and a queue in front of ingest if
  write bursts outgrow the database.
