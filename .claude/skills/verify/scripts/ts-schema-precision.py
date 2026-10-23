#!/usr/bin/env python3
"""Compile standalone public oap-client consumers against packaged declarations."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    environment = dict(os.environ, OAP_TS_SCHEMA_PRECISION_OUTPUT=str(output / "consumer"))
    commands = [
        ["npm", "--prefix", "clients/ts", "run", "build"],
        ["node", "--test", "clients/ts/dist/test/schema-precision.test.js"],
    ]
    for index, command in enumerate(commands):
        child = subprocess.Popen(command, cwd=ROOT, env=environment, text=True,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        try:
            stdout, stderr = child.communicate(timeout=300)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            stdout, stderr = child.communicate()
            raise
        finally:
            (output / f"step-{index}.stdout").write_text(stdout)
            (output / f"step-{index}.stderr").write_text(stderr)
        print(stdout, end="", flush=True)
        if child.returncode != 0:
            raise SystemExit(f"step {index} exited {child.returncode}: {stderr}")
    results = json.loads((output / "consumer/results.json").read_text())
    if len(results) != 4 or any((row["exit"] == 0) != row["expected"] for row in results):
        raise SystemExit(f"unexpected consumer results: {results}")
    print(f"PASS: packaged public declarations accept valid controls and refuse both empty arrays; artifacts: {output}")


if __name__ == "__main__":
    main()
