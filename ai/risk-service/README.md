# RASID risk oracle

Fraud and risk scoring for invoices before they are financed. Python 3.9+,
standard library only.

| Signal | Points at full strength | Catches |
|---|---|---|
| CIRCULAR_TRADE | 75 (45 for a two-firm loop) | Rings of 3–4 firms invoicing each other within 90 days |
| NEAR_DUPLICATE | 75 | Re-issued invoice: same pair, amount within 1%, within 10 days, doc number differing only in format or a short suffix |
| VELOCITY_SPIKE | 20 | Supplier's last 7 days far above its 90-day weekly average |
| BUYER_PAYS_LATE | 20 | Buyer's average days late on past repayments |
| NEW_PAIR_LARGE | 15 | First invoice to a buyer, many times the supplier's median ticket |
| NOT_E_INVOICE | 10 | No IRP signature |
| ROUND_AMOUNT | 5 | Exact lakh multiples |

The score (0–100), model version, reason codes and the SHA-256 of the full
report go on-chain through `InvoiceRegistry:RecordRiskScore`. Open invoices are
re-scored as new ones arrive, because the first legs of a trading ring only
look circular once the ring closes; the chaincode re-checks the score at award.

```bash
cd ai/risk-service
python -m unittest discover -s tests -t .
python -m rasid_risk.synth                     # synthetic network + precision/recall
RASID_RISK_KEY=change-me python -m rasid_risk.server --port 8090
```

On the built-in synthetic network (300 firms, about 6,100 invoices, 123 injected
frauds), it reaches precision 0.98 and recall 1.0 with re-scoring, and recall 0.53
when each invoice is scored only once on arrival. The generator injects exactly the
patterns the detectors look for, so these numbers show the pipeline works, not
real-world accuracy. A learned graph model (rasid-graph-0.2) would use these
signals as features.
