#!/usr/bin/env bash
# Deploys the RASID chaincode to NPCI's DRUNIX test network as a service
# (ccaas), issues the demo identities with RASID roles, and runs InitLedger.
# Run network/drunix/up.sh first.
#   bash network/drunix/deploy.sh
set -euo pipefail
source "$(dirname "$0")/env.sh"
SELLER=${SELLER:-27AAACM1234A1Z5}
BUYER=${BUYER:-29AABCB5678B1Z2}
mkdir -p "$WORK/logs" "$WORK/cc" "$WORK/ids"
LOG="$WORK/logs/deploy.log"
: >"$LOG"
step() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
fail() { echo "failed: $*; see $LOG" >&2; tail -15 "$LOG" >&2; exit 1; }

step "Building the chaincode image (linux/amd64)"
(cd "$ROOT/chaincode/rasid" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o rasid-cc . \
  && docker build -q --platform linux/amd64 -t rasid-cc:"$CC_VERSION" . >>"$LOG" && rm -f rasid-cc)
(cd "$ROOT/tools/issue-identity" && go build -o "$WORK/issue-identity" .)

step "Packaging and installing on the lite peers"
cd "$WORK/cc"
# Keep one package per version: tar stamps the time, and a new package ID
# for the same version would need a fresh approval.
if [ ! -f "rasid-$CC_VERSION.tar.gz" ]; then
  printf '{"address":"rasid-cc:9999","dial_timeout":"10s","tls_required":false}' >connection.json
  printf '{"type":"ccaas","label":"%s_%s"}' "$CC_NAME" "$CC_VERSION" >metadata.json
  COPYFILE_DISABLE=1 tar czf code.tar.gz connection.json
  COPYFILE_DISABLE=1 tar czf "rasid-$CC_VERSION.tar.gz" metadata.json code.tar.gz
fi
cp "rasid-$CC_VERSION.tar.gz" rasid.tar.gz
PKG_ID=$(peer lifecycle chaincode calculatepackageid rasid.tar.gz)
# On DRUNIX only lite peers run chaincode; committing peers refuse installs.
for org in 1 2; do
  as_admin $org
  if ! peer lifecycle chaincode queryinstalled --output json 2>>"$LOG" | grep -q "$PKG_ID"; then
    peer lifecycle chaincode install rasid.tar.gz >>"$LOG" 2>&1 || fail "install on Org${org}MSP's lite peer"
  fi
  echo "  installed on Org${org}MSP's lite peer $(lite_peer $org)"
done

step "Starting the chaincode service $PKG_ID"
docker rm -f rasid-cc >/dev/null 2>&1 || true
docker run -d --name rasid-cc --network drunix_test -e CHAINCODE_ID="$PKG_ID" -e CHAINCODE_SERVER_ADDRESS=0.0.0.0:9999 \
  rasid-cc:"$CC_VERSION" >>"$LOG"

step "Approving and committing (endorsement policy $POLICY)"
SEQ=1
as_admin 1
COMMITTED=$(peer lifecycle chaincode querycommitted -C "$CHANNEL" -n "$CC_NAME" --output json 2>/dev/null | sed -n 's/.*"sequence": *\([0-9]*\).*/\1/p' || true)
[ -n "$COMMITTED" ] && SEQ=$((COMMITTED + 1))
# No --init-required: on DRUNIX the lifecycle's "initialised" marker is not
# JSON, and the committing peers' YugabyteDB state store cannot hold it.
# InitLedger is an ordinary transaction instead, and it refuses to run twice.
INIT=""
# Approve one organisation at a time, each in its own block: a DRUNIX 1.0.0
# committing peer can deadlock on a block holding two lifecycle transactions
# (see restart_peers in env.sh). Lite peers keep no ledger, so they cannot
# stream block events back to the CLI (--waitForEvent); poll instead.
for org in 1 2; do
  as_admin $org
  # shellcheck disable=SC2046
  peer lifecycle chaincode approveformyorg $(orderer_args) --channelID "$CHANNEL" --name "$CC_NAME" --version "$CC_VERSION" \
    --sequence "$SEQ" --package-id "$PKG_ID" --signature-policy "$POLICY" $INIT --waitForEvent=false >>"$LOG" 2>&1 || fail "approve for Org${org}MSP"
  APPROVED=""
  for _ in $(seq 1 30); do
    # shellcheck disable=SC2046
    peer lifecycle chaincode checkcommitreadiness --channelID "$CHANNEL" --name "$CC_NAME" --version "$CC_VERSION" --sequence "$SEQ" \
      --signature-policy "$POLICY" $INIT --output json 2>>"$LOG" | grep -q "\"Org${org}MSP\": true" && { APPROVED=1; break; }
    sleep 2
  done
  [ -n "$APPROVED" ] || fail "approval for Org${org}MSP did not commit"
  echo "  approved for Org${org}MSP"
