#!/usr/bin/env bash
# Runs the whole RASID flow on a laptop: real chaincode on the in-memory
# devnet ledger, the AI risk service, and the settlement adapters.
# Needs Go 1.24+, Python 3.10+, curl and jq. Run from the repo root:
#   ./scripts/demo-devnet.sh
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
GW=http://127.0.0.1:${DEVNET_PORT:-8080}
RISK=http://127.0.0.1:${RISK_PORT:-8090}
PIDS=()
# Silence stderr first: macOS bash 3.2 reports each killed background job.
cleanup() { exec 2>/dev/null; kill ${PIDS[@]+"${PIDS[@]}"} || true; rm -rf "$WORK"; }
trap cleanup EXIT

SELLER=27AAACM1234A1Z5
BUYER=29AABCB5678B1Z2
OPERATOR='{"msp":"NPCIMSP","role":"operator"}'
RISKID='{"msp":"NPCIMSP","role":"risk_oracle"}'
SUPPLIER="{\"msp\":\"SupplierBankMSP\",\"role\":\"supplier\",\"gstin\":\"$SELLER\"}"
BUYERID="{\"msp\":\"BuyerBankMSP\",\"role\":\"buyer\",\"gstin\":\"$BUYER\"}"
CITI='{"msp":"CitiMSP","role":"financier"}'
NBFC='{"msp":"NbfcMSP","role":"financier"}'

step() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
tx() { # tx <identity> <Contract/Function> <json body>
  local out; out=$(curl -sS -X POST "$GW/tx/$2" -H "X-Rasid-Identity: $1" -d "$3")
  if echo "$out" | jq -e '.error' >/dev/null; then echo "  ✗ $2: $(echo "$out" | jq -r .error)" >&2; return 1; fi
  echo "$out"
}
status() { curl -sS "$GW/query/InvoiceRegistry/GetReceivable?arg=$1" -H "X-Rasid-Identity: $CITI" | jq -r .status; }
wait_for() { for _ in $(seq 1 50); do [ "$(status "$1")" = "$2" ] && return 0; sleep 0.2; done; echo "timed out waiting for $2" >&2; return 1; }

step "Starting devnet, risk service and settlement adapters"
(cd "$ROOT/app/devnet" && go build -o "$WORK/devnet" .)
(cd "$ROOT/tools/mock-irp" && go build -o "$WORK/mock-irp" .)
"$WORK/devnet" -trust-headers -listen "${GW#http://}" >"$WORK/devnet.log" 2>&1 & PIDS+=($!)
(cd "$ROOT/ai/risk-service" && exec python3 -m rasid_risk.server --port "${RISK##*:}") >"$WORK/risk.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 50); do curl -sf "$GW/health" >/dev/null && curl -sf "$RISK/health" >/dev/null && break; sleep 0.2; done
cd "$ROOT/adapters/payments"
python3 -u adapter.py financier --msp CitiMSP --gateway "$GW" --identity "$CITI" >"$WORK/citi.log" 2>&1 & PIDS+=($!)
python3 -u adapter.py financier --msp NbfcMSP --gateway "$GW" --identity "$NBFC" >"$WORK/nbfc.log" 2>&1 & PIDS+=($!)
python3 -u adapter.py operator --gateway "$GW" --identity "$OPERATOR" >"$WORK/operator.log" 2>&1 & PIDS+=($!)
sleep 1

step "NPCI initialises the network and trusts the (mock) GST IRP key"
"$WORK/mock-irp" keygen -out "$WORK/irp"
tx "$OPERATOR" InvoiceRegistry/InitLedger '{"args":["NPCIMSP",["NPCIMSP"]]}' >/dev/null
tx "$OPERATOR" InvoiceRegistry/SetIRPPublicKey "$(jq -n --rawfile k "$WORK/irp/irp-public.pem" '{args:["irp-2026",$k]}')" >/dev/null

TODAY=$(date -u +%d/%m/%Y)
DUE=$(date -u -d '+60 days' +%Y-%m-%d 2>/dev/null || date -u -v+60d +%Y-%m-%d)
cat >"$WORK/invoice.json" <<JSON
{"SellerGstin":"$SELLER","BuyerGstin":"$BUYER","DocNo":"MSME/26-27/0042","DocTyp":"INV","DocDt":"$TODAY","TotInvVal":1000000.00,"ItemCnt":3,"MainHsnCode":"8481"}
JSON
QR=$("$WORK/mock-irp" sign -key "$WORK/irp/irp-private.pem" -invoice "$WORK/invoice.json")

