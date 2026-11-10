"""Guards for the trained fraud model (rasid-gam-1.0).

These fail if a retrain or a code change makes the shipped model worse than
the agreed floor, breaks the console demo, or stops explaining its scores.
"""

import hashlib
import json
import math
import os
import threading
import unittest
import urllib.request
from datetime import date, timedelta
from http.server import ThreadingHTTPServer

from rasid_risk import Invoice, RiskEngine
from rasid_risk.metrics import average_precision, calibration, roc_auc
from rasid_risk.model import DEFAULT_MODEL_PATH, AdditiveModel, fit
from rasid_risk.train import FEATURES, VERSION, build, demo_scenario, evaluate

MODEL = AdditiveModel.load()
D = date(2026, 11, 10)
LAKH = 1_00_000_00


class ShippedModelTest(unittest.TestCase):
    def test_loads_with_expected_version_and_features(self):
        self.assertEqual(MODEL.version, VERSION)
        self.assertEqual(MODEL.features, FEATURES)
        self.assertEqual(MODEL.meta["sha256"], hashlib.sha256(DEFAULT_MODEL_PATH.read_bytes()).hexdigest())

    def test_scores_split_exactly_into_contributions(self):
        e = RiskEngine(MODEL)
        a = Invoice("R1", "S", "B", 7 * LAKH, D)
        e.add_invoice(a)
        r = e.score(a)
        p = r.score / 100
        logit = MODEL.bias + sum(MODEL.explain(e.features(a)[0])[1].values())
        self.assertAlmostEqual(1 / (1 + math.exp(-logit)), MODEL.predict(e.features(a)[0]), places=9)
        self.assertLessEqual(abs(p - MODEL.predict(e.features(a)[0])), 0.005)
        self.assertEqual(set(r.contributions), set(FEATURES))

    def test_learned_curves_point_the_right_way(self):
        def effect(name, lo, hi):
            return MODEL.curves[name][MODEL.bin_of(name, hi)] - MODEL.curves[name][MODEL.bin_of(name, lo)]
        self.assertGreater(effect("dup_doc_match", 0, 1), 1.0)
        self.assertGreater(effect("dup_amount_date", 0, 1), 1.0)
        self.assertGreater(effect("loop_3plus", 0, 1) + effect("loop_amount_match", 0, 1), 1.0)
        self.assertGreater(effect("ticket_vs_pair", 0, math.log(5)), 0.5)


class ConsoleDemoTest(unittest.TestCase):
    """The browser demo in TESTING.md depends on these numbers."""

    def test_demo_scores(self):
        demo = demo_scenario(MODEL)
        self.assertLess(demo["first_invoice"], 50, "a clean first invoice must list")
        self.assertGreater(demo["reissued_copy"], 70, "the re-issued copy must be blocked at the e-invoice limit")
        self.assertGreater(demo["ring_leg"], 70)

    def test_demo_reasons(self):
        e = RiskEngine(MODEL)
        first = Invoice("DEMO-1", "MSME", "BUYER", 10 * LAKH, D)
        copy = Invoice("DEMO-2", "MSME", "BUYER", 10 * LAKH, D)
        e.add_invoice(first)
        e.add_invoice(copy)
        r = e.score(copy)
        self.assertEqual(r.reason_codes[0], "NEAR_DUPLICATE")
        self.assertEqual(r.details["duplicateOf"], "DEMO-1")
        ring = RiskEngine(MODEL)
        legs = [Invoice("1", "A", "B", 25 * LAKH, D), Invoice("2", "B", "C", 25 * LAKH, D + timedelta(days=3)),
                Invoice("3", "C", "A", 25 * LAKH, D + timedelta(days=6))]
        for leg in legs:
            ring.add_invoice(leg)
        r = ring.score(legs[0])
        self.assertIn("CIRCULAR_TRADE", r.reason_codes)
        self.assertEqual(r.details["cycle"], ["A", "B", "C", "A"])


