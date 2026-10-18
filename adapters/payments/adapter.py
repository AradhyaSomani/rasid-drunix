"""Settlement adapter: turns RASID chaincode events into payments and posts
the payment UTRs back to the ledger (the two-phase settlement pattern).

    # financier bank: pays MSMEs for its winning bids
    python adapter.py financier --msp CitiMSP --gateway http://localhost:8081

    # NPCI operator: pays each holder its share of every buyer repayment
    python adapter.py operator --gateway http://localhost:8080

    # operator: collect a due receivable through the buyer's e-NACH mandate
    python adapter.py collect --gateway http://localhost:8080 --id <receivableId>

Each mode talks only to its own organisation's gateway, so every confirmation
is signed by the identity the chaincode expects.
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.parse
import urllib.request
from typing import Callable, Iterator

from rails import MockRails, pick_rail

Call = tuple[str, list]  # (contract:function, args)


def funding_calls(event: dict, my_msp: str, rails: MockRails) -> list[Call]:
    """FundingRequested -> pay the MSME for each of my awards -> ConfirmFunding."""
    p = event["payload"]
    calls = []
    for a in p.get("awards", []):
        if a["financierMsp"] != my_msp or a.get("confirmed"):
            continue
        rail = pick_rail(a["fundingPaise"])
        pay = rails.pay(rail, a["fundingPaise"], payer=my_msp, payee=f"supplier-of:{p['id']}")
        calls.append(("Settlement:ConfirmFunding", [p["id"], a["bidId"], pay.utr, rail, a["fundingPaise"]]))
    return calls


def payout_calls(event: dict, rails: MockRails) -> list[Call]:
    """RepaymentReceived -> pay each holder its share -> ConfirmPayout."""
    p = event["payload"]
    calls = []
    for po in p.get("payouts") or []:
        pay = rails.pay(pick_rail(po["amountPaise"]), po["amountPaise"], payer="NPCI-collection", payee=po["holderId"])
        calls.append(("Settlement:ConfirmPayout", [p["id"], po["payoutId"], pay.utr]))
    return calls


def collect_call(receivable: dict, rails: MockRails) -> Call:
    """Debit the buyer's mandate for whatever is outstanding -> ConfirmRepayment."""
    if not receivable.get("mandateRef"):
        raise ValueError(f"receivable {receivable['id']} has no repayment mandate")
    outstanding = receivable["amountPaise"] - receivable.get("repaidPaise", 0)
    pay = rails.pay("E_NACH", outstanding, payer=receivable["buyerId"], payee="NPCI-collection")
    return ("Settlement:ConfirmRepayment", [receivable["id"], pay.utr, outstanding])


class Gateway:
    def __init__(self, base: str, identity: str | None = None) -> None:
        self.base = base.rstrip("/")
        # Only the devnet needs this; a real gateway signs with its own certificate.
        self.headers = {"X-Rasid-Identity": identity} if identity else {}

    def login(self, username: str, password: str) -> None:
        """Signs in to a gateway that has LOGIN_USERS set; later calls carry the session."""
        body = json.dumps({"username": username, "password": password}).encode()
        with self._open(f"{self.base}/auth/login", body, timeout=30) as r:
            self.headers["Authorization"] = "Bearer " + json.loads(r.read())["token"]

    def _open(self, url: str, body: bytes | None = None, timeout: float | None = None):
        headers = dict(self.headers)
        if body is not None:
            headers["Content-Type"] = "application/json"
        return urllib.request.urlopen(urllib.request.Request(url, body, headers), timeout=timeout)

    def submit(self, call: Call) -> dict:
        contract, fn = call[0].split(":")
        body = json.dumps({"args": call[1]}).encode()
        with self._open(f"{self.base}/tx/{contract}/{fn}", body, timeout=60) as r:
            return json.loads(r.read())

    def query(self, fn: str, *args: str) -> dict:
        contract, name = fn.split(":")
        qs = "&".join(f"arg={urllib.parse.quote(a)}" for a in args)
        with self._open(f"{self.base}/query/{contract}/{name}?{qs}", timeout=30) as r:
            return json.loads(r.read())

    def events(self) -> Iterator[dict]:
        """Reads the gateway's Server-Sent Events stream."""
        with self._open(f"{self.base}/events") as r:
            for raw in r:
                line = raw.decode().strip()
                if line.startswith("data: "):
                    yield json.loads(line[6:])


def run(gw: Gateway, handle: Callable[[dict], list[Call]]) -> None:
    for event in gw.events():
        for call in handle(event):
            try:
                res = gw.submit(call)
                print(f"{call[0]} {call[1][0][:12]}… committed in {res.get('txId')}", flush=True)
            except Exception as e:  # keep serving; the operator re-drives failed legs
                print(f"{call[0]} failed: {e}", file=sys.stderr, flush=True)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["financier", "operator", "collect"])
    ap.add_argument("--gateway", required=True)
    ap.add_argument("--msp")
    ap.add_argument("--id")
    ap.add_argument("--identity", help='devnet with -trust-headers only: JSON like {"msp":"CitiMSP","role":"financier"}')
    ap.add_argument("--login", metavar="NAME:PASSWORD", help="sign in first, for gateways with LOGIN_USERS set")
    a = ap.parse_args()
    gw, rails = Gateway(a.gateway, a.identity), MockRails()
    if a.login:
        gw.login(*a.login.split(":", 1))

    if a.mode == "financier":
        if not a.msp:
            ap.error("--msp is required for financier mode")
        run(gw, lambda ev: funding_calls(ev, a.msp, rails) if ev["name"] == "FundingRequested" else [])
    elif a.mode == "operator":
        run(gw, lambda ev: payout_calls(ev, rails) if ev["name"] == "RepaymentReceived" else [])
    else:
        if not a.id:
            ap.error("--id is required for collect mode")
        print(gw.submit(collect_call(gw.query("InvoiceRegistry:GetReceivable", a.id), rails)))


if __name__ == "__main__":
    main()
