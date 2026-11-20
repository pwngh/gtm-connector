#!/bin/sh
set -eu
[ $# -eq 0 ] || { echo "This script takes no arguments. For options, run: gtm-connector demo --help" >&2; exit 2; }
cd "$(dirname "$0")/.."
umask 077
demo_dir=$(mktemp -d "${TMPDIR:-/tmp}/gtm-demo.XXXXXX")
printf 'Demo output directory: %s\n' "$demo_dir"
python3 -m venv .venv
.venv/bin/python -m pip install --quiet --disable-pip-version-check --require-hashes -r requirements.lock
.venv/bin/python -m unittest discover -s tests
.venv/bin/python -m gtm_connector demo --json --state-dir "$demo_dir/state" > "$demo_dir/demo.json"
.venv/bin/python - "$demo_dir" <<'PY'
import json
from pathlib import Path
import re
import subprocess
import sys

from gtm_connector.cli import human_result

directory = Path(sys.argv[1])
state = directory / "state"
demo = json.loads((directory / "demo.json").read_text())
brief = demo["brief"]
saved = json.loads((state / "briefs" / (brief["brief_id"] + ".json")).read_text())
assert {key: saved[key] for key in brief} == brief
assert demo["status"] == "accepted" and demo["account"]["id"] == brief["account_id"] == "acc-003"
assert demo["denied_read"]["status"] == 403 and demo["audit"]["rows"] == 7
assert demo["activities_created"] == 0
# Every number printed on the card resolves to a record and a finished receipt.
numbers = sorted({int(value) for value in re.findall(r"\[(\d+)\]", brief["text"])})
traced = json.loads(subprocess.run(
    [sys.executable, "-m", "gtm_connector", "inspect", "--json", brief["brief_id"], *map(str, numbers),
     "--state-dir", str(state)], capture_output=True, text=True, check=True).stdout)
assert [row["n"] for row in traced] == numbers and all(row["receipt"]["status"] == "ok" for row in traced)
print(human_result("demo", demo, state_dir=state))
print("\nEvery number on the card resolved to its record and audit receipt.")
print("The saved audit has seven receipts: five successful reads, one scope refusal, and the card's seal.")
print(f"Inspect {directory}/demo.json and state/.")
print("Run separate connector checks with: .venv/bin/python -m gtm_connector debug verify "
      "--with-slack --state-dir var/verification")
PY
