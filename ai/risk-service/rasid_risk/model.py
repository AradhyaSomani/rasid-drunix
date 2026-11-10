"""A trained, explainable fraud model in pure Python (no numpy needed).

The model is a boosted additive model, the same family as InterpretML's
Explainable Boosting Machine: the log-odds of fraud is a sum of one learned
curve per feature,

    logit(p) = bias + f1(x1) + f2(x2) + ... + fk(xk)

Each curve is built by gradient boosting one-split trees ("stumps") on binned
features. Because nothing mixes features, every score splits exactly into
per-feature contributions, so a lender or auditor can see why an invoice got
its score, and the curves can be plotted and sanity-checked one by one.

The whole model is a small JSON file (bin edges and curve values per feature)
that the risk service loads at start-up; its SHA-256 identifies the model.
"""

from __future__ import annotations

import bisect
import hashlib
import json
import math
import random
from dataclasses import dataclass, field
from pathlib import Path

# Which reason code a feature reports when it pushes the score up.
REASON_FOR_FEATURE = {
    "loop_3plus": "CIRCULAR_TRADE",
    "loop_2firm": "CIRCULAR_TRADE",
    "loop_amount_match": "CIRCULAR_TRADE",
    "dup_doc_match": "NEAR_DUPLICATE",
    "dup_amount_date": "NEAR_DUPLICATE",
    "dup_exact_same_day": "NEAR_DUPLICATE",
    "velocity_ratio": "VELOCITY_SPIKE",
    "invoices_last_7d": "VELOCITY_SPIKE",
    "days_to_quarter_end": "VELOCITY_SPIKE",
    "new_pair": "NEW_PAIR_LARGE",
    "ticket_vs_supplier": "NEW_PAIR_LARGE",
    "ticket_vs_pair": "INFLATED_AMOUNT",
    "supplier_history": "THIN_HISTORY",
    "supplier_tenure_days": "THIN_HISTORY",
    "buyer_suppliers": "UNPROVEN_BUYER",
    "buyer_repaid": "UNPROVEN_BUYER",
    "buyer_days_late": "BUYER_PAYS_LATE",
    "round_amount": "ROUND_AMOUNT",
    "not_e_invoice": "NOT_E_INVOICE",
}

# A feature must add at least this much log-odds to be named as a reason.
REASON_MIN_LOGIT = 0.35

DEFAULT_MODEL_PATH = Path(__file__).with_name("models") / "rasid-gam-1.0.json"


def sigmoid(z: float) -> float:
    if z >= 0:
        return 1.0 / (1.0 + math.exp(-z))
    e = math.exp(z)
    return e / (1.0 + e)


def quantile_edges(values: list[float], max_bins: int) -> list[float]:
    """Cut points between distinct values, at most max_bins - 1 of them."""
    vs = sorted(values)
    distinct = sorted(set(vs))
    if len(distinct) <= max_bins:
        return [(a + b) / 2 for a, b in zip(distinct, distinct[1:])]
    edges = []
    for q in range(1, max_bins):
        v = vs[int(q * len(vs) / max_bins)]
        if not edges or v > edges[-1]:
            edges.append(v)
    return edges


@dataclass
class AdditiveModel:
    version: str
    features: list[str]
    edges: dict[str, list[float]]
    curves: dict[str, list[float]]  # one value per bin, centred on the training mean
    bias: float
    meta: dict = field(default_factory=dict)

    # ---- inference -------------------------------------------------------
    def bin_of(self, name: str, x: float) -> int:
        return bisect.bisect_right(self.edges[name], x)

    def explain(self, feats: dict[str, float]) -> tuple[float, dict[str, float]]:
        """Probability of fraud and each feature's contribution in log-odds."""
        contrib = {n: self.curves[n][self.bin_of(n, float(feats.get(n, 0.0)))] for n in self.features}
        return sigmoid(self.bias + sum(contrib.values())), contrib

    def predict(self, feats: dict[str, float]) -> float:
        return self.explain(feats)[0]

    @staticmethod
    def reason_codes(contrib: dict[str, float]) -> list[str]:
        by_code: dict[str, float] = {}
        for name, c in contrib.items():
            if c >= REASON_MIN_LOGIT:
                code = REASON_FOR_FEATURE.get(name, name.upper())
                by_code[code] = by_code.get(code, 0.0) + c
        return sorted(by_code, key=lambda k: -by_code[k]) or ["OK"]

    # ---- persistence -----------------------------------------------------
    def to_json(self) -> str:
        return json.dumps({"version": self.version, "features": self.features, "bias": round(self.bias, 6),
                           "edges": {k: [round(e, 6) for e in v] for k, v in self.edges.items()},
                           "curves": {k: [round(c, 6) for c in v] for k, v in self.curves.items()},
                           "meta": self.meta}, indent=1, sort_keys=True)

    def save(self, path: Path) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(self.to_json() + "\n")

    @classmethod
    def load(cls, path: Path | str = DEFAULT_MODEL_PATH) -> "AdditiveModel":
        raw = Path(path).read_bytes()
        d = json.loads(raw)
        m = cls(d["version"], d["features"], d["edges"], d["curves"], d["bias"], d.get("meta", {}))
        m.meta["sha256"] = hashlib.sha256(raw).hexdigest()
        return m


