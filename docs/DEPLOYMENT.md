# Deployment

Everything PathLedger needs is free: the code, PostgreSQL, Docker, Caddy,
Telegram and the hosting options below. No hardware is bought; agents run
on computers the customer already has.

## Zero-cost deployment options

| Stage | Controller runs on | How agents reach it | Cost and sign-ups |
| --- | --- | --- | --- |
| **Demo / viva** | Your laptop (Docker Compose, or WSL2 on Windows) | Same machine; `demo` simulates three branches | Nothing |
| **Team pilot** | One team member's laptop or a lab PC | [Tailscale](https://tailscale.com/plans/) free Personal plan (non-commercial use): every agent joins the tailnet, nothing is exposed publicly. Or a [Cloudflare Quick Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/do-more-with-tunnels/trycloudflare/) (`cloudflared tunnel --url http://localhost:8080`, no account; testing only, the URL changes on every restart) | Free accounts at most |
| **Always-on pilot** | A college server, or an [Oracle Cloud Always Free](https://docs.oracle.com/iaas/Content/FreeTier/freetier.htm) Arm VM (sign-up asks for a card for verification; it is not charged unless you upgrade) | HTTPS through Caddy with a free [DuckDNS](https://www.duckdns.org) subdomain and Let's Encrypt certificates | Free |

A controller on a laptop works only while the laptop is on. For anything
longer than a demo, use a machine that stays on.

## Always-on pilot: one small VM

1. **VM.** Any Linux machine with 1 vCPU and 1–2 GB RAM. Open ports 80 and
   443 only.
2. **Database and controller.** Copy the repository, then:
   ```sh
   cp .env.example .env    # set a strong POSTGRES_PASSWORD
   docker compose -f deploy/docker-compose.yml up -d --build
   docker compose -f deploy/docker-compose.yml exec controller app tenant create -name "Customer name"
   ```
   The controller listens on `127.0.0.1:8080` only.
3. **TLS.** Point a free DuckDNS name at the VM and put
   [Caddy](https://caddyserver.com) in front; it obtains and renews Let's
   Encrypt certificates automatically:
   ```
   yourname.duckdns.org {
       reverse_proxy 127.0.0.1:8080
   }
   ```
4. **Optional settings** (in `.env` / the controller environment):
   - `PATHLEDGER_ASN_DB=/data/ip2asn-v4.tsv.gz` after
     `scripts/fetch-asn-db.sh deploy/data/ip2asn-v4.tsv.gz` (free data;
     refresh weekly).
   - Email: `PATHLEDGER_SMTP_HOST`, `PATHLEDGER_SMTP_PORT` (587),
     `PATHLEDGER_SMTP_USERNAME`, `PATHLEDGER_SMTP_PASSWORD`,
     `PATHLEDGER_SMTP_FROM`. A Gmail account works for free: host
     `smtp.gmail.com`, port 587, and an app password (requires 2-step
     verification) as the password.
   - Telegram (free): create a bot with @BotFather and set
     `PATHLEDGER_TELEGRAM_BOT_TOKEN`; customers add the bot to their group
     and register the group's chat ID as a channel.
   - `PATHLEDGER_PUBLIC_URL=https://yourname.duckdns.org` so notifications
     link to the dashboard, and `PATHLEDGER_TRUST_PROXY=true` behind Caddy.
   - `PATHLEDGER_RETENTION_DAYS` (default 90, range 7–3650): raw probe results
     older than this are deleted hourly. Resolved events are kept 365 days,
     notification deliveries 30 days; expired sessions, invitations and
     enrollment tokens are removed too. With TimescaleDB, old result chunks
     are dropped instead of deleted row by row. Only one replica purges at a
     time (PostgreSQL advisory lock).
   - `PATHLEDGER_METRICS_ADDR=127.0.0.1:9090` exposes Prometheus metrics at
     `/metrics` on a separate listener (off by default; never publish it).
     See "Monitoring the controller" below.
5. **Backups.** Schedule `pg_dump` daily (cron or a systemd timer) and copy
   the dumps to another machine or a free cloud drive.

## Demo data

`pathledger-controller demo` (needs `PATHLEDGER_DATABASE_URL`) creates the
tenant "Demo: Sunrise Clinics (simulated data)" with three sites, five tests,
`-days` of history (default 14, at most 29) and eight scripted incidents, and
a login (`-email`, password from `PATHLEDGER_PASSWORD` or generated and
printed once). It then feeds live measurements until stopped; run `serve`
at the same time. `-fault isp|local|upstream|dns|app|offline -site 1-3
-after 2m -for 8m` injects a fault into the live feed, which the controller
detects, diagnoses and alerts on like a real one. `-tenant <id>` resumes the
feed for an existing demo tenant; `-seed-only` skips it. Demo data uses only
documentation address ranges and `.example` names.

## Agents

The agent is a single program with no dependencies. Install it on a computer
at the branch that **stays on during business hours** (a reception PC, a
server, a VM); a laptop that sleeps or leaves the office reports the site as
offline or measures another network. Create an enrolment token for the site
in the dashboard (Setup) first. The installers use the token once and never
store it.

| Computer | Install | Probes |
| --- | --- | --- |
| **Windows 10/11, Server 2016+** | Download `pathledger-agent-windows-amd64.exe` and `deploy/windows/*.ps1` into one folder; in an elevated PowerShell: `.\install-agent.ps1 -ControllerUrl https://… -EnrollToken ple_…` | All. ICMP and traceroute use Windows' own ICMP service (as `ping`/`tracert` do), with 1 ms timing resolution |
| **macOS 12+** | `sudo PATHLEDGER_ENROLL_TOKEN=ple_… ./install-agent.sh https://…` from `deploy/unix/` next to `pathledger-agent-darwin-arm64` (Apple silicon) or `-amd64` | All |
| **Linux (systemd)** | Same script, next to `pathledger-agent-linux-amd64` or `-arm64`; installs the hardened unit in `deploy/systemd/` | All |
| **Any Docker host** | See below | All |

Remove with `uninstall-agent.ps1` / `uninstall-agent.sh` (add `-Purge` /
`--purge` to delete the credential too) and revoke the agent on its site page.
`make dist` builds all six binaries. CI installs the agent as a service on
real Windows, macOS and Linux machines and checks that ICMP, TCP and
traceroute results arrive.

On a Docker host:

```sh
docker run -d --name pathledger-agent --restart unless-stopped \
  -e PATHLEDGER_ENROLL_TOKEN=ple_... \
  -v pathledger-agent:/var/lib/pathledger-agent \
  <image built with BINARY=pathledger-agent> \
  -controller https://pathledger.example.com -state-dir /var/lib/pathledger-agent
```

Docker allows unprivileged ICMP sockets inside containers by default, so no
extra capabilities are required.

## Agent updates

Agents on Linux and macOS update themselves only to releases signed with **your** release key. Windows agents are updated by running the installer again with the new binary.
The controller relays files; it cannot sign, so a compromised controller
cannot push code to customer networks.

1. **Once, on a trusted machine** (not the controller):
   `go build -o pathledger-release ./cmd/pathledger-release && ./pathledger-release keygen -out release.key`.
   Keep `release.key` offline with a backup; note the printed public key.
2. **Agents**: set `PATHLEDGER_UPDATE_PUBLIC_KEY=<public key>` in
   `/etc/pathledger/agent.env` (or `-update-key`). Without it agents never
   update. Several keys, comma-separated, allow a key rollover.
3. **Each release**: `make dist VERSION=v0.3.0`, then
   `./pathledger-release sign -key release.key -version v0.3.0 -dir dist` and
   `./pathledger-release verify -pub <public key> -dir dist`.
4. **Publish**: copy `dist/` to the controller and set
   `PATHLEDGER_AGENT_RELEASES_DIR` to it (plus `PATHLEDGER_UPDATE_PUBLIC_KEY`
   so the controller refuses a release you did not sign). Replacing the
   directory's contents publishes a new release; no restart is needed.

Agents check within a minute, install, and restart in place. The dashboard
shows each agent's version on its site page. A release that fails to start
three times on a device is rolled back there and skipped.

**Credential rotation.** Agents replace their credential every 30 days
(`PATHLEDGER_AGENT_TOKEN_MAX_AGE`), and immediately when you press
*Rotate credential* on the site page — for example after a device was
serviced by a third party. *Revoke* disables an agent permanently.

## Monitoring the controller

Metrics exposed on `PATHLEDGER_METRICS_ADDR`:

| Metric | Meaning |
|---|---|
| `pathledger_http_request_duration_seconds_{count,sum}{route,code}` | Requests by route pattern and status class |
| `pathledger_results_ingested_total`, `..._rejected_total` | Probe results stored / rejected |
| `pathledger_analytics_runs_total`, `..._failures_total` | Evaluations on this replica |
| `pathledger_analytics_last_success_timestamp_seconds` | Last good evaluation (alert if > 5 min old on every replica) |
| `pathledger_notifications_total{kind,outcome}` | delivered / retry / failed |
| `pathledger_agents`, `pathledger_agents_stale` | Fleet size and agents silent > 5 min |
| `pathledger_events_open`, `pathledger_deliveries_pending`, `pathledger_deliveries_failed_last_day` | Backlog health |
| `pathledger_results_rows_estimate` | Table growth (planner estimate) |
| `go_goroutines`, `go_memstats_*`, `process_start_time_seconds` | Runtime |

Suggested alerts: analytics last success older than 5 minutes; any
`deliveries_failed_last_day`; `deliveries_pending` growing for 15 minutes;
5xx rate above 1%.

## Production checklist (Milestone 3)

- [ ] Two controller replicas behind a load balancer using `/readyz`
- [ ] PostgreSQL streaming replica and WAL archiving for point-in-time recovery (both free, built into PostgreSQL)
- [ ] TimescaleDB with compression and a retention policy
- [x] Rate limiting per credential
- [x] Prometheus metrics (alerting rules are the operator's)
- [x] Data retention job
- [x] Agent credential rotation and signed agent updates
- [ ] Mutual TLS for agents (enterprise option; see ARCHITECTURE.md)
- [ ] Documented incident and data-retention policies (DPDP Act)
