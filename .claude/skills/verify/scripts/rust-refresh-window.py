#!/usr/bin/env python3
"""Replay the Rust validator's public API on real and mutated refresh traces."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    command = ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
               "-p", "oap-validation", "--example", "validate", "--"]
    for name in ["permission", "user-input"]:
        path = ROOT / "fixtures/valid" / f"{name}-resolved-in-the-refresh-window.json"
        for mutation in [False, True]:
            trace = json.loads(path.read_text())
            response = next(e for e in trace if e["type"].endswith(".resolve.response"))
            if mutation:
                response["capability_revision"] = "v2"
            with tempfile.TemporaryDirectory(prefix="oap-rust-refresh-") as temporary:
                replay = Path(temporary) / path.name
                replay.write_text(json.dumps(trace))
                result = subprocess.run(command + [str(replay)], cwd=ROOT,
                                        capture_output=True, text=True, timeout=180)
            label = name + ("-wrong-echo" if mutation else "-valid")
            (output / f"{label}.json").write_text(result.stdout)
            (output / f"{label}.stderr").write_text(result.stderr)
            print(f"{label}: exit {result.returncode}\n{result.stdout}", flush=True)
            assert result.returncode == int(mutation), result.stderr
            diagnostics = json.loads(result.stdout)["diagnostics"]
            if mutation:
                assert all(d["phase"] == "semantic" for d in diagnostics), diagnostics
                assert any(d["code"] == "stale_capability_revision"
                           and d["envelope_id"] == response["id"]
                           and d.get("expected") == "v1" and d.get("actual") == "v2"
                           for d in diagnostics), diagnostics
            else:
                assert diagnostics == [], diagnostics


if __name__ == "__main__":
    main()
