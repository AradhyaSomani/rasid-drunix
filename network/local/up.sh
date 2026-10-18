#!/usr/bin/env bash
# Starts a local 3-org Fabric network for RASID as plain processes (no Docker):
# one Raft orderer, one peer each for NPCI, BankA and Citi, channel "rasid".
#
# Needs on PATH: peer, orderer, configtxgen, cryptogen, osnadmin (Fabric 2.5),
# and FABRIC_CONFIG_DIR pointing at Fabric's sample core.yaml and orderer.yaml.
# Without the release download, build them from source:
#   GOBIN=$HOME/fabric-bin go install github.com/hyperledger/fabric/cmd/{peer,orderer,configtxgen,cryptogen,osnadmin}@release-2.5
#   (FABRIC_CONFIG_DIR is then the module's sampleconfig folder)
#
#   WORK=/tmp/rasid-net bash up.sh      then bash deploy.sh; bash down.sh to stop
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${WORK:-/tmp/rasid-net}
: "${FABRIC_CONFIG_DIR:?set FABRIC_CONFIG_DIR to the folder with core.yaml and orderer.yaml}"

if [ -f "$WORK/pids" ]; then bash "$HERE/down.sh"; fi
rm -rf "$WORK" && mkdir -p "$WORK"/{logs,config,data}
cd "$WORK"

cryptogen generate --config="$HERE/crypto-config.yaml" --output=crypto >/dev/null
cp "$HERE/configtx.yaml" .
configtxgen -configPath . -profile RasidChannel -channelID rasid -outputBlock rasid.block 2>logs/configtxgen.log

