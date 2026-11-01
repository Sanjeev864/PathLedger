# PathLedger

**Evidence-grade internet and ISP monitoring for multi-site businesses.**

PathLedger is pure software, and free to run. A small agent runs on a
computer each branch already has (a Windows or Mac office PC, a Linux
server, a cloud VM or a Docker host); nothing needs to be bought or
installed in the network. The agent continuously measures the
links the business depends on — ICMP loss and jitter, DNS resolution, TCP and
TLS handshakes, HTTP checks against the apps people actually use, and
Paris-style traceroutes — and ships the results to a central controller.

The controller answers three questions automatically: *is it us, the ISP,
or the app?*, *where on the path is the fault?*, and *did the ISP meet its
SLA this month?* Every problem becomes an event with a verdict (`local`,
`isp`, `upstream`, `dns`, `app` or `offline`) and the evidence behind it,
delivered by email, Telegram or signed webhook, and shown in a web dashboard.

> **Status:** Milestones 1 (Foundation) and 2 (Insight) are complete, and the
> Milestone 3 (production release) software is built: user accounts and roles,
> MSP multi-tenancy, monthly service reports, data retention, Prometheus
> metrics, agent credential rotation and signed agent self-updates. The
> fault-injection lab in CI checks the verdicts against real injected faults —
> see [lab/results.md](lab/results.md), and CI installs the agent as a
> service on real Windows, macOS and Linux machines. What remains is piloting
> with real customers. Details, open items and known issues:
> [docs/ROADMAP.md](docs/ROADMAP.md).

## How it works

```
 Branch site                         Controller                     Storage
┌──────────────────────┐  HTTPS   ┌──────────────────────────┐   ┌────────────┐
│ pathledger-agent     │ ───────► │ agent API: enroll, plan, │   │ PostgreSQL │
│  scheduler → probes  │ outbound │   results ingest         │──►│ (optional  │
│  → spool → uploader  │   only   │ management API: sites,   │   │ TimescaleDB│
└──────────────────────┘          │   tests, agents, results │   └────────────┘
                                  └──────────────────────────┘
```

* Agents only make outbound HTTPS connections: no inbound ports at the branch,
  no inspection of customer traffic.
* Results are buffered on disk if the uplink is down and uploaded later.
* Every customer (tenant) is isolated at the query and database-constraint level.

Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) · API reference:
[docs/API.md](docs/API.md) · Deployment: [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)

## Try it in five minutes (no hardware, no branch offices)

The `demo` command creates a simulated customer: three clinics, two weeks of
measurements and incidents, and a live feed that can inject a fault while you
watch the real analysis find it.

```sh
cp .env.example .env
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml exec -e PATHLEDGER_PASSWORD='choose a password' \
    controller app demo -fault isp -after 1m -for 6m
```

Open `http://localhost:8080`, sign in as `demo@pathledger.example` with that
password, and within about three minutes one clinic turns red with an
"Internet provider" verdict and its evidence. The simulated data uses only
addresses and names reserved for documentation. Without Docker:
`pathledger-controller demo` against any PostgreSQL works the same way.

## Quick start (local)

Requirements: Go 1.24+, PostgreSQL 14+ (or Docker for the Compose stack).

```sh
make build                      # builds bin/pathledger-controller and bin/pathledger-agent

export PATHLEDGER_DATABASE_URL='postgres://user:pass@localhost:5432/pathledger?sslmode=disable'
./bin/pathledger-controller tenant create -name "My Company"   # prints the tenant ID and an API key once
echo 'a strong password' | ./bin/pathledger-controller user create \
    -email you@example.com -name "Your Name" -tenant <tenant-id> -role owner
./bin/pathledger-controller serve                               # listens on :8080
```

In another terminal, create a site, an enrolment token and a test:

```sh
KEY=plk_...   # from "tenant create"
H=(-H "Authorization: Bearer $KEY" -H "Content-Type: application/json")
SITE=$(curl -s "${H[@]}" -d '{"name":"Head office","location":"Bengaluru"}' localhost:8080/api/v1/sites | jq -r .id)
TOKEN=$(curl -s "${H[@]}" -X POST localhost:8080/api/v1/sites/$SITE/enrollment-tokens | jq -r .token)
curl -s "${H[@]}" -d '{"kind":"http","target":"https://www.google.com","interval_seconds":60,"timeout_millis":5000}' \
  localhost:8080/api/v1/tests
```

