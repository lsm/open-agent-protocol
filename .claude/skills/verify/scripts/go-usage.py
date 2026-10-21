#!/usr/bin/env python3
"""Observe measured zero and absent counts through the public Go Usage type."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = r'''package main

import (
    "encoding/json"
    "os"

    "github.com/lsm/open-agent-protocol/go/protocol"
)

func main() {
    var usage []protocol.Usage
    if err := json.NewDecoder(os.Stdin).Decode(&usage); err != nil {
        panic(err)
    }
    if err := json.NewEncoder(os.Stdout).Encode(usage); err != nil {
        panic(err)
    }
}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    source = output / "consumer.go"
    source.write_text(SOURCE)
    cases = [
        {},
        {"input_tokens": 0},
        {"output_tokens": 0},
        {"total_tokens": 0},
        {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
        {"input_tokens": 7, "output_tokens": 0},
        {"total_tokens": 18446744073709551615},
    ]
    raw = json.dumps(cases) + "\n"
    (output / "input.json").write_text(raw)
    command = ["go", "run", str(source)]
    result = subprocess.run(command, cwd=ROOT, input=raw, text=True,
                            capture_output=True, timeout=120)
    (output / "output.json").write_text(result.stdout)
    (output / "stderr.txt").write_text(result.stderr)
    print(f"public Go consumer: exit {result.returncode}", flush=True)
    print(result.stdout, end="", flush=True)
    assert result.returncode == 0, result.stderr
    actual = json.loads(result.stdout)
    assert actual == cases, f"reported counts changed: {actual!r} != {cases!r}"
    print("PASS: measured zeros remain 0; unknown counts stay absent; uint64 maximum survives")


if __name__ == "__main__":
    main()
