#!/usr/bin/env python3
"""Replay token-free Copilot observations in Zig, then validate their OAP trace."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    result = subprocess.run(["zig", "build", "--build-file", "zig/build.zig", "test-copilot-corpus",
                             "-Doptimize=ReleaseSafe"], cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / "zig-replay.txt").write_text(result.stdout + result.stderr)
    assert result.returncode == 0, result.stdout + result.stderr
    pin = next(v for v in json.loads((ROOT / "harnesses/copilot-cli.json").read_text())["versions"]
               if v["status"] == "current")
    corpus = ROOT / pin["corpus"]
    manifest = json.loads((corpus / "manifest.json").read_text())
    for case in manifest["cases"]:
        events = json.loads((corpus / case["path"] / "expected-oap.json").read_text())
        prefix = json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())[:2]
        prefix[0]["payload"]["messages"][0]["content"] = "hello"
        prefix[1]["payload"]["run_id"] = "run-1"
        trace = output / (case["id"] + ".json")
        trace.write_text(json.dumps(prefix + events, indent=2) + "\n")
        for label, command in [("go", ["go", "run", "./go/cmd/goap", "validate", str(trace)]),
                               ("zig", [str(ROOT / "zig/zig-out/bin/oapx"), "validate", str(trace)])]:
            checked = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=60)
            (output / (case["id"] + "-" + label + ".txt")).write_text(checked.stdout + checked.stderr)
            assert checked.returncode == 0, checked.stdout + checked.stderr
            print(label, case["id"], "exit", checked.returncode, flush=True)
    print("PASS: Zig decoded and replayed the synthetic Copilot corpus; public Go and oapx validators accepted the complete admission trace; no native harness or credentials used")


if __name__ == "__main__":
    main()
