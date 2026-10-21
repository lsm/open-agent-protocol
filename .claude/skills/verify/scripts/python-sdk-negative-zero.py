#!/usr/bin/env python3
"""Observe Python SDK raw negative-zero refusal on all four public APIs."""
import argparse
import json
from pathlib import Path
import runpy
import shutil
import sys

ROOT = Path(__file__).resolve().parents[4]
helpers = runpy.run_path(str(Path(__file__).with_name("python-sdk-usage.py")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--python-sdk", default=shutil.which("python3.11") or sys.executable)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    cases = ['{"' + field + '":-0}' for field in helpers["FIELDS"]]
    cases.append('{"input_tokens":7,"output_tokens":-0}')
    results = []
    for index, raw in enumerate(cases):
        observed = helpers["consumer"](args.python_sdk, raw, output, f"negative-zero-{index}")
        results.append({"input": raw, "observed": observed})
        assert all(value.get("error") == "malformed_response" and
                   value.get("error_type") == "MakaiProtocolError" and "usage" not in value
                   for value in observed), (raw, observed)
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"PASS: {len(cases)} raw negative-zero cases × 4 public APIs refused with typed malformed_response. Artifacts: {output}")


if __name__ == "__main__":
    main()
