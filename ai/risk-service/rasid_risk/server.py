"""HTTP API for the RASID risk oracle (standard library only).

    POST /invoices    {"invoices": [Invoice...]}       feed history (from the ledger event listener)
    POST /repayments  {"repayments": [Repayment...]}
    POST /score       {"invoice": Invoice}             score one invoice
    GET  /health

/score returns the report plus the exact arguments for
InvoiceRegistry:RecordRiskScore(id, score, modelVersion, reasonCodes, reportHash).
Dates are YYYY-MM-DD; ids are the hashed GSTINs used on the ledger.

Run:  RASID_RISK_KEY=change-me python -m rasid_risk.server --port 8090
"""

from __future__ import annotations

import argparse
import json
import os
from dataclasses import asdict
from datetime import date
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Lock

from .engine import Invoice, Repayment, RiskEngine

ENGINE = RiskEngine()
LOCK = Lock()
SIGNING_KEY = os.environ.get("RASID_RISK_KEY", "dev-only-key").encode()


def parse_invoice(d: dict) -> Invoice:
    return Invoice(
        receivable_id=d["receivableId"],
        supplier_id=d["supplierId"],
        buyer_id=d["buyerId"],
        amount_paise=int(d["amountPaise"]),
        invoice_date=date.fromisoformat(d["invoiceDate"]),
        doc_no=d.get("docNo", ""),
        e_invoice=bool(d.get("eInvoice", True)),
    )


def parse_repayment(d: dict) -> Repayment:
    return Repayment(
        buyer_id=d["buyerId"],
        due_date=date.fromisoformat(d["dueDate"]),
        paid_date=date.fromisoformat(d["paidDate"]),
    )


def score_payload(inv: Invoice) -> dict:
    with LOCK:
        if not any(i.receivable_id == inv.receivable_id for i in ENGINE.invoices):
            ENGINE.add_invoice(inv)
        report = ENGINE.score(inv)
    report_hash = report.report_hash()
    return {
        "report": asdict(report),
        "reportHash": report_hash,
        "signature": report.signature(SIGNING_KEY),
        "recordRiskScoreArgs": [inv.receivable_id, str(report.score), report.model_version,
                                json.dumps(report.reason_codes), report_hash],
    }


class Handler(BaseHTTPRequestHandler):
    def _send(self, status: int, body: dict) -> None:
        raw = json.dumps(body, default=str).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/health":
            self._send(200, {"status": "ok", "invoices": len(ENGINE.invoices)})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        try:
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
            if self.path == "/invoices":
                invs = [parse_invoice(d) for d in body.get("invoices", [])]
                with LOCK:
                    for inv in invs:
                        ENGINE.add_invoice(inv)
                self._send(200, {"added": len(invs)})
            elif self.path == "/repayments":
                reps = [parse_repayment(d) for d in body.get("repayments", [])]
                with LOCK:
                    for rep in reps:
                        ENGINE.add_repayment(rep)
                self._send(200, {"added": len(reps)})
            elif self.path == "/score":
                self._send(200, score_payload(parse_invoice(body["invoice"])))
            else:
                self._send(404, {"error": "not found"})
        except (KeyError, ValueError, TypeError) as e:
            self._send(400, {"error": f"bad request: {e}"})


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8090)
    args = ap.parse_args()
    print(f"RASID risk oracle on http://{args.host}:{args.port}")
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
