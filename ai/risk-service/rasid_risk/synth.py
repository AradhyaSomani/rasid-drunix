"""Synthetic MSME invoice network with labelled fraud, for training and testing.

    python -m rasid_risk.synth --firms 300 --invoices 6000 --seed 7

There is no public, labelled dataset of Indian GST invoice fraud, so the model
is trained on a simulated network built to look like trade finance data, with
honest behaviour that resembles fraud, so the model cannot win by spotting a
single tell:

Honest trade
  * Firms in three tiers (small suppliers sell up to larger buyers), joining
    the platform at different times, so many honest invoices have a thin
    history, as on any new platform.
  * Log-normal ticket sizes per supplier and per buying relationship.
  * Fixed-price contracts: the same amount every month, and sometimes two
    deliveries a few days apart for the same amount (look like duplicates).
  * Reciprocal trade between two firms, and genuine three-firm loops
    (raw material out, finished goods on, scrap back) at unrelated amounts.
  * Quarter-end rushes (honest velocity spikes), a few firms that bill in
    round lakhs, small firms below the e-invoice threshold, buyers that pay
    30 to 90 days late.

Fraud, labelled by type
  RING      circular trading among 3-5 firms (often shells, sometimes one
            complicit real firm), the same money going round, sometimes for
            several rounds, sometimes with cover invoices to real firms.
  DUP       a real invoice re-issued for financing again: reformatted number,
            short suffix, or a brand-new number, amount within 0.5%.
  INFLATED  a real relationship, invoice blown up 1.6-8x the usual amount.
  FAKE_BUYER  a real supplier invoices a shell buyer that has no other
            suppliers and never pays.
  BUST_OUT  a supplier floods its buyers with invoices in one week, then stops.

Each invoice is scored twice, as the service does: on arrival, and again
14 days later (just before award), when the rest of the network has caught up.
"""

from __future__ import annotations

import argparse
import hashlib
import math
import random
from dataclasses import dataclass, field
from datetime import date, timedelta

from .engine import Invoice, Repayment, RiskEngine

START = date(2026, 4, 1)
HORIZON_DAYS = 180
RESCORE_AFTER_DAYS = 14
LAKH = 1_00_000_00  # paise
FRAUD_TYPES = ("RING", "DUP", "INFLATED", "FAKE_BUYER", "BUST_OUT")


def firm_id(n: int, seed: int = 0) -> str:
    return hashlib.sha256(f"GSTIN:SYNTH{seed}:{n:06d}".encode()).hexdigest()


@dataclass
class Network:
    invoices: list[Invoice]
    repayments: list[Repayment]
    labels: dict[str, str] = field(default_factory=dict)  # receivable id -> fraud type; honest ones absent

    @property
    def fraud(self) -> set[str]:
        return set(self.labels)


