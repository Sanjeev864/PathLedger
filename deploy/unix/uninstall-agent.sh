#!/bin/sh
# Stops and removes the PathLedger agent installed by install-agent.sh.
# Add --purge to delete its credential and buffered results too (revoke the
# agent in the dashboard as well).
set -eu
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
purge=${1:-}
case $(uname -s) in
Linux)
  systemctl disable --now pathledger-agent 2>/dev/null || true
  rm -f /etc/systemd/system/pathledger-agent.service && systemctl daemon-reload
  [ "$purge" = --purge ] && rm -rf /var/lib/pathledger-agent /var/lib/private/pathledger-agent /etc/pathledger
  ;;
Darwin)
  launchctl bootout system /Library/LaunchDaemons/com.pathledger.agent.plist 2>/dev/null || true
  rm -f /Library/LaunchDaemons/com.pathledger.agent.plist
  [ "$purge" = --purge ] && rm -rf "/Library/Application Support/PathLedger" /Library/Logs/PathLedger
  ;;
esac
rm -f /usr/local/bin/pathledger-agent
echo "PathLedger agent removed."
