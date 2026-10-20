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
JOURNEYS = [
    ("steer", ["run.steer.applied", "user.input.requested"], ["go", "zig"], "unmatched_steer"),
    ("compact", ["run.compaction.started", "run.compaction.ended"], list(VALIDATORS),
     "illegal_run_transition"),
    ("call", ["action.call.completed", "user.input.requested"], ["go", "zig"],
     "illegal_tool_transition"),
]


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
    for mode, adjacent, rejecting, code in JOURNEYS:
        produced = run(CARGO + ["-p", "oap-adapter", "--example", "memory_trace", mode],
                       output, mode)
        assert produced.returncode == 0, produced.stderr
        trace = json.loads(produced.stdout)
        kinds = [envelope["type"] for envelope in trace]
        assert kinds[-1] == "run.completed", kinds
        first = kinds.index(adjacent[0])
        assert kinds[first + 1] == adjacent[1], kinds
        whole = output / f"{mode}.trace.json"
        whole.write_text(json.dumps(trace))
        cut = output / f"{mode}-unadmitted.trace.json"
        cut.write_text(json.dumps([e for e in trace if not e["id"].startswith(mode)]))
        for name, command in VALIDATORS.items():
            result = run(command + [str(whole)], output, f"{mode}-{name}")
            assert result.returncode == 0, result.stderr
        for name in rejecting:
            result = run(VALIDATORS[name] + [str(cut)], output, f"{mode}-unadmitted-{name}")
            assert result.returncode == 1, result.stderr
            assert code in result.stdout + result.stderr


if __name__ == "__main__":
    main()
