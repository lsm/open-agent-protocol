#!/usr/bin/env python3
"""Stress the stdio overflow contract under race instrumentation and CPU load."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--count", type=int, default=200)
    parser.add_argument("--workers", type=int, nargs="+", default=[0, 2])
    parser.add_argument("--gomaxprocs", type=int, nargs="+", default=[1, 2, 4, 8])
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    binary = output / "stdio-overflow.test"
    with (output / "build.log").open("wb") as log:
        subprocess.run(["go", "test", "-race", "-c", "-o", str(binary), "./go/serve/servestdio"],
                       cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=120)
    outcomes = []
    with tempfile.TemporaryDirectory(prefix="oap-overflow-stress-") as home:
        environment = {"HOME": home, "PATH": os.defpath}
        for workers in args.workers:
            for procs in args.gomaxprocs:
                label = f"procs-{procs}-workers-{workers}"
                stress = []
                try:
                    for _ in range(workers):
                        stress.append(subprocess.Popen([sys.executable, "-c", "while True: pass"],
                                      env=environment, start_new_session=True))
                    command = [str(binary), "-test.run=^TestEventsSignalAnOverflowWithItsRunAndCursor$",
                               f"-test.count={args.count}", "-test.v", "-test.timeout=120s"]
                    with (output / f"{label}.log").open("wb") as log:
                        child = subprocess.Popen(command, cwd=ROOT, env={**environment, "GOMAXPROCS": str(procs)},
                                                 stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
                        try:
                            code = child.wait(timeout=130)
                        finally:
                            if child.poll() is None:
                                os.killpg(child.pid, signal.SIGKILL)
                                child.wait()
                    outcome = {"case": label, "count": args.count, "exit": code}
                    outcomes.append(outcome)
                    print(json.dumps(outcome), flush=True)
                finally:
                    for worker in stress:
                        worker.terminate()
                    for worker in stress:
                        worker.wait(timeout=10)
    (output / "summary.json").write_text(json.dumps(outcomes, indent=2) + "\n")
    assert all(case["exit"] == 0 for case in outcomes), f"stress failed; artifacts: {output}"
    print(f"PASS: {args.count * len(outcomes)} overflow repetitions; artifacts: {output}")


if __name__ == "__main__":
    main()
