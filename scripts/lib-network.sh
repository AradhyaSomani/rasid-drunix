# Shared by demo-network.sh and console-network.sh: starts one gateway per
# identity on the local Fabric network, the risk service (with the risk bridge
# on NPCI's risk-oracle gateway) and the settlement adapters. Source it.
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORK=${WORK:-/tmp/rasid-net}
TMP=$(mktemp -d)
PIDS=()
# Silence stderr first: macOS bash 3.2 reports each killed background job.
cleanup() { exec 2>/dev/null; kill ${PIDS[@]+"${PIDS[@]}"} || true; rm -rf "$TMP"; }
trap cleanup EXIT
OP=http://127.0.0.1:8101 RISKGW=http://127.0.0.1:8102 SUP=http://127.0.0.1:8103 BUY=http://127.0.0.1:8104 CITI=http://127.0.0.1:8105
RISK=http://127.0.0.1:8090

# CONSOLE_LOGIN=1 turns on sign-in at every gateway: one demo user each,
# password $RASID_PASSWORD (default rasid). The adapters sign in too.
PASS=${RASID_PASSWORD:-rasid}
login_arg() { if [ "${CONSOLE_LOGIN:-}" = 1 ]; then echo "--login $1:$PASS"; fi; }

gw() { # name org msp url login-user "label" [extra env...]
  local name=$1 org=$2 msp=$3 url=$4 user=$5 label=$6 users="" port; shift 6
  if [ "${CONSOLE_LOGIN:-}" = 1 ]; then users="$user:$PASS"; fi
  case $org in npci) port=7051 ;; banka) port=8051 ;; citi) port=9051 ;; esac
  env PEER_ENDPOINT=localhost:$port PEER_HOST_ALIAS=localhost \
    PEER_TLS_CA="$WORK/crypto/peerOrganizations/$org.rasid/peers/peer0.$org.rasid/tls/ca.crt" \
    MSP_ID=$msp CERT_PATH="$WORK/ids/$name/cert.pem" KEY_PATH="$WORK/ids/$name/key.pem" \
    CHANNEL=rasid CHAINCODE=rasid LISTEN=${url#http://} CORS_ORIGIN=${CORS_ORIGIN:-} \
    LOGIN_USERS="$users" LOGIN_LABEL="$label" "$@" \
    "$TMP/gateway" >"$TMP/gw-$name.log" 2>&1 &
  PIDS+=($!)
}

start_stack() { # [extra env for the operator gateway...]
  (cd "$ROOT/app/gateway" && go build -o "$TMP/gateway" .)
  (cd "$ROOT/tools/mock-irp" && go build -o "$TMP/mock-irp" .)
  (cd "$ROOT/ai/risk-service" && exec python3 -m rasid_risk.server --port "${RISK##*:}") >"$TMP/risk.log" 2>&1 &
  PIDS+=($!)
  gw operator npci NPCIMSP $OP npci "NPCI operator" "$@"
  gw risk npci NPCIMSP $RISKGW risk "AI risk desk" RISK_URL=$RISK
  gw supplier banka BankAMSP $SUP msme "MSME supplier"
  gw buyer banka BankAMSP $BUY buyer "Corporate buyer"
  gw citi citi CitiMSP $CITI citi "Citi"
  local u
  for u in $OP $RISKGW $SUP $BUY $CITI $RISK; do
    for _ in $(seq 1 50); do curl -sf "$u/health" >/dev/null && break; sleep 0.2; done
    curl -sf "$u/health" >/dev/null || { echo "$u did not start; see $TMP" >&2; exit 1; }
  done
  (cd "$ROOT/adapters/payments" && exec python3 -u adapter.py financier --msp CitiMSP --gateway $CITI $(login_arg citi)) >"$TMP/citi-adapter.log" 2>&1 &
  PIDS+=($!)
  (cd "$ROOT/adapters/payments" && exec python3 -u adapter.py operator --gateway $OP $(login_arg npci)) >"$TMP/op-adapter.log" 2>&1 &
  PIDS+=($!)
  sleep 1
}
