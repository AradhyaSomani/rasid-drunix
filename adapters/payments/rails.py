"""Mock NPCI / RBI payment rails for the RASID demo.

Stands in for the UPI, IMPS, e-NACH, NEFT and RTGS sandbox APIs: every
payment "succeeds" and returns a reference in the shape the real rail uses.
Swap MockRails for real sandbox clients when NPCI sandbox access arrives.
"""

from __future__ import annotations

import random
from dataclasses import dataclass

LAKH_PAISE = 1_00_000 * 100

UPI_LIMIT = 1 * LAKH_PAISE  # general UPI P2M cap
IMPS_LIMIT = 5 * LAKH_PAISE
RTGS_MIN = 2 * LAKH_PAISE


def pick_rail(amount_paise: int) -> str:
    """UPI or IMPS for small tickets, RTGS for large ones (NEFT is the fallback
    when RTGS is unavailable)."""
    if amount_paise <= UPI_LIMIT:
        return "UPI"
    if amount_paise <= IMPS_LIMIT:
        return "IMPS"
    return "RTGS" if amount_paise >= RTGS_MIN else "NEFT"


@dataclass
class Payment:
    rail: str
    utr: str
    amount_paise: int
    payer: str
    payee: str


class MockRails:
    """References are random, not sequential, so several adapter processes
    (one per bank) never hand out the same UTR. Pass a seed for repeatable tests."""

    def __init__(self, seed: int | None = None) -> None:
        self._rng = random.Random(seed) if seed is not None else random.SystemRandom()
        self.ledger: list[Payment] = []

    def _digits(self, n: int) -> str:
        return "".join(str(self._rng.randint(0, 9)) for _ in range(n))

    def _utr(self, rail: str) -> str:
        if rail in ("UPI", "IMPS"):  # 12-digit RRN
            return str(self._rng.randint(1, 9)) + self._digits(11)
        if rail == "NEFT":  # 16-character UTR
            return "MOCKN" + self._digits(11)
        if rail == "RTGS":  # 22-character UTR
            return "MOCKR" + self._digits(17)
        if rail == "E_NACH":
            return "NACH" + self._digits(12)
        raise ValueError(f"unknown rail {rail}")

    def pay(self, rail: str, amount_paise: int, payer: str, payee: str) -> Payment:
        if amount_paise <= 0:
            raise ValueError("amount must be positive")
        if rail == "UPI" and amount_paise > UPI_LIMIT:
            raise ValueError("over UPI limit")
        if rail == "IMPS" and amount_paise > IMPS_LIMIT:
            raise ValueError("over IMPS limit")
        p = Payment(rail, self._utr(rail), amount_paise, payer, payee)
        self.ledger.append(p)
        return p
