#!/usr/bin/env bash
# Runs the smoke benchmark against RASID on NPCI's DRUNIX test network
# (network/drunix up.sh and deploy.sh first). Needs Node.js 18+ and npm.
# The report lands in benchmarks/caliper/report.html.
#   bash benchmarks/caliper/run-drunix.sh
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/../../network/drunix/env.sh"
cd "$HERE"
bash networks/make-drunix.sh

echo "Trusting a fresh mock IRP key on the ledger (as the NPCI operator)"
(cd "$ROOT/tools/mock-irp" && go run . keygen -out "$HERE/keys") >/dev/null
as_identity 1 operator
ARGS=$(jq -cn --rawfile k keys/irp-public.pem '{function:"InvoiceRegistry:SetIRPPublicKey",Args:["irp-2026",$k]}')
# shellcheck disable=SC2046
peer chaincode invoke $(orderer_args) -C "$CHANNEL" -n "$CC_NAME" $(both_lite_peers) --waitForEvent=false -c "$ARGS" 2>&1 | tail -1
sleep 4

if [ ! -d node_modules/@hyperledger/caliper-cli ] || [ ! -d node_modules/@hyperledger/fabric-gateway ]; then
  # Saved to a local package.json (git-ignored): "caliper bind" runs npm
  # install, which would otherwise prune an unsaved caliper-cli.
  [ -f package.json ] || echo '{"private":true}' >package.json
  npm install --no-audit --no-fund @hyperledger/caliper-cli@0.6.0
  npx caliper bind --caliper-bind-sut fabric:fabric-gateway
fi
npx caliper launch manager --caliper-workspace . \
  --caliper-networkconfig networks/drunix.yaml --caliper-benchconfig benchmark-drunix.yaml \
  --caliper-flow-only-test --caliper-fabric-gateway-enabled
