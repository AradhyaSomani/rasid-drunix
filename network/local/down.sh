#!/usr/bin/env bash
# Stops the local RASID network processes started by up.sh and deploy.sh.
WORK=${WORK:-/tmp/rasid-net}
[ -f "$WORK/pids" ] && xargs kill <"$WORK/pids" 2>/dev/null
rm -f "$WORK/pids"
echo "stopped"
