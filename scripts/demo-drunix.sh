#!/usr/bin/env bash
# Runs the RASID flow on NPCI's DRUNIX test network: the same steps as
# demo-network.sh, with every party acting through its own gateway and
# certificate on DRUNIX's lite peers (NPCI on Org1MSP; Citi, and the supplier
# and buyer it enrols, on Org2MSP). Run network/drunix/setup.sh, up.sh and
# deploy.sh first.
#   bash scripts/demo-drunix.sh
RASID_NET=drunix exec bash "$(dirname "$0")/demo-network.sh" "$@"
