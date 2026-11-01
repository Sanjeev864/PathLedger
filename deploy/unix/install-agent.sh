#!/bin/sh
# Installs the PathLedger agent as a background service on Linux (systemd)
# or macOS (launchd). Run as root, next to the downloaded agent binary:
#
#   sudo PATHLEDGER_ENROLL_TOKEN=ple_... ./install-agent.sh https://pathledger.example.com
#
# The enrolment token is used once and never written to disk.
set -eu

url=${1:-}
[ -n "$url" ] || { echo "usage: PATHLEDGER_ENROLL_TOKEN=ple_... $0 <controller-url>" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
[ -n "${PATHLEDGER_ENROLL_TOKEN:-}" ] || { echo "set PATHLEDGER_ENROLL_TOKEN" >&2; exit 2; }

here=$(cd "$(dirname "$0")" && pwd)
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported CPU $(uname -m)" >&2; exit 1 ;; esac
bin=${PATHLEDGER_AGENT_BINARY:-$here/pathledger-agent-$os-$arch}
[ -f "$bin" ] || { echo "agent binary not found: $bin" >&2; exit 1; }

install -m 0755 "$bin" /usr/local/bin/pathledger-agent

case $os in
linux)
  state=/var/lib/pathledger-agent
  install -d -m 0700 "$state"
  /usr/local/bin/pathledger-agent -controller "$url" -state-dir "$state" -enroll-only -log-format text
  unset PATHLEDGER_ENROLL_TOKEN
  install -d -m 0700 /etc/pathledger
  [ -f /etc/pathledger/agent.env ] || { echo "PATHLEDGER_CONTROLLER_URL=$url" > /etc/pathledger/agent.env; chmod 0600 /etc/pathledger/agent.env; }
  install -m 0644 "$here/../systemd/pathledger-agent.service" /etc/systemd/system/pathledger-agent.service 2>/dev/null \
    || install -m 0644 "$here/pathledger-agent.service" /etc/systemd/system/pathledger-agent.service
  # The unit uses a dynamic user; hand the enrolled state to systemd's private state dir.
  if [ ! -e /var/lib/private/pathledger-agent ]; then
    install -d -m 0700 /var/lib/private
    mv "$state" /var/lib/private/pathledger-agent
    ln -s private/pathledger-agent "$state"
  fi
  systemctl daemon-reload
  systemctl enable --now pathledger-agent
  echo "PathLedger agent running. Logs: journalctl -u pathledger-agent"
  ;;
darwin)
  state="/Library/Application Support/PathLedger/agent"
  logs=/Library/Logs/PathLedger
  install -d -m 0700 "$state"
  install -d -m 0755 "$logs"
  /usr/local/bin/pathledger-agent -controller "$url" -state-dir "$state" -enroll-only -log-format text
  unset PATHLEDGER_ENROLL_TOKEN
  plist=/Library/LaunchDaemons/com.pathledger.agent.plist
  launchctl bootout system "$plist" 2>/dev/null || true
  install -m 0644 "$here/com.pathledger.agent.plist" "$plist"
  launchctl bootstrap system "$plist"
  echo "PathLedger agent running. Logs: $logs/agent.log"
  ;;
*)
  echo "unsupported OS $os; run the agent with Docker instead" >&2; exit 1 ;;
esac
