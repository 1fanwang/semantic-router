# Decision-level routing evaluation harness

Grades whether the router selects the correct **decision** and **model** for a
labeled dataset of requests, and does it **without any upstream model
completion**. It drives the router's classification API, so it isolates the
routing decision from answer quality — the gap between signal-level eval
("did the extractor fire?") and end-to-end eval ("was the answer good?").

Implements the harness described in
[vllm-project/semantic-router#2333](https://github.com/vllm-project/semantic-router/issues/2333).

Stdlib only — no pip install needed.

## Dataset format

One JSON object per line (`.jsonl`). Blank lines and `#` comments are ignored.

| Field | Required | Meaning |
|-------|----------|---------|
| `id` | yes | Unique record id (used for regression tracking). |
| `text` | yes | The request text to classify. |
| `expected_decision` | one of the three | Decision name the router should match (e.g. `business_decision`). |
| `expected_model` | one of the three | Model the decision should recommend (e.g. `Model-A`). |
| `expected_domain` | one of the three | Domain signal that should match (e.g. `computer science`). |

At least one `expected_*` field must be present per record. Each present field
is graded independently, and a record passes only if all its expectations hold.
`expected_model` passes if the value is in the router's recommended models;
`expected_domain` passes if it is in the matched domain signals.

Example: [`datasets/domain_routing.jsonl`](datasets/domain_routing.jsonl),
labeled to match the decisions in `config/config.yaml` / the e2e recipe.

## Usage

Bring up a router first (it only needs the classifiers, not a model backend):

```bash
make run-router CONFIG_FILE=config/config.yaml   # or any recipe
curl -s http://localhost:8080/ready              # wait for {"ready":true}
```

Then grade a dataset:

```bash
python3 bench/decision_eval/harness.py \
  --dataset bench/decision_eval/datasets/domain_routing.jsonl \
  --json-out .agent-harness/decision-eval/report.json
```

```text
Decision-level routing eval  (eval @ http://127.0.0.1:8080)
  overall: 12/12 passed  (accuracy 100.0%)
  decision : 12/12  (100.0%)
  domain   : 12/12  (100.0%)
  model    : 12/12  (100.0%)
```

Options:

- `--endpoint {eval,intent}` — `eval` (default) uses `/api/v1/eval` and carries
  full signal evidence; `intent` uses `/api/v1/classify/intent`.
- `--router-url` — default `http://127.0.0.1:8080`.
- `--json-out PATH` — write the stable JSON report.
- `--baseline PATH` — a prior JSON report; any record that passed then and fails
  now is reported as a regression (non-zero exit).
- `--fail-under FLOAT` — minimum overall accuracy `[0-1]`; below it exits non-zero.
- `--timeout FLOAT` — per-request timeout, seconds.

Exit codes: `0` clean · `1` failures / regression / below `--fail-under` ·
`2` harness error (router unreachable, bad dataset/baseline).

## Regression guard (CI / config tuning)

Save a baseline on a known-good config, then fail the build when a config,
threshold, or recipe change flips a previously-correct route:

```bash
# baseline (once, on the reference config)
python3 bench/decision_eval/harness.py --dataset <ds> --json-out baseline.json

# on a change
python3 bench/decision_eval/harness.py --dataset <ds> \
  --baseline baseline.json --fail-under 0.9
```

The JSON report is deterministic (`sort_keys`, no wall-clock in the graded
fields) and records expected vs. actual decision/model, matched domains, and —
via `--endpoint eval` — the underlying signal evidence, so it can gate adaptive
thresholds and agentic config tuning.

## Tests

```bash
python3 bench/decision_eval/test_harness.py        # or: python3 -m unittest bench.decision_eval.test_harness
```

`test_harness.py` is stdlib-only and self-contained: it starts an in-process
mock router, so the whole client path (request → HTTP → parse → grade → report →
exit code) is covered without a live backend. It exercises dataset loading and
validation, grading, regression detection, both endpoint response shapes, and
every `main()` exit code including `--fail-under` and `--baseline`.
