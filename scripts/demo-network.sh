#!/usr/bin/env bash
# Runs the RASID flow on the local Fabric network (network/local up.sh +
# deploy.sh first). Every org acts through its own gateway and certificate:
# NPCI operator, NPCI risk oracle (with the AI risk bridge), BankA supplier,
# BankA buyer, Citi financier. Needs Go 1.24+, Python 3.10+, curl, jq.
#   WORK=/tmp/rasid-net bash scripts/demo-network.sh
set -euo pipefail
source "$(dirname "$0")/lib-network.sh"
SELLER=${SELLER:-27AAACM1234A1Z5}
BUYER=${BUYER:-29AABCB5678B1Z2}

step() { printf '\n\033[1m▶ %s\033[0m\n' "$*"; }
tx() { # tx <gateway> <Contract/Function> <json body>
  local out; out=$(curl -sS -X POST "$1/tx/$2" -d "$3")
  if echo "$out" | jq -e '.error' >/dev/null; then echo "  ✗ $2: $(echo "$out" | jq -r .error)" >&2; return 1; fi
  echo "$out"
}
get() { curl -sS "$CITI/query/InvoiceRegistry/GetReceivable?arg=$1"; }
wait_for() { # id jq-condition label
  for _ in $(seq 1 100); do get "$1" | jq -e "$2" >/dev/null 2>&1 && return 0; sleep 0.3; done
  echo "timed out waiting for $3" >&2; return 1
}

step "Starting one gateway per identity, the risk service and settlement adapters"
start_stack

step "NPCI trusts the (mock) GST IRP key"
"$TMP/mock-irp" keygen -out "$TMP/irp"
tx $OP InvoiceRegistry/SetIRPPublicKey "$(jq -n --rawfile k "$TMP/irp/irp-public.pem" '{args:["irp-2026",$k]}')" >/dev/null

DOC="NET/$(date +%s)"
DUE=$(date -u -d '+60 days' +%Y-%m-%d 2>/dev/null || date -u -v+60d +%Y-%m-%d)
jq -n --arg s "$SELLER" --arg b "$BUYER" --arg d "$(date -u +%d/%m/%Y)" --arg n "$DOC" \
  '{SellerGstin:$s,BuyerGstin:$b,DocNo:$n,DocTyp:"INV",DocDt:$d,TotInvVal:1000000.00,ItemCnt:3,MainHsnCode:"8481"}' >"$TMP/invoice.json"
QR=$("$TMP/mock-irp" sign -key "$TMP/irp/irp-private.pem" -invoice "$TMP/invoice.json")

step "MSME (via BankA) registers a ₹10,00,000 e-invoice due $DUE"
ID=$(tx $SUP InvoiceRegistry/RegisterEInvoice "$(jq -n --arg q "$QR" --arg d "$DUE" '{args:[$q,$d]}')" | jq -r .result.id)
echo "  receivable $ID"
echo "  re-registering the same invoice:"
tx $SUP InvoiceRegistry/RegisterEInvoice "$(jq -n --arg q "$QR" --arg d "$DUE" '{args:[$q,$d]}')" >/dev/null || true

step "Risk bridge scores it on-chain; buyer accepts"
wait_for "$ID" '.risk != null' "the AI risk score"
echo "  score $(get "$ID" | jq -c '{score: .risk.score, reasons: .risk.reasonCodes, model: .risk.modelVersion}')"
tx $BUY InvoiceRegistry/AcceptInvoice "{\"args\":[\"$ID\"]}" >/dev/null

step "Listed; Citi places a sealed bid for 60%"
tx $SUP Marketplace/ListReceivable "{\"args\":[\"$ID\"]}" >/dev/null
BID=$(curl -sS -X POST "$CITI/bids/$ID" -d '{"units":6000,"rateBps":1000}')
echo "$BID" | jq -e .reveal >/dev/null || { echo "  ✗ bid: $BID" >&2; exit 1; }
echo "  public ledger shows only: $(curl -sS "$CITI/query/Marketplace/ListBids?arg=$ID" | jq -c '[.[] | {bidderMsp, hash: .hash[0:12]}]')"
echo "  Citi's private copy: $(curl -sS "$CITI/query/Marketplace/GetMyBid?arg=$ID&arg=$(echo "$BID" | jq -r .reveal.bidId)" | jq -c '{units, rateBps}')"

step "MSME awards it; Citi's adapter pays over RTGS and posts the UTR"
tx $SUP Marketplace/AwardBids "$(jq -n --arg id "$ID" --argjson r "$(echo "$BID" | jq .reveal)" '{args:[$id],transient:{awards:[$r]}}')" \
  | jq -r '.result.awards[] | "  \(.financierMsp) pays ₹\(.fundingPaise/100) for \(.units) units"'
wait_for "$ID" '.status == "FUNDED"' FUNDED && echo "  status FUNDED"
echo "  holdings: $(curl -sS "$CITI/query/ReceivableToken/GetHoldings?arg=$ID" | jq -c '[.[] | {(.holderId[0:16]): .units}] | add')"

step "Buyer's e-NACH mandate is debited; operator pays each holder"
tx $BUY Settlement/RegisterMandate "{\"args\":[\"$ID\",\"UMRN0001\",\"E_NACH\"]}" >/dev/null
(cd "$ROOT/adapters/payments" && python3 adapter.py collect --gateway $OP --id "$ID") >/dev/null
wait_for "$ID" '.status == "CLOSED"' CLOSED && echo "  status CLOSED"
get "$ID" | jq -r '.payouts[] | "  paid \(.holderId[0:16]) ₹\(.amountPaise/100) (UTR \(.payoutUtr))"'
