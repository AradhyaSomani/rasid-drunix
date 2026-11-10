# RASID risk oracle

Fraud and risk scoring for invoices before they are financed. Python 3.9+,
standard library only (no numpy, no pip install).

The score comes from **rasid-gam-1.0**, a trained, explainable model. Every
score splits exactly into per-feature contributions, so a lender or auditor
can see why an invoice scored what it did. Full results, data description and
limits are in [MODEL_CARD.md](MODEL_CARD.md).

| On held-out test networks (18,466 invoices, 900 fraudulent) | rasid-gam-1.0 | old hand-set rules |
|---|---|---|
| ROC-AUC | 0.95 | 0.80 |
| PR-AUC (headline metric when fraud is rare) | 0.75 | 0.27 |
| Precision at the listing limit | 0.90 | 0.36 |
| Recall at the listing limit | 0.50 | 0.43 |
| Honest invoices wrongly blocked | 49 (0.28%) | 689 (3.9%) |

These are measured on simulated data; see "Limits" in the model card.

## How it works

1. **Features.** `RiskEngine.features()` turns each invoice into 19 numbers
   using what the ledger holds about the whole network: trading loops and
   whether their amounts match, near-duplicates (same pair, amount, date,
   document number), invoicing bursts and quarter-end timing, ticket size
   against the supplier's and the relationship's history, how established
   the supplier and buyer are, the buyer's repayment record, round amounts,
   and whether it is an IRP-signed e-invoice.
2. **Model.** A boosted additive model (Explainable Boosting Machine family):
   one learned curve per feature, summed into log-odds of fraud. Built from
   gradient-boosted one-split trees in `model.py`, early-stopped on a
   validation network. The model is a 14 KB JSON file in `rasid_risk/models/`;
   its SHA-256 is reported by `GET /model`.
3. **Score and reasons.** Score = 100 x probability of fraud. Any feature that
   adds at least 0.35 to the log-odds is reported as a reason code
   (CIRCULAR_TRADE, NEAR_DUPLICATE, VELOCITY_SPIKE, INFLATED_AMOUNT,
   NEW_PAIR_LARGE, UNPROVEN_BUYER, THIN_HISTORY, BUYER_PAYS_LATE, ROUND_AMOUNT,
   NOT_E_INVOICE). The report also carries every feature value and
   contribution.
4. **On-chain.** The score, model version, reason codes and the SHA-256 of the
   full report go on-chain through `InvoiceRegistry:RecordRiskScore`. The
   chaincode refuses to list an e-invoice above 70 or a manual invoice above
   50. Open invoices are re-scored as new ones arrive, because a trading ring
   only looks circular once it closes.

## Data

No public, labelled dataset of GST invoice fraud exists, so `synth.py`
simulates MSME trade networks: honest trade with look-alikes (fixed-price
repeat orders, second deliveries the same day, reciprocal trade, genuine
three-firm loops, quarter-end rushes, new firms, one-off customers, late
payers) and five labelled fraud types: circular-trading rings, re-issued
duplicates, inflated invoices, invoices to fake buyers, and bust-out bursts.
Training uses three networks (41,474 invoice snapshots), early stopping a
fourth, and testing two larger networks the model never saw.

## Commands

```bash
cd ai/risk-service
python3 -m unittest discover -s tests -t .
python3 -m rasid_risk.synth
python3 -m rasid_risk.train
RASID_RISK_KEY=change-me python3 -m rasid_risk.server --port 8090
```

The tests (27, about 6 seconds) include a quality floor on a fresh network,
the console demo scores, and the API contract with the Go risk bridge.
`rasid_risk.synth` prints one simulated network and the old rules' results on
it. `rasid_risk.train` retrains (about 2-3 minutes) and rewrites the model
file and MODEL_CARD.md; it is deterministic, so a rerun gives the same model.

## API

    POST /invoices    {"invoices": [Invoice...]}       feed history
    POST /repayments  {"repayments": [Repayment...]}
    POST /score       {"invoice": Invoice}             score one invoice
    GET  /model       model version, file hash, test results
    GET  /health

`/score` returns the report and `recordRiskScoreArgs`, the five arguments for
`InvoiceRegistry:RecordRiskScore`, unchanged from the earlier version, so the
Go risk bridges in `app/devnet` and `app/gateway` work as before. Set
`RASID_RISK_MODEL=rules` to fall back to the old hand-set rules
(`rasid-graph-0.1`), or to a path to load another model file.