step "MSME registers a ₹10,00,000 e-invoice due $DUE"
ID=$(tx "$SUPPLIER" InvoiceRegistry/RegisterEInvoice "$(jq -n --arg q "$QR" --arg d "$DUE" '{args:[$q,$d]}')" | jq -r .result.id)
echo "  receivable $ID"
echo "  re-registering the same invoice:"
tx "$SUPPLIER" InvoiceRegistry/RegisterEInvoice "$(jq -n --arg q "$QR" --arg d "$DUE" '{args:[$q,$d]}')" >/dev/null || true

step "Buyer accepts; AI risk oracle scores it"
tx "$BUYERID" InvoiceRegistry/AcceptInvoice "{\"args\":[\"$ID\"]}" >/dev/null
R=$(curl -sS "$GW/query/InvoiceRegistry/GetReceivable?arg=$ID" -H "X-Rasid-Identity: $CITI")
SCORE=$(curl -sS -X POST "$RISK/score" -d "$(echo "$R" | jq '{invoice:{receivableId:.id,supplierId:.supplierId,buyerId:.buyerId,amountPaise:.amountPaise,invoiceDate:.invoiceDate,eInvoice:.eInvoice}}')")
echo "  score $(echo "$SCORE" | jq -r '.report.score') reasons $(echo "$SCORE" | jq -c '.report.reason_codes')"
tx "$RISKID" InvoiceRegistry/RecordRiskScore "$(echo "$SCORE" | jq '{args:.recordRiskScoreArgs}')" >/dev/null

step "Listed; Citi and an NBFC place sealed bids"
tx "$SUPPLIER" Marketplace/ListReceivable "{\"args\":[\"$ID\"]}" >/dev/null
B1=$(curl -sS -X POST "$GW/bids/$ID" -H "X-Rasid-Identity: $CITI" -d '{"units":6000,"rateBps":1000}' | jq .reveal)
B2=$(curl -sS -X POST "$GW/bids/$ID" -H "X-Rasid-Identity: $NBFC" -d '{"units":4000,"rateBps":1200}' | jq .reveal)
echo "  public ledger shows only: $(curl -sS "$GW/query/Marketplace/ListBids?arg=$ID" -H "X-Rasid-Identity: $CITI" | jq -c '[.[] | {bidderMsp, hash: .hash[0:12]}]')"

step "MSME awards both bids; adapters pay over RTGS and post UTRs"
tx "$SUPPLIER" Marketplace/AwardBids "$(jq -n --arg id "$ID" --argjson a "[$B1,$B2]" '{args:[$id],transient:{awards:$a}}')" \
  | jq -r '.result.awards[] | "  \(.financierMsp) pays ₹\(.fundingPaise/100) for \(.units) units"'
wait_for "$ID" FUNDED && echo "  status FUNDED"
tx "$CITI" ReceivableToken/TransferUnits "{\"args\":[\"$ID\",\"NbfcMSP\",1000]}" >/dev/null
echo "  holdings after Citi sells 1,000 units to the NBFC: $(curl -sS "$GW/query/ReceivableToken/GetHoldings?arg=$ID" -H "X-Rasid-Identity: $CITI" | jq -c '[.[] | {(.holderId): .units}] | add')"

step "Buyer's e-NACH mandate is debited; operator pays each holder"
tx "$BUYERID" Settlement/RegisterMandate "{\"args\":[\"$ID\",\"UMRN0001\",\"E_NACH\"]}" >/dev/null
python3 adapter.py collect --gateway "$GW" --identity "$OPERATOR" --id "$ID" >/dev/null
wait_for "$ID" CLOSED && echo "  status CLOSED"
curl -sS "$GW/query/InvoiceRegistry/GetReceivable?arg=$ID" -H "X-Rasid-Identity: $CITI" \
  | jq -r '.payouts[] | "  paid \(.holderId) ₹\(.amountPaise/100) (UTR \(.payoutUtr))"'
