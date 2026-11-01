#!/usr/bin/env bash
# End-to-end check: real controller + real agent + real PostgreSQL.
#
# Requires: built binaries in ./bin, curl, jq, and PATHLEDGER_DATABASE_URL
# pointing at an empty database. Exits non-zero if results do not arrive.
set -euo pipefail

: "${PATHLEDGER_DATABASE_URL:?set PATHLEDGER_DATABASE_URL to an empty database}"
PORT="${E2E_PORT:-18080}"
BASE="http://127.0.0.1:${PORT}"
WORK="$(mktemp -d)"
trap 'kill ${CTL_PID:-} ${AGENT_PID:-} 2>/dev/null || true; rm -rf "$WORK"' EXIT

export PATHLEDGER_LOG_FORMAT=text

echo "==> starting controller"
PATHLEDGER_LISTEN_ADDR="127.0.0.1:${PORT}" ./bin/pathledger-controller serve >"$WORK/controller.log" 2>&1 &
CTL_PID=$!
for _ in $(seq 1 50); do curl -fsS "$BASE/readyz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "$BASE/readyz" >/dev/null || { cat "$WORK/controller.log"; exit 1; }

echo "==> creating tenant"
API_KEY="$(./bin/pathledger-controller tenant create -name "E2E Tenant" 2>/dev/null | awk '/api key:/ {print $3}')"
[ -n "$API_KEY" ] || { echo "no API key printed"; exit 1; }
auth=(-H "Authorization: Bearer ${API_KEY}" -H "Content-Type: application/json")

SITE_ID="$(curl -fsS "${auth[@]}" -d '{"name":"E2E Branch","location":"Lab"}' "$BASE/api/v1/sites" | jq -r .id)"
TOKEN="$(curl -fsS "${auth[@]}" -X POST "$BASE/api/v1/sites/${SITE_ID}/enrollment-tokens" | jq -r .token)"

echo "==> defining tests (TCP and HTTP against the controller itself)"
curl -fsS "${auth[@]}" -d "{\"site_id\":\"${SITE_ID}\",\"kind\":\"tcp\",\"target\":\"127.0.0.1:${PORT}\",\"interval_seconds\":10,\"timeout_millis\":2000}" "$BASE/api/v1/tests" >/dev/null
curl -fsS "${auth[@]}" -d "{\"kind\":\"http\",\"target\":\"${BASE}/healthz\",\"interval_seconds\":10,\"timeout_millis\":2000}" "$BASE/api/v1/tests" >/dev/null

echo "==> starting agent"
PATHLEDGER_ENROLL_TOKEN="$TOKEN" ./bin/pathledger-agent -controller "$BASE" -state-dir "$WORK/agent" >"$WORK/agent.log" 2>&1 &
AGENT_PID=$!

echo "==> waiting for results (up to 60s)"
for _ in $(seq 1 60); do
  COUNT="$(curl -fsS "${auth[@]}" "$BASE/api/v1/results?site_id=${SITE_ID}" | jq '[.items[] | select(.success)] | length')"
  KINDS="$(curl -fsS "${auth[@]}" "$BASE/api/v1/results?site_id=${SITE_ID}" | jq -r '[.items[].kind] | unique | join(",")')"
  if [ "$COUNT" -ge 2 ] && [ "$KINDS" = "http,tcp" ]; then
    echo "PASS: ${COUNT} successful results (${KINDS})"
    curl -fsS "${auth[@]}" "$BASE/api/v1/results?site_id=${SITE_ID}&limit=2" | jq -c '.items[] | {kind, success, latency_ms, details}'
    stat -c 'agent state file mode: %a' "$WORK/agent/agent.json"
    exit 0
  fi
  sleep 1
done
echo "FAIL: results did not arrive"; echo "--- controller"; tail -20 "$WORK/controller.log"; echo "--- agent"; tail -20 "$WORK/agent.log"
exit 1
