# vSR Copilot CLI plugin

- This folder is a GitHub Copilot CLI plugin (`vsr-copilot`), listed in the root
  `.github/plugin/marketplace.json` as `vsr-copilot@vllm-sr`. It is a client of
  a decision runtime's public `/v1/decisions` API; it never imports router or
  runtime code, and nothing from `../claude-code`.
- Copilot CLI reads only the first marketplace file it finds, and
  `.github/plugin/marketplace.json` comes before `.claude-plugin/marketplace.json`.
  That is why the file also lists the Claude Code plugin as `vsr`; keep the two
  `vsr` entries in step.
- `hooks/vsr.py` handles both hook events and uses only the Python standard
  library, since Copilot CLI runs it with the user's `python3`.
- `hooks/framing.py` is a copy of the Claude Code plugin's questions. Change
  the Claude Code plugin first, then copy the file here; `test_framing_copy.py`
  fails while the two differ.
- `hooks.json` keeps the `Bash` matcher and the two timeouts (15 s before the
  command, 30 s after it) that were measured on Copilot CLI 1.0.114. A hook that
  outruns its timeout is skipped and the command runs unchecked, so keep the
  `timeoutSeconds` default in `hooks/vsr.py` below them.
- Before a change is done, run `python3 -m unittest discover -s hooks`. CI
  runs it through the `copilot-cli-plugin` entry in `tools/agent/domains.yaml`.