def generate_network(firms: int = 300, invoices: int = 6000, seed: int = 7) -> Network:
    rng = random.Random(seed)
    fid = lambda n: firm_id(n, seed)  # noqa: E731
    tier = {f: rng.choice([1, 1, 1, 2, 2, 3]) for f in range(firms)}
    joined = {f: 0 if rng.random() < 0.3 else rng.randint(0, HORIZON_DAYS - 20) for f in range(firms)}
    ticket = {f: int(min(max(math.exp(rng.gauss(math.log(2 * LAKH), 0.9)), 20_000_00), 2_00 * LAKH)) for f in range(firms)}
    e_inv_rate = {f: {1: rng.choice([0.0, 0.5, 1.0]), 2: 0.9, 3: 1.0}[tier[f]] for f in range(firms)}
    round_biller = {f: rng.random() < 0.06 for f in range(firms)}
    rusher = {f: rng.random() < 0.3 for f in range(firms)}
    doc_fmt = {f: rng.choice(["INV/{:05d}", "MSME/26-27/{:04d}", "B{:06d}", "TI-{:04d}", "{:04d}/26-27"]) for f in range(firms)}
    lateness = {f: rng.choices([(0, 3), (5, 30), (30, 90)], [0.7, 0.2, 0.1])[0] for f in range(firms)}

    customers: dict[int, list[tuple[int, float]]] = {}
    for f in range(firms):
        pool = [g for g in range(firms) if tier[g] > tier[f] or (tier[g] == tier[f] == 2 and g != f)]
        k = min(len(pool), rng.randint(2, 5)) if tier[f] < 3 else 0
        customers[f] = [(g, math.exp(rng.gauss(0, 0.3))) for g in rng.sample(pool, k)]
    for f in range(firms):  # some honest reciprocal trade
        for g, _ in list(customers[f]):
            if rng.random() < 0.04 and all(c != f for c, _ in customers[g]):
                customers[g].append((f, math.exp(rng.gauss(-0.5, 0.4))))
    fixed_price = {(f, g): rng.random() < 0.15 for f in range(firms) for g, _ in customers[f]}
    sellers = [f for f in range(firms) if customers[f]]
    weight = [ticket[f] ** 0.3 for f in sellers]

    out: list[Invoice] = []
    labels: dict[str, str] = {}
    counter = 0
    seq = {}
    rev: dict[str, int] = {}  # firm id -> firm number

    def next_doc(f: int) -> str:
        seq[f] = seq.get(f, 0) + 1
        return doc_fmt.get(f, "SH/{:04d}").format(seq[f])

    def add(s: int, b: int, amount: int, d: date, e_inv: bool, label: str | None = None, doc: str | None = None) -> Invoice:
        nonlocal counter
        counter += 1
        rev[fid(s)], rev[fid(b)] = s, b
        inv = Invoice(f"R{seed}-{counter:06d}", fid(s), fid(b), max(int(amount), 1_000_00), d,
                      doc if doc is not None else next_doc(s), e_inv)
        out.append(inv)
        if label:
            labels[inv.receivable_id] = label
        return inv

    def honest_amount(s: int, b: int, mult: float) -> int:
        amount = ticket[s] * mult * math.exp(rng.gauss(0, 0.35))
        if round_biller[s] and amount >= LAKH:
            amount = round(amount / LAKH) * LAKH
        return int(amount)

    # ---- honest trade ----------------------------------------------------
    honest_n = int(invoices * 0.95)
    for _ in range(honest_n):
        s = rng.choices(sellers, weight)[0]
        b, mult = rng.choice(customers[s])
        first_day = min(max(joined[s], joined[b]), HORIZON_DAYS - 1)
        day = rng.randint(first_day, HORIZON_DAYS - 1)
        if rusher[s] and rng.random() < 0.25:  # quarter-end rush
            day = rng.choice([84, 87, 89, 175, 177, 179]) - rng.randint(0, 3)
            day = max(day, first_day)
        d = START + timedelta(days=day)
        e_inv = rng.random() < e_inv_rate[s]
        add(s, b, honest_amount(s, b, mult), d, e_inv)

    # Fixed-price contracts: the same amount every month, sometimes a second
    # delivery at the same price a few days later (or the same day).
    for (s, b), fixed in fixed_price.items():
        if not fixed:
            continue
        mult = dict(customers[s])[b]
        amount = int(ticket[s] * mult)
        bill_day = rng.randint(0, 27)
        e_inv = rng.random() < e_inv_rate[s]
        for month in range(HORIZON_DAYS // 30):
            day = month * 30 + bill_day + rng.randint(-2, 2)
            if day < max(joined[s], joined[b]) or not 0 <= day < HORIZON_DAYS:
                continue
            d = START + timedelta(days=day)
            add(s, b, amount, d, e_inv)
            if rng.random() < 0.2:
                add(s, b, amount, d + timedelta(days=0 if rng.random() < 0.3 else rng.randint(1, 6)), e_inv)

    # Genuine three-firm loops: raw material out, goods on, scrap back.
    for _ in range(max(3, firms // 40)):
        a, b, c = rng.sample(range(firms), 3)
        d = START + timedelta(days=rng.randint(min(max(joined[a], joined[b], joined[c]), HORIZON_DAYS - 45), HORIZON_DAYS - 45))
        base = ticket[a]
        add(a, b, base * rng.uniform(0.8, 1.2), d, True)
        add(b, c, base * rng.uniform(1.5, 3.0), d + timedelta(days=rng.randint(5, 20)), True)
        add(c, a, base * rng.uniform(0.1, 0.3), d + timedelta(days=rng.randint(20, 40)), True)

    # One-off project customers: a buyer seen once or twice, from one supplier,
    # who pays normally. Common in real trade, and it looks like a fake buyer.
    walk_in = firms + 5_000
    for _ in range(int(invoices * 0.04)):
        s = rng.choices(sellers, weight)[0]
        d = START + timedelta(days=rng.randint(joined[s], HORIZON_DAYS - 1))
        e_inv = rng.random() < e_inv_rate[s]
        for _ in range(rng.choice([1, 1, 2])):
            add(s, walk_in, ticket[s] * math.exp(rng.gauss(0.3, 0.5)), d + timedelta(days=rng.randint(0, 20)), e_inv)
        walk_in += 1

    honest_pool = [i for i in out if i.receivable_id not in labels]
    pair_count: dict[tuple[str, str], int] = {}
    for i in honest_pool:
        pair_count[(i.supplier_id, i.buyer_id)] = pair_count.get((i.supplier_id, i.buyer_id), 0) + 1

    shell = firms + 10_000
    scale = invoices / 6000

    # ---- RING ------------------------------------------------------------
    for _ in range(max(2, int(18 * scale))):
        size = rng.choice([3, 3, 4, 4, 5])
        ring = list(range(shell, shell + size))
        shell += size
        if rng.random() < 0.4:
            ring[0] = rng.choice(sellers)  # one complicit real firm
        day0 = rng.randint(0, HORIZON_DAYS - 60)
        round_base = rng.random() < 0.5
        base = rng.randint(5, 60) * LAKH if round_base else int(math.exp(rng.gauss(math.log(15 * LAKH), 0.7)))
        exact = round_base and rng.random() < 0.7  # the same round sum goes round unchanged
        e_inv = rng.random() < 0.6
        if rng.random() < 0.4:  # cover invoices to look established
            for f in ring[1:]:
                for _ in range(rng.randint(1, 4)):
                    add(f, rng.choice(sellers), base * rng.uniform(0.1, 0.5),
                        START + timedelta(days=max(0, day0 - rng.randint(1, 40))), e_inv)
        for r in range(rng.choice([1, 1, 2, 3])):
            d = START + timedelta(days=day0 + r * rng.randint(20, 40))
            for i, f in enumerate(ring):
                add(f, ring[(i + 1) % size], base if exact else base * rng.uniform(0.97, 1.03), d + timedelta(days=i * rng.randint(1, 4)),
                    e_inv, "RING")

    # ---- DUP -------------------------------------------------------------
    for orig in rng.sample(honest_pool, max(2, int(60 * scale))):
        style = rng.random()
        s_firm = rev[orig.supplier_id]
        if style < 0.5:
            doc = orig.doc_no.replace("/", "-") if "/" in orig.doc_no else orig.doc_no.replace("-", "/") + "/"
        elif style < 0.7:
            doc = orig.doc_no + rng.choice(["A", "R", "-1"])
        else:
            doc = next_doc(s_firm)
        amount = orig.amount_paise if rng.random() < 0.5 else orig.amount_paise * rng.uniform(0.995, 1.005)
        shift = 0 if rng.random() < 0.5 else rng.randint(1, 7)
        add(s_firm, rev[orig.buyer_id], amount, orig.invoice_date + timedelta(days=shift), orig.e_invoice, "DUP", doc)

    # ---- INFLATED --------------------------------------------------------
    candidates = [i for i in honest_pool if pair_count[(i.supplier_id, i.buyer_id)] >= 3]
    for base in rng.sample(candidates, min(len(candidates), max(2, int(45 * scale)))):
        k = rng.uniform(1.6, 2.5) if rng.random() < 0.25 else rng.uniform(2.5, 8)
        add(rev[base.supplier_id], rev[base.buyer_id], base.amount_paise * k,
            base.invoice_date + timedelta(days=rng.randint(1, 20)), base.e_invoice, "INFLATED")

    # ---- FAKE_BUYER ------------------------------------------------------
    for _ in range(max(2, int(14 * scale))):
        buyer = shell
        shell += 1
        for s in rng.sample(sellers, rng.choice([1, 1, 2])):
            d = START + timedelta(days=rng.randint(joined[s], HORIZON_DAYS - 1))
            for _ in range(rng.choice([1, 1, 2])):
                add(s, buyer, ticket[s] * rng.uniform(2, 10), d + timedelta(days=rng.randint(0, 10)),
                    rng.random() < e_inv_rate[s], "FAKE_BUYER")

    # ---- BUST_OUT --------------------------------------------------------
    for s in rng.sample(sellers, max(1, int(7 * scale))):
        day = rng.randint(max(joined[s], 30), HORIZON_DAYS - 8)
        for _ in range(rng.randint(10, 20)):
            b, mult = rng.choice(customers[s])
            add(s, b, ticket[s] * mult * rng.uniform(1.0, 2.0), START + timedelta(days=day + rng.randint(0, 6)),
                rng.random() < e_inv_rate[s], "BUST_OUT")

    # ---- repayments ------------------------------------------------------
    end = START + timedelta(days=HORIZON_DAYS)
    repayments: list[Repayment] = []
    for inv in out:
        label = labels.get(inv.receivable_id)
        if label in ("FAKE_BUYER", "BUST_OUT", "DUP", "INFLATED"):
            continue  # these are never repaid
        if label == "RING" and rng.random() < 0.5:
            continue
        due = inv.invoice_date + timedelta(days=rng.choice([30, 45, 60, 90]))
        b = rev.get(inv.buyer_id)
        lo, hi = lateness.get(b, (0, 3)) if b is not None else (0, 3)
        paid = due + timedelta(days=rng.randint(lo, hi))
        if paid < end:
            repayments.append(Repayment(inv.buyer_id, due, paid))

    out.sort(key=lambda i: (i.invoice_date, i.receivable_id))
    return Network(out, repayments, labels)


def generate(firms: int, invoices: int, seed: int) -> tuple[list[Invoice], set[str]]:
    """Older interface: invoices and the set of fraudulent receivable ids."""
    net = generate_network(firms, invoices, seed)
    return net.invoices, net.fraud


@dataclass
class Replay:
    ids: list[str]
    arrival: list[dict]   # features when the invoice first arrives
    rescored: list[dict]  # features 14 days later, just before award
    rule_scores: list[int]  # hand-set baseline score at the same point, for comparison


def featurize(net: Network) -> Replay:
    """Replay the network in time order, as the live service sees it."""
    engine = RiskEngine()
    events: list[tuple[date, int, int, object]] = []
    for k, inv in enumerate(net.invoices):
        events.append((inv.invoice_date, 1, k, inv))
        events.append((inv.invoice_date + timedelta(days=RESCORE_AFTER_DAYS), 2, k, inv))
    for k, rep in enumerate(net.repayments):
        events.append((rep.paid_date, 0, k, rep))
    events.sort(key=lambda e: (e[0], e[1], e[2]))
    arrival: dict[str, dict] = {}
    later: dict[str, dict] = {}
    rule: dict[str, int] = {}
    for _, kind, _, obj in events:
        if kind == 0:
            engine.add_repayment(obj)
        elif kind == 1:
            engine.add_invoice(obj)
            arrival[obj.receivable_id] = engine.features(obj)[0]
        else:
            later[obj.receivable_id] = engine.features(obj)[0]
            rule[obj.receivable_id] = engine.rule_score(obj).score
    ids = [i.receivable_id for i in net.invoices]
    return Replay(ids, [arrival[i] for i in ids], [later[i] for i in ids], [rule[i] for i in ids])


def evaluate(invs: list[Invoice], fraud: set[str], rescore: bool) -> dict[str, float]:
    """The hand-set rule baseline on a network, at the chaincode's listing limits
    (70 for e-invoices, 50 otherwise). Kept for comparison with the model."""
    engine = RiskEngine()
    reports = {}
    for inv in invs:
        engine.add_invoice(inv)
        reports[inv.receivable_id] = engine.rule_score(inv)
    if rescore:
        reports = {inv.receivable_id: engine.rule_score(inv) for inv in invs}
    tp = fp = fn = tn = 0
    for inv in invs:
        flagged = reports[inv.receivable_id].score > (70 if inv.e_invoice else 50)
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
    net = generate_network(a.firms, a.invoices, a.seed)
    by_type: dict[str, int] = {}
    for t in net.labels.values():
        by_type[t] = by_type.get(t, 0) + 1
    print(f"{len(net.invoices)} invoices, {len(net.repayments)} repayments, {len(net.labels)} fraudulent {by_type}")
    print("rule baseline, scored on arrival:", evaluate(net.invoices, net.fraud, rescore=False))
    print("rule baseline, with re-scoring:  ", evaluate(net.invoices, net.fraud, rescore=True))


if __name__ == "__main__":
    main()
