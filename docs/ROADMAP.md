# Roadmap and status

Last updated: 2026-11-01 (Milestone 2).

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
| Dockerfile, Compose stack, CI workflow | Done | GitHub Actions: images build, Compose stack serves `/readyz`, tenant creation works |
| Hardened systemd unit for agents | Done | `systemd-analyze verify`; not yet run on a real device |
| README, architecture, API, deployment docs | Done | — |

## Milestone 2 — Insight ✅ complete (MVP)

| Feature | Status | Verified by |
| --- | --- | --- |
| Paris traceroute probe (constant 5-tuple, per-hop loss/RTT, unreachable codes) | Done | Unit tests with a simulated network; real run over loopback; fault lab |
| Hop scope (LAN / CGNAT / public) and IP → AS attribution (free IPtoASN data) | Done | Unit and ingest tests; fault lab |
| Rolling baselines (7-day median + MAD) and per-test status | Done | Unit and database tests |
| Explainable verdicts: local, ISP, upstream, DNS, app, offline | Done | Rule tests; engine tests against PostgreSQL; fault lab |
| Site events with hysteresis; events API | Done | Engine and API tests |
| Alerts: email, Telegram, HMAC-signed webhooks via a retrying outbox; SSRF guard | Done | End-to-end delivery tests (fake SMTP, Telegram and webhook servers) |
| Rate limiting; `PATCH` tests; API key list/create/revoke | Done | API tests |
| Aggregated series endpoint for charts | Done | API test |
| Web dashboard (Svelte 5 + ECharts) embedded in the controller | Done | Type-checked; built in CI and Docker |
| Fault-injection lab (network namespaces, real controller + agent) | Done | Runs in CI; latest results in [lab/results.md](../lab/results.md) |
| Campus pilot | **Not started** | Needs written permission from the college IT department |

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
| Analytics | Packet loss on the ISP link shows up only on ICMP tests (TCP/HTTP retransmit and still succeed), so a lossy-but-up ISP link can be reported as a single-destination `app` problem. Latency-only degradation also depends on 30+ baseline samples and is not yet covered by the lab. | Correlate loss with traceroute hop loss and add loss/latency scenarios to the lab (M3). |
| Analytics | Baselines are one 7-day median per test, not per hour of week, so busy-hour slowdowns that are "normal" may alert. | Hour-of-week buckets once pilots provide data. |
| Analytics | A traceroute is at most 60 s old, so `local`/`upstream` verdicts can start as `isp` and be refined within a minute (the lab measures this). | Trigger an on-demand traceroute when a site turns unhealthy (M3). |
| Agents | Not yet run on a Raspberry Pi or OpenWrt router; only cross-compiled. ICMP and traceroute are IPv4-only. | Device test and ICMPv6 in M3. |
| Database | TimescaleDB path untested; no retention policy. | M3. |
| Dashboard | Signs in with an API key held in the browser tab; no user accounts yet. Bundle is ~200 KB gzipped (mostly ECharts). | User accounts and roles in M3. |
| Rate limits | Kept in memory per controller replica. | Shared limits only if several replicas are run. |
| DNS probe | Reports answers and timing, not RCODE/TTL/DNSSEC. | Raw DNS client if customers need it. |
| Product | No licence chosen. | Owner decision. |
