# RASID build handover (updated 18 Oct 2026)

Everything here mirrors the path it should take in your repo
`github.com/AradhyaSomani/rasid-drunix`. Copy each folder to the same path
from the repo root. Nothing was pushed to GitHub.

## Components, in build order

| # | Component | Repo path | Language | Status |
|---|---|---|---|---|
| 1 | Chaincode: InvoiceRegistry, Marketplace, ReceivableToken, Settlement | `chaincode/rasid/` | Go | 14 unit tests pass; ran on a local Fabric 2.5 network |
| 2 | Mock GST IRP signer (signed QR JWTs) | `tools/mock-irp/` | Go | Output verified by the chaincode's signature check |
| 3 | AI fraud and risk oracle | `ai/risk-service/` | Python (stdlib) | 10 tests pass; synthetic evaluation included |
| 4 | Chaincode deploy script for DRUNIX | `network/deploy-chaincode.sh` | Bash | Syntax-checked; the local equivalent (`network/local/deploy.sh`) runs |
| 5 | REST gateway + risk bridge (Fabric Gateway SDK) | `app/gateway/` | Go | Ran live on the local Fabric network, risk bridge included |
| 6 | Settlement adapters (mock UPI/IMPS/NEFT/RTGS/e-NACH) | `adapters/payments/` | Python (stdlib) | 6 tests pass |
| 7 | Devnet: real chaincode on an in-memory ledger, same API as the gateway | `app/devnet/` | Go | End-to-end HTTP test passes |
| 8 | Web console: sign-in and one screen per party | `app/web/` | HTML/JS | Six parties in six tabs clicked one invoice to CLOSED in a headless browser, on devnet and on the live network, no errors |
| 9 | End-to-end demo | `scripts/demo-devnet.sh` | Bash | Runs the full flow to CLOSED |
| 10 | Caliper benchmark pack (registration, month-end spike, duplicate checks) | `benchmarks/caliper/` | JS + YAML | Smoke run on the local network: 8,132 transactions, 0 failures |
| 11 | Analytics indexer (YugabyteDB/PostgreSQL, SQLite for dev) | `analytics/` | Python + SQL | 4 tests pass; indexed the devnet demo live |
| 12 | Local 3-org Fabric 2.5 network (NPCI, BankA, Citi), no Docker | `network/local/` | Bash + YAML | Up, deploy and full demo run clean |
| 13 | Identity issuer with RASID attributes | `tools/issue-identity/` | Go | Its certificates pass the chaincode's role checks on real peers |
| 14 | Live-network demo | `scripts/demo-network.sh` | Bash | Runs the full flow to CLOSED through 5 gateways |
| 15 | Live-network console | `scripts/console-network.sh` | Bash | Serves the console; each user signs in at their organisation's gateway |
| 16 | Devnet console with AI scoring | `scripts/console-devnet.sh` | Bash | Starts the devnet and the AI risk service; every new invoice is scored by the AI |

Each component folder has its own README with what it does and how to run it.

## Run it

Needs Go 1.24+, Python 3.9+, curl, jq. `TESTING.md` walks through testing on
your own machine step by step, including the console clicks.

```bash
bash scripts/demo-devnet.sh        # whole flow in the terminal, about 10 seconds
bash scripts/console-devnet.sh     # devnet + AI scoring; open http://localhost:8080 for the console
```

On a real Fabric network (see `network/local/README.md` for getting the binaries):

```bash
export WORK=/tmp/rasid-net
bash network/local/up.sh && bash network/local/deploy.sh
bash scripts/demo-network.sh
bash scripts/console-network.sh    # same network in the web console, http://127.0.0.1:8101
```

The demo registers a ₹10,00,000 e-invoice, blocks a re-registration, scores it,
takes sealed bids from Citi and an NBFC, funds it over RTGS/IMPS, moves 1,000
units between financiers, collects through e-NACH and pays both holders.

Tests:

```bash
(cd chaincode/rasid && go test ./...)
(cd app/devnet && go test ./...)
(cd app/gateway && go test ./...)
(cd ai/risk-service && python3 -m unittest discover -s tests -t .)
(cd adapters/payments && python3 -m unittest discover -s tests -t .)
(cd analytics && python3 -m unittest discover -s tests -t .)
```

## What the recheck found and fixed

1. **Every function returning a receivable would have failed on a real peer.**
   The contract API rejected optional fields as missing. Found by running the
   chaincode through the real dispatch in devnet; fields are now tagged optional.
2. **Any member bank's CA could mint an "operator" or "risk_oracle" identity.**
   Those roles now count only from the MSPs set once in `InitLedger`.
3. **A financier that never paid left the invoice stuck in FUNDING_PENDING.**
   `ExpireUnfundedAwards` drops unpaid awards after 24 hours.
4. **A later AI re-score could not stop financing.** Scores can now be updated
   while LISTED, and the limit is checked again at award.
