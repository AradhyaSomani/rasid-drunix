#!/usr/bin/env bash
# Opens the web console on NPCI's DRUNIX test network. Each demo user signs in
# at the gateway that holds its certificate. Run network/drunix/setup.sh, up.sh
# and deploy.sh first. Ctrl-C stops it.
#   bash scripts/console-drunix.sh   then open http://127.0.0.1:8101
RASID_NET=drunix exec bash "$(dirname "$0")/console-network.sh" "$@"
