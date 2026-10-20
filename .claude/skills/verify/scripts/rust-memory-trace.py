#!/usr/bin/env python3
"""Drive the Rust memory adapter through its public traits and validate the trace."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
CARGO = ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml"]
VALIDATORS = {
    "rust": CARGO + ["-p", "oap-validation", "--example", "validate", "--"],
    "go": ["go", "run", "./go/cmd/goap", "validate"],
    "zig": ["./zig/zig-out/bin/oapx", "validate"],
}
REJECTING = ["go", "zig"]


def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / f"{label}.out").write_text(result.stdout)
    (output / f"{label}.err").write_text(result.stderr)
    print(f"{label}: exit {result.returncode}\n{result.stdout}{result.stderr}", flush=True)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    produced = run(CARGO + ["-p", "oap-adapter", "--example", "memory_trace"], output, "steer")
    assert produced.returncode == 0, produced.stderr
    trace = json.loads(produced.stdout)
    kinds = [envelope["type"] for envelope in trace]
    assert "run.steer.applied" in kinds and kinds[-1] == "run.completed", kinds
    applied = kinds.index("run.steer.applied")
    assert kinds[applied + 1] == "user.input.requested", kinds
    steered = output / "steer.trace.json"
    steered.write_text(json.dumps(trace))
    unsteered = output / "unsteered.trace.json"
    unsteered.write_text(json.dumps([e for e in trace if not e["id"].startswith("steer")]))
    for name, command in VALIDATORS.items():
        result = run(command + [str(steered)], output, f"steer-{name}")
        assert result.returncode == 0, result.stderr
    for name in REJECTING:
        result = run(VALIDATORS[name] + [str(unsteered)], output, f"unsteered-{name}")
        assert result.returncode == 1, result.stderr
        assert "unmatched_steer" in result.stdout + result.stderr


if __name__ == "__main__":
    main()
