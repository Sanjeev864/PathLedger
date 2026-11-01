# Deployment

## Pilot: one small VM

Suitable for a campus pilot and the first handful of customer sites.

1. **VM.** Any Linux VM with 1 vCPU and 1–2 GB RAM (for example a free-tier
   ARM instance, or a low-cost VPS). Open ports 80 and 443 only.
2. **Database and controller.** Copy the repository, then:
   ```sh
   cp .env.example .env    # set a strong POSTGRES_PASSWORD
   docker compose -f deploy/docker-compose.yml up -d --build
   docker compose -f deploy/docker-compose.yml exec controller app tenant create -name "Customer name"
   ```
   The controller listens on `127.0.0.1:8080` only.
3. **TLS.** Put [Caddy](https://caddyserver.com) in front; it obtains and
   renews Let's Encrypt certificates automatically:
   ```
   pathledger.example.com {
       reverse_proxy 127.0.0.1:8080
   }
   ```
   Alternatively, Cloudflare Tunnel exposes the controller without opening
   any inbound port.
4. **Optional settings** (in `.env` / the controller environment):
   - `PATHLEDGER_ASN_DB=/data/ip2asn-v4.tsv.gz` after
     `scripts/fetch-asn-db.sh deploy/data/ip2asn-v4.tsv.gz` (refresh weekly).
   - Email: `PATHLEDGER_SMTP_HOST`, `PATHLEDGER_SMTP_PORT` (587),
     `PATHLEDGER_SMTP_USERNAME`, `PATHLEDGER_SMTP_PASSWORD`,
     `PATHLEDGER_SMTP_FROM`. Any provider works, including a free-tier
     transactional email service.
   - Telegram: create a bot with @BotFather and set
     `PATHLEDGER_TELEGRAM_BOT_TOKEN`; customers add the bot to their group
     and register the group's chat ID as a channel.
   - `PATHLEDGER_PUBLIC_URL=https://pathledger.example.com` so notifications
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
5. **Backups.** Schedule `pg_dump` daily and copy the dumps off the VM.

## Agents

Download the binary for the device (`make dist` builds amd64, arm64, armv7,
mips and mipsle), then install the systemd unit in
`deploy/systemd/pathledger-agent.service` — its header lists the exact
commands. The unit runs the agent as a dynamic, unprivileged user with only
`CAP_NET_RAW` and a 128 MB memory ceiling.

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
- [ ] Managed or replicated PostgreSQL with point-in-time recovery
- [ ] TimescaleDB with compression and a retention policy
- [x] Rate limiting per credential
- [x] Prometheus metrics (alerting rules are the operator's)
- [x] Data retention job
- [ ] Mutual TLS for agents and signed agent updates
- [ ] Documented incident and data-retention policies (DPDP Act)
