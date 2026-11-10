#!/usr/bin/env bash
# Shows where RASID lives inside DRUNIX: which containers play which part,
# the ledger height on each committing peer, the latest receivables as rows
# in each organisation's YugabyteDB state database, and the private-data
# entries in each lite peer's KeyDB transient store. Good for a live demo.
#   bash network/drunix/inspect.sh
set -euo pipefail
source "$(dirname "$0")/env.sh"
h() { printf '\n\033[1m%s\033[0m\n' "$*"; }

h "DRUNIX containers"
docker ps --format '{{.Names}}' | sort | while read -r c; do
  case $c in
    lp*) r="lite peer: endorses (runs chaincode), keeps no ledger" ;;
    cp*) r="committing peer: validates and commits blocks" ;;
    vs*) r="validation service (stateless VSCC)" ;;
    yugabyte*) r="YugabyteDB: world state as SQL tables" ;;
    hlf_keydb*) r="KeyDB: transient store for private data" ;;
    orderer*) r="Raft orderer" ;;
    rasid-cc) r="RASID chaincode as a service" ;;
    *) continue ;;
  esac
  printf '  %-22s %s\n' "$c" "$r"
done

h "Ledger height on each committing peer"
for o in 1 2; do
  as_admin $o "$(committing_peer $o)"
  printf '  Org%sMSP %s  ' "$o" "$(committing_peer $o)"
  peer channel getinfo -c "$CHANNEL" 2>/dev/null | sed -n 's/.*"height":\([0-9]*\).*/height \1/p'
done

SQL="select block_number as block, left(db_value->>'id',16) as receivable, db_value->>'status' as status,
  db_value->'risk'->>'score' as risk, (db_value->>'amountPaise')::bigint/100 as rupees
  from \"$CHANNEL\".\"$CC_NAME\" where jsonb_typeof(db_value)='object' and db_value ? 'amountPaise'
  order by block_number desc limit 5;"
for o in 1 2; do
  h "Latest receivables in Org${o}MSP's YugabyteDB (table $CHANNEL.$CC_NAME)"
  docker exec "yugabyte-org$o" bash -c 'bin/ysqlsh -h "$(hostname)" -U yugabyte -d yugabyte -c "$1"' _ "$SQL" | sed 's/^/  /'
done

h "Private data waiting in each lite peer's KeyDB transient store"
for o in 1 2; do
  printf '  Org%sMSP: %s keys\n' "$o" "$(docker exec "hlf_keydb_org${o}msp" keydb-cli dbsize)"
done
echo "  Sealed bids pass through here on their way to the committing peer; the shared ledger only gets a hash."
