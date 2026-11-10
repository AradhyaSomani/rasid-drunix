"""Evaluation metrics, standard library only."""

from __future__ import annotations


def roc_auc(scores: list[float], labels: list[int]) -> float:
    """Probability that a random fraud scores above a random honest invoice (ties count half)."""
    pairs = sorted(zip(scores, labels))
    pos = sum(labels)
    neg = len(labels) - pos
    if not pos or not neg:
        return float("nan")
    rank_sum = 0.0
    i = 0
    while i < len(pairs):
        j = i
        while j < len(pairs) and pairs[j][0] == pairs[i][0]:
            j += 1
        avg_rank = (i + j + 1) / 2  # 1-based average rank of the tie group
        rank_sum += avg_rank * sum(y for _, y in pairs[i:j])
        i = j
    return (rank_sum - pos * (pos + 1) / 2) / (pos * neg)


def average_precision(scores: list[float], labels: list[int]) -> float:
    """Area under the precision-recall curve (step-wise), the headline metric
    when fraud is rare."""
    pos = sum(labels)
    if not pos:
        return float("nan")
    ranked = sorted(zip(scores, labels), key=lambda t: -t[0])
    tp = 0
    ap = 0.0
    i = 0
    while i < len(ranked):
        j = i
        while j < len(ranked) and ranked[j][0] == ranked[i][0]:
            j += 1
        new_tp = sum(y for _, y in ranked[i:j])
        tp += new_tp
        ap += (new_tp / pos) * (tp / j)
        i = j
    return ap


def confusion(flags: list[bool], labels: list[int]) -> dict[str, float]:
    tp = sum(1 for f, y in zip(flags, labels) if f and y)
    fp = sum(1 for f, y in zip(flags, labels) if f and not y)
    fn = sum(1 for f, y in zip(flags, labels) if not f and y)
    tn = len(labels) - tp - fp - fn
    return {"tp": tp, "fp": fp, "fn": fn, "tn": tn,
            "precision": round(tp / (tp + fp), 3) if tp + fp else 0.0,
            "recall": round(tp / (tp + fn), 3) if tp + fn else 0.0,
            "false_positive_rate": round(fp / (fp + tn), 4) if fp + tn else 0.0}


def brier(probs: list[float], labels: list[int]) -> float:
    return sum((p - y) ** 2 for p, y in zip(probs, labels)) / len(labels)


def calibration(probs: list[float], labels: list[int], bins: int = 10) -> list[dict]:
    """Mean predicted probability against the observed fraud rate, per score band."""
    rows = []
    for b in range(bins):
        lo, hi = b / bins, (b + 1) / bins
        idx = [i for i, p in enumerate(probs) if lo <= p < hi or (b == bins - 1 and p == 1.0)]
        if idx:
            rows.append({"band": f"{int(lo * 100)}-{int(hi * 100)}", "invoices": len(idx),
                         "predicted": round(sum(probs[i] for i in idx) / len(idx), 3),
                         "observed": round(sum(labels[i] for i in idx) / len(idx), 3)})
    return rows