Start an agent on this or any other computer (the enrolment token is read
from the environment, never a flag). To install it as a background service
instead, use `deploy/windows/install-agent.ps1` on Windows or
`deploy/unix/install-agent.sh` on Linux and macOS (see
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md#agents)):

```sh
PATHLEDGER_ENROLL_TOKEN=$TOKEN ./bin/pathledger-agent \
  -controller http://localhost:8080 -state-dir ./var/agent -log-format text
```

Results appear at `GET /api/v1/results`, and the dashboard is served at
`http://localhost:8080/`. Sign in with the email and password you created;
invite colleagues from **Setup → People** as viewers, admins or owners.
MSP staff who belong to several organisations switch between them from
the header. Plain `http://` is
accepted only for localhost; any other controller URL must be `https://`.

The dashboard is embedded at build time: run `make web` (Node 22+) before
`make build`, or the controller serves a "dashboard not built" page.

Optional controller settings: `PATHLEDGER_ASN_DB` (free IPtoASN data, see
`scripts/fetch-asn-db.sh`) to name the networks on each path;
`PATHLEDGER_SMTP_*` and `PATHLEDGER_TELEGRAM_BOT_TOKEN` for notifications.
Run `pathledger-controller` with no arguments for the full list.

### With Docker Compose

```sh
cp .env.example .env            # set POSTGRES_PASSWORD
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml exec controller app tenant create -name "My Company"
```

## Development

```sh
make lint                       # gofmt + go vet
make test                       # unit tests (database tests skip)
PATHLEDGER_TEST_DATABASE_URL='postgres://postgres@localhost:5432/postgres?sslmode=disable' make test-db
PATHLEDGER_DATABASE_URL='postgres://.../pathledger_e2e?sslmode=disable' make e2e
make dist                       # agent binaries for Windows, macOS and Linux (amd64 and arm64)
# Fault-injection lab (Linux, root, pip install pyroute2):
sudo env PATH="$PATH" PATHLEDGER_DATABASE_URL="$PATHLEDGER_DATABASE_URL" make lab
```

Database tests create and drop their own uniquely named databases, so the URL
must belong to a user allowed to `CREATE DATABASE`.

ICMP probes use unprivileged ping sockets where the OS allows them
(`sysctl net.ipv4.ping_group_range`) and otherwise need `CAP_NET_RAW`.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/pathledger-agent` | Agent binary |
| `cmd/pathledger-controller` | Controller binary and admin CLI |
| `cmd/pathledger-release` | Release signing tool (keygen, sign, verify) |
| `pkg/wire` | Versioned agent ↔ controller protocol and validation |
| `internal/probe` | ICMP, DNS, TCP, TLS and HTTP probes |
| `internal/agent` | Enrolment, scheduler, spool, uploader |
| `internal/controller/api` | HTTP handlers and middleware |
| `internal/controller/store` | PostgreSQL access and migrations |
| `internal/controller/auth` | Credential generation and hashing |
| `internal/controller/analytics` | Baselines, site status, fault verdicts, events |
| `internal/controller/alert` | Email, Telegram and webhook delivery via an outbox |
| `internal/controller/demo` | Simulated demo customer and live fault injection |
| `internal/controller/report` | Monthly service-level report |
| `internal/controller/maintenance` | Data retention job |
| `internal/controller/releases` | Serves signed agent releases |
| `internal/controller/webui` | Embedded dashboard |
| `internal/metrics` | Prometheus text-format metrics |
| `internal/fsperm` | Owner-only permissions for secrets on Unix and Windows |
| `internal/update` | Signed release manifests and version rules |
| `internal/enrich` | Address scope and IP → AS lookup |
| `web/` | Dashboard source (Svelte 5, TypeScript, ECharts) |
| `lab/` | Fault-injection lab and its latest results |
| `deploy/` | Dockerfile, Compose stack, agent installers (Windows, macOS, Linux) |
| `docs/` | Architecture, API, deployment, roadmap |

## Licence

Not yet chosen. Until a licence is added, all rights are reserved by the author.