# Peers find the chaincode-as-a-service builder here (copied so it is executable).
cp -r "$HERE/builders" builders && chmod +x builders/ccaas/bin/*
sed "s#/opt/hyperledger/ccaas_builder#$WORK/builders/ccaas#" "$FABRIC_CONFIG_DIR/core.yaml" > config/core.yaml
cp "$FABRIC_CONFIG_DIR/orderer.yaml" config/

O=$WORK/crypto/ordererOrganizations/orderer.rasid/orderers/orderer0.orderer.rasid
FABRIC_CFG_PATH=$WORK/config \
ORDERER_GENERAL_LISTENADDRESS=0.0.0.0 ORDERER_GENERAL_LISTENPORT=7050 \
ORDERER_GENERAL_LOCALMSPID=OrdererMSP ORDERER_GENERAL_LOCALMSPDIR=$O/msp \
ORDERER_GENERAL_TLS_ENABLED=true ORDERER_GENERAL_TLS_PRIVATEKEY=$O/tls/server.key \
ORDERER_GENERAL_TLS_CERTIFICATE=$O/tls/server.crt ORDERER_GENERAL_TLS_ROOTCAS="[$O/tls/ca.crt]" \
ORDERER_GENERAL_CLUSTER_CLIENTCERTIFICATE=$O/tls/server.crt ORDERER_GENERAL_CLUSTER_CLIENTPRIVATEKEY=$O/tls/server.key \
ORDERER_GENERAL_CLUSTER_ROOTCAS="[$O/tls/ca.crt]" ORDERER_GENERAL_BOOTSTRAPMETHOD=none \
ORDERER_CHANNELPARTICIPATION_ENABLED=true ORDERER_ADMIN_LISTENADDRESS=127.0.0.1:7053 \
ORDERER_ADMIN_TLS_ENABLED=true ORDERER_ADMIN_TLS_CERTIFICATE=$O/tls/server.crt ORDERER_ADMIN_TLS_PRIVATEKEY=$O/tls/server.key \
ORDERER_ADMIN_TLS_ROOTCAS="[$O/tls/ca.crt]" ORDERER_ADMIN_TLS_CLIENTROOTCAS="[$O/tls/ca.crt]" \
ORDERER_OPERATIONS_LISTENADDRESS=127.0.0.1:9440 ORDERER_METRICS_PROVIDER=disabled \
ORDERER_FILELEDGER_LOCATION=$WORK/data/orderer ORDERER_CONSENSUS_WALDIR=$WORK/data/orderer/wal \
ORDERER_CONSENSUS_SNAPDIR=$WORK/data/orderer/snap \
  nohup orderer >logs/orderer.log 2>&1 &
echo $! >>pids

start_peer() { # domain msp port operations-port
  local d=$1 msp=$2 port=$3 ops=$4 P=$WORK/crypto/peerOrganizations/$1/peers/peer0.$1
  FABRIC_CFG_PATH=$WORK/config \
  CORE_PEER_ID=peer0.$d CORE_PEER_ADDRESS=localhost:$port CORE_PEER_LISTENADDRESS=0.0.0.0:$port \
  CORE_PEER_CHAINCODEADDRESS=localhost:$((port + 1)) CORE_PEER_CHAINCODELISTENADDRESS=0.0.0.0:$((port + 1)) \
  CORE_PEER_GOSSIP_EXTERNALENDPOINT=localhost:$port CORE_PEER_GOSSIP_BOOTSTRAP=localhost:$port \
  CORE_PEER_LOCALMSPID=$msp CORE_PEER_MSPCONFIGPATH=$P/msp \
  CORE_PEER_TLS_ENABLED=true CORE_PEER_TLS_CERT_FILE=$P/tls/server.crt CORE_PEER_TLS_KEY_FILE=$P/tls/server.key \
  CORE_PEER_TLS_ROOTCERT_FILE=$P/tls/ca.crt CORE_PEER_FILESYSTEMPATH=$WORK/data/$d \
  CORE_OPERATIONS_LISTENADDRESS=127.0.0.1:$ops CORE_METRICS_PROVIDER=disabled \
  CORE_LEDGER_STATE_STATEDATABASE=goleveldb CORE_PEER_GOSSIP_RECONNECTINTERVAL=3s \
    nohup peer node start >logs/peer-$d.log 2>&1 &
  echo $! >>pids
}
start_peer npci.rasid NPCIMSP 7051 9444
start_peer banka.rasid BankAMSP 8051 9445
start_peer citi.rasid CitiMSP 9051 9446

wait_port() { for _ in $(seq 1 100); do (echo >/dev/tcp/127.0.0.1/"$1") 2>/dev/null && return 0; sleep 0.2; done; echo "port $1 never opened" >&2; exit 1; }
for p in 7050 7053 7051 8051 9051; do wait_port $p; done

osnadmin channel join --channelID rasid --config-block rasid.block -o localhost:7053 \
  --ca-file "$O/tls/ca.crt" --client-cert "$O/tls/server.crt" --client-key "$O/tls/server.key" >logs/osnadmin.log

source "$HERE/env.sh"
for org in npci banka citi; do
  as_admin $org
  for _ in $(seq 1 20); do peer channel join -b rasid.block >>logs/join.log 2>&1 && break; sleep 0.5; done
done
# Peers that joined before the others reject them at first and retry later;
# endorsement across orgs fails until every peer sees the other two.
echo "waiting for the peers to find each other"
sees_others() { sed 's/\x1b\[[0-9;]*m//g' "logs/peer-$1.log" | grep "current view" | tail -1 | sed 's/.*current view://' | grep -o 'localhost:[0-9]*' | sort -u | wc -l; }
for _ in $(seq 1 120); do
  n=0; for d in npci.rasid banka.rasid citi.rasid; do [ "$(sees_others $d)" -ge 2 ] && n=$((n + 1)); done
  [ $n -eq 3 ] && break; sleep 0.5
done
[ $n -eq 3 ] || echo "warning: peers have not all found each other yet; give it a minute before the demo" >&2
echo "RASID network up in $WORK (orderer :7050, peers NPCI :7051, BankA :8051, Citi :9051)"
