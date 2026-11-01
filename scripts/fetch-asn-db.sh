#!/usr/bin/env bash
# Downloads the free IPtoASN IPv4 database (Public Domain Dedication and
# License) used to attribute traceroute hops to networks. Refresh weekly.
#   scripts/fetch-asn-db.sh /var/lib/pathledger/ip2asn-v4.tsv.gz
set -euo pipefail
dest="${1:-./ip2asn-v4.tsv.gz}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp" https://iptoasn.com/data/ip2asn-v4.tsv.gz
gzip -t "$tmp"
mkdir -p "$(dirname "$dest")"
mv "$tmp" "$dest"
trap - EXIT
echo "saved $(zcat "$dest" | wc -l) ranges to $dest"
