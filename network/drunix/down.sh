#!/usr/bin/env bash
# Stops the DRUNIX test network and RASID's chaincode service, and deletes
# their data (ledgers, YugabyteDB, KeyDB, certificates, issued identities).
#   bash network/drunix/down.sh
set -euo pipefail
source "$(dirname "$0")/env.sh"
docker rm -f rasid-cc >/dev/null 2>&1 || true
(cd "$NET" && ./network.sh down) >"${TMPDIR:-/tmp}/rasid-drunix-down.log" 2>&1 || {
  tail -20 "${TMPDIR:-/tmp}/rasid-drunix-down.log"; exit 1; }
rm -rf "$WORK/ids"
echo "DRUNIX network stopped and its data removed."
