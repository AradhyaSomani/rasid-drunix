# shellcheck shell=bash
# Shared by demo-network.sh and console-network.sh: starts one gateway per
# identity, the risk service (with the risk bridge on NPCI's risk-oracle
# gateway) and the settlement adapters. Source it.
# RASID_NET picks the ledger: "local" (network/local, the default) or "drunix"
# (NPCI's DRUNIX test network, after network/drunix/up.sh and deploy.sh).
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
RASID_NET=${RASID_NET:-local}
case $RASID_NET in
  local)
    WORK=${WORK:-/tmp/rasid-net}
    NPCI_MSP=NPCIMSP SUP_MSP=BankAMSP CITI_MSP=CitiMSP LEDGER="Fabric network" ;;
  drunix)
    # shellcheck source=../network/drunix/env.sh
    source "$ROOT/network/drunix/env.sh"
    NPCI_MSP=Org1MSP SUP_MSP=Org2MSP CITI_MSP=Org2MSP LEDGER="DRUNIX network" ;;
  *) echo "RASID_NET must be local or drunix" >&2; exit 2 ;;
esac
TMP=$(mktemp -d)
PIDS=()
# Silence stderr first: macOS bash 3.2 reports each killed background job.
# Each run's logs are kept in $WORK/logs/last-run for when something fails.
cleanup() {
  exec 2>/dev/null; kill ${PIDS[@]+"${PIDS[@]}"} || true
  rm -rf "$WORK/logs/last-run"; mkdir -p "$WORK/logs/last-run"; cp "$TMP"/*.log "$WORK/logs/last-run/" || true
  rm -rf "$TMP"
}
trap cleanup EXIT
OP=http://127.0.0.1:8101 RISKGW=http://127.0.0.1:8102 SUP=http://127.0.0.1:8103 BUY=http://127.0.0.1:8104 CITI=http://127.0.0.1:8105
RISK=http://127.0.0.1:8090

# CONSOLE_LOGIN=1 turns on sign-in at every gateway: one demo user each,
# password $RASID_PASSWORD (default rasid). The adapters sign in too.
PASS=${RASID_PASSWORD:-rasid}
login_arg() { if [ "${CONSOLE_LOGIN:-}" = 1 ]; then echo "--login $1:$PASS"; fi; }

# peer_env <org>: sets PEER_ENV to the gateway settings that reach that org's peer.
peer_env() {
  if [ "$RASID_NET" = drunix ]; then
    # DRUNIX splits a peer in two: the lite peer endorses and forwards to the
    # orderer, the committing peer keeps the ledger and sends block events.
    local n; case $1 in npci) n=1 ;; *) n=2 ;; esac
    PEER_ENV=(PEER_ENDPOINT="$(lite_peer $n)" PEER_HOST_ALIAS="peer0.org$n.example.com"
      EVENTS_PEER_ENDPOINT="$(committing_peer $n)" EVENTS_PEER_HOST_ALIAS="peer1.org$n.example.com"
      PEER_TLS_CA="$(tls_ca $n)" CHANNEL="$CHANNEL")
  else
    local port; case $1 in npci) port=7051 ;; banka) port=8051 ;; citi) port=9051 ;; esac
    PEER_ENV=(PEER_ENDPOINT="localhost:$port" PEER_HOST_ALIAS=localhost CHANNEL=rasid
      PEER_TLS_CA="$WORK/crypto/peerOrganizations/$1.rasid/peers/peer0.$1.rasid/tls/ca.crt")
  fi
}

gw() { # name org msp url login-user "label" [extra env...]
  local name=$1 org=$2 msp=$3 url=$4 user=$5 label=$6 users=""; shift 6
  if [ "${CONSOLE_LOGIN:-}" = 1 ]; then users="$user:$PASS"; fi
  peer_env "$org"
  env "${PEER_ENV[@]}" \
    MSP_ID="$msp" OPERATOR_MSP="$NPCI_MSP" LEDGER_NAME="$LEDGER" \
    CERT_PATH="$WORK/ids/$name/cert.pem" KEY_PATH="$WORK/ids/$name/key.pem" \
    CHAINCODE=rasid LISTEN="${url#http://}" CORS_ORIGIN="${CORS_ORIGIN:-}" \
    LOGIN_USERS="$users" LOGIN_LABEL="$label" "$@" \
    "$TMP/gateway" >"$TMP/gw-$name.log" 2>&1 &
  PIDS+=($!)
}

start_stack() { # [extra env for the operator gateway...]
  (cd "$ROOT/app/gateway" && go build -o "$TMP/gateway" .)
  (cd "$ROOT/tools/mock-irp" && go build -o "$TMP/mock-irp" .)
  (cd "$ROOT/ai/risk-service" && exec python3 -m rasid_risk.server --port "${RISK##*:}") >"$TMP/risk.log" 2>&1 &
  PIDS+=($!)
  gw operator npci "$NPCI_MSP" $OP npci "NPCI operator" "$@"
  gw risk npci "$NPCI_MSP" $RISKGW risk "AI risk desk" RISK_URL=$RISK
  gw supplier banka "$SUP_MSP" $SUP msme "MSME supplier"
  gw buyer banka "$SUP_MSP" $BUY buyer "Corporate buyer"
  gw citi citi "$CITI_MSP" $CITI citi "Citi"
  local u
  for u in $OP $RISKGW $SUP $BUY $CITI $RISK; do
    for _ in $(seq 1 50); do curl -sf "$u/health" >/dev/null && break; sleep 0.2; done
    curl -sf "$u/health" >/dev/null || { echo "$u did not start; logs are in $WORK/logs/last-run" >&2; exit 1; }
  done
  (cd "$ROOT/adapters/payments" && exec python3 -u adapter.py financier --msp "$CITI_MSP" --gateway $CITI $(login_arg citi)) >"$TMP/citi-adapter.log" 2>&1 &
  PIDS+=($!)
  (cd "$ROOT/adapters/payments" && exec python3 -u adapter.py operator --gateway $OP $(login_arg npci)) >"$TMP/op-adapter.log" 2>&1 &
  PIDS+=($!)
  sleep 1
}
