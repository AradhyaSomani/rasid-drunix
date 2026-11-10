"""Train, evaluate and save the RASID fraud model.

    python3 -m rasid_risk.train            full run, writes models/rasid-gam-1.0.json and MODEL_CARD.md
    python3 -m rasid_risk.train --quick    small run for a smoke test, writes nothing

Data: simulated invoice networks (see synth.py). Training uses three networks,
early stopping uses a fourth, and the reported numbers come from two larger
networks the model never saw, generated with different seeds and sizes.
"""

from __future__ import annotations

import argparse
import hashlib
from datetime import date, timedelta
from pathlib import Path

from .engine import Invoice, RiskEngine
from .metrics import average_precision, brier, calibration, confusion, roc_auc
from .model import DEFAULT_MODEL_PATH, AdditiveModel, fit
from .synth import FRAUD_TYPES, featurize, generate_network

VERSION = "rasid-gam-1.0"
FEATURES = [
    "loop_3plus", "loop_2firm", "loop_amount_match", "dup_doc_match", "dup_amount_date", "dup_exact_same_day", "velocity_ratio",
    "invoices_last_7d", "days_to_quarter_end",
    "new_pair", "ticket_vs_supplier", "ticket_vs_pair", "supplier_history", "supplier_tenure_days",
    "buyer_suppliers", "buyer_repaid", "buyer_days_late", "round_amount", "not_e_invoice",
]
TRAIN = [(300, 6000, 11), (300, 6000, 12), (300, 6000, 13)]
VALID = [(300, 6000, 14)]
TEST = [(400, 8000, 21), (400, 8000, 22)]
CARD_PATH = Path(__file__).resolve().parent.parent / "MODEL_CARD.md"


def listing_limit(e_invoice: bool) -> int:
    """The chaincode refuses to list above these scores."""
    return 70 if e_invoice else 50


def build(specs):
    rows, labels, types, e_inv, arrival, rule = [], [], [], [], [], []
    for firms, invoices, seed in specs:
        net = generate_network(firms, invoices, seed)
        rep = featurize(net)
        rows += rep.rescored
        arrival += rep.arrival
        rule += rep.rule_scores
        labels += [1 if i in net.labels else 0 for i in rep.ids]
        types += [net.labels.get(i, "HONEST") for i in rep.ids]
        e_inv += [inv.e_invoice for inv in net.invoices]
    return {"rows": rows, "arrival": arrival, "labels": labels, "types": types, "e_inv": e_inv, "rule": rule}


def demo_scenario(model: AdditiveModel | None) -> dict[str, int]:
    """The console demo: an MSME's first e-invoice of Rs 10 lakh, the same
    invoice signed again under a new number (the devnet does not send the
    number to the AI), and a three-firm trading ring."""
    d = date(2026, 11, 10)
    e = RiskEngine(model)
    first = Invoice("DEMO-1", "MSME", "BUYER", 10 * 1_00_000_00, d)
    again = Invoice("DEMO-2", "MSME", "BUYER", 10 * 1_00_000_00, d)
    e.add_invoice(first)
    s_first = e.score(first).score
    e.add_invoice(again)
    out = {"first_invoice": s_first, "reissued_copy": e.score(again).score}
    ring = RiskEngine(model)
    legs = [Invoice("RG-1", "A", "B", 25 * 1_00_000_00, d), Invoice("RG-2", "B", "C", 25 * 1_00_000_00, d + timedelta(days=3)),
            Invoice("RG-3", "C", "A", 25 * 1_00_000_00, d + timedelta(days=6))]
    for leg in legs:
        ring.add_invoice(leg)
    out["ring_leg"] = ring.score(legs[0]).score
    return out


