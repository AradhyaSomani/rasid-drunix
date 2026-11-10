"""Fraud and risk scoring for invoices before they are listed on RASID.

The engine sees the whole network's invoice graph (hashed GSTINs, amounts,
dates, document numbers), which no single lender can see. Each detector
returns a partial score and a reason code; the final 0-100 score is anchored
on-chain by InvoiceRegistry:RecordRiskScore together with the hash of the
full report, so every lending decision can be explained later.

This is the rule-and-graph baseline (model "rasid-graph-0.1"). A learned
model trained on synthetic invoice graphs replaces the weights later; the
detectors stay as features.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import re
import statistics
from collections import defaultdict
from dataclasses import asdict, dataclass, field
from datetime import date, timedelta

MODEL_VERSION = "rasid-graph-0.1"

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
    def __init__(self) -> None:
        self.invoices: list[Invoice] = []
        self.seq: dict[str, int] = {}  # arrival order, so the original is never flagged as its own copy
        self.by_supplier: dict[str, list[Invoice]] = defaultdict(list)
        self.by_pair: dict[tuple[str, str], list[Invoice]] = defaultdict(list)
        self.edges: dict[str, list[Invoice]] = defaultdict(list)  # supplier -> invoices it issued
        self.repayments: dict[str, list[Repayment]] = defaultdict(list)

    # ---- ingestion -------------------------------------------------------
    def add_invoice(self, inv: Invoice) -> None:
        self.seq.setdefault(inv.receivable_id, len(self.invoices))
        self.invoices.append(inv)
        self.by_supplier[inv.supplier_id].append(inv)
        self.by_pair[(inv.supplier_id, inv.buyer_id)].append(inv)
        self.edges[inv.supplier_id].append(inv)

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

    # ---- scoring ---------------------------------------------------------
    def score(self, inv: Invoice) -> ScoreReport:
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
            model_version=MODEL_VERSION,
            reason_codes=reasons,
            signals={k: round(v, 3) for k, v in signals.items()},
            details=details,
        )
