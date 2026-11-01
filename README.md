# PathLedger

**Evidence-grade internet and ISP monitoring for multi-site businesses.**

PathLedger puts a small agent at every branch (a Raspberry Pi, a spare PC, a
Docker host or, soon, an OpenWrt router). The agent continuously measures the
links the business depends on — ICMP loss and jitter, DNS resolution, TCP and
TLS handshakes, and HTTP checks against the apps people actually use — and
ships the results to a central controller. The goal is to answer three
questions automatically: *is it us, the ISP, or the app?*, *where on the path
is the fault?*, and *did the ISP meet its SLA this month?*

> **Status:** Milestone 1 (Foundation) is complete — agent, controller, API,
> storage, tests and packaging. Fault attribution, alerts and the web dashboard
> are Milestone 2. See [docs/ROADMAP.md](docs/ROADMAP.md) for what is done,
> what is next and known issues.

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

## Quick start (local)

Requirements: Go 1.24+, PostgreSQL 14+ (or Docker for the Compose stack).

```sh
make build                      # builds bin/pathledger-controller and bin/pathledger-agent

export PATHLEDGER_DATABASE_URL='postgres://user:pass@localhost:5432/pathledger?sslmode=disable'
./bin/pathledger-controller tenant create -name "My Company"   # prints an API key once
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

Start an agent (the enrolment token is read from the environment, never a flag):

```sh
PATHLEDGER_ENROLL_TOKEN=$TOKEN ./bin/pathledger-agent \
  -controller http://localhost:8080 -state-dir ./var/agent -log-format text
```

Results appear at `GET /api/v1/results`. Plain `http://` is accepted only for
localhost; any other controller URL must be `https://`.

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
make dist                       # cross-compiles the agent for amd64, arm64, armv7, mips, mipsle
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
| `pkg/wire` | Versioned agent ↔ controller protocol and validation |
| `internal/probe` | ICMP, DNS, TCP, TLS and HTTP probes |
| `internal/agent` | Enrolment, scheduler, spool, uploader |
| `internal/controller/api` | HTTP handlers and middleware |
| `internal/controller/store` | PostgreSQL access and migrations |
| `internal/controller/auth` | Credential generation and hashing |
| `deploy/` | Dockerfile, Compose stack, systemd unit |
| `docs/` | Architecture, API, deployment, roadmap |

## Licence

Not yet chosen. Until a licence is added, all rights are reserved by the author.