5. **Future-dated invoices were accepted.** Now refused.
6. **Two bank adapters could issue the same mock UTR.** References are now random.
7. Two of my own test expectations had rounding wrong (discount rounding, pro-rata
   remainder); the code was right, the tests were fixed.

Found by the live network run (18 Oct):

8. **Sealed bids were refused by the Fabric gateway.** It will not send
   transient data to an org outside the private collection unless the client
   names it. The gateway's bid endpoint now names the bidder and NPCI, and
   `/tx` accepts `endorsingOrgs`.
9. **Gateway errors hid the reason** for some failures. They now include it.
10. **Caliper workloads** read the IRP key from the wrong folder and sent every
    call as the first org's identity. Both fixed; `invokerMspId` is now set.
11. **Listing receivables broke past about 4,000 invoices.** One unpaged reply
    went over gRPC's 4 MB limit after the benchmark. `ListReceivables` now
    takes a page size and bookmark (max 200 a page); the console, indexer and
    devnet page through it.
12. **The console's live event feed never connected** on a live network (wrong
    URL) and the gateway held its first byte back. Both fixed.
13. **The sample invoice was dated 10 Nov 2026**, so the chaincode now refuses
    it as future-dated. The mock IRP fills in today's date when `DocDt` is left
    out, and the sample leaves it out.

Found by re-running everything from a fresh copy of the zip (18 Oct):

14. **The live demo could fail right after start-up** with "failed to find any
    endorsing peers": the last peer to join took about 30 seconds to see the
    others. Peers now retry every 3 seconds and `up.sh` waits until every peer
    sees the other two.
15. **`deploy.sh` could fail** with "certificate has expired or is not yet
    valid", depending on the second it ran in. Issued identities could start
    before their CA's certificate, which Fabric rejects. They no longer do.

Added with sign-in and per-party screens (18 Oct):

16. **On Fabric, the winning bid was recorded as lost.** Award marks every
    other sealed bid LOST in the same transaction, and Fabric reads don't see
    that transaction's own writes, so the bids just awarded still read SEALED
    and were overwritten. Funding was unaffected, but the bid record was
    wrong. Fixed. The devnet and the chaincode tests' ledger used to let a
    transaction read its own writes, which hid this; both now behave like
    Fabric, and the test that caught it passes.
17. **A sixth console tab would hang.** Browsers allow about six connections
    per server and each tab's live event stream held one. One tab now holds
    the stream and shares it with the others.

## Sign-in and roles

- The devnet has six demo accounts (`npci`, `risk`, `msme`, `buyer`, `citi`,
  `nbfc`, password `rasid`); `-users file.json` replaces them. A gateway turns
  on sign-in with `LOGIN_USERS`, and everyone signing in there acts as its
  certificate. `scripts/console-network.sh` does this for the live network.
- Requests without a session get 401. The devnet only accepts the
  `X-Rasid-Identity` header when started with `-trust-headers`, which
  `scripts/demo-devnet.sh` uses.
- The screens decide what to show; the chaincode decides what is allowed.
- On the devnet the AI scores invoices when started with `-risk-url`
  (`scripts/console-devnet.sh` does this). The risk desk can override a score;
  the console shows whether the AI or a person set it.

## Design decisions to know

- Uniqueness key is SHA-256 of seller GSTIN + document number + financial year
  for both e-invoices and manual invoices, plus an IRN index. So one invoice
  cannot be registered once per route.
- Each receivable is 10,000 units (1 unit = 0.01% of face value).
- Bid rates travel only in the transient map and the bidder's implicit private
  collection; the public ledger holds a hash. Competing banks never see a rate,
  but the peers that endorse the bid (the bidder's and, under the default
  policy, NPCI's) do see it while simulating. The award records the funding
  amount, from which the rate could be worked out; moving awards into a
  supplier–financier private collection is the next privacy step.
- The chaincode emits one event per transaction (a Fabric limit), carrying
  everything the adapters need.

## Not done yet

- Nothing has run on DRUNIX itself. The local network is stock Fabric 2.5, so
  DRUNIX-only features (lite peers, stateless validation, KeyDB, YugabyteDB
  state database, Falcon) are untested.
- The risk service is a rule-and-graph baseline, not a trained model. Its
  synthetic results (precision 0.98, recall 1.0 with re-scoring) come from data
  shaped like its own detectors, so they prove the plumbing, not accuracy.
- The risk bridge sends no document numbers (they are not on the ledger), so
  near-duplicate checks need the supplier's platform to post invoices to the
  risk service directly.
- GSTIN attributes are trusted from the issuing org's CA; a GSTIN-to-MSP
  onboarding registry would stop one org enrolling another's MSME.
- Not built: Falcon/Helm and KEDA autoscaling profiles, Account Aggregator
  integration, the GenAI vernacular assistant.
