# Roadmap and status

Last updated: 2026-11-01 (software-only, zero-cost delivery).

**Product constraint:** PathLedger ships as pure software and costs nothing
to build, run or demonstrate. No hardware is bought or required: agents run
on computers the customer already has (Windows, macOS, Linux, cloud VMs,
Docker), and every service it uses has a free option (see
[DEPLOYMENT.md](DEPLOYMENT.md#zero-cost-deployment-options)).

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
| Agent binaries for Windows, macOS and Linux (amd64, arm64) | Done | `make dist`; installed and run as a service on all three in CI |
| Dockerfile, Compose stack, CI workflow | Done | GitHub Actions: images build, Compose stack serves `/readyz`, tenant creation works |
| Hardened systemd unit for agents | Done | Installed and run by CI on Ubuntu |
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
| Web dashboard (Svelte 5 + ECharts) embedded in the controller | Done | Type-checked; built in CI and Docker; checked in a real browser (light, dark, phone width) against lab data |
| Fault-injection lab (network namespaces, real controller + agent) | Done | Runs in CI; latest results in [lab/results.md](../lab/results.md) |
| Campus pilot | **Not started** | Needs written permission from the college IT department |

## Milestone 3 — Production release 🟡 software complete; pilots open

| Feature | Status | Verified by |
| --- | --- | --- |
| User accounts (argon2id), owner/admin/viewer roles, sessions with CSRF protection | Done | API tests; real browser (cookie flags, viewer is read-only) |
| MSP multi-tenancy: one login across customers, organisation switcher, invitations | Done | API tests; real browser |
| Monthly service report: availability, ISP-attributed time, incidents by verdict, latency percentiles, loss, data coverage; printable / save as PDF | Done | Unit tests of the arithmetic; database and API tests; checked in a real browser and printed to PDF |
| Data retention job (results, events, deliveries, expired sessions/tokens; TimescaleDB `drop_chunks`) | Done | Database test |
| Prometheus metrics on a separate listener | Done | Unit tests; scraped from a running controller |
| Agent credential rotation (30-day policy and on demand, crash-safe hand-over) | Done | Database and API tests; real agent rotated against a running controller |
| Signed agent self-update (offline ed25519 key, pinned on agents, rollback after 3 failed starts) | Done | Unit tests (tampering, wrong key, downgrade, crash loop); real v0.1.0 → v0.2.0 update with exec in place and relaunch after restart |
| Mutual TLS for agents | **Deferred** | TLS terminates at Caddy/Cloudflare in the recommended deployment, so mTLS needs certificate forwarding and a private CA; bearer tokens are hashed at rest and rotated. Enterprise option. |
| Agent on ordinary computers: Windows (system ICMP service, no admin rights for probes), macOS, Linux; service installers for each; owner-only credential storage on Windows | Done | CI on real Windows, macOS and Ubuntu machines: unit tests plus installer round-trip until ICMP, TCP and traceroute results arrive |
| Simulated demo customer with live fault injection (`demo`) | Done | Unit and database tests; live ISP fault diagnosed by the running controller and shown in the dashboard |
| ~~OpenWrt router package~~ | **Dropped** | Would need router hardware; out of scope for a software-only product |
| Backups | Documented (`pg_dump` daily) | Operator's responsibility; not automated |
| First 5–10 paying pilot sites | **Not started** | Owner's task |

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
| Analytics | Fixed: during recovery, a network incident was relabelled "application" as some tests came back first. Network verdicts are now kept until the event resolves, and the lab checks the stored verdict after recovery. | — |
| Analytics | A traceroute is at most 60 s old, so `local`/`upstream` verdicts can start as `isp` and be refined within a minute (the lab measures this). | Trigger an on-demand traceroute when a site turns unhealthy (M3). |
| Agents | ICMP and traceroute are IPv4-only. On Windows, round-trip times have 1 ms resolution and traceroute uses ICMP echo (like `tracert`) rather than Paris UDP, so load-balanced paths may blend. Windows agents do not self-update (reinstall instead). | ICMPv6; a Windows service wrapper if customers need it. |
| Agents | An agent must run on a computer that stays on; a sleeping laptop shows the site as offline. | Document per customer; "business hours" schedules per site later. |
| Analytics | With the default 5-minute window, a site-wide outage can first be labelled "application" for 1–2 minutes while tests fail one by one, then corrected (seen in the live demo). The first alert may carry the early label. | Delay the first alert until the verdict is stable for one evaluation. |
| Database | TimescaleDB path (hypertable, `drop_chunks` retention) untested against a real TimescaleDB; compression not configured. | Test before the first large customer. |
| Dashboard | Bundle is ~210 KB gzipped (mostly ECharts). | Code-split charts if load time matters on slow links. |
| Reports | Availability counts time with an open incident; an incident's final verdict is applied to its whole duration. Times are in the viewer's browser time zone. | Per-tenant report time zone setting. |
| Agent updates | Self-update execs in place on Linux/Unix only; the agent's state directory must allow executing files. Rollout is all-at-once per controller. | Staged rollouts per tenant (enterprise). |
| Metrics | Analytics counters are per replica; HTTP latency is a sum/count pair, not a histogram. | Histograms if latency SLOs are needed. |
| Rate limits | Kept in memory per controller replica. | Shared limits only if several replicas are run. |
| DNS probe | Reports answers and timing, not RCODE/TTL/DNSSEC. | Raw DNS client if customers need it. |
| Product | No licence chosen. | Owner decision. |