def fit(rows: list[dict[str, float]], labels: list[int], features: list[str], *, version: str,
        rounds: int = 600, learning_rate: float = 0.08, max_bins: int = 24, min_hessian: float = 1.0,
        l2: float = 2.0, valid: tuple[list[dict[str, float]], list[int]] | None = None,
        patience: int = 60, seed: int = 0) -> AdditiveModel:
    """Gradient-boost one-split trees on binned features, minimising log loss.

    Each round tries the best split on every feature and keeps the one that
    reduces the loss most (Newton step, L2-regularised leaves). With a
    validation set, training stops once validation log loss has not improved
    for `patience` rounds and the best round is kept.
    """
    rng = random.Random(seed)
    n = len(rows)
    edges = {f: quantile_edges([r.get(f, 0.0) for r in rows], max_bins) for f in features}
    bins = {f: [bisect.bisect_right(edges[f], r.get(f, 0.0)) for r in rows] for f in features}
    nb = {f: len(edges[f]) + 1 for f in features}
    curves = {f: [0.0] * nb[f] for f in features}
    base_rate = min(max(sum(labels) / n, 1e-6), 1 - 1e-6)
    bias = math.log(base_rate / (1 - base_rate))
    logit = [bias] * n

    if valid:
        vrows, vy = valid
        vbins = {f: [bisect.bisect_right(edges[f], r.get(f, 0.0)) for r in vrows] for f in features}
        vlogit = [bias] * len(vrows)
    best = (float("inf"), 0, None)
    history = []

    for rnd in range(rounds):
        g = [sigmoid(z) - y for z, y in zip(logit, labels)]
        h = [max(p * (1 - p), 1e-6) for p in (sigmoid(z) for z in logit)]
        choice = None
        order = features[:]
        rng.shuffle(order)  # break ties between equally good features fairly
        for f in order:
            G = [0.0] * nb[f]
            H = [0.0] * nb[f]
            for b, gi, hi in zip(bins[f], g, h):
                G[b] += gi
                H[b] += hi
            gt, ht = sum(G), sum(H)
            gl = hl = 0.0
            for cut in range(nb[f] - 1):
                gl += G[cut]
                hl += H[cut]
                gr, hr = gt - gl, ht - hl
                if hl < min_hessian or hr < min_hessian:
                    continue
                gain = gl * gl / (hl + l2) + gr * gr / (hr + l2) - gt * gt / (ht + l2)
                if choice is None or gain > choice[0]:
                    choice = (gain, f, cut, -gl / (hl + l2), -gr / (hr + l2))
        if choice is None or choice[0] <= 1e-9:
            break
        _, f, cut, left, right = choice
        left *= learning_rate
        right *= learning_rate
        for b in range(nb[f]):
            curves[f][b] += left if b <= cut else right
        for i, b in enumerate(bins[f]):
            logit[i] += left if b <= cut else right
        if valid:
            for i, b in enumerate(vbins[f]):
                vlogit[i] += left if b <= cut else right
            vloss = sum(-(y * math.log(max(sigmoid(z), 1e-12)) + (1 - y) * math.log(max(1 - sigmoid(z), 1e-12)))
                        for z, y in zip(vlogit, vy)) / len(vy)
            history.append(vloss)
            if vloss < best[0] - 1e-6:
                best = (vloss, rnd + 1, {k: v[:] for k, v in curves.items()})
            elif rnd + 1 - best[1] >= patience:
                break

    if valid and best[2] is not None:
        curves = best[2]
    # Centre each curve on its training mean so a contribution reads as
    # "compared with an average invoice", and fold the means into the bias.
    for f in features:
        counts = [0] * nb[f]
        for b in bins[f]:
            counts[b] += 1
        mean = sum(c * v for c, v in zip(counts, curves[f])) / n
        curves[f] = [v - mean for v in curves[f]]
        bias += mean
    meta = {"rounds": best[1] if valid else rounds, "learning_rate": learning_rate, "max_bins": max_bins,
            "l2": l2, "train_rows": n, "train_fraud_rate": round(base_rate, 5)}
    return AdditiveModel(version, features, edges, curves, bias, meta)
