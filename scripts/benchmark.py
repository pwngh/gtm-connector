#!/usr/bin/env python3
"""Measure fresh processes, server readiness, and audited calls on invented data."""

import argparse
from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import select
import signal
import statistics
import ssl
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[1]
WARMUPS = 5


def summarize(samples):
    """Keep every observation and use the nearest-rank definition for p95."""
    ordered = sorted(samples)
    return {"count": len(samples), "samples_ms": samples,
            "p50_ms": statistics.median(samples),
            "p95_ms": ordered[math.ceil(0.95 * len(ordered)) - 1]}


def elapsed(start):
    return (time.perf_counter_ns() - start) / 1_000_000


def environment(source):
    # Select the requested checkout even when the interpreter has another copy installed.
    return {**os.environ, "PYTHONPATH": str(source)}


def cli_sample(source, command):
    with tempfile.TemporaryDirectory(prefix="gtm-benchmark-") as temporary:
        arguments = [command] if command.startswith("--") else [
            "brief", "acc-003", "--json", "--state-dir", str(Path(temporary) / "state")]
        start = time.perf_counter_ns()
        result = subprocess.run([sys.executable, "-m", "gtm_connector", *arguments],
                                cwd=source, env=environment(source), capture_output=True,
                                text=True, timeout=60, check=True)
        duration = elapsed(start)
        if command == "brief":
            brief = json.loads(result.stdout)
            if brief["status"] != "accepted" or brief["account_id"] != "acc-003":
                raise RuntimeError("The fixture brief did not pass.")
        elif not result.stdout.strip():
            raise RuntimeError("The CLI returned no output.")
        return duration


def stop_process(process):
    if process.poll() is None:
        process.send_signal(signal.SIGINT)
    try:
        process.communicate(timeout=10)
    except subprocess.TimeoutExpired:
        process.terminate()
        try:
            process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()


