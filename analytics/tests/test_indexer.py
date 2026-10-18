import sqlite3
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from indexer import Store, load_queries  # noqa: E402


def rec(rid, buyer, amount, due, status="FUNDED", repaid=0, breach=False, late=0):
    return {"id": rid, "supplierId": "S", "buyerId": buyer, "originMsp": "SupplierBankMSP", "eInvoice": True,
            "amountPaise": amount, "fundedPaise": amount, "repaidPaise": repaid, "invoiceDate": "2026-10-01",
            "dueDate": due, "status": status, "risk": {"score": 10}, "daysLate": late, "breach45Days": breach,
            "updatedAt": "2026-10-17T00:00:00Z"}


class IndexerTest(unittest.TestCase):
    def setUp(self):
        self.s = Store(sqlite3.connect(":memory:"))
        self.s.upsert(rec("R1", "B1", 100_000_00, "2026-12-01"), [{"holderId": "FIN:CitiMSP", "units": 6000}, {"holderId": "FIN:NbfcMSP", "units": 4000}])
        self.s.upsert(rec("R2", "B2", 300_000_00, "2026-12-08"), [{"holderId": "FIN:CitiMSP", "units": 10000}])
        self.s.upsert(rec("R3", "B1", 50_000_00, "2026-11-01", status="CLOSED", repaid=50_000_00, breach=True, late=12), [])

    def test_all_queries_parse(self):
        self.assertEqual(set(load_queries()), {"exposure_by_buyer", "maturity_ladder", "buyer_concentration", "late_payers"})

    def test_exposure_by_buyer(self):
        rows = self.s.report("exposure_by_buyer", holder="FIN:CitiMSP")
        self.assertEqual(rows, [("B2", 1, 300_000_00), ("B1", 1, 60_000_00)])

    def test_upsert_replaces_state_and_holdings(self):
        self.s.upsert(rec("R1", "B1", 100_000_00, "2026-12-01", status="CLOSED", repaid=100_000_00), [])
        self.assertEqual(self.s.report("exposure_by_buyer", holder="FIN:CitiMSP"), [("B2", 1, 300_000_00)])

    def test_concentration_and_late_payers(self):
        self.assertEqual(self.s.report("buyer_concentration")[0][0], "B2")
        self.assertEqual(self.s.report("late_payers"), [("B1", 1, 12)])
        self.assertEqual(len(self.s.report("maturity_ladder")), 2)


if __name__ == "__main__":
    unittest.main()
