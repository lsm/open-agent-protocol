#!/usr/bin/env python3
"""Check refresh-window diagnostic positions through all three validator CLIs."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def run(command, output, label):
    child = subprocess.Popen(command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                             text=True, start_new_session=True)
    try:
        stdout, stderr = child.communicate(timeout=300)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        stdout, stderr = child.communicate()
        raise
    finally:
        (output / f"{label}.stdout").write_text(stdout)
        (output / f"{label}.stderr").write_text(stderr)
    return child.returncode, stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    goap = output / "goap"
    for label, command in [
        ("go-build", ["go", "build", "-o", str(goap), "./go/cmd/goap"]),
        ("rust-build", ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"]),
        ("zig-build", ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"]),
    ]:
        status, stdout = run(command, output, label)
        assert status == 0, (label, status, stdout)
    commands = {
        "go": [str(goap), "validate", "--format", "json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }
    cases = [
        ("semantic-invalid/capability-refresh-requires-announced-revision.json", [4, 5, 13, 14, 18, 19]),
        ("semantic-invalid/capability-refresh-late-response-after-refresh.json", [6]),
        ("valid/permission-resolved-in-the-refresh-window.json", []),
        ("valid/user-input-resolved-in-the-refresh-window.json", []),
    ]
    results = []
    for fixture, expected in cases:
        for language, command in commands.items():
            label = Path(fixture).stem + "." + language
            status, stdout = run(command + [str(ROOT / "fixtures" / fixture)], output, label)
            parsed = json.loads(stdout)
            reports = parsed if isinstance(parsed, list) else parsed.get("reports", [parsed])
            diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
            assert status == int(bool(expected)), (label, status, diagnostics)
            assert all(d["phase"] == "semantic" and d["code"] == "stale_capability_revision"
                       for d in diagnostics), (label, diagnostics)
            indices = [d.get("index", 0) for d in diagnostics]
            assert indices == expected, (label, expected, diagnostics)
            results.append({"fixture": fixture, "language": language, "exit": status, "indices": indices})
            print(f"{label}: exit {status}, stale indices {indices}", flush=True)
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print("PASS: 12 public CLI judgments, 0 disagreements", flush=True)


if __name__ == "__main__":
    main()
