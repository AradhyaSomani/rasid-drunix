# shellcheck shell=bash
# Shared settings for the RASID scripts on NPCI's DRUNIX test network.
# DRUNIX_HOME is your clone of github.com/npci/drunix (default ~/drunix).
DRUNIX_HOME=${DRUNIX_HOME:-$HOME/drunix}
NET="$DRUNIX_HOME/drunix-network/test-network"
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
WORK=${WORK:-/tmp/rasid-drunix}
CHANNEL=${CHANNEL:-mychannel}
CC_NAME=rasid
CC_VERSION=${CC_VERSION:-0.2.0}
export PATH="$DRUNIX_HOME/drunix-network/bin:$PATH"
export FABRIC_CFG_PATH="$DRUNIX_HOME/drunix-network/config"
export CORE_PEER_TLS_ENABLED=true
ORDERER_CA="$NET/organizations/ordererOrganizations/example.com/tlsca/tlsca.example.com-cert.pem"
# RASID's parties on the two DRUNIX organisations: Org1MSP is NPCI (network
# operator and AI risk oracle); Org2MSP is Citi, which runs a supply-chain
# finance programme, so it enrols the MSME supplier and the corporate buyer
# and finances their invoices.
OPERATOR_MSP=Org1MSP
POLICY="AND('Org1MSP.peer','Org2MSP.peer')"

org_dir() { echo "$NET/organizations/peerOrganizations/org$1.example.com"; }
tls_ca() { echo "$(org_dir "$1")/tlsca/tlsca.org$1.example.com-cert.pem"; }
# Lite peers (endorsers) listen on 7051 and 9051, committing peers on 7061 and 9061.
lite_peer() { case $1 in 1) echo localhost:7051 ;; 2) echo localhost:9051 ;; esac; }
committing_peer() { case $1 in 1) echo localhost:7061 ;; 2) echo localhost:9061 ;; esac; }
as_admin() { # org [peer address]
  export CORE_PEER_LOCALMSPID=Org$1MSP
  export CORE_PEER_TLS_ROOTCERT_FILE
  CORE_PEER_TLS_ROOTCERT_FILE=$(tls_ca "$1")
  export CORE_PEER_MSPCONFIGPATH
  CORE_PEER_MSPCONFIGPATH="$(org_dir "$1")/users/Admin@org$1.example.com/msp"
  export CORE_PEER_ADDRESS=${2:-$(lite_peer "$1")}
}
as_identity() { # org name
  as_admin "$1"
  CORE_PEER_MSPCONFIGPATH="$WORK/ids/$2/msp"
}
both_lite_peers() {
  echo --peerAddresses "$(lite_peer 1)" --tlsRootCertFiles "$(tls_ca 1)" --peerAddresses "$(lite_peer 2)" --tlsRootCertFiles "$(tls_ca 2)"
}
orderer_args() { echo -o localhost:7050 --ordererTLSHostnameOverride orderer.example.com --tls --cafile "$ORDERER_CA"; }

# restart_peers: restarts the lite peers, then the committing peers, and waits
# for each lite peer to reconnect. A DRUNIX 1.0.0 committing peer keeps a
# config listener for every lite-peer connection it has had. After a lite peer
# restarts, the old listener has no reader, and the next block holding two
# lifecycle transactions (or two config updates) blocks the committing peer for
# good: it stops committing about ten blocks later. Restarting the committing
# peers last clears the old listeners. See docs/drunix/findings.md.
restart_peers() {
  local t c
  docker restart lp1.org1 lp1.org2 >/dev/null
  t=$(date +%s)
  docker restart cp.org1 cp.org2 >/dev/null
  for c in lp1.org1 lp1.org2; do
    for _ in $(seq 1 90); do
      docker logs --since "$t" "$c" 2>&1 | grep -q "Connected to committing peer" && break
      sleep 1
    done
  done
}
