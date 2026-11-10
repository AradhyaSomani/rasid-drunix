#!/usr/bin/env bash
# Brings up NPCI's DRUNIX test network for RASID: per organisation a lite peer
# (endorses), a committing peer (validates and commits), a stateless
# validation service, YugabyteDB as the state database and KeyDB as the
# transient store, plus a Raft orderer. Then it adds the chaincode-as-a-service
# builder to the lite peers, which RASID's chaincode needs.
#   bash network/drunix/setup.sh    (once: fetches DRUNIX and its tools)
#   bash network/drunix/up.sh
set -euo pipefail
source "$(dirname "$0")/env.sh"
[ -x "$DRUNIX_HOME/drunix-network/bin/peer" ] || { echo "run network/drunix/setup.sh first" >&2; exit 1; }
mkdir -p "$WORK/logs"
cd "$NET"
echo "Starting the DRUNIX test network (YugabyteDB takes a minute or two)"
./network.sh up createChannel -c "$CHANNEL" >"$WORK/logs/network.log" 2>&1 || { tail -30 "$WORK/logs/network.log"; exit 1; }
echo "Channel $CHANNEL created and joined"

# The DRUNIX peer image names /opt/hyperledger/ccaas_builder in its core.yaml
# but does not ship it. Copy in the same three-script builder the local
# Fabric network uses, then restart the peers so the lite peers load it.
for c in lp1.org1 lp1.org2; do
  docker exec "$c" mkdir -p /opt/hyperledger/ccaas_builder
  docker cp "$ROOT/network/local/builders/ccaas/bin" "$c:/opt/hyperledger/ccaas_builder/" >/dev/null
  docker exec "$c" chmod 755 /opt/hyperledger/ccaas_builder/bin/build /opt/hyperledger/ccaas_builder/bin/detect /opt/hyperledger/ccaas_builder/bin/release
done
restart_peers
for org in 1 2; do
  as_admin "$org"
  for _ in $(seq 1 30); do peer channel getinfo -c "$CHANNEL" >/dev/null 2>&1 && break; sleep 2; done
done
echo "DRUNIX is up. Next: bash network/drunix/deploy.sh"
