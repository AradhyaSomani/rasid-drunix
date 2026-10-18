#!/usr/bin/env bash
# Opens the web console on the local Fabric network. Each demo user signs in
# at the gateway that holds its organisation's certificate, so every role acts
# through its own identity. Ctrl-C stops it.
#   WORK=/tmp/rasid-net bash scripts/console-network.sh   then open http://127.0.0.1:8101
# RASID_PASSWORD sets the password (default rasid); a custom one is not shown on the page.
set -euo pipefail
CONSOLE_LOGIN=1
source "$(dirname "$0")/lib-network.sh"
mkdir -p "$TMP/web"
cp "$ROOT/app/web/index.html" "$TMP/web/"
DEMO=""
if [ -z "${RASID_PASSWORD:-}" ]; then DEMO=",\"demoPassword\":\"$PASS\""; fi
cat >"$TMP/web/gateways.json" <<JSON
{"gateways":["$OP","$RISKGW","$SUP","$BUY","$CITI"]$DEMO}
JSON
CORS_ORIGIN='*' start_stack WEB_DIR="$TMP/web"
"$TMP/mock-irp" keygen -out "$TMP/irp" >/dev/null
echo "Console: $OP"
echo "Sign in as npci, risk, msme, buyer or citi, password $PASS (one browser tab each)."
echo "IRP public key for the operator's setup card: $TMP/irp/irp-public.pem"
echo "Sign an invoice:  $TMP/mock-irp sign -key $TMP/irp/irp-private.pem -invoice $ROOT/tools/mock-irp/sample-invoice.json"
wait
