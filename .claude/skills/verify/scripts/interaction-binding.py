#!/usr/bin/env python3
"""Replay user-input binding through the Go, Rust and oapx consumer surfaces."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
COMMANDS = {
    "go": ["go", "run", "./go/cmd/goap", "validate", "--format", "json"],
    "rust": ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
             "-p", "oap-validation", "--example", "validate", "--"],
    "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    fixture = ROOT / "fixtures/semantic-invalid/user-input-resolved-moves-its-tool-binding.json"
    original = json.loads(fixture.read_text())
    for variant in ["wrong-call", "same-call", "omitted-call", "unbound"]:
        trace = copy.deepcopy(original)
        requested = next(e for e in trace if e["type"] == "user.input.requested")
        resolved = next(e for e in trace if e["type"] == "user.input.resolved")
        if variant == "same-call":
            resolved["tool_call_id"] = "t1"
        elif variant == "omitted-call":
            del resolved["tool_call_id"]
        elif variant == "unbound":
            del requested["tool_call_id"]
            del requested["payload"]["tool_call_id"]
            del resolved["tool_call_id"]
        replay = output / f"{variant}.trace.json"
        replay.write_text(json.dumps(trace, indent=2) + "\n")
        expected = ["scope_mismatch"] if variant == "wrong-call" else []
        for language, command in COMMANDS.items():
            result = subprocess.run(command + [str(replay)], cwd=ROOT,
                                    capture_output=True, text=True, timeout=300)
            label = f"{variant}.{language}"
            (output / f"{label}.json").write_text(result.stdout)
            (output / f"{label}.stderr").write_text(result.stderr)
            data = json.loads(result.stdout)
            reports = data if isinstance(data, list) else data.get("reports", [data])
            diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
            codes = sorted({d["code"] for d in diagnostics})
            print(f"{label}: exit {result.returncode}, codes {codes}", flush=True)
            assert result.returncode == int(bool(expected)), result.stderr
            assert codes == expected, diagnostics
            assert all(d["phase"] == "semantic" for d in diagnostics), diagnostics
    print("PASS: all three reject a moved binding and accept matching, omitted and unbound resolutions")


if __name__ == "__main__":
    main()
