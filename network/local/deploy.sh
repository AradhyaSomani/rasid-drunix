#!/usr/bin/env bash
# Deploys the RASID chaincode to the local network as a service (ccaas),
# issues demo identities with RASID attributes, and runs InitLedger.
#   WORK=/tmp/rasid-net bash deploy.sh
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
WORK=${WORK:-/tmp/rasid-net}
source "$HERE/env.sh"
CC_ADDR=${CC_ADDR:-localhost:9999}
POLICY="AND('NPCIMSP.peer', OR('BankAMSP.peer','CitiMSP.peer'))"
SELLER=${SELLER:-27AAACM1234A1Z5}
BUYER=${BUYER:-29AABCB5678B1Z2}
cd "$WORK"

echo "building chaincode and tools"
(cd "$ROOT/chaincode/rasid" && go build -o "$WORK/rasid-cc" .)
(cd "$ROOT/tools/issue-identity" && go build -o "$WORK/issue-identity" .)

mkdir -p cc && cd cc
printf '{"address":"%s","dial_timeout":"10s","tls_required":false}' "$CC_ADDR" >connection.json
printf '{"type":"ccaas","label":"rasid_0.1.0"}' >metadata.json
# COPYFILE_DISABLE stops macOS tar adding ._ resource files to the package.
COPYFILE_DISABLE=1 tar czf code.tar.gz connection.json && COPYFILE_DISABLE=1 tar czf rasid.tar.gz metadata.json code.tar.gz
cd "$WORK"

echo "installing on every peer"
for org in npci banka citi; do as_admin $org; peer lifecycle chaincode install cc/rasid.tar.gz >>logs/deploy.log 2>&1; done
PKG_ID=$(peer lifecycle chaincode calculatepackageid cc/rasid.tar.gz)

echo "starting chaincode server $PKG_ID"
CHAINCODE_ID=$PKG_ID CHAINCODE_SERVER_ADDRESS=$CC_ADDR nohup ./rasid-cc >logs/chaincode.log 2>&1 &
echo $! >>pids

echo "approving and committing"
for org in npci banka citi; do
  as_admin $org
  peer lifecycle chaincode approveformyorg -o localhost:7050 --tls --cafile "$ORDERER_CA" --channelID rasid \
    --name rasid --version 0.1.0 --sequence 1 --package-id "$PKG_ID" --signature-policy "$POLICY" --init-required \
    --waitForEvent >>logs/deploy.log 2>&1
done
as_admin npci
# shellcheck disable=SC2046
peer lifecycle chaincode commit -o localhost:7050 --tls --cafile "$ORDERER_CA" --channelID rasid --name rasid \
  --version 0.1.0 --sequence 1 --signature-policy "$POLICY" --init-required $(all_peers) >>logs/deploy.log 2>&1

echo "issuing identities"
issue() { # org name attrs...
  local org=$1 name=$2; shift 2
  local args=() a; for a in "$@"; do args+=(-attr "$a"); done
  ./issue-identity -ca crypto/peerOrganizations/$org.rasid/ca -name "$name" -out "ids/$name" "${args[@]}"
  mkdir -p "ids/$name/msp/signcerts" "ids/$name/msp/keystore"
  cp "ids/$name/cert.pem" "ids/$name/msp/signcerts/" && cp "ids/$name/key.pem" "ids/$name/msp/keystore/"
  cp -r crypto/peerOrganizations/$org.rasid/msp/{cacerts,tlscacerts,config.yaml} "ids/$name/msp/"
}
issue npci operator rasid.role=operator
issue npci risk rasid.role=risk_oracle
issue banka supplier rasid.role=supplier rasid.gstin=$SELLER
issue banka buyer rasid.role=buyer rasid.gstin=$BUYER
issue citi citi rasid.role=financier

echo "initialising the ledger as the NPCI operator"
as_identity npci operator
# shellcheck disable=SC2046
peer chaincode invoke -o localhost:7050 --tls --cafile "$ORDERER_CA" -C rasid -n rasid --isInit --waitForEvent $(all_peers) \
  -c '{"function":"InvoiceRegistry:InitLedger","Args":["NPCIMSP","[\"NPCIMSP\"]"]}' >>logs/deploy.log 2>&1
echo "RASID chaincode deployed; identities in $WORK/ids"
