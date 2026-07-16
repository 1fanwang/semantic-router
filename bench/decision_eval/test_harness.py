#!/usr/bin/env python3
"""Tests for the decision-level routing evaluation harness.

Stdlib only. A mock router (an in-process ``http.server``) stands in for the
real classification API, so the whole client path — urllib request, HTTP,
response parsing, grading, reporting, and exit codes — is exercised without a
live backend. A companion live run against a real router is in the PR test plan.

Run:  python3 -m unittest bench.decision_eval.test_harness
  or:  python3 bench/decision_eval/test_harness.py
"""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

HERE = Path(__file__).resolve().parent

# Import the sibling module by path so the test runs from any CWD. Register it in
# sys.modules so dataclass type resolution works under `from __future__ import
# annotations` on Python 3.9.
_spec = importlib.util.spec_from_file_location("decision_eval_harness", HERE / "harness.py")
harness = importlib.util.module_from_spec(_spec)
sys.modules["decision_eval_harness"] = harness
_spec.loader.exec_module(harness)


# A tiny routing table the mock router grades against: text substring -> decision.
ROUTING_TABLE = {
    "prime number": ("computer_science_decision", ["Model-B"], ["computer science"]),
    "go-to-market": ("business_decision", ["Model-A"], ["business"]),
    "capital of": ("general_decision", ["Model-B"], ["other"]),
}
DEFAULT_ROUTE = ("general_decision", ["Model-B"], ["other"])


def _route_for(text: str):
    for needle, route in ROUTING_TABLE.items():
        if needle in text:
            return route
    return DEFAULT_ROUTE


