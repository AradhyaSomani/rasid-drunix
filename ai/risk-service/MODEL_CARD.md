# Model card: rasid-gam-1.0

Fraud score for invoices offered for financing on RASID. Written by
`python3 -m rasid_risk.train`; regenerate it whenever the model changes.

## What it is

A boosted additive model (the Explainable Boosting Machine family): the
log-odds of fraud is a sum of one learned curve per feature, so every
score splits exactly into per-feature contributions. Those contributions
are returned with each score, and the reason codes come from them.
Pure Python, no dependencies. Model file `rasid-gam-1.0.json`,
SHA-256 `7a9fc99eccae91ba7436a2d0c95155aafafd78c2f4a565aacb118acb5a1eb1f3`.

## Data

Simulated MSME invoice networks (`rasid_risk/synth.py`), because no
public labelled dataset of GST invoice fraud exists. The simulator mixes
honest behaviour that looks suspicious (repeat orders at a fixed price,
reciprocal trade, genuine three-firm loops, quarter-end rushes, new firms,
late-paying buyers) with five labelled fraud types: circular trading
rings, re-issued duplicates, inflated invoices, invoices to fake buyers,
and bust-out bursts.

Trained on 41,474 invoice snapshots from three networks (each
invoice on arrival and 14 days later), early-stopped on a fourth, and
tested on two larger networks it never saw: 18,466 invoices,
900 of them fraudulent (4.9%).

## Results on the held-out networks

Score = 100 x predicted probability of fraud. The chaincode refuses to list
an e-invoice scoring above 70, or a manual invoice above 50.

| | ROC-AUC | PR-AUC | Precision at limit | Recall at limit | False alarms |
|---|---|---|---|---|---|
| rasid-gam-1.0, before award | 0.9462 | 0.7508 | 0.903 | 0.504 | 49 of 17,566 |
| rasid-gam-1.0, on arrival only | 0.9358 | 0.6957 | 0.896 | 0.393 | 41 |
| rasid-graph-0.1 rules, before award | 0.7977 | 0.2659 | 0.36 | 0.43 | 689 |

Brier score 0.0211. 282 invoices land in the 40-70 band, where the
risk desk should look before listing.

Recall by fraud type, at the listing limit:

| Type | Invoices | Model | Rules |
|---|---|---|---|
| RING | 302 | 0.815 | 0.818 |
| DUP | 160 | 0.694 | 0.75 |
| INFLATED | 120 | 0.258 | 0.008 |
| FAKE_BUYER | 62 | 0.194 | 0.0 |
| BUST_OUT | 256 | 0.211 | 0.074 |

Calibration (does a score of 30 mean about a 30% chance?):

| Score band | Invoices | Mean predicted | Observed fraud rate |
|---|---|---|---|
| 0-10 | 16710 | 0.015 | 0.01 |
| 10-20 | 706 | 0.147 | 0.098 |
| 20-30 | 200 | 0.244 | 0.26 |
| 30-40 | 117 | 0.349 | 0.291 |
| 40-50 | 91 | 0.452 | 0.374 |
| 50-60 | 93 | 0.548 | 0.581 |
| 60-70 | 92 | 0.651 | 0.63 |
| 70-80 | 76 | 0.752 | 0.829 |
| 80-90 | 133 | 0.842 | 0.917 |
| 90-100 | 248 | 0.963 | 0.976 |

Average size of each feature's push on the log-odds (bigger matters more):

| Feature | Mean abs. contribution | Reason code when it raises the score |
|---|---|---|
| `ticket_vs_supplier` | 0.471 | NEW_PAIR_LARGE |
| `supplier_tenure_days` | 0.413 | THIN_HISTORY |
| `invoices_last_7d` | 0.411 | VELOCITY_SPIKE |
| `velocity_ratio` | 0.395 | VELOCITY_SPIKE |
| `buyer_suppliers` | 0.356 | UNPROVEN_BUYER |
| `supplier_history` | 0.349 | THIN_HISTORY |
| `days_to_quarter_end` | 0.246 | VELOCITY_SPIKE |
| `new_pair` | 0.148 | NEW_PAIR_LARGE |
| `loop_2firm` | 0.141 | CIRCULAR_TRADE |
| `ticket_vs_pair` | 0.137 | INFLATED_AMOUNT |
| `dup_amount_date` | 0.109 | NEAR_DUPLICATE |
| `round_amount` | 0.104 | ROUND_AMOUNT |
| `loop_amount_match` | 0.099 | CIRCULAR_TRADE |
| `dup_doc_match` | 0.09 | NEAR_DUPLICATE |
| `loop_3plus` | 0.049 | CIRCULAR_TRADE |
| `buyer_repaid` | 0.032 | UNPROVEN_BUYER |
| `buyer_days_late` | 0.005 | BUYER_PAYS_LATE |
| `dup_exact_same_day` | 0.001 | NEAR_DUPLICATE |
| `not_e_invoice` | 0.0 | NOT_E_INVOICE |

## Console demo check

An MSME's first Rs 10 lakh e-invoice scores 38 and can be listed. The same
invoice signed again under a new number (the devnet does not pass the number to the
model) scores 76: above the e-invoice limit of 70, so the ledger refuses to list it with no one stepping in. A leg of a three-firm ring scores 93.

## Limits

* The numbers measure how well the model separates the simulator's fraud
  from its honest trade. They are not real-world accuracy; that needs
  labelled outcomes from a pilot (defaults, confirmed fraud, buyer disputes).
* The simulator was written by the same team as the model, so it shares
  our assumptions about what fraud looks like. A fraud type nobody
  simulated will score low.
* Inputs are only what the ledger holds (hashed GSTINs, amounts, dates,
  numbers, repayments). GSTR-1/2B matching, e-way bills and bank statements
  would add strong signals in production.
* Retrain with `python3 -m rasid_risk.train`; the tests in
  `tests/test_model.py` fail if the shipped model drops below the quality
  floor, loses the demo behaviour, or stops matching this card.
