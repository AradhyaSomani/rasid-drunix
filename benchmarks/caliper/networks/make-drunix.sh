#!/usr/bin/env bash
# Writes a Caliper network config for NPCI's DRUNIX test network
# (network/drunix), pointing at the identities deploy.sh issued. Caliper talks
# to Org2MSP's lite peer, which endorses on both organisations' lite peers and
# hands the transaction to the orderer; the committing peers commit it.
# Output: networks/drunix.yaml
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/../../../network/drunix/env.sh"
cat >"$HERE/connection-drunix-org2.yaml" <<YAML
name: drunix-org2
version: 1.0.0
client: { organization: Org2 }
organizations:
  Org2: { mspid: Org2MSP, peers: [peer0.org2.example.com] }
peers:
  peer0.org2.example.com:
    url: grpcs://$(lite_peer 2)
    tlsCACerts: { path: $(tls_ca 2) }
    grpcOptions: { ssl-target-name-override: peer0.org2.example.com }
YAML
cat >"$HERE/drunix.yaml" <<YAML
name: rasid-drunix
version: "2.0.0"
caliper:
  blockchain: fabric
channels:
  - channelName: $CHANNEL
    contracts: [{ id: $CC_NAME }]
organizations:
  - mspid: Org2MSP
    identities:
      certificates:
        - name: supplier1
          clientPrivateKey: { path: $WORK/ids/supplier/key.pem }
          clientSignedCert: { path: $WORK/ids/supplier/cert.pem }
        - name: financier1
          clientPrivateKey: { path: $WORK/ids/citi/key.pem }
          clientSignedCert: { path: $WORK/ids/citi/cert.pem }
    connectionProfile: { path: $HERE/connection-drunix-org2.yaml, discover: true }
YAML
echo "wrote $HERE/drunix.yaml"
