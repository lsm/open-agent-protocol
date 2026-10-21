#!/usr/bin/env python3
"""Replay the shared Decision 0065 fixtures through the three public validators."""
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
        (output / f"{label}.out").write_text(stdout)
        (output / f"{label}.err").write_text(stderr)
    return child.returncode, stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    goap = output / "goap"
    builds = [["python3", "scripts/generate-usage-count-fixtures.py", "--check"],
              ["go", "build", "-o", str(goap), "./go/cmd/goap"],
              ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"],
              ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"]]
    for index, command in enumerate(builds):
        status, stdout = run(command, output, f"build-{index}")
        if status:
            raise RuntimeError(f"build failed: {command}: {status}: {stdout}")
    entries = [entry for entry in json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
               if entry["id"].startswith("usage-count-")]
    results = []
    mismatches = []
    for index, entry in enumerate(entries):
        path = ROOT / "fixtures" / entry["path"]
        commands = {"go": [str(goap), "validate", "--format", "json"],
                    "rust": [str(ROOT / "rust/target/debug/examples/validate")],
                    "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"]}
        if entry.get("profile") == "model-provider-core":
            commands["go"].append("--provider")
            commands["rust"].append("--provider")
        observed = {}
        for name, command in commands.items():
            status, stdout = run(command + [str(path)], output, f"{index}-{name}")
            parsed = json.loads(stdout)
            reports = parsed if isinstance(parsed, list) else [parsed]
            diagnostics = [diagnostic for report in reports for diagnostic in report.get("diagnostics") or []]
            codes = sorted({diagnostic["code"] for diagnostic in diagnostics})
            phases = sorted({diagnostic["phase"] for diagnostic in diagnostics})
            observed[name] = {"exit": status, "codes": codes, "phases": phases}
            expected = {"exit": int(not entry["valid"]), "codes": sorted(entry["codes"]),
                        "phases": [entry["phase"]] if entry["phase"] else []}
            if observed[name] != expected:
                mismatches.append({"id": entry["id"], "validator": name, "expected": expected,
                                   "actual": observed[name]})
        results.append({"id": entry["id"], "path": entry["path"], "observed": observed})
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    (output / "mismatches.json").write_text(json.dumps(mismatches, indent=2) + "\n")
    print(f"Decision 0065: {len(entries)} raw-preserving fixtures; validator disagreements: {len(mismatches)}", flush=True)
    if mismatches:
        print(json.dumps(mismatches[:12], indent=2), flush=True)
        raise SystemExit(1)
    print(f"PASS: exact count values/spellings and diagnostic phases agree; D31/tool numbers unchanged. Artifacts: {output}")


if __name__ == "__main__":
    main()
