#!/usr/bin/env python3
"""Decision-level routing evaluation harness.

Grades whether the router picks the correct decision/model for a labeled
dataset, without requiring any upstream model completion. It drives the
router's classification API (``/api/v1/eval`` or ``/api/v1/classify/intent``),
so it isolates the routing decision from answer quality.

Stdlib only, on purpose: maintainers can run it on dev and AMD validation
hosts without installing anything. See README.md for the dataset format and
CI usage.

Exit codes:
  0  all graded records passed and no regression vs. baseline
  1  one or more records failed, accuracy below --fail-under, or a regression
  2  harness error (router unreachable, bad dataset, bad baseline)
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, NoReturn

SCHEMA_VERSION = "decision-eval/v1"
GRADED_FIELDS = ("expected_decision", "expected_model", "expected_domain")


def die(message: str) -> NoReturn:
    """Write a harness-error message to stderr and exit with the reserved code 2."""
    print(message, file=sys.stderr)
    raise SystemExit(2)


@dataclass(frozen=True)
class EvalRecord:
    """One labeled request from the dataset."""

    id: str
    text: str
    expected_decision: str | None = None
    expected_model: str | None = None
    expected_domain: str | None = None

    @property
    def has_expectation(self) -> bool:
        return any(getattr(self, f) is not None for f in GRADED_FIELDS)


@dataclass
class Actual:
    """Normalized routing decision returned by the router."""

    decision: str | None
    models: list[str]
    domains: list[str]
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass
class RecordResult:
    id: str
    passed: bool
    checks: dict[str, dict[str, Any]]
    actual_decision: str | None
    actual_models: list[str]
    actual_domains: list[str]
    error: str | None = None


def load_dataset(path: Path) -> list[EvalRecord]:
    records: list[EvalRecord] = []
    seen: set[str] = set()
    try:
        fh = path.open(encoding="utf-8")
    except OSError as exc:
        die(f"[dataset] {path}: {exc}")
    with fh:
        for lineno, line in enumerate(fh, start=1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError as exc:
                die(f"[dataset] {path}:{lineno}: invalid JSON: {exc}")
            if not isinstance(row, dict):
                die(f"[dataset] {path}:{lineno}: each line must be a JSON object")
            if "id" not in row or "text" not in row:
                die(f"[dataset] {path}:{lineno}: 'id' and 'text' are required")
            rec = EvalRecord(
                id=str(row["id"]),
                text=str(row["text"]),
                expected_decision=row.get("expected_decision"),
                expected_model=row.get("expected_model"),
                expected_domain=row.get("expected_domain"),
            )
            if rec.id in seen:
                die(f"[dataset] {path}:{lineno}: duplicate id {rec.id!r}")
            seen.add(rec.id)
            if not rec.has_expectation:
                die(f"[dataset] {path}:{lineno}: record {rec.id!r} has no expected_* field to grade")
            records.append(rec)
    if not records:
        die(f"[dataset] {path}: no records found")
    return records


def call_router(router_url: str, endpoint: str, text: str, timeout: float) -> Actual:
    """POST one text to the classification API and normalize the response.

    Handles both the ``/api/v1/eval`` and ``/api/v1/classify/intent`` shapes so
    the harness works against either without upstream model completion.
    """
    path = "/api/v1/eval" if endpoint == "eval" else "/api/v1/classify/intent"
    body = json.dumps({"text": text}).encode("utf-8")
    req = urllib.request.Request(
        router_url.rstrip("/") + path,
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.loads(resp.read().decode("utf-8"))

    decision = payload.get("routing_decision")
    if decision is None:
        decision = (payload.get("decision_result") or {}).get("decision_name")

    models: list[str] = []
    if isinstance(payload.get("recommended_models"), list):
        models = [str(m) for m in payload["recommended_models"]]
    elif payload.get("recommended_model") is not None:
        models = [str(payload["recommended_model"])]

    matched = (
        (payload.get("decision_result") or {}).get("matched_signals")
        or payload.get("matched_signals")
        or {}
    )
    domains = [str(d) for d in matched.get("domains", [])]

    return Actual(decision=decision, models=models, domains=domains, raw=payload)


def grade(record: EvalRecord, actual: Actual) -> RecordResult:
    checks: dict[str, dict[str, Any]] = {}
    if record.expected_decision is not None:
        checks["decision"] = {
            "expected": record.expected_decision,
            "actual": actual.decision,
            "pass": actual.decision == record.expected_decision,
        }
    if record.expected_model is not None:
        checks["model"] = {
            "expected": record.expected_model,
            "actual": actual.models,
            "pass": record.expected_model in actual.models,
        }
    if record.expected_domain is not None:
        checks["domain"] = {
            "expected": record.expected_domain,
            "actual": actual.domains,
            "pass": record.expected_domain in actual.domains,
        }
    passed = all(c["pass"] for c in checks.values())
    return RecordResult(
        id=record.id,
        passed=passed,
        checks=checks,
        actual_decision=actual.decision,
        actual_models=actual.models,
        actual_domains=actual.domains,
    )


def run(records: list[EvalRecord], router_url: str, endpoint: str, timeout: float) -> dict[str, Any]:
    results: list[RecordResult] = []
    for rec in records:
        try:
            actual = call_router(router_url, endpoint, rec.text, timeout)
            results.append(grade(rec, actual))
        except urllib.error.URLError as exc:
            die(f"[router] {router_url} unreachable: {exc}")
        except (KeyError, ValueError) as exc:
            results.append(
                RecordResult(
                    id=rec.id,
                    passed=False,
                    checks={},
                    actual_decision=None,
                    actual_models=[],
                    actual_domains=[],
                    error=f"parse error: {exc}",
                )
            )

    per_field: dict[str, dict[str, int]] = {}
    for res in results:
        for name, chk in res.checks.items():
            bucket = per_field.setdefault(name, {"graded": 0, "passed": 0})
            bucket["graded"] += 1
            bucket["passed"] += int(chk["pass"])

    total = len(results)
    passed = sum(1 for r in results if r.passed)
    return {
        "schema_version": SCHEMA_VERSION,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "router_url": router_url,
        "endpoint": endpoint,
        "summary": {
            "total": total,
            "passed": passed,
            "failed": total - passed,
            "accuracy": round(passed / total, 4) if total else 0.0,
            "per_field": {
                name: {
                    **counts,
                    "accuracy": round(counts["passed"] / counts["graded"], 4)
                    if counts["graded"]
                    else 0.0,
                }
                for name, counts in sorted(per_field.items())
            },
        },
        "records": [
            {
                "id": r.id,
                "pass": r.passed,
                "checks": r.checks,
                "actual_decision": r.actual_decision,
                "actual_models": r.actual_models,
                "actual_domains": r.actual_domains,
                **({"error": r.error} if r.error else {}),
            }
            for r in results
        ],
    }


def detect_regressions(report: dict[str, Any], baseline: dict[str, Any]) -> list[str]:
    """Records that passed in the baseline but fail now."""
    was_pass = {r["id"]: r["pass"] for r in baseline.get("records", [])}
    return sorted(
        r["id"] for r in report["records"] if was_pass.get(r["id"]) is True and not r["pass"]
    )


def render_summary(report: dict[str, Any], regressions: list[str]) -> str:
    s = report["summary"]
    lines = [
        f"Decision-level routing eval  ({report['endpoint']} @ {report['router_url']})",
        f"  overall: {s['passed']}/{s['total']} passed  (accuracy {s['accuracy']:.1%})",
    ]
    for name, c in s["per_field"].items():
        lines.append(f"  {name:9s}: {c['passed']}/{c['graded']}  ({c['accuracy']:.1%})")
    failures = [r for r in report["records"] if not r["pass"]]
    if failures:
        lines.append("  mismatches:")
        for r in failures:
            if r.get("error"):
                lines.append(f"    - {r['id']}: {r['error']}")
                continue
            bad = [
                f"{name} expected={chk['expected']!r} actual={chk['actual']!r}"
                for name, chk in r["checks"].items()
                if not chk["pass"]
            ]
            lines.append(f"    - {r['id']}: " + "; ".join(bad))
    if regressions:
        lines.append(f"  REGRESSIONS vs baseline: {', '.join(regressions)}")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--dataset", required=True, type=Path, help="JSONL dataset path")
    parser.add_argument(
        "--router-url", default="http://127.0.0.1:8080", help="router apiserver base URL"
    )
    parser.add_argument("--endpoint", choices=("eval", "intent"), default="eval")
    parser.add_argument("--json-out", type=Path, help="write the JSON report to this path")
    parser.add_argument(
        "--baseline", type=Path, help="prior JSON report; flag pass->fail regressions"
    )
    parser.add_argument("--fail-under", type=float, default=None, help="min overall accuracy [0-1]")
    parser.add_argument("--timeout", type=float, default=30.0, help="per-request timeout (s)")
    args = parser.parse_args(argv)

    records = load_dataset(args.dataset)
    report = run(records, args.router_url, args.endpoint, args.timeout)

    regressions: list[str] = []
    if args.baseline:
        try:
            baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            die(f"[baseline] {args.baseline}: {exc}")
        regressions = detect_regressions(report, baseline)

    if args.json_out:
        args.json_out.parent.mkdir(parents=True, exist_ok=True)
        args.json_out.write_text(
            json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )

    print(render_summary(report, regressions))

    if regressions:
        return 1
    if args.fail_under is not None:
        return 0 if report["summary"]["accuracy"] >= args.fail_under else 1
    if report["summary"]["failed"]:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
