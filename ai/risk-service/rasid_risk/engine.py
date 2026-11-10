"""Fraud and risk scoring for invoices before they are listed on RASID.

The engine sees the whole network's invoice graph (hashed GSTINs, amounts,
dates, document numbers), which no single lender can see. Each detector
returns a partial score and a reason code; the final 0-100 score is anchored
on-chain by InvoiceRegistry:RecordRiskScore together with the hash of the
full report, so every lending decision can be explained later.

The detectors below are the hand-written baseline ("rasid-graph-0.1"). The
production score comes from a trained additive model (rasid_risk.model) that
uses the same detectors, plus history and counterparty features, as inputs;
see features() and RiskEngine.score(). If no trained model file is present the
engine falls back to the hand-set weights.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import math
import re
import statistics
from collections import defaultdict
from dataclasses import asdict, dataclass, field
from datetime import date, timedelta

RULE_MODEL_VERSION = "rasid-graph-0.1"
MODEL_VERSION = RULE_MODEL_VERSION  # kept for older imports

# Detector weights: how many points each signal adds at full strength.
WEIGHTS = {
    "CIRCULAR_TRADE": 75,
    "NEAR_DUPLICATE": 75,
    "VELOCITY_SPIKE": 20,
    "NEW_PAIR_LARGE": 15,
    "BUYER_PAYS_LATE": 20,
    "ROUND_AMOUNT": 5,
    "NOT_E_INVOICE": 10,
}

CYCLE_WINDOW_DAYS = 90
MAX_CYCLE_LEN = 4
DUPLICATE_WINDOW_DAYS = 10


@dataclass(frozen=True)
class Invoice:
    receivable_id: str
    supplier_id: str  # hashed GSTIN, as on the ledger
    buyer_id: str
    amount_paise: int
    invoice_date: date
    doc_no: str = ""
    e_invoice: bool = True


@dataclass(frozen=True)
class Repayment:
    buyer_id: str
    due_date: date
    paid_date: date


@dataclass
class ScoreReport:
    receivable_id: str
    score: int
    model_version: str
    reason_codes: list[str]
    signals: dict[str, float]
    details: dict[str, object] = field(default_factory=dict)
    # Trained model only: each feature's push on the log-odds of fraud,
    # relative to an average invoice. Positive raises the score.
    contributions: dict[str, float] = field(default_factory=dict)

    def canonical_json(self) -> bytes:
        return json.dumps(asdict(self), sort_keys=True, separators=(",", ":"), default=str).encode()

    def report_hash(self) -> str:
        """SHA-256 of the full report; this is what goes on-chain as reportHash."""
        return hashlib.sha256(self.canonical_json()).hexdigest()

    def signature(self, key: bytes) -> str:
        """HMAC over the report so the oracle's submitter can prove it came from this service.
        In production the risk oracle signs with its Fabric identity held in an HSM."""
        return hmac.new(key, self.canonical_json(), hashlib.sha256).hexdigest()


def normalise_doc_no(doc_no: str) -> str:
    return re.sub(r"[^A-Z0-9]", "", doc_no.upper())


def docs_match(a: str, b: str) -> bool:
    """Document numbers that are the same once separators are ignored, or
    differ only by a short suffix (INV/0042 vs INV-0042 vs INV/0042A)."""
    a, b = normalise_doc_no(a), normalise_doc_no(b)
    if not a or not b:
        return False
    short, long_ = sorted((a, b), key=len)
    return long_.startswith(short) and len(long_) - len(short) <= 2


class RiskEngine:
    def __init__(self, model: "object | None" = None) -> None:
        # model: a rasid_risk.model.AdditiveModel, or None for the hand-set weights.
        self.model = model
        self.invoices: list[Invoice] = []
        self.seq: dict[str, int] = {}  # arrival order, so the original is never flagged as its own copy
        self.by_supplier: dict[str, list[Invoice]] = defaultdict(list)
        self.by_pair: dict[tuple[str, str], list[Invoice]] = defaultdict(list)
        self.edges: dict[str, list[Invoice]] = defaultdict(list)  # supplier -> invoices it issued
        self.repayments: dict[str, list[Repayment]] = defaultdict(list)
        self.by_buyer: dict[str, list[Invoice]] = defaultdict(list)

    # ---- ingestion -------------------------------------------------------
    def add_invoice(self, inv: Invoice) -> None:
        self.seq.setdefault(inv.receivable_id, len(self.invoices))
        self.invoices.append(inv)
        self.by_supplier[inv.supplier_id].append(inv)
        self.by_pair[(inv.supplier_id, inv.buyer_id)].append(inv)
        self.edges[inv.supplier_id].append(inv)
        self.by_buyer[inv.buyer_id].append(inv)

    def add_repayment(self, rep: Repayment) -> None:
        self.repayments[rep.buyer_id].append(rep)

    # ---- detectors -------------------------------------------------------
    def circular_trade(self, inv: Invoice) -> tuple[float, list[str]]:
        """Is there a recent chain of invoices buyer -> ... -> supplier?
        Together with this invoice it closes a loop of goods that may never
        have moved, the classic circular-trading pattern."""
        lo = inv.invoice_date - timedelta(days=CYCLE_WINDOW_DAYS)
        hi = inv.invoice_date + timedelta(days=CYCLE_WINDOW_DAYS)

        def walk(node: str, path: list[str]) -> list[str] | None:
            if len(path) > MAX_CYCLE_LEN:
                return None
            for e in self.edges.get(node, []):
                if e.receivable_id == inv.receivable_id or not (lo <= e.invoice_date <= hi):
                    continue
                if e.buyer_id == inv.supplier_id:
                    return path + [e.buyer_id]
                if e.buyer_id not in path:
                    found = walk(e.buyer_id, path + [e.buyer_id])
                    if found:
                        return found
            return None

        cycle = walk(inv.buyer_id, [inv.supplier_id, inv.buyer_id])
        if not cycle:
            return 0.0, []
        # Two firms selling to each other is common in real trade; a loop
        # through three or four firms rarely is.
        return (0.6 if len(cycle) == 3 else 1.0), cycle

    def cycle_amount_similarity(self, inv: Invoice, cycle: list[str]) -> float:
        """How alike the amounts around a detected loop are (min/max, 0-1).
        Rings recycle one sum of money, so their legs carry nearly the same
        amount; honest loops (raw material out, scrap back) do not."""
        amounts = [inv.amount_paise]
        lo = inv.invoice_date - timedelta(days=CYCLE_WINDOW_DAYS)
        hi = inv.invoice_date + timedelta(days=CYCLE_WINDOW_DAYS)
        for a, b in zip(cycle[1:], cycle[2:]):
            legs = [e.amount_paise for e in self.by_pair.get((a, b), []) if lo <= e.invoice_date <= hi]
            if not legs:
                return 0.0
            amounts.append(min(legs, key=lambda x: abs(x - inv.amount_paise)))
        return min(amounts) / max(amounts) if max(amounts) else 0.0

    def near_duplicate(self, inv: Invoice) -> tuple[float, str | None]:
        """Same pair, nearly the same amount and date, and a document number
        that differs only in formatting: a re-issued invoice that slips past
        the chaincode's exact uniqueness key. Only earlier invoices count."""
        mine = self.seq.get(inv.receivable_id, len(self.invoices))
        for other in self.by_pair.get((inv.supplier_id, inv.buyer_id), []):
            if other.receivable_id == inv.receivable_id or self.seq[other.receivable_id] > mine:
                continue
            if abs((other.invoice_date - inv.invoice_date).days) > DUPLICATE_WINDOW_DAYS:
                continue
            amount_close = abs(other.amount_paise - inv.amount_paise) <= 0.01 * inv.amount_paise
            if amount_close and docs_match(inv.doc_no, other.doc_no):
                return 1.0, other.receivable_id
            if amount_close and other.invoice_date == inv.invoice_date:
                return 0.6, other.receivable_id
        return 0.0, None

    def velocity_spike(self, inv: Invoice) -> tuple[float, float]:
        """Supplier's invoicing in the last 7 days against its 90-day weekly average."""
        hist = [
            i for i in self.by_supplier.get(inv.supplier_id, [])
            if i.receivable_id != inv.receivable_id and timedelta(0) <= inv.invoice_date - i.invoice_date <= timedelta(days=90)
        ]
        if len(hist) < 5:
            return 0.0, 0.0
        recent = sum(i.amount_paise for i in hist if inv.invoice_date - i.invoice_date <= timedelta(days=7))
        recent += inv.amount_paise
        weekly_avg = sum(i.amount_paise for i in hist) / (90 / 7)
        ratio = recent / weekly_avg if weekly_avg else 0.0
        return min(max((ratio - 3) / 3, 0.0), 1.0), round(ratio, 2)

    def new_pair_large(self, inv: Invoice) -> tuple[float, float]:
        """First invoice to this buyer, and far larger than the supplier's usual ticket."""
        if any(o.receivable_id != inv.receivable_id for o in self.by_pair.get((inv.supplier_id, inv.buyer_id), [])):
            return 0.0, 0.0
        amounts = [i.amount_paise for i in self.by_supplier.get(inv.supplier_id, []) if i.receivable_id != inv.receivable_id]
        if len(amounts) < 3:
            return 0.0, 0.0
        ratio = inv.amount_paise / statistics.median(amounts)
        return min(max((ratio - 3) / 7, 0.0), 1.0), round(ratio, 2)

    def buyer_pays_late(self, inv: Invoice) -> tuple[float, float]:
        reps = self.repayments.get(inv.buyer_id, [])
        if not reps:
            return 0.0, 0.0
        avg_late = statistics.mean(max((r.paid_date - r.due_date).days, 0) for r in reps)
        return min(avg_late / 60, 1.0), round(avg_late, 1)

    @staticmethod
    def round_amount(inv: Invoice) -> float:
        return 1.0 if inv.amount_paise >= 1_00_000_00 and inv.amount_paise % 1_00_000_00 == 0 else 0.0

    # ---- features for the trained model ---------------------------------
    def features(self, inv: Invoice) -> tuple[dict[str, float], dict[str, object]]:
        """Numeric inputs for the trained model, plus human-readable details.
        History features only count invoices that arrived before this one;
        graph features (loops) use the whole window, which is why open
        invoices are re-scored as the network grows."""
        f: dict[str, float] = {}
        details: dict[str, object] = {}
        mine = self.seq.get(inv.receivable_id, len(self.invoices))

        def earlier(xs: list[Invoice]) -> list[Invoice]:
            return [x for x in xs if x.receivable_id != inv.receivable_id and self.seq.get(x.receivable_id, 0) < mine]

        s, cycle = self.circular_trade(inv)
        f["loop_3plus"] = 1.0 if s == 1.0 else 0.0
        f["loop_2firm"] = 1.0 if s == 0.6 else 0.0
        f["loop_amount_match"] = self.cycle_amount_similarity(inv, cycle) if cycle else 0.0
        if cycle:
            details["cycle"] = cycle

        s, dup = self.near_duplicate(inv)
        f["dup_doc_match"] = 1.0 if s == 1.0 else 0.0
        if dup:
            details["duplicateOf"] = dup
        # Same pair, amount within 1%, within 10 days, whatever the doc number.
        closest = None
        f["dup_exact_same_day"] = 0.0
        for o in earlier(self.by_pair.get((inv.supplier_id, inv.buyer_id), [])):
            days = abs((o.invoice_date - inv.invoice_date).days)
            if days == 0 and o.amount_paise == inv.amount_paise:
                f["dup_exact_same_day"] = 1.0
                details.setdefault("duplicateOf", o.receivable_id)
            if days <= DUPLICATE_WINDOW_DAYS and abs(o.amount_paise - inv.amount_paise) <= 0.01 * inv.amount_paise:
                closest = days if closest is None else min(closest, days)
        f["dup_amount_date"] = 0.0 if closest is None else 1.0 - closest / (DUPLICATE_WINDOW_DAYS + 1)

        _, vratio = self.velocity_spike(inv)
        f["velocity_ratio"] = min(vratio, 20.0)
        week = [i for i in self.by_supplier.get(inv.supplier_id, [])
                if timedelta(0) <= inv.invoice_date - i.invoice_date <= timedelta(days=6)]
        f["invoices_last_7d"] = math.log1p(len(week))
        # Indian firms bill heavily in the last days of a quarter; a burst then is normal.
        q_end = date(inv.invoice_date.year + (inv.invoice_date.month > 9), (((inv.invoice_date.month - 1) // 3 + 1) * 3) % 12 + 1, 1)
        f["days_to_quarter_end"] = float(min((q_end - inv.invoice_date).days, 30))
        details["velocityRatio"] = vratio

        sup_hist = earlier(self.by_supplier.get(inv.supplier_id, []))
        pair_hist = earlier(self.by_pair.get((inv.supplier_id, inv.buyer_id), []))
        f["new_pair"] = 0.0 if pair_hist else 1.0
        sup_med = statistics.median([i.amount_paise for i in sup_hist]) if sup_hist else 0
        f["ticket_vs_supplier"] = math.log(inv.amount_paise / sup_med) if sup_med else 0.0
        pair_med = statistics.median([i.amount_paise for i in pair_hist]) if pair_hist else 0
        f["ticket_vs_pair"] = math.log(inv.amount_paise / pair_med) if pair_med else 0.0
        details["ticketVsMedian"] = round(inv.amount_paise / sup_med, 2) if sup_med else 0.0
        f["supplier_history"] = math.log1p(len(sup_hist))
        first = min((i.invoice_date for i in sup_hist), default=inv.invoice_date)
        f["supplier_tenure_days"] = float(min(max((inv.invoice_date - first).days, 0), 365))
        f["buyer_suppliers"] = math.log1p(len({i.supplier_id for i in earlier(self.by_buyer.get(inv.buyer_id, []))
                                                if i.supplier_id != inv.supplier_id}))

        reps = self.repayments.get(inv.buyer_id, [])
        late = statistics.mean(max((r.paid_date - r.due_date).days, 0) for r in reps) if reps else 0.0
        f["buyer_days_late"] = float(min(late, 120))
        f["buyer_repaid"] = math.log1p(len(reps))
        details["buyerAvgDaysLate"] = round(late, 1)

        f["round_amount"] = self.round_amount(inv)
        f["not_e_invoice"] = 0.0 if inv.e_invoice else 1.0
        return f, details

    # ---- scoring ---------------------------------------------------------
    def score(self, inv: Invoice) -> ScoreReport:
        if self.model is None:
            return self.rule_score(inv)
        feats, details = self.features(inv)
        prob, contrib = self.model.explain(feats)
        score = int(round(100 * prob))
        return ScoreReport(
            receivable_id=inv.receivable_id,
            score=score,
            model_version=self.model.version,
            reason_codes=self.model.reason_codes(contrib),
            signals={k: round(v, 3) for k, v in feats.items()},
            details=details,
            contributions={k: round(v, 3) for k, v in contrib.items()},
        )

    def rule_score(self, inv: Invoice) -> ScoreReport:
        """The hand-set baseline, kept for comparison and as a fallback."""
        signals: dict[str, float] = {}
        details: dict[str, object] = {}

        s, cycle = self.circular_trade(inv)
        signals["CIRCULAR_TRADE"] = s
        if cycle:
            details["cycle"] = cycle
        s, dup = self.near_duplicate(inv)
        signals["NEAR_DUPLICATE"] = s
        if dup:
            details["duplicateOf"] = dup
        s, ratio = self.velocity_spike(inv)
        signals["VELOCITY_SPIKE"] = s
        details["velocityRatio"] = ratio
        s, ratio = self.new_pair_large(inv)
        signals["NEW_PAIR_LARGE"] = s
        details["ticketVsMedian"] = ratio
        s, late = self.buyer_pays_late(inv)
        signals["BUYER_PAYS_LATE"] = s
        details["buyerAvgDaysLate"] = late
        signals["ROUND_AMOUNT"] = self.round_amount(inv)
        signals["NOT_E_INVOICE"] = 0.0 if inv.e_invoice else 1.0

        raw = sum(WEIGHTS[k] * v for k, v in signals.items())
        score = int(round(min(raw, 100)))
        reasons = sorted((k for k, v in signals.items() if v > 0), key=lambda k: -WEIGHTS[k] * signals[k]) or ["OK"]
        return ScoreReport(
            receivable_id=inv.receivable_id,
            score=score,
            model_version=RULE_MODEL_VERSION,
            reason_codes=reasons,
            signals={k: round(v, 3) for k, v in signals.items()},
            details=details,
        )