done
as_admin 1
# shellcheck disable=SC2046
peer lifecycle chaincode commit $(orderer_args) --channelID "$CHANNEL" --name "$CC_NAME" --version "$CC_VERSION" --sequence "$SEQ" \
  --signature-policy "$POLICY" $INIT $(both_lite_peers) --waitForEvent=false >>"$LOG" 2>&1 || fail "commit"
for _ in $(seq 1 30); do
  peer lifecycle chaincode querycommitted -C "$CHANNEL" -n "$CC_NAME" --output json 2>>"$LOG" | grep -q "\"sequence\": *$SEQ" && break
  sleep 2
done
echo "  committed $CC_NAME $CC_VERSION, sequence $SEQ"

# The committing peer tells each lite peer about the new definition. If a lite
# peer still calls the chaincode unknown, restart the peers so it reloads.
as_admin 1
KNOWN=""
for _ in $(seq 1 15); do
  peer chaincode query -C "$CHANNEL" -n "$CC_NAME" -c '{"function":"org.hyperledger.fabric:GetMetadata","Args":[]}' >/dev/null 2>>"$LOG" && { KNOWN=1; break; }
  sleep 2
done
if [ -z "$KNOWN" ]; then
  echo "  lite peers have not picked up the definition; restarting the peers"
  restart_peers
fi
for org in 1 2; do
  as_admin $org
  for _ in $(seq 1 30); do peer channel getinfo -c "$CHANNEL" >/dev/null 2>&1 && break; sleep 2; done
done

step "Issuing RASID identities"
issue() { # org name attrs...
  local org=$1 name=$2 a args=(); shift 2
  for a in "$@"; do args+=(-attr "$a"); done
  rm -rf "$WORK/ids/$name"
  "$WORK/issue-identity" -ca "$(org_dir "$org")/ca" -name "$name" -out "$WORK/ids/$name" "${args[@]}" >>"$LOG"
  mkdir -p "$WORK/ids/$name/msp/signcerts" "$WORK/ids/$name/msp/keystore"
  cp "$WORK/ids/$name/cert.pem" "$WORK/ids/$name/msp/signcerts/"
  cp "$WORK/ids/$name/key.pem" "$WORK/ids/$name/msp/keystore/"
  cp -r "$(org_dir "$org")/msp/cacerts" "$(org_dir "$org")/msp/tlscacerts" "$(org_dir "$org")/msp/config.yaml" "$WORK/ids/$name/msp/"
  echo "  $name: Org${org}MSP $*"
}
issue 1 operator rasid.role=operator
issue 1 risk rasid.role=risk_oracle
issue 2 supplier rasid.role=supplier rasid.gstin="$SELLER"
issue 2 buyer rasid.role=buyer rasid.gstin="$BUYER"
issue 2 citi rasid.role=financier

step "Initialising the ledger as the NPCI operator"
as_identity 1 operator
for _ in $(seq 1 10); do
  # shellcheck disable=SC2046
  OUT=$(peer chaincode invoke $(orderer_args) -C "$CHANNEL" -n "$CC_NAME" $(both_lite_peers) --waitForEvent=false \
    -c "{\"function\":\"InvoiceRegistry:InitLedger\",\"Args\":[\"$OPERATOR_MSP\",\"[\\\"$OPERATOR_MSP\\\"]\"]}" 2>&1) && { INITOK=1; break; }
  echo "$OUT" >>"$LOG"
  echo "$OUT" | grep -q "already initialised" && { INITOK=1; echo "  already initialised"; break; }
  sleep 3
done
[ -n "${INITOK:-}" ] || fail "InitLedger"
for _ in $(seq 1 20); do
  peer chaincode query -C "$CHANNEL" -n "$CC_NAME" -c '{"function":"InvoiceRegistry:ListReceivables","Args":["","1",""]}' >>"$LOG" 2>&1 && break
  sleep 2
done
echo
echo "RASID is deployed on DRUNIX. Identities are in $WORK/ids."