def ready_sample(source):
    with tempfile.TemporaryDirectory(prefix="gtm-benchmark-ready-") as temporary:
        start = time.perf_counter_ns()
        process = subprocess.Popen(
            [sys.executable, "-m", "gtm_connector", "serve", "--json", "--state-dir",
             str(Path(temporary) / "state")], cwd=source, env=environment(source),
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            readable, _, _ = select.select([process.stdout], [], [], 30)
            if not readable:
                raise RuntimeError("The server did not report readiness.")
            ready = json.loads(process.stdout.readline())
            duration = elapsed(start)
            if ready.get("type") != "ready" or ready.get("crm_backend") != "sqlite":
                raise RuntimeError("The fixture server did not report readiness.")
            return duration
        finally:
            stop_process(process)


def certificate_worker(samples):
    from gtm_connector.runtime import certificate
    from cryptography import __version__ as cryptography_version
    from cryptography.hazmat.backends import default_backend

    values = []
    with tempfile.TemporaryDirectory(prefix="gtm-benchmark-cert-") as temporary:
        for index in range(samples):
            directory = Path(temporary) / str(index)
            directory.mkdir(mode=0o700)
            start = time.perf_counter_ns()
            key, cert = certificate(directory)
            values.append(elapsed(start))
            if not key.is_file() or not cert.is_file():
                raise RuntimeError("Certificate generation did not create both files.")
    return {"metric": summarize(values), "libraries": {
        "cryptography": cryptography_version,
        "cryptography_openssl": default_backend().openssl_version_text(),
        "python_openssl": ssl.OPENSSL_VERSION}}


def call_worker(calls):
    from gtm_connector.client import LocalClient
    from gtm_connector.runtime import Runtime
    from gtm_connector import store

    values, direct, overhead = [], [], []
    seen = set()
    with tempfile.TemporaryDirectory(prefix="gtm-benchmark-calls-") as temporary:
        with Runtime(Path(temporary) / "state") as runtime, LocalClient(runtime.config) as client:
            token = client.authorize(["crm:read"])
            for index in range(WARMUPS + calls):
                start = time.perf_counter_ns()
                response = client.call(token, "get_account", {"account_id": "acc-003"})
                result = response.json()["result"]
                duration = elapsed(start)
                if response.status_code != 200 or result.get("isError"):
                    raise RuntimeError("An audited fixture call failed.")
                start = time.perf_counter_ns()
                with closing(store.connect(runtime.directory / "crm.sqlite3", readonly=True)) as connection:
                    account = store.get_account(connection, "acc-003")
                direct_duration = elapsed(start)
                if account is None:
                    raise RuntimeError("The direct fixture read returned no account.")
                receipt_id = result["_meta"]["gtm.demo/auditId"]
                # An independent read-only connection observes each final commit
                # after the timer stops, before another request is measured.
                receipts = runtime.connector.audit.read()
                matching = [row for row in receipts if row["audit_id"] == receipt_id]
                if (receipt_id in seen or len(matching) != 1 or matching[0]["status"] != "ok"
                        or matching[0]["http_status"] != 200 or any(row["status"] == "pending" for row in receipts)):
                    raise RuntimeError("The call lacks one committed successful receipt.")
                seen.add(receipt_id)
                if index >= WARMUPS:
                    values.append(duration)
                    direct.append(direct_duration)
                    overhead.append(duration - direct_duration)
            if len(receipts) != calls + WARMUPS:
                raise RuntimeError("The receipt count does not match the call count.")
    return {"metrics": {"audited_call": summarize(values), "direct_read": summarize(direct),
                        "connector_overhead": summarize(overhead)}, "audit": {
        "measured_calls": calls, "warmup_calls": WARMUPS,
        "committed_receipts": len(seen), "pending_receipts": 0}}


def worker(source, kind, samples, calls):
    result = subprocess.run(
        [sys.executable, str(Path(__file__).resolve()), "--source", str(source),
         "--worker", kind, "--samples", str(samples), "--calls", str(calls)],
        cwd=source, env=environment(source), capture_output=True, text=True, timeout=120, check=True)
    return json.loads(result.stdout)


def git_value(source, *arguments):
    result = subprocess.run(["git", "-C", str(source), *arguments], capture_output=True, text=True, timeout=10)
    return result.stdout.strip() if result.returncode == 0 else None


def machine():
    details = {"python": platform.python_version(), "python_implementation": platform.python_implementation(),
               "os": platform.system(), "os_release": platform.release(), "architecture": platform.machine(),
               "logical_cpus": os.cpu_count(), "cpu": platform.processor() or "unknown", "memory_bytes": None}
    if sys.platform == "darwin":
        details["os_version"] = platform.mac_ver()[0]
        for key, field in (("cpu", "machdep.cpu.brand_string"), ("memory_bytes", "hw.memsize")):
            result = subprocess.run(["sysctl", "-n", field], capture_output=True, text=True, timeout=10)
            if result.returncode == 0:
                value = result.stdout.strip()
                details[key] = int(value) if key == "memory_bytes" else value
    elif sys.platform.startswith("linux"):
        try:
            for line in Path("/proc/cpuinfo").read_text().splitlines():
                if line.startswith("model name"):
                    details["cpu"] = line.split(":", 1)[1].strip()
                    break
            details["memory_bytes"] = os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")
        except (OSError, ValueError):
            pass
    return details


def benchmark(args):
    metrics = {}
    started = datetime.now(timezone.utc).isoformat()
    for name, command in (("version", "--version"), ("help", "--help"), ("brief", "brief")):
        print(f"Measuring {name}: {args.samples} fresh processes.", file=sys.stderr, flush=True)
        metrics[name] = summarize([cli_sample(args.source, command) for _ in range(args.samples)])
    print(f"Measuring server readiness: {args.samples} fresh processes.", file=sys.stderr, flush=True)
    metrics["server_ready"] = summarize([ready_sample(args.source) for _ in range(args.samples)])
    print(f"Measuring certificate generation: {args.samples} samples.", file=sys.stderr, flush=True)
    certificates = worker(args.source, "certificate", args.samples, args.calls)
    metrics["certificate_generation"] = certificates["metric"]
    print(f"Measuring audited calls: {args.calls} calls after {WARMUPS} warmups.", file=sys.stderr, flush=True)
    calls = worker(args.source, "calls", args.samples, args.calls)
    metrics.update(calls["metrics"])
    status = git_value(args.source, "status", "--porcelain", "--untracked-files=normal")
    return {
        "schema_version": 1, "started_at_utc": started, "finished_at_utc": datetime.now(timezone.utc).isoformat(),
        "source_commit": git_value(args.source, "rev-parse", "HEAD"),
        "source_dirty": status != "" if status is not None else None,
        "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "machine": machine(), "libraries": certificates["libraries"], "metrics": metrics, "audit": calls["audit"],
        "method": {
            "cold": "A fresh Python process for each observation. Filesystem caches are not cleared.",
            "brief": "A fresh fixture state folder. Total process time includes startup, output, and shutdown.",
            "server_ready": "Process launch through the first parsed ready event. Shutdown is excluded.",
            "certificate_generation": "The repository certificate function, including its private key and cert writes.",
            "audited_call": "Complete client request and response JSON decoding, including the source read and both "
                            "audit commits. Authorization and five warmups are excluded. Each final receipt is "
                            "verified outside its timer through an independent read-only connection.",
            "connector_overhead": "Each audited call duration minus its paired direct SQLite get_account read, "
                                  "including connection open and close. The direct read follows the HTTPS call. "
                                  "Receipt verification follows both timers. Negative differences are retained.",
            "percentiles": "p50 is the median. p95 is nearest rank: sorted sample at ceil(0.95 * count).",
        },
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=ROOT, help="Measure this source checkout.")
    parser.add_argument("--samples", type=int, default=20, help="Use this many process and certificate samples.")
    parser.add_argument("--calls", type=int, default=50, help="Measure 1 to 50 calls after five warmups.")
    parser.add_argument("--output", type=Path, help="Also save the JSON report to this file.")
    parser.add_argument("--worker", choices=("calls", "certificate"), help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.samples < 1 or not 1 <= args.calls <= 50:
        parser.error("--samples must be positive; --calls must be between 1 and 50.")
    args.source = args.source.resolve()
    if not (args.source / "gtm_connector" / "__main__.py").is_file():
        parser.error("--source must contain the gtm_connector package.")
    try:
        if args.worker:
            sys.path.insert(0, str(args.source))
            result = call_worker(args.calls) if args.worker == "calls" else certificate_worker(args.samples)
        else:
            result = benchmark(args)
        encoded = json.dumps(result, indent=2) + "\n"
        if args.output:
            args.output.write_text(encoded)
        print(encoded, end="")
    except KeyboardInterrupt:
        print("Benchmark interrupted. Temporary fixture servers are stopped.", file=sys.stderr)
        return 130
    except Exception:
        print("Benchmark failed. Run the repository checks, then retry with --samples 1 --calls 1.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
