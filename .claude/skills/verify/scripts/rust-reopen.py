#!/usr/bin/env python3
"""Check reopen outcomes on all three consumer surfaces, or mutation-check Rust's tests."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
RUST = ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
        "-p", "oap-validation", "--example", "validate", "--"]
COMMANDS = {
    "rust": RUST,
    "go": ["go", "run", "./go/cmd/goap", "validate", "--format", "json"],
    "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
}


def mutations(output):
    path = ROOT / "rust/validation/src/state/admissions.rs"
    original = path.read_text()
    command = ["cargo", "test", "--manifest-path", "rust/Cargo.toml",
               "-p", "oap-validation", "--test", "reopen"]
    baseline = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / "baseline.log").write_text(baseline.stdout + baseline.stderr)
    assert baseline.returncode == 0, "focused tests must pass before mutation"
    changes = [
        ("skip-recovery-check", "if !recovered {", "if false {"),
        ("skip-active-check", "if request.message.is_none()", "if false"),
        ("allow-running-entry", ".all(|entry| entry.status == RunStatus::QUEUED)", ".all(|_| true)"),
        ("ignore-legacy-active", "&& active.is_empty()", "&& true"),
        ("drop-message-exemption", "if request.message.is_none()", "if true"),
        ("judge-ordinary-opens", ".filter(|request| request.reopen)", ".filter(|_| true)"),
    ]
    try:
        for label, before, after in changes:
            assert original.count(before) == 1, label
            path.write_text(original.replace(before, after, 1))
            result = subprocess.run(command, cwd=ROOT, capture_output=True,
                                    text=True, timeout=300)
            (output / f"{label}.log").write_text(result.stdout + result.stderr)
            assert result.returncode != 0 and "test result: FAILED" in result.stdout, label
            print(f"KILLED {label}", flush=True)
    finally:
        path.write_text(original)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    if args.mutation_check:
        mutations(output)
        return
    names = [
        ("valid/session-reopen-recovered.json", []),
        ("valid/session-reopen-lists-restored-queued-runs.json", []),
        ("semantic-invalid/session-reopen-not-recovered.json", ["session_state_mismatch"]),
        ("semantic-invalid/session-reopen-with-active-run.json", ["session_state_mismatch"]),
        ("semantic-invalid/session-reopen-unrecovered-lists-queued-runs.json", ["session_state_mismatch"]),
        ("semantic-invalid/session-reopen-recovered-lists-a-running-run.json", ["session_state_mismatch"]),
    ]
    for name, expected in names:
        for language, command in COMMANDS.items():
            result = subprocess.run(command + [str(ROOT / "fixtures" / name)], cwd=ROOT,
                                    capture_output=True, text=True, timeout=300)
            label = Path(name).stem + "." + language
            (output / f"{label}.json").write_text(result.stdout)
            (output / f"{label}.stderr").write_text(result.stderr)
            data = json.loads(result.stdout)
            reports = data if isinstance(data, list) else data.get("reports", [data])
            diagnostics = [d for r in reports for d in r.get("diagnostics") or []]
            codes = sorted({d["code"] for d in diagnostics})
            print(f"{label}: exit {result.returncode}, codes {codes}", flush=True)
            assert result.returncode == int(bool(expected)), result.stderr
            assert codes == expected, diagnostics
            assert all(d["phase"] == "semantic" for d in diagnostics), diagnostics
    print("PASS: idle and queued reopens accepted; unrecovered and running reopens rejected")


if __name__ == "__main__":
    main()
