# Roadmap and status

Last updated: 2026-11-01.

* **MVP** = Milestones 1 + 2: enough for a campus pilot that shows real ISP faults.
* **Production release** = Milestone 3: the point at which a customer can be charged.
* **Enterprise** features follow paying-customer demand.

## Milestone 1 — Foundation ✅ complete

| Feature | Status | Verified by |
| --- | --- | --- |
| Wire protocol + validation (`pkg/wire`) | Done | Unit tests |
| ICMP probe (ping socket + raw fallback), loss, jitter | Done | Unit tests, loopback test in both socket modes |
| DNS, TCP, TLS, HTTP probes with phase timing | Done | Unit tests against local servers |
| Agent enrolment, 0600 state file | Done | Agent tests, e2e |
| Scheduler (jitter, reconcile, concurrency cap) | Done | Unit tests |
| Crash-safe, size-capped spool | Done | Unit tests (torn writes, cap, restart) |
| Uploader (gzip, backoff, poison-batch handling, revocation stop) | Done | Agent tests against a fake controller |
| PostgreSQL schema, embedded migrations, advisory lock | Done | Integration tests |
| Credentials: hashed API keys, single-use enrolment tokens, agent tokens | Done | Integration + API tests (incl. concurrent enrolment) |
| Agent API and management API | Done | API tests against real PostgreSQL |
| Tenant isolation | Done | API + store tests |
| Controller CLI (`serve`, `migrate`, `tenant create`, `apikey create`) | Done | e2e |
| End-to-end script | Done | Passes locally |
| Cross-compiled agents (amd64, arm64, armv7, mips, mipsle) | Done | `make dist` |
| Dockerfile, Compose stack, systemd unit, CI workflow | Written | **Not yet executed** — see known issues |
| README, architecture, API, deployment docs | Done | — |

## Milestone 2 — Insight (next; completes the MVP)

- [ ] Paris traceroute probe with per-hop loss and RTT
- [ ] IP → ASN enrichment from free data (IPtoASN / RIR dumps)
- [ ] Per-link baselines (hour-of-week) and anomaly events
- [ ] Verdicts: local network / ISP / upstream / application
- [ ] Alerts: email (SMTP), Telegram, generic webhook
- [ ] Rate limiting per credential and per IP on enrolment
- [ ] `PATCH /api/v1/tests/{id}`, API key list/revoke endpoints
- [ ] SvelteKit dashboard: sites, link health, path view, event timeline
- [ ] containerlab + `tc netem` fault-injection lab with precision/recall report
- [ ] Campus pilot (requires permission from the college IT department)

## Milestone 3 — Production release

- [ ] User accounts (argon2id), roles, MSP multi-tenant console
- [ ] Mutual TLS for agents; signed agent auto-update; OpenWrt package
- [ ] Prometheus metrics; retention, compression and backup policies
- [ ] Monthly ISP SLA report (PDF)
- [ ] First 5–10 paying pilot sites

## Milestone 4 — Differentiation

- [ ] Cross-site and cross-customer incident correlation
- [ ] ISP scorecards and SLA evidence packs
- [ ] Public BGP feeds (RIPE RIS, RouteViews)
- [ ] Billing and self-serve signup

## Enterprise (demand-driven)

SSO/OIDC, audit-log export, on-premises controller, ISP white-label, active
failover built on PathLedger measurements.

## Known issues and limitations

| Area | Issue | Plan |
| --- | --- | --- |
| Packaging | The Dockerfile, Compose stack and GitHub Actions workflow were written but could not be run in the development environment (no Docker; repository push not yet authorised). | Run on first push; fix anything CI reports. |
| Database | The TimescaleDB migration path is untested; plain PostgreSQL is fully tested. | Test with the `timescale/timescaledb` image in M2. |
| ICMP | IPv4 only; targets resolving only to IPv6 fail with a `config` error. | Add ICMPv6 in M2. |
| DNS | Uses Go's resolver: reports answers and timing, not RCODE, TTL or DNSSEC status. | Raw DNS client in M2 if customers need it. |
| Time | Results are stamped with the agent's clock; no NTP drift check (CERT-In expects synchronised clocks). | Report clock offset in M2; reject skew > 5 min already. |
| Storage | No retention policy; `results` grows indefinitely. | Retention + compression in M3. |
| Security | No rate limiting yet. Credentials are 256-bit, so guessing is infeasible, but request floods are not throttled. | M2. |
| Product | No licence chosen. | Owner decision. |
