#!/usr/bin/env bash
# One-time setup for running RASID on NPCI's DRUNIX: fetches DRUNIX at the
# commit RASID is tested with, puts its command-line tools in
# $DRUNIX_HOME/drunix-network/bin and pulls its container images.
# Needs git, Go, make and Docker (Docker Desktop on a Mac) running.
#   bash network/drunix/setup.sh
set -euo pipefail
source "$(dirname "$0")/env.sh"
DRUNIX_COMMIT=${DRUNIX_COMMIT:-8090e0c}
TOOLS="peer configtxgen configtxlator osnadmin cryptogen discover"
BIN="$DRUNIX_HOME/drunix-network/bin"
step() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }

step "DRUNIX source in $DRUNIX_HOME"
if [ ! -d "$DRUNIX_HOME/.git" ]; then
  git clone https://github.com/npci/drunix.git "$DRUNIX_HOME"
fi
if [ "$(git -C "$DRUNIX_HOME" rev-parse --short=7 HEAD)" != "$DRUNIX_COMMIT" ]; then
  if [ -n "$(git -C "$DRUNIX_HOME" status --porcelain --untracked-files=no)" ]; then
    echo "  $DRUNIX_HOME has local changes; leaving it on $(git -C "$DRUNIX_HOME" rev-parse --short HEAD)"
  else
    git -C "$DRUNIX_HOME" fetch -q origin
    git -C "$DRUNIX_HOME" checkout -q "$DRUNIX_COMMIT"
  fi
fi
echo "  at $(git -C "$DRUNIX_HOME" rev-parse --short HEAD)"

step "Command-line tools in $BIN"
mkdir -p "$BIN"
missing=""
for t in $TOOLS; do [ -x "$BIN/$t" ] || missing="$missing $t"; done
if [ -n "$missing" ]; then
  if [ "$(uname -s)" = Linux ] && [ -z "${DRUNIX_BUILD:-}" ]; then
    # On Linux, copy them out of the released images instead of compiling:
    # the peer CLI from DRUNIX's peer image, the rest from Fabric 2.5's tools.
    docker pull -q npcioss/drunix-peer:1.0.0 >/dev/null
    docker pull -q hyperledger/fabric-tools:2.5 >/dev/null
    c=$(docker create npcioss/drunix-peer:1.0.0)
    docker cp "$c:/usr/local/bin/peer" "$BIN/peer"
    docker rm "$c" >/dev/null
    c=$(docker create hyperledger/fabric-tools:2.5)
    for t in configtxgen configtxlator osnadmin cryptogen discover; do docker cp "$c:/usr/local/bin/$t" "$BIN/$t"; done
    docker rm "$c" >/dev/null
  else
    # Elsewhere (macOS), build them from the DRUNIX source as its README says.
    # Go fetches the Go version DRUNIX asks for if yours is older.
    (cd "$DRUNIX_HOME" && GOTOOLCHAIN=${GOTOOLCHAIN:-auto} make $TOOLS)
    for t in $TOOLS; do cp "$DRUNIX_HOME/build/bin/$t" "$BIN/$t"; done
  fi
fi
for t in $TOOLS; do [ -x "$BIN/$t" ] || { echo "  missing $BIN/$t" >&2; exit 1; }; done
echo "  $TOOLS"
# DRUNIX's network.sh calls docker-compose; newer Docker only has "docker compose".
if ! command -v docker-compose >/dev/null 2>&1; then
  printf '#!/bin/sh\nexec docker compose "$@"\n' >"$BIN/docker-compose"
  chmod +x "$BIN/docker-compose"
  echo "  added a docker-compose shim for \"docker compose\""
fi

step "Container images"
# DRUNIX publishes linux/amd64 images; Docker Desktop on Apple silicon runs them emulated.
for i in npcioss/drunix-peer:1.0.0 npcioss/drunix-orderer:1.0.0 npcioss/drunix-vscc:1.0.0 \
  yugabytedb/yugabyte:2025.2.0.0-b131 eqalpha/keydb busybox; do
  docker image inspect "$i" >/dev/null 2>&1 || docker pull -q "$i" >/dev/null
  echo "  $i"
done
echo
echo "Ready. Next: bash network/drunix/up.sh"