class QualityFloorTest(unittest.TestCase):
    """A fresh network the model never saw (seed 31). Floors sit a little
    under the full held-out results in MODEL_CARD.md."""

    @classmethod
    def setUpClass(cls):
        cls.report = evaluate(MODEL, build([(150, 3000, 31)]))

    def test_ranking_quality(self):
        m = self.report["model"]
        self.assertGreater(m["roc_auc"], 0.90)
        self.assertGreater(m["pr_auc"], 0.60)

    def test_few_false_alarms_at_listing_limit(self):
        c = self.report["model"]["at_listing_limit"]
        self.assertGreater(c["precision"], 0.80)
        self.assertGreater(c["recall"], 0.40)
        self.assertLess(c["false_positive_rate"], 0.01)

    def test_beats_hand_set_rules(self):
        self.assertGreater(self.report["model"]["pr_auc"], self.report["rule_baseline"]["pr_auc"] + 0.2)
        self.assertLess(self.report["model"]["at_listing_limit"]["fp"], self.report["rule_baseline"]["at_listing_limit"]["fp"])

    def test_catches_rings_and_duplicates(self):
        self.assertGreater(self.report["per_type"]["RING"]["model_recall"], 0.6)
        self.assertGreater(self.report["per_type"]["DUP"]["model_recall"], 0.5)


class TrainingTest(unittest.TestCase):
    def test_fit_learns_a_planted_signal(self):
        import random
        rng = random.Random(1)
        rows, labels = [], []
        for _ in range(2000):
            x, noise = rng.random(), rng.random()
            rows.append({"x": x, "noise": noise})
            labels.append(1 if rng.random() < (0.9 if x > 0.8 else 0.05) else 0)
        m = fit(rows, labels, ["x", "noise"], version="t", rounds=200)
        self.assertGreater(m.predict({"x": 0.95, "noise": 0.5}), 0.7)
        self.assertLess(m.predict({"x": 0.2, "noise": 0.5}), 0.15)
        imp_x = max(m.curves["x"]) - min(m.curves["x"])
        imp_noise = max(m.curves["noise"]) - min(m.curves["noise"])
        self.assertGreater(imp_x, 5 * imp_noise)

    def test_save_and_load_round_trip(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "m.json"
            MODEL.save(p)
            m2 = AdditiveModel.load(p)
        f = {"dup_amount_date": 1.0, "supplier_history": 0.7, "round_amount": 1.0}
        self.assertAlmostEqual(m2.predict(f), MODEL.predict(f), places=5)


class MetricsTest(unittest.TestCase):
    def test_roc_auc(self):
        self.assertEqual(roc_auc([0.1, 0.2, 0.8, 0.9], [0, 0, 1, 1]), 1.0)
        self.assertEqual(roc_auc([0.5, 0.5], [0, 1]), 0.5)
        self.assertAlmostEqual(roc_auc([0.1, 0.4, 0.35, 0.8], [0, 0, 1, 1]), 0.75)

    def test_average_precision(self):
        self.assertEqual(average_precision([0.9, 0.8, 0.1], [1, 1, 0]), 1.0)
        self.assertAlmostEqual(average_precision([0.9, 0.8, 0.1], [0, 1, 1]), (0.5 + 2 / 3) / 2)

    def test_calibration_bands(self):
        rows = calibration([0.05, 0.05, 0.95], [0, 0, 1])
        self.assertEqual([r["band"] for r in rows], ["0-10", "90-100"])


class ServerModelTest(unittest.TestCase):
    def setUp(self):
        from rasid_risk import server
        server.Handler.log_message = lambda *a: None
        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.srv.server_port}"

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()

    def post(self, path, body):
        req = urllib.request.Request(self.base + path, json.dumps(body).encode(), {"Content-Type": "application/json"})
        return json.loads(urllib.request.urlopen(req).read())

    def test_model_endpoint(self):
        info = json.loads(urllib.request.urlopen(self.base + "/model").read())
        if os.environ.get("RASID_RISK_MODEL") == "rules":
            self.skipTest("server started with the rule baseline")
        self.assertEqual(info["version"], VERSION)
        self.assertIn("roc_auc", info["test"]["model"])

    def test_score_is_explained_and_bridge_compatible(self):
        inv = {"receivableId": "SV-1", "supplierId": "SVS", "buyerId": "SVB", "amountPaise": 10 * LAKH,
               "invoiceDate": "2026-11-10", "eInvoice": True}
        self.post("/score", {"invoice": inv})
        out = self.post("/score", {"invoice": dict(inv, receivableId="SV-2")})
        args = out["recordRiskScoreArgs"]
        self.assertEqual(len(args), 5)
        self.assertEqual(args[0], "SV-2")
        self.assertEqual(int(args[1]), out["report"]["score"])
        self.assertEqual(args[2], VERSION)
        self.assertEqual(json.loads(args[3]), out["report"]["reason_codes"])
        self.assertEqual(args[4], out["reportHash"])
        self.assertIn("NEAR_DUPLICATE", out["report"]["reason_codes"])
        self.assertIn("dup_amount_date", out["report"]["contributions"])


if __name__ == "__main__":
    unittest.main()
