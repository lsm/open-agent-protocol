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
    for name, feature in [("permission", "permissions"), ("user-input", "user_input")]:
        path = ROOT / "fixtures/valid" / f"{name}-resolved-in-the-refresh-window.json"
        for variant in ["valid", "wrong-echo", "after-refresh-withdrawn"]:
            trace = json.loads(path.read_text())
            response = next(e for e in trace if e["type"].endswith(".resolve.response"))
            if variant == "wrong-echo":
                response["capability_revision"] = "v2"
            elif variant == "after-refresh-withdrawn":
                trace.remove(response)
                refreshed = next(i for i, e in enumerate(trace) if e["id"] == "capr2")
                trace[refreshed]["payload"]["features"][feature]["level"] = "unavailable"
                trace.insert(refreshed + 1, response)
            label = name + "-" + variant
            (output / f"{label}.trace.json").write_text(json.dumps(trace))
            with tempfile.TemporaryDirectory(prefix="oap-rust-refresh-") as temporary:
                replay = Path(temporary) / path.name
                replay.write_text(json.dumps(trace))
                result = subprocess.run(command + [str(replay)], cwd=ROOT,
                                        capture_output=True, text=True, timeout=180)
            (output / f"{label}.json").write_text(result.stdout)
            (output / f"{label}.stderr").write_text(result.stderr)
            print(f"{label}: exit {result.returncode}\n{result.stdout}", flush=True)
            assert result.returncode == int(variant != "valid"), result.stderr
            diagnostics = json.loads(result.stdout)["diagnostics"]
            assert all(d["phase"] == "semantic" for d in diagnostics), diagnostics
            if variant == "wrong-echo":
                assert any(d["code"] == "stale_capability_revision"
                           and d["envelope_id"] == response["id"]
                           and d.get("expected") == "v1" and d.get("actual") == "v2"
                           for d in diagnostics), diagnostics
            elif variant == "after-refresh-withdrawn":
                assert any(d["code"] == "unavailable_capability"
                           and d["envelope_id"] == response["id"]
                           for d in diagnostics), diagnostics
                assert not any(d["code"] == "stale_capability_revision"
                               for d in diagnostics), diagnostics
            else:
                assert diagnostics == [], diagnostics


if __name__ == "__main__":
    main()
