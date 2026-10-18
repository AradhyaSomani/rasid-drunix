# Helpers for the peer CLI against the local RASID network. Source after up.sh.
WORK=${WORK:-/tmp/rasid-net}
export FABRIC_CFG_PATH=$WORK/config CORE_PEER_TLS_ENABLED=true
export ORDERER_CA=$WORK/crypto/ordererOrganizations/orderer.rasid/orderers/orderer0.orderer.rasid/tls/ca.crt

org_domain() { echo "$1.rasid"; }
org_msp() { case $1 in npci) echo NPCIMSP ;; banka) echo BankAMSP ;; citi) echo CitiMSP ;; esac; }
org_port() { case $1 in npci) echo 7051 ;; banka) echo 8051 ;; citi) echo 9051 ;; esac; }
peer_tls_ca() { echo "$WORK/crypto/peerOrganizations/$1.rasid/peers/peer0.$1.rasid/tls/ca.crt"; }

# as_admin <org>: act as that org's admin (channel join, chaincode lifecycle).
as_admin() {
  export CORE_PEER_LOCALMSPID=$(org_msp "$1") CORE_PEER_ADDRESS=localhost:$(org_port "$1")
  export CORE_PEER_TLS_ROOTCERT_FILE=$(peer_tls_ca "$1")
  export CORE_PEER_MSPCONFIGPATH=$WORK/crypto/peerOrganizations/$1.rasid/users/Admin@$1.rasid/msp
}

# as_identity <org> <name>: act as an identity made by deploy.sh (ids/<name>/msp).
as_identity() {
  as_admin "$1"
  export CORE_PEER_MSPCONFIGPATH=$WORK/ids/$2/msp
}

# all_peers: --peerAddresses/--tlsRootCertFiles for every org, for commit and invoke.
all_peers() {
  for o in npci banka citi; do printf -- '--peerAddresses localhost:%s --tlsRootCertFiles %s ' "$(org_port $o)" "$(peer_tls_ca $o)"; done
}
