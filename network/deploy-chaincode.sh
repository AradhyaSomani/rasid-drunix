#!/usr/bin/env bash
# Deploys the RASID chaincode with the Fabric v2 lifecycle that DRUNIX inherits.
# Run once per peer org for install/approve (set the CORE_PEER_* env vars for
# that org's admin), then once for commit and init.
#
#   ./deploy-chaincode.sh package
#   ./deploy-chaincode.sh install          # each org
#   ./deploy-chaincode.sh approve          # each org
#   ./deploy-chaincode.sh commit           # one org, with --peerAddresses for every org
#   ./deploy-chaincode.sh init             # operator (NPCI) identity, role=operator
#
# Untested against a live DRUNIX network yet: check flags against the DRUNIX
# peer CLI and Falcon docs when the network is available.
set -euo pipefail

CHANNEL=${CHANNEL:-rasid}
CC_NAME=${CC_NAME:-rasid}
CC_VERSION=${CC_VERSION:-0.1.0}
CC_SEQUENCE=${CC_SEQUENCE:-1}
CC_PATH=${CC_PATH:-../chaincode/rasid}
ORDERER=${ORDERER:-orderer.npci.rasid:7050}
ORDERER_CA=${ORDERER_CA:?set ORDERER_CA to the orderer TLS CA file}
OPERATOR_MSP=${OPERATOR_MSP:-NPCIMSP}
RISK_MSPS=${RISK_MSPS:-'["NPCIMSP"]'}
# Every receivable change needs the originating org plus the network operator.
# Tighten to state-based endorsement per receivable in a later version.
POLICY=${POLICY:-"AND('NPCIMSP.peer', OR('CitiMSP.peer','BankAMSP.peer','NbfcMSP.peer'))"}
PEER_CONN=${PEER_CONN:-}   # e.g. "--peerAddresses p0.npci:7051 --tlsRootCertFiles ... --peerAddresses ..."

pkg="${CC_NAME}_${CC_VERSION}.tar.gz"

case "${1:-}" in
  package)
    (cd "$CC_PATH" && GO111MODULE=on go mod vendor)
    peer lifecycle chaincode package "$pkg" --path "$CC_PATH" --lang golang --label "${CC_NAME}_${CC_VERSION}"
    ;;
  install)
    peer lifecycle chaincode install "$pkg"
    ;;
  approve)
    PKG_ID=$(peer lifecycle chaincode calculatepackageid "$pkg")
    peer lifecycle chaincode approveformyorg -o "$ORDERER" --tls --cafile "$ORDERER_CA" \
      --channelID "$CHANNEL" --name "$CC_NAME" --version "$CC_VERSION" --sequence "$CC_SEQUENCE" \
      --package-id "$PKG_ID" --signature-policy "$POLICY" --init-required
    ;;
  commit)
    peer lifecycle chaincode checkcommitreadiness --channelID "$CHANNEL" --name "$CC_NAME" \
      --version "$CC_VERSION" --sequence "$CC_SEQUENCE" --signature-policy "$POLICY" --init-required
    # shellcheck disable=SC2086
    peer lifecycle chaincode commit -o "$ORDERER" --tls --cafile "$ORDERER_CA" --channelID "$CHANNEL" \
      --name "$CC_NAME" --version "$CC_VERSION" --sequence "$CC_SEQUENCE" \
      --signature-policy "$POLICY" --init-required $PEER_CONN
    ;;
  init)
    # shellcheck disable=SC2086
    peer chaincode invoke -o "$ORDERER" --tls --cafile "$ORDERER_CA" -C "$CHANNEL" -n "$CC_NAME" \
      --isInit $PEER_CONN \
      -c "{\"function\":\"InvoiceRegistry:InitLedger\",\"Args\":[\"$OPERATOR_MSP\",$(printf '%s' "$RISK_MSPS" | jq -c tostring)]}"
    ;;
  *)
    sed -n '2,13p' "$0"; exit 1 ;;
esac
