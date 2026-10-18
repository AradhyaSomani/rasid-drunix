"""Synthetic invoice network with injected fraud, and an evaluation of the engine.

    python -m rasid_risk.synth --firms 300 --invoices 6000 --seed 7

Honest firms trade along a supply-chain hierarchy (small suppliers sell to
larger buyers). Fraud is injected as circular-trading rings of 3-4 shell
firms and as near-duplicate re-issued invoices. Prints precision and recall
at the listing thresholds used by the chaincode (70 e-invoice, 50 manual).
"""

from __future__ import annotations

import argparse
import hashlib
import random
from datetime import date, timedelta

from .engine import Invoice, RiskEngine


def firm_id(n: int) -> str:
    return hashlib.sha256(f"GSTIN:SYNTH{n:05d}".encode()).hexdigest()


def generate(firms: int, invoices: int, seed: int) -> tuple[list[Invoice], set[str]]:
    rng = random.Random(seed)
    start = date(2026, 4, 1)
    tier = {f: rng.choice([1, 1, 1, 2, 2, 3]) for f in range(firms)}  # 1 = small supplier, 3 = large buyer
    typical = {f: rng.randint(50_000, 5_00_000) * 100 for f in range(firms)}
    customers = {f: rng.sample([g for g in range(firms) if tier[g] > tier[f]], k=3) if tier[f] < 3 else [] for f in range(firms)}
    sellers = [f for f in range(firms) if customers[f]]

    out: list[Invoice] = []
    fraud: set[str] = set()
    counter = 0

    def add(s: int, b: int, amount: int, d: date, doc: str, e_inv: bool = True) -> Invoice:
        nonlocal counter
        counter += 1
        inv = Invoice(f"R{counter:06d}", firm_id(s), firm_id(b), amount, d, doc, e_inv)
        out.append(inv)
        return inv

    seq = {f: 0 for f in range(firms)}
    for _ in range(invoices):
        s = rng.choice(sellers)
        b = rng.choice(customers[s])
        seq[s] += 1
        amount = int(typical[s] * rng.uniform(0.5, 1.6))
        add(s, b, amount, start + timedelta(days=rng.randint(0, 180)), f"INV/{seq[s]:05d}", rng.random() > 0.2)

    # Circular-trading rings among fresh shell firms.
    shell = firms + 1000
    for _ in range(25):
        size = rng.choice([3, 4])
        ring = list(range(shell, shell + size))
        shell += size
        d = start + timedelta(days=rng.randint(0, 150))
        amount = rng.randint(10, 50) * 1_00_000_00
        for i, f in enumerate(ring):
            inv = add(f, ring[(i + 1) % size], amount, d + timedelta(days=i * 3), f"SH/{i}", rng.random() > 0.5)
            fraud.add(inv.receivable_id)

    # Near-duplicate re-issues of honest invoices.
    for orig in rng.sample(out[:invoices], 40):
        doc = orig.doc_no.replace("/", "-") + rng.choice(["", "A", "1"])
        inv = Invoice(f"R{len(out) + 1:06d}X", orig.supplier_id, orig.buyer_id,
                      int(orig.amount_paise * rng.uniform(0.995, 1.005)),
                      orig.invoice_date + timedelta(days=rng.randint(0, 5)), doc, orig.e_invoice)
        out.append(inv)
        fraud.add(inv.receivable_id)

    out.sort(key=lambda i: i.invoice_date)
    return out, fraud


def evaluate(invs: list[Invoice], fraud: set[str], rescore: bool) -> dict[str, float]:
    """rescore=False scores each invoice once, on arrival, seeing only the past.
    rescore=True also re-scores open invoices after later ones arrive, which is
    how the service runs: the first legs of a trading ring only look circular
    once the ring closes."""
    engine = RiskEngine()
    reports = {}
    for inv in invs:
        engine.add_invoice(inv)
        reports[inv.receivable_id] = engine.score(inv)
    if rescore:
        reports = {inv.receivable_id: engine.score(inv) for inv in invs}
    tp = fp = fn = tn = 0
    for inv in invs:
        report = reports[inv.receivable_id]
        limit = 70 if inv.e_invoice else 50
        flagged = report.score > limit
        is_fraud = inv.receivable_id in fraud
        tp += flagged and is_fraud
        fp += flagged and not is_fraud
        fn += (not flagged) and is_fraud
        tn += (not flagged) and not is_fraud
    precision = tp / (tp + fp) if tp + fp else 0.0
    recall = tp / (tp + fn) if tp + fn else 0.0
    return {"invoices": len(invs), "fraud": len(fraud), "tp": tp, "fp": fp, "fn": fn, "tn": tn,
            "precision": round(precision, 3), "recall": round(recall, 3)}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--firms", type=int, default=300)
    ap.add_argument("--invoices", type=int, default=6000)
    ap.add_argument("--seed", type=int, default=7)
    a = ap.parse_args()
    invs, fraud = generate(a.firms, a.invoices, a.seed)
    print("scored on arrival:   ", evaluate(invs, fraud, rescore=False))
    print("with re-scoring:     ", evaluate(invs, fraud, rescore=True))


if __name__ == "__main__":
    main()
