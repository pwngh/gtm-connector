#!/usr/bin/env python3
"""Check the captured transcript before rendering documentation images."""

import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


def render(source):
    media = Path("docs/media")
    raw = source.read_text()
    entries = [json.loads(line) for line in raw.splitlines()]
    header, events = entries[0], entries[1:]
    assert header["version"] == 2 and header["width"] == 112 and header["height"] == 28
    assert set(header.get("env", {})) <= {"TERM"}
    assert events and events[-1][1:] == ["x", "0"], "Recording must end with a successful exit"
    assert all(event[1] == "o" for event in events[:-1]), "Only output may be recorded"
    times = [event[0] for event in events]
    assert times == sorted(times), "Nonmonotonic recording"
    text = "".join(event[2] for event in events if event[1] == "o")
    opening = next(index for index, event in enumerate(events)
                   if event[1] == "o" and "1. Run the fixture demo" in event[2])
    assert "Six tools returned by Inspector CLI." in text
    assert "error: Brief contains an uncited, unsupported, or misattributed statement." in text
    assert "HTTP 403" in text and "[crm/opportunities/opp-003]" in text
    published = [media / name for name in (
        "recording-session.txt", "recording-provenance.json", "inspector-tools.json")]
    contents = raw + "\n" + "\n".join(path.read_text() for path in published)
    forbidden = (r"Bearer\s+\S+|owner_secret|access_token|refresh_token|BEGIN .*PRIVATE KEY|"
                 r"/Users/|/home/|/Volumes/|/private/var/|/var/folders/")
    assert not re.search(forbidden, contents), "Private material in recording output"
    # Check actual locally issued credentials as well as recognizable patterns.
    for path in Path("var").glob("recording-*/**/owner.json"):
        secret = json.loads(path.read_text()).get("owner_secret")
        assert not secret or secret not in contents, "Owner credential in recording output"
    for path in Path("var").glob("recording-*/**/inspector.json"):
        config = json.loads(path.read_text())
        for server in config.get("mcpServers", {}).values():
            credential = server.get("headers", {}).get("Authorization", "").removeprefix("Bearer ")
            assert not credential or credential not in contents, "Bearer credential in recording output"
    cast = media / "walkthrough.cast"
    cast.write_text(raw)
    common = ["agg", "--quiet", "--theme", "github-dark", "--font-size", "18",
              "--line-height", "1.25", "--idle-time-limit", "12", "--fps-cap", "5"]
    commands = [
        common + ["--select", f"event:{opening}..", "--last-frame-duration", "10",
                  str(cast), str(media / "walkthrough.gif")],
        common + ["--select", "100%", "--last-frame-duration", "1",
                  str(cast), str(media / "inspector-cli.gif")],
    ]
    for command in commands:
        subprocess.run(command, check=True)
    provenance_path = media / "recording-provenance.json"
    provenance = json.loads(provenance_path.read_text())
    provenance["rendering"] = {
        "asciinema": subprocess.check_output(["asciinema", "--version"], text=True).strip(),
        "agg": subprocess.check_output(["agg", "--version"], text=True).strip(),
        "commands": commands,
        "idle_limit_seconds": 12,
        "source_duration_seconds": times[-1],
        "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "note": "Real output; selected fields; idle periods capped; fixed reading pauses.",
    }
    provenance["artifact_sha256"] = {
        path.name: hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(media.iterdir()) if path.is_file() and path != provenance_path
    }
    provenance_path.write_text(json.dumps(provenance, indent=2) + "\n")


if __name__ == "__main__":
    try:
        render(Path(sys.argv[1]))
    except (AssertionError, OSError, ValueError, KeyError, subprocess.SubprocessError):
        print("Recording verification or rendering failed. Nothing was uploaded.", file=sys.stderr)
        raise SystemExit(1) from None
