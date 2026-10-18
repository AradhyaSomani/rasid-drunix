# RASID chaincode

Go chaincode for DRUNIX (Hyperledger Fabric v2 lifecycle, `fabric-contract-api-go/v2`).
One chaincode package holds four contracts that share one receivable record.
Call functions as `<Contract>:<Function>`.

## Lifecycle

```
REGISTERED ─▶ ACCEPTED ─▶ LISTED ─▶ FUNDING_PENDING ─▶ FUNDED ─▶ REPAID ─▶ CLOSED
    │            │          │  ▲            │              │  ▲
    ▼            ▼          ▼  └── lapse ───┘              ▼  │
 REJECTED    CANCELLED   CANCELLED                     OVERDUE ─▶ DEFAULTED ─▶ REPAID
```

## Roles

Each org's Fabric CA puts `rasid.role` (and `rasid.gstin` for suppliers and
buyers) in the enrollment certificate. `operator` and `risk_oracle` only count
from the MSPs named in `InitLedger`, so a member bank cannot mint them.

| Role | Who | Party ID on ledger |
|---|---|---|
| supplier | MSME, enrolled by its bank or TReDS platform | SHA-256 of GSTIN |
| buyer | corporate buyer or its bank | SHA-256 of GSTIN |
| financier | bank or NBFC | `FIN:<MSP>` |
| risk_oracle | AI risk service | MSP |
| operator | NPCI | MSP |

## Functions

| Contract | Function | Caller | What it does |
|---|---|---|---|
| InvoiceRegistry | InitLedger(operatorMSP, riskOracleMSPs[]) | operator, once | Names trusted operator and risk MSPs |
| | SetIRPPublicKey(kid, pem) | operator | Trusts a GST IRP signing key |
| | SetRiskThresholds(maxEInvoice, maxManual) | operator | Listing limits (default 70 / 50) |
| | RegisterEInvoice(signedQR, dueDate) | supplier | Verifies IRP RS256 signature, blocks duplicates |
| | RegisterManualInvoice(seller, buyer, docNo, invDate, dueDate, amountPaise) | supplier | Non e-invoice route, stricter risk limit |
| | AcceptInvoice(id) / RejectInvoice(id, reason) | buyer | Buyer's irrevocable acknowledgement |
| | CancelInvoice(id) | supplier | Before financing; uniqueness key stays |
| | RecordRiskScore(id, score, model, reasons[], reportHash) | risk_oracle | Anchors AI score; re-scores allowed while LISTED |
| | GetReceivable, CheckDuplicate, GetReceivableHistory | anyone | Queries |
| | ListReceivables(status, pageSize, bookmark) | anyone | Paged list (max 200 per page); evaluate only |
| Marketplace | ListReceivable(id) | supplier | Needs ACCEPTED and score under limit |
| | SubmitBid(id) + transient `bid` | financier | Rate stays private; hash goes public |
| | WithdrawBid, GetMyBid, ListBids | financier / anyone | |
| | AwardBids(id) + transient `awards` | supplier | Reveal must match hash; several awards co-finance |
| ReceivableToken | GetHoldings(id) | anyone | 10,000 units per receivable (1 unit = 0.01%) |
| | TransferUnits(id, toMSP, units) | financier | Secondary sale between financiers |
| Settlement | ConfirmFunding(id, bidId, utr, rail, amountPaise) | financier | UTR posted back; units move; FUNDED when all paid |
| | ExpireUnfundedAwards(id) | supplier / operator | After 24 h, drops unpaid awards |
| | RegisterMandate(id, ref, E_NACH\|UPI_AUTOPAY) | buyer | Repayment mandate |
| | ConfirmRepayment(id, utr, amountPaise) | buyer / operator | Full or partial; pro-rata payouts to holders |
| | ConfirmPayout(id, payoutId, utr) | operator | CLOSED when repaid and all payouts confirmed |
| | MarkOverdue(id) / MarkDefaulted(id) | anyone / operator | Due date, 45-day MSMED flag, 90-day default |

Every UTR can be used once across the whole network.

## Example calls (peer CLI)

```bash
# Sealed bid: the rate travels only in the transient map
BID=$(echo -n '{"units":6000,"rateBps":1000,"salt":"citi-7f3a9c1e22b04d"}' | base64 | tr -d '\n')
peer chaincode invoke ... -n rasid -c '{"function":"Marketplace:SubmitBid","Args":["<receivableId>"]}' \
  --transient "{\"bid\":\"$BID\"}"

# Award: reveal the winning bids (bidId is the bid's transaction ID)
AW=$(echo -n '[{"bidId":"<txid>","units":6000,"rateBps":1000,"salt":"citi-7f3a9c1e22b04d"}]' | base64 | tr -d '\n')
peer chaincode invoke ... -c '{"function":"Marketplace:AwardBids","Args":["<receivableId>"]}' --transient "{\"awards\":\"$AW\"}"
```

## Test

```bash
cd chaincode/rasid
go test ./...
```

Tests run every contract against an in-memory ledger (`contracts/fake_test.go`).
They do not yet run on a live DRUNIX network.
