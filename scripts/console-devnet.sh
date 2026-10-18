#!/usr/bin/env bash
# Opens the web console on the devnet with the AI risk service switched on, so
# every new invoice is scored by the AI as soon as it is registered, as on a
# live network. Ctrl-C stops it.
#   bash scripts/console-devnet.sh        then open http://localhost:8080
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
PORT=${DEVNET_PORT:-8080}
RISK=http://127.0.0.1:${RISK_PORT:-8090}
PIDS=()
# Silence stderr first: macOS bash 3.2 reports each killed background job.
cleanup() { exec 2>/dev/null; kill ${PIDS[@]+"${PIDS[@]}"} || true; rm -rf "$WORK"; }
trap cleanup EXIT

(cd "$ROOT/app/devnet" && go build -o "$WORK/devnet" .)
(cd "$ROOT/ai/risk-service" && exec python3 -m rasid_risk.server --port "${RISK##*:}") >"$WORK/risk.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 50); do curl -sf "$RISK/health" >/dev/null && break; sleep 0.2; done
curl -sf "$RISK/health" >/dev/null || { echo "the AI risk service did not start; see $WORK/risk.log" >&2; exit 1; }
(cd "$ROOT/app/devnet" && exec "$WORK/devnet" -listen ":$PORT" -risk-url "$RISK") & PIDS+=($!)
sleep 1
echo
echo "Console: http://localhost:$PORT"
echo "Sign in as npci, risk, msme, buyer, citi or nbfc, password rasid (one browser tab each)."
echo "The AI risk service scores each new invoice. Ctrl-C stops everything."
wait
