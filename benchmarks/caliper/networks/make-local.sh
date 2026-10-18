#!/usr/bin/env bash
# Writes a Caliper network config for the local network (network/local),
# pointing at the identities deploy.sh issued. Output: networks/local.yaml
set -euo pipefail
WORK=${WORK:-/tmp/rasid-net}
HERE=$(cd "$(dirname "$0")" && pwd)
profile() { # org msp port
  cat >"$HERE/connection-$1.yaml" <<YAML
name: $1
version: 1.0.0
client: { organization: $1 }
organizations:
  $1: { mspid: $2, peers: [peer0.$1.rasid] }
peers:
  peer0.$1.rasid:
    url: grpcs://localhost:$3
    tlsCACerts: { path: $WORK/crypto/peerOrganizations/$1.rasid/peers/peer0.$1.rasid/tls/ca.crt }
    grpcOptions: { ssl-target-name-override: localhost }
YAML
}
profile banka BankAMSP 8051
profile citi CitiMSP 9051
cat >"$HERE/local.yaml" <<YAML
name: rasid-local
version: "2.0.0"
caliper:
  blockchain: fabric
channels:
  - channelName: rasid
    contracts: [{ id: rasid }]
organizations:
  - mspid: BankAMSP
    identities:
      certificates:
        - name: supplier1
          clientPrivateKey: { path: $WORK/ids/supplier/key.pem }
          clientSignedCert: { path: $WORK/ids/supplier/cert.pem }
    connectionProfile: { path: $HERE/connection-banka.yaml, discover: true }
  - mspid: CitiMSP
    identities:
      certificates:
        - name: financier1
          clientPrivateKey: { path: $WORK/ids/citi/key.pem }
          clientSignedCert: { path: $WORK/ids/citi/cert.pem }
    connectionProfile: { path: $HERE/connection-citi.yaml, discover: true }
YAML
echo "wrote $HERE/local.yaml"
