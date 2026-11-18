#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
demo_dir=$(mktemp -d "${TMPDIR:-/tmp}/gtm-demo.XXXXXX")
printf 'Demo output directory: %s\n' "$demo_dir"
./scripts/demo.sh --json --state-dir "$demo_dir/state" > "$demo_dir/connector.json"
.venv/bin/python -m gtm_connector evaluate --json > "$demo_dir/evaluation.json"
.venv/bin/python -m gtm_connector usage --json --state-dir "$demo_dir/state" > "$demo_dir/usage.json"
.venv/bin/python - "$demo_dir" <<'PY'
import json
from pathlib import Path
import subprocess
import sys

directory = Path(sys.argv[1])
demo = json.loads((directory / "connector.json").read_text())
evaluation = json.loads((directory / "evaluation.json").read_text())
usage = json.loads((directory / "usage.json").read_text())
brief = demo["briefs"][0]
candidate = directory / "candidate.txt"
candidate.write_text(brief["text"])
command = [sys.executable, "-m", "gtm_connector", "check-brief", "--json", brief["brief_id"],
           str(candidate), "--state-dir", str(directory / "state")]
accepted = subprocess.run(command, capture_output=True, text=True, check=True)
(directory / "checked.json").write_text(accepted.stdout)
assert json.loads(accepted.stdout) == {"status": "valid", "brief_id": brief["brief_id"]}
candidate.write_text(brief["text"] + "The renewal was approved.\n")
rejected = subprocess.run(command, capture_output=True, text=True, check=False)
(directory / "rejected.stderr.txt").write_text(rejected.stderr)
assert rejected.returncode == 4 and rejected.stdout == ""
assert "unsupported" in rejected.stderr
candidate.write_text(brief["text"])
assert evaluation["status"] == "passed" and evaluation["accounts"] == 20
assert usage["calls"] == {"total": 23, "ok": 20, "error": 3, "pending": 0}
assert usage["model_tokens"] == {"saved_briefs": 3, "unknown_briefs": 0,
                                 "input_tokens": 0, "output_tokens": 0}
print(f'Accounts: {demo["accounts"]}')
print(f'Calls scope denial: HTTP {demo["notes_without_scope"]["status"]}')
print(f'Idempotent write: {demo["idempotent_write"]["requests"]} requests, '
      f'{demo["idempotent_write"]["activities"]} activity')
print(f'Rate limit: HTTP {demo["rate_limit"]["status"]}')
print(f'Audit receipts: {demo["audit_rows"]} connector + {demo["brief_audit_rows"]} briefing')
print(demo["contradiction_flags"][0])
print(f'Activities from briefing: {demo["injection_brief"]["activities_created"]}')
print('Citation checker: accepted generated text; rejected an uncited claim with exit 4')
print(f'Evaluation: {evaluation["accounts"]} accounts, '
      f'{evaluation["facts_found"]}/{evaluation["expected_facts"]} facts found, '
      f'{evaluation["facts_cited_correctly"]}/{evaluation["expected_facts"]} cited correctly, '
      f'{evaluation["facts_invented"]} invented')
print(f'Usage: {usage["calls"]["total"]} calls, {usage["model_tokens"]["saved_briefs"]} saved briefs, '
      '0 input and 0 output model tokens')
print(f'Inspect {directory}/connector.json, evaluation.json, usage.json, and state/')
PY
