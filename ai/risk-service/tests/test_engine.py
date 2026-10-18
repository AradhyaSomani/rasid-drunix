import json
import threading
import unittest
import urllib.request
from datetime import date, timedelta
from http.server import ThreadingHTTPServer

from rasid_risk import Invoice, Repayment, RiskEngine
from rasid_risk.engine import docs_match
from rasid_risk.server import Handler
from rasid_risk.synth import evaluate, generate

D = date(2026, 11, 10)


def inv(rid, s, b, amount=10_00_000_00, d=D, doc="INV/1", e=True):
    return Invoice(rid, s, b, amount, d, doc, e)


class EngineTest(unittest.TestCase):
    def test_clean_invoice_scores_low(self):
        e = RiskEngine()
        i = inv("R1", "A", "B", amount=12_34_567_89)
        e.add_invoice(i)
        r = e.score(i)
        self.assertEqual(r.score, 0)
        self.assertEqual(r.reason_codes, ["OK"])

    def test_three_firm_ring_is_blocked(self):
        e = RiskEngine()
        for x in [inv("R1", "A", "B"), inv("R2", "B", "C", d=D + timedelta(days=3)), inv("R3", "C", "A", d=D + timedelta(days=6))]:
            e.add_invoice(x)
        r = e.score(e.invoices[0])
        self.assertIn("CIRCULAR_TRADE", r.reason_codes)
        self.assertEqual(r.details["cycle"], ["A", "B", "C", "A"])
        self.assertGreater(r.score, 70)

    def test_reciprocal_trade_alone_is_not_blocked_for_e_invoice(self):
        e = RiskEngine()
        for x in [inv("R1", "A", "B", amount=12_34_567_89), inv("R2", "B", "A", amount=12_34_567_89)]:
            e.add_invoice(x)
        r = e.score(e.invoices[0])
        self.assertEqual(r.signals["CIRCULAR_TRADE"], 0.6)
        self.assertLessEqual(r.score, 70)

    def test_old_trades_do_not_form_a_ring(self):
        e = RiskEngine()
        for x in [inv("R1", "A", "B"), inv("R2", "B", "A", d=D - timedelta(days=200))]:
            e.add_invoice(x)
        self.assertEqual(e.score(e.invoices[0]).signals["CIRCULAR_TRADE"], 0)

    def test_reissued_invoice_is_flagged_but_original_is_not(self):
        e = RiskEngine()
        a = inv("R1", "A", "B", amount=5_00_000_00, doc="INV/0042")
        b = inv("R2", "A", "B", amount=5_00_250_00, d=D + timedelta(days=2), doc="INV-0042A")
        e.add_invoice(a)
        e.add_invoice(b)
        self.assertEqual(e.score(b).details["duplicateOf"], "R1")
        self.assertGreater(e.score(b).score, 70)
        self.assertEqual(e.score(a).signals["NEAR_DUPLICATE"], 0)

    def test_sequential_invoices_are_not_duplicates(self):
        self.assertFalse(docs_match("INV/00012", "INV/00013"))
        self.assertTrue(docs_match("inv/00012", "INV-00012"))

    def test_late_paying_buyer_raises_score(self):
        e = RiskEngine()
        i = inv("R1", "A", "B", amount=12_34_567_89)
        e.add_invoice(i)
        e.add_repayment(Repayment("B", D, D + timedelta(days=60)))
        self.assertEqual(e.score(i).signals["BUYER_PAYS_LATE"], 1.0)

    def test_report_hash_is_stable(self):
        e = RiskEngine()
        i = inv("R1", "A", "B")
        e.add_invoice(i)
        self.assertEqual(e.score(i).report_hash(), e.score(i).report_hash())

    def test_synthetic_evaluation(self):
        invs, fraud = generate(150, 2000, 3)
        m = evaluate(invs, fraud, rescore=True)
        self.assertGreater(m["recall"], 0.9)
        self.assertGreater(m["precision"], 0.8)


class ServerTest(unittest.TestCase):
    def setUp(self):
        Handler.log_message = lambda *a: None

    def test_score_endpoint(self):
        srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            body = json.dumps({"invoice": {"receivableId": "R9", "supplierId": "S", "buyerId": "B",
                                           "amountPaise": 123456789, "invoiceDate": "2026-11-10"}}).encode()
            req = urllib.request.Request(f"http://127.0.0.1:{srv.server_port}/score", body, {"Content-Type": "application/json"})
            out = json.loads(urllib.request.urlopen(req).read())
            self.assertEqual(out["recordRiskScoreArgs"][0], "R9")
            self.assertEqual(len(out["reportHash"]), 64)
        finally:
            srv.shutdown()
            srv.server_close()


if __name__ == "__main__":
    unittest.main()
