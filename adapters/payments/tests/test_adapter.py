import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from adapter import Gateway, collect_call, funding_calls, payout_calls, run  # noqa: E402
from rails import LAKH_PAISE, MockRails, pick_rail  # noqa: E402

FUNDING = {"name": "FundingRequested", "payload": {"id": "R1", "awards": [
    {"bidId": "b1", "financierMsp": "CitiMSP", "units": 6000, "fundingPaise": 59095891, "confirmed": False},
    {"bidId": "b2", "financierMsp": "NbfcMSP", "units": 4000, "fundingPaise": 39276712, "confirmed": False},
]}}
REPAID = {"name": "RepaymentReceived", "payload": {"id": "R1", "utr": "NACH1", "status": "REPAID", "payouts": [
    {"payoutId": "tx9-0", "holderId": "FIN:CitiMSP", "amountPaise": 70000000},
    {"payoutId": "tx9-1", "holderId": "FIN:NbfcMSP", "amountPaise": 30000000},
]}}


class RailsTest(unittest.TestCase):
    def test_rail_choice(self):
        self.assertEqual(pick_rail(50_000 * 100), "UPI")
        self.assertEqual(pick_rail(3 * LAKH_PAISE), "IMPS")
        self.assertEqual(pick_rail(6 * LAKH_PAISE), "RTGS")

    def test_utr_formats_are_unique(self):
        r = MockRails(seed=1)
        utrs = {r.pay(rail, 100, "a", "b").utr for rail in ["UPI", "IMPS", "NEFT", "RTGS", "E_NACH"] * 20}
        self.assertEqual(len(utrs), 100)
        self.assertEqual(len(r.pay("RTGS", 100, "a", "b").utr), 22)
        self.assertEqual(len(r.pay("NEFT", 100, "a", "b").utr), 16)
        with self.assertRaises(ValueError):
            r.pay("UPI", 2 * LAKH_PAISE, "a", "b")


class HandlerTest(unittest.TestCase):
    def test_financier_pays_only_its_own_awards(self):
        calls = funding_calls(FUNDING, "CitiMSP", MockRails(seed=1))
        self.assertEqual(len(calls), 1)
        fn, args = calls[0]
        self.assertEqual(fn, "Settlement:ConfirmFunding")
        self.assertEqual(args[0:2], ["R1", "b1"])
        self.assertEqual(args[3:], ["RTGS", 59095891])

    def test_operator_pays_every_holder(self):
        calls = payout_calls(REPAID, MockRails(seed=1))
        self.assertEqual([c[1][1] for c in calls], ["tx9-0", "tx9-1"])

    def test_collect_debits_outstanding_amount(self):
        fn, args = collect_call({"id": "R1", "buyerId": "B", "amountPaise": 100, "repaidPaise": 40, "mandateRef": "M"}, MockRails())
        self.assertEqual((fn, args[0], args[2]), ("Settlement:ConfirmRepayment", "R1", 60))
        with self.assertRaises(ValueError):
            collect_call({"id": "R1", "buyerId": "B", "amountPaise": 100}, MockRails())


class FakeGateway(BaseHTTPRequestHandler):
    submitted: list = []

    def log_message(self, *a):
        pass

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for ev in (FUNDING, {"name": "ReceivableListed", "payload": {"id": "R2"}}):
            self.wfile.write(f"data: {json.dumps(ev)}\n\n".encode())
        # closing the stream ends run()

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        FakeGateway.submitted.append((self.path, body["args"]))
        raw = json.dumps({"txId": f"tx{len(FakeGateway.submitted)}"}).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


class EndToEndTest(unittest.TestCase):
    def test_financier_loop_against_gateway(self):
        srv = ThreadingHTTPServer(("127.0.0.1", 0), FakeGateway)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            rails = MockRails(seed=2)
            run(Gateway(f"http://127.0.0.1:{srv.server_port}"),
                lambda ev: funding_calls(ev, "NbfcMSP", rails) if ev["name"] == "FundingRequested" else [])
            self.assertEqual(len(FakeGateway.submitted), 1)
            path, args = FakeGateway.submitted[0]
            self.assertEqual(path, "/tx/Settlement/ConfirmFunding")
            self.assertEqual(args[1], "b2")
        finally:
            srv.shutdown()
            srv.server_close()


if __name__ == "__main__":
    unittest.main()