def evaluate(model: AdditiveModel, data: dict) -> dict:
    def block(feature_rows):
        probs = [model.predict(r) for r in feature_rows]
        scores = [int(round(100 * p)) for p in probs]
        flags = [s > listing_limit(e) for s, e in zip(scores, data["e_inv"])]
        return probs, scores, flags

    y = data["labels"]
    probs, scores, flags = block(data["rows"])
    _, a_scores, a_flags = block(data["arrival"])
    rule_flags = [s > listing_limit(e) for s, e in zip(data["rule"], data["e_inv"])]
    per_type = {}
    for t in FRAUD_TYPES:
        idx = [i for i, tt in enumerate(data["types"]) if tt == t]
        if idx:
            per_type[t] = {"count": len(idx),
                           "model_recall": round(sum(flags[i] for i in idx) / len(idx), 3),
                           "rule_recall": round(sum(rule_flags[i] for i in idx) / len(idx), 3)}
    contrib_sum = {f: 0.0 for f in model.features}
    for r in data["rows"]:
        for f, c in model.explain(r)[1].items():
            contrib_sum[f] += abs(c)
    importance = sorted(((f, round(v / len(y), 3)) for f, v in contrib_sum.items()), key=lambda t: -t[1])
    review = sum(1 for s in scores if 40 <= s <= 70)
    return {
        "invoices": len(y), "fraud": sum(y), "fraud_rate": round(sum(y) / len(y), 4),
        "model": {"roc_auc": round(roc_auc(probs, y), 4), "pr_auc": round(average_precision(probs, y), 4),
                  "brier": round(brier(probs, y), 4), "at_listing_limit": confusion(flags, y),
                  "review_band_40_70": review},
        "model_on_arrival": {"roc_auc": round(roc_auc(a_scores, y), 4), "pr_auc": round(average_precision(a_scores, y), 4),
                             "at_listing_limit": confusion(a_flags, y)},
        "rule_baseline": {"roc_auc": round(roc_auc(data["rule"], y), 4), "pr_auc": round(average_precision(data["rule"], y), 4),
                          "at_listing_limit": confusion(rule_flags, y)},
        "per_type": per_type,
        "calibration": calibration(probs, y),
        "importance": importance,
    }