class _MockRouterHandler(BaseHTTPRequestHandler):
    def log_message(self, *args):  # silence test noise
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        payload = json.loads(self.rfile.read(length) or b"{}")
        text = payload.get("text", "")
        decision, models, domains = _route_for(text)

        if self.path == "/api/v1/eval":
            body = {
                "original_text": text,
                "routing_decision": decision,
                "recommended_models": models,
                "decision_result": {
                    "decision_name": decision,
                    "matched_signals": {"domains": domains},
                },
            }
        elif self.path == "/api/v1/classify/intent":
            body = {
                "classification": {"category": decision, "confidence": 0.99},
                "routing_decision": decision,
                "recommended_model": models[0] if models else None,
                "matched_signals": {"domains": domains},
                "decision_result": {"decision_name": decision},
            }
        else:
            self.send_response(404)
            self.end_headers()
            return

        data = json.dumps(body).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class MockRouterMixin:
    def setUp(self):
        self.server = HTTPServer(("127.0.0.1", 0), _MockRouterHandler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


def _write(tmp: Path, name: str, lines: list[str]) -> Path:
    path = tmp / name
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return path


GOOD_ROWS = [
    '{"id":"cs","text":"a prime number please","expected_decision":"computer_science_decision","expected_model":"Model-B","expected_domain":"computer science"}',
    '{"id":"biz","text":"a go-to-market plan","expected_decision":"business_decision","expected_model":"Model-A","expected_domain":"business"}',
    '{"id":"gen","text":"the capital of France","expected_model":"Model-B"}',
]


class LoadDatasetTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def test_valid_including_comments_and_blanks(self):
        ds = _write(self.tmp, "d.jsonl", ["# comment", "", GOOD_ROWS[0], GOOD_ROWS[2]])
        recs = harness.load_dataset(ds)
        self.assertEqual([r.id for r in recs], ["cs", "gen"])
        self.assertIsNone(recs[1].expected_decision)  # partial expectation kept

    def test_ships_dataset_loads(self):
        recs = harness.load_dataset(HERE / "datasets" / "domain_routing.jsonl")
        self.assertGreaterEqual(len(recs), 10)

    def _assert_exit2(self, path: Path):
        with self.assertRaises(SystemExit) as ctx:
            harness.load_dataset(path)
        self.assertEqual(ctx.exception.code, 2)

    def test_missing_file_exit2(self):
        self._assert_exit2(self.tmp / "nope.jsonl")

    def test_bad_json_exit2(self):
        self._assert_exit2(_write(self.tmp, "d.jsonl", ["{not json}"]))

    def test_non_object_row_exit2(self):
        self._assert_exit2(_write(self.tmp, "d.jsonl", ["42"]))

    def test_missing_required_fields_exit2(self):
        self._assert_exit2(_write(self.tmp, "d.jsonl", ['{"id":"x"}']))

    def test_duplicate_id_exit2(self):
        self._assert_exit2(_write(self.tmp, "d.jsonl", [GOOD_ROWS[0], GOOD_ROWS[0]]))

    def test_no_expectation_exit2(self):
        self._assert_exit2(_write(self.tmp, "d.jsonl", ['{"id":"x","text":"hi"}']))

    def test_empty_dataset_exit2(self):
        self._assert_exit2(_write(self.tmp, "d.jsonl", ["# only a comment"]))


class GradeTests(unittest.TestCase):
    def _rec(self, **kw):
        return harness.EvalRecord(id="r", text="t", **kw)

    def test_all_fields_match(self):
        actual = harness.Actual("business_decision", ["Model-A"], ["business"])
        res = self._grade(expected_decision="business_decision", expected_model="Model-A",
                          expected_domain="business", actual=actual)
        self.assertTrue(res.passed)

    def test_model_membership(self):
        actual = harness.Actual("d", ["Model-A", "Model-B"], [])
        self.assertTrue(self._grade(expected_model="Model-B", actual=actual).passed)
        self.assertFalse(self._grade(expected_model="Model-C", actual=actual).passed)

    def test_domain_membership(self):
        actual = harness.Actual("d", [], ["law", "business"])
        self.assertTrue(self._grade(expected_domain="law", actual=actual).passed)

    def test_partial_expectation_only_grades_present(self):
        actual = harness.Actual("business_decision", ["Model-Z"], [])
        res = self._grade(expected_decision="business_decision", actual=actual)
        self.assertTrue(res.passed)  # model mismatch ignored: not expected
        self.assertEqual(set(res.checks), {"decision"})

    def test_mismatch_fails(self):
        actual = harness.Actual("law_decision", ["Model-B"], ["law"])
        self.assertFalse(self._grade(expected_decision="business_decision", actual=actual).passed)

    def _grade(self, actual, **expected):
        return harness.grade(self._rec(**expected), actual)


class DetectRegressionTests(unittest.TestCase):
    def test_only_pass_to_fail_flagged(self):
        baseline = {"records": [
            {"id": "a", "pass": True}, {"id": "b", "pass": True},
            {"id": "c", "pass": False}, {"id": "d", "pass": True},
        ]}
        report = {"records": [
            {"id": "a", "pass": True},   # stable pass
            {"id": "b", "pass": False},  # regression
            {"id": "c", "pass": True},   # fixed (not a regression)
            {"id": "e", "pass": False},  # new record (not a regression)
        ]}
        self.assertEqual(harness.detect_regressions(report, baseline), ["b"])


class CallRouterTests(MockRouterMixin, unittest.TestCase):
    def test_eval_shape_normalized(self):
        a = harness.call_router(self.url, "eval", "a prime number", timeout=5)
        self.assertEqual(a.decision, "computer_science_decision")
        self.assertEqual(a.models, ["Model-B"])
        self.assertEqual(a.domains, ["computer science"])

    def test_intent_shape_normalized(self):
        a = harness.call_router(self.url, "intent", "a go-to-market plan", timeout=5)
        self.assertEqual(a.decision, "business_decision")
        self.assertEqual(a.models, ["Model-A"])
        self.assertEqual(a.domains, ["business"])


class MainExitCodeTests(MockRouterMixin, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)
        self.good = _write(self.tmp, "good.jsonl", GOOD_ROWS)
        # one row deliberately mislabeled so accuracy is 2/3
        self.mixed = _write(self.tmp, "mixed.jsonl", [
            GOOD_ROWS[0], GOOD_ROWS[1],
            '{"id":"gen","text":"the capital of France","expected_model":"Model-A"}',
        ])

    def tearDown(self):
        self._tmp.cleanup()
        super().tearDown()

    def _run(self, *args):
        return harness.main(["--router-url", self.url, *args])

    def test_clean_exit0(self):
        self.assertEqual(self._run("--dataset", str(self.good)), 0)

    def test_failure_exit1(self):
        self.assertEqual(self._run("--dataset", str(self.mixed)), 1)

    def test_fail_under_tolerates(self):
        # accuracy 2/3 = 0.667 >= 0.5 -> pass
        self.assertEqual(self._run("--dataset", str(self.mixed), "--fail-under", "0.5"), 0)

    def test_fail_under_strict(self):
        # accuracy 0.667 < 0.9 -> fail
        self.assertEqual(self._run("--dataset", str(self.mixed), "--fail-under", "0.9"), 1)

    def test_both_endpoints_clean(self):
        self.assertEqual(self._run("--dataset", str(self.good), "--endpoint", "intent"), 0)
        self.assertEqual(self._run("--dataset", str(self.good), "--endpoint", "eval"), 0)

    def test_router_unreachable_exit2(self):
        with self.assertRaises(SystemExit) as ctx:
            harness.main(["--router-url", "http://127.0.0.1:1", "--dataset", str(self.good)])
        self.assertEqual(ctx.exception.code, 2)

    def test_baseline_regression_exit1(self):
        baseline = self.tmp / "baseline.json"
        self.assertEqual(self._run("--dataset", str(self.good), "--json-out", str(baseline)), 0)
        # now grade the mixed dataset (gen flips pass->fail) against the good baseline
        self.assertEqual(self._run("--dataset", str(self.mixed), "--baseline", str(baseline)), 1)

    def test_json_report_is_deterministic_and_valid(self):
        out1, out2 = self.tmp / "r1.json", self.tmp / "r2.json"
        self._run("--dataset", str(self.good), "--json-out", str(out1))
        self._run("--dataset", str(self.good), "--json-out", str(out2))
        r1, r2 = json.loads(out1.read_text()), json.loads(out2.read_text())
        self.assertEqual(r1["summary"], r2["summary"])
        self.assertEqual(r1["records"], r2["records"])
        self.assertEqual(r1["schema_version"], harness.SCHEMA_VERSION)


if __name__ == "__main__":
    unittest.main(verbosity=2)
