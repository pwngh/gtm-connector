#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
demo_dir=$(mktemp -d "${TMPDIR:-/tmp}/gtm-demo.XXXXXX")
printf 'Demo output directory: %s\n' "$demo_dir"
./scripts/demo.sh --json --state-dir "$demo_dir/state" > "$demo_dir/demo.json"
.venv/bin/python -m gtm_connector usage --json --state-dir "$demo_dir/state" > "$demo_dir/usage.json"
.venv/bin/python - "$demo_dir" <<'PY'
import json
from pathlib import Path
import subprocess
import sys

from gtm_connector.cli import human_result

directory = Path(sys.argv[1])
demo = json.loads((directory / "demo.json").read_text())
usage = json.loads((directory / "usage.json").read_text())
brief = demo["brief"]
saved = json.loads((directory / "state" / "briefs" / (brief["brief_id"] + ".json")).read_text())
assert {key: saved[key] for key in brief} == brief
assert demo["status"] == "accepted" and demo["rep"]["name"] == "Morgan Reed"
assert demo["account"]["id"] == brief["account_id"] == "acc-007"
assert demo["denied_read"]["status"] == 403 and demo["audit"]["rows"] == 6
assert demo["injection"]["quoted"] and demo["activities_created"] == 0
assert usage["calls"] == {"total": 6, "ok": 5, "error": 1, "pending": 0}
assert usage["model_tokens"] == {"saved_briefs": 1, "unknown_briefs": 0,
                                 "input_tokens": 0, "output_tokens": 0}
for reference in ("crm/accounts/acc-007", "calls/call_notes/note-007", "slack/messages/msg-007"):
    assert "[" + reference + "]" in brief["text"]
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
assert json.loads(rejected.stderr)["error"]["code"] == "check_failed"
candidate.write_text(brief["text"])
print(human_result("demo", demo))
print("\nInspect the same renewal brief.")
print("The citation checker accepted Morgan's saved brief.")
print("Appending 'The renewal was approved.' was rejected with exit 4 and empty stdout.")
print("The saved audit has six receipts: five successful reads and one scope refusal.")
print("One brief was saved. No model tokens or activity writes were used.")
print(f"Inspect {directory}/demo.json, usage.json, candidate.txt, and state/.")
print("Run separate connector checks with: .venv/bin/python -m gtm_connector verify --with-slack")
PY