def model_card(model: AdditiveModel, report: dict, demo: dict) -> str:
    m, a, r = report["model"], report["model_on_arrival"], report["rule_baseline"]
    lines = [
        f"# Model card: {model.version}",
        "",
        "Fraud score for invoices offered for financing on RASID. Written by",
        "`python3 -m rasid_risk.train`; regenerate it whenever the model changes.",
        "",
        "## What it is",
        "",
        "A boosted additive model (the Explainable Boosting Machine family): the",
        "log-odds of fraud is a sum of one learned curve per feature, so every",
        "score splits exactly into per-feature contributions. Those contributions",
        "are returned with each score, and the reason codes come from them.",
        f"Pure Python, no dependencies. Model file `{DEFAULT_MODEL_PATH.name}`,",
        f"SHA-256 `{model.meta.get('sha256', 'n/a')}`.",
        "",
        "## Data",
        "",
        "Simulated MSME invoice networks (`rasid_risk/synth.py`), because no",
        "public labelled dataset of GST invoice fraud exists. The simulator mixes",
        "honest behaviour that looks suspicious (repeat orders at a fixed price,",
        "reciprocal trade, genuine three-firm loops, quarter-end rushes, new firms,",
        "late-paying buyers) with five labelled fraud types: circular trading",
        "rings, re-issued duplicates, inflated invoices, invoices to fake buyers,",
        "and bust-out bursts.",
        "",
        f"Trained on {model.meta['train_rows']:,} invoice snapshots from three networks (each",
        "invoice on arrival and 14 days later), early-stopped on a fourth, and",
        f"tested on two larger networks it never saw: {report['invoices']:,} invoices,",
        f"{report['fraud']} of them fraudulent ({report['fraud_rate'] * 100:.1f}%).",
        "",
        "## Results on the held-out networks",
        "",
        "Score = 100 x predicted probability of fraud. The chaincode refuses to list",
        "an e-invoice scoring above 70, or a manual invoice above 50.",
        "",
        "| | ROC-AUC | PR-AUC | Precision at limit | Recall at limit | False alarms |",
        "|---|---|---|---|---|---|",
        f"| {model.version}, before award | {m['roc_auc']} | {m['pr_auc']} | {m['at_listing_limit']['precision']} | {m['at_listing_limit']['recall']} | {m['at_listing_limit']['fp']} of {m['at_listing_limit']['fp'] + m['at_listing_limit']['tn']:,} |",
        f"| {model.version}, on arrival only | {a['roc_auc']} | {a['pr_auc']} | {a['at_listing_limit']['precision']} | {a['at_listing_limit']['recall']} | {a['at_listing_limit']['fp']} |",
        f"| rasid-graph-0.1 rules, before award | {r['roc_auc']} | {r['pr_auc']} | {r['at_listing_limit']['precision']} | {r['at_listing_limit']['recall']} | {r['at_listing_limit']['fp']} |",
        "",
        f"Brier score {m['brier']}. {m['review_band_40_70']} invoices land in the 40-70 band, where the",
        "risk desk should look before listing.",
        "",
        "Recall by fraud type, at the listing limit:",
        "",
        "| Type | Invoices | Model | Rules |",
        "|---|---|---|---|",
    ]
    for t, v in report["per_type"].items():
        lines.append(f"| {t} | {v['count']} | {v['model_recall']} | {v['rule_recall']} |")
    lines += ["", "Calibration (does a score of 30 mean about a 30% chance?):", "",
              "| Score band | Invoices | Mean predicted | Observed fraud rate |", "|---|---|---|---|"]
    for c in report["calibration"]:
        lines.append(f"| {c['band']} | {c['invoices']} | {c['predicted']} | {c['observed']} |")
    lines += ["", "Average size of each feature's push on the log-odds (bigger matters more):", "",
              "| Feature | Mean abs. contribution | Reason code when it raises the score |", "|---|---|---|"]
    from .model import REASON_FOR_FEATURE
    for f, v in report["importance"]:
        lines.append(f"| `{f}` | {v} | {REASON_FOR_FEATURE.get(f, '')} |")
    lines += [
        "",
        "## Console demo check",
        "",
        f"An MSME's first Rs 10 lakh e-invoice scores {demo['first_invoice']} and can be listed. The same",
        f"invoice signed again under a new number (the devnet does not pass the number to the",
        f"model) scores {demo['reissued_copy']}: " + ("above the e-invoice limit of 70, so the ledger refuses to"
        " list it with no one stepping in." if demo["reissued_copy"] > 70 else "under the limit, so the risk desk"
        " must override it to block the listing.") + f" A leg of a three-firm ring scores {demo['ring_leg']}.",
        "",
        "## Limits",
        "",
        "* The numbers measure how well the model separates the simulator's fraud",
        "  from its honest trade. They are not real-world accuracy; that needs",
        "  labelled outcomes from a pilot (defaults, confirmed fraud, buyer disputes).",
        "* The simulator was written by the same team as the model, so it shares",
        "  our assumptions about what fraud looks like. A fraud type nobody",
        "  simulated will score low.",
        "* Inputs are only what the ledger holds (hashed GSTINs, amounts, dates,",
        "  numbers, repayments). GSTR-1/2B matching, e-way bills and bank statements",
        "  would add strong signals in production.",
        "* Retrain with `python3 -m rasid_risk.train`; the tests in",
        "  `tests/test_model.py` fail if the shipped model drops below the quality",
        "  floor or loses the console demo behaviour.",
        "",
    ]
    return "\n".join(lines)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--quick", action="store_true", help="small smoke run; writes nothing")
    ap.add_argument("--out", default=str(DEFAULT_MODEL_PATH))
    ap.add_argument("--card-only", action="store_true", help="re-evaluate the saved model and rewrite MODEL_CARD.md")
    a = ap.parse_args()
    if a.card_only:
        model = AdditiveModel.load(a.out)
        report, demo = evaluate(model, build(TEST)), demo_scenario(model)
        CARD_PATH.write_text(model_card(model, report, demo))
        print(f"wrote {CARD_PATH}")
        return
    train_specs, valid_specs, test_specs = (TRAIN, VALID, TEST) if not a.quick else (
        [(120, 2000, 11)], [(120, 2000, 14)], [(120, 2000, 21)])
    print("building training data...", flush=True)
    tr = build(train_specs)
    va = build(valid_specs)
    te = build(test_specs)
    rows = tr["rows"] + tr["arrival"]
    labels = tr["labels"] * 2
    print(f"training on {len(rows):,} snapshots ({sum(labels)} fraudulent)...", flush=True)
    model = fit(rows, labels, FEATURES, version=VERSION, valid=(va["rows"], va["labels"]), max_bins=64,
                rounds=150 if a.quick else 1500)
    report = evaluate(model, te)
    demo = demo_scenario(model)
    print("held-out:", {k: report[k] for k in ("invoices", "fraud", "model", "rule_baseline")})
    print("per type:", report["per_type"])
    print("demo:", demo)
    if a.quick:
        return
    model.meta["test"] = {k: report[k] for k in ("invoices", "fraud", "model", "model_on_arrival", "rule_baseline", "per_type")}
    model.meta["demo"] = demo
    out = Path(a.out)
    model.save(out)
    model.meta["sha256"] = hashlib.sha256(out.read_bytes()).hexdigest()
    CARD_PATH.write_text(model_card(model, report, demo))
    print(f"saved {out} and {CARD_PATH}")


if __name__ == "__main__":
    main()
