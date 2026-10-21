#!/usr/bin/env python3
"""Observe public Python usage projections and compare real validator boundaries."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[4]
FIELDS = {"input_tokens": "input", "output_tokens": "output", "total_tokens": "total",
          "cache_read_input_tokens": "cache_read", "cache_write_input_tokens": "cache_write",
          "reasoning_tokens": "reasoning"}
MAX = 2**64 - 1


def run(command, output, label, expected=0):
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
    assert child.returncode == expected, (command, child.returncode, stdout, stderr)
    return stdout + stderr if label.endswith(".zig") else stdout


def consumer(python, raw, output, label, site="completed"):
    (output / f"{label}.input").write_text(raw)
    values = json.loads(run([python, str(ROOT / "sdk/python/examples/usage.py"), python, "-u",
                             str(ROOT / "sdk/python/tests/fixtures/usage.py"), raw, site], output, label))
    assert [(value["agent"], value["stream"]) for value in values] == [
        (False, False), (False, True), (True, False), (True, True)], values
    return values


def validators(output):
    run(["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"], output, "go-build")
    run(["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation",
         "--example", "validate"], output, "rust-build")
    commands = {"go": [str(output / "goap"), "validate", "--format", "json"],
                "rust": [str(ROOT / "rust/target/debug/examples/validate")],
                "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate"]}
    template = json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())
    cases = [("absent", True), ("{}", True)]
    cases += [(json.dumps({field: count}), True) for field in FIELDS for count in (0, MAX)]
    cases += [(json.dumps({field: count}), False) for field in FIELDS
              for count in (None, -1, 1.5, True, "3")]
    cases += [("null", False), ("[]", False)]
    disagreements = []
    for index, (raw, valid) in enumerate(cases):
        trace = json.loads(json.dumps(template))
        for frame in trace:
            if frame["type"] == "run.completed":
                frame["payload"].pop("usage", None)
                if raw != "absent":
                    frame["payload"]["usage"] = json.loads(raw)
        path = output / f"boundary-{index}.json"
        path.write_text(json.dumps(trace))
        outcomes = {}
        for name, command in commands.items():
            stdout = run(command + [str(path)], output, f"boundary-{index}.{name}", int(not valid))
            if name == "zig":
                codes = set(re.findall(r"\b(?:decode|schema|semantic) ([a-z_]+) at \d+", stdout))
            else:
                reports = json.loads(stdout)
                if not isinstance(reports, list):
                    reports = [reports]
                codes = {diagnostic["code"] for report in reports
                         for diagnostic in report.get("diagnostics") or []}
            outcomes[name] = sorted(codes)
            assert codes == (set() if valid else {"schema_invalid"}), (name, raw, codes)
        if len({tuple(codes) for codes in outcomes.values()}) != 1:
            disagreements.append({"usage": raw, "outcomes": outcomes})
    (output / "validators.json").write_text(json.dumps({"cases": len(cases), "disagreements": disagreements}))
    assert not disagreements, disagreements
    print(f"Go/Rust/Zig: {len(cases)} agreed usage boundaries, 0 validation disagreements; full Decision 0065 matrix runs in usage-count-validation.py", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--python-sdk", default=shutil.which("python3.11") or sys.executable)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    cases = [("absent", None), ("{}", {})]
    cases += [(json.dumps({wire: count}), {name: count})
              for wire, name in FIELDS.items() for count in (0, MAX)]
    cases += [(json.dumps({"input_tokens": 0, "total_tokens": 17}), {"input": 0, "total": 17})]
    for index, (raw, expected) in enumerate(cases):
        expected = None if expected is None else {name: expected.get(name) for name in FIELDS.values()}
        values = consumer(args.python_sdk, raw, output, f"valid-{index}")
        assert all(value.get("usage", "missing") == expected for value in values), (raw, values)
    invalid = [json.dumps({field: count}) for field in FIELDS
               for count in (None, -1, 1.5, 1.0, True, "3", {}, [], MAX + 1)]
    invalid += ["null", "true", "[]", "3", '"usage"']
    for index, raw in enumerate(invalid):
        values = consumer(args.python_sdk, raw, output, f"invalid-{index}")
        assert all(value.get("error") == "malformed_response" and
                   value.get("error_type") == "MakaiProtocolError" and "usage" not in value
                   for value in values), (raw, values)
    print(f"Public Python consumer: {len(cases)} valid + {len(invalid)} invalid cases × 4 APIs passed", flush=True)
    validators(output)
    print(f"PASS: optional uint64 usage retained; malformed responses refused. Artifacts: {output}")


if __name__ == "__main__":
    main()
