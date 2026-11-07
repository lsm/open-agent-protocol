#!/usr/bin/env python3
"""Check Stage 2 with alternating preserved-baseline and candidate resource runs."""
import argparse
import datetime
import hashlib
import json
from pathlib import Path
import shutil
import statistics
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[4]


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True)
    parser.add_argument("--candidate", default=str(ROOT / "zig/zig-out/bin/oapx"))
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--rounds", type=int, default=3)
    args = parser.parse_args()
    if args.rounds < 2:
        parser.error("--rounds must be at least two")
    state = subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True)
    assert not state, "commit the candidate source and verification scripts before measuring"
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    snapshots = output / "binaries"
    snapshots.mkdir()
    binaries = {}
    for label, source in (("baseline", args.baseline), ("candidate", args.candidate)):
        binary = snapshots / (label + "-oapx")
        shutil.copy2(Path(source).resolve(), binary)
        binaries[label] = binary
    summary = {"started": datetime.datetime.now(datetime.timezone.utc).isoformat(),
               "candidate_revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
               "binary_sha256": {label: digest(path) for label, path in binaries.items()},
               "runs": [], "passed": False}
    try:
        for round_index in range(args.rounds):
            order = ("baseline", "candidate") if round_index % 2 == 0 else ("candidate", "baseline")
            for label in order:
                name = f"round-{round_index + 1:02d}-{label}"
                command = [sys.executable, str(ROOT / "scripts/bench-resources.py"),
                           "--targets", "zig-loop", "--scenarios", "empty,working", "--n", "1000,1500",
                           "--rss-cap-mib", "12000", "--timeout", "600", "--idle-settle", "10",
                           "--idle-window", "5", "--no-build", "--loop-binary", str(binaries[label]),
                           "--out", str(output / name)]
                with (output / (name + ".log")).open("w") as log:
                    subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, check=True)
                rows = json.loads((output / name / "results.json").read_text())
                observations = []
                for row in rows:
                    seen = {key: row[key] for key in ("target", "scenario", "n", "status")}
                    if row["scenario"] == "working" and row["status"] == "ok":
                        seen.update(runs_s=row["runs_s"], state_s=row["polls"] / row["wall_s"],
                                    bad_results=row["bad_results"], refused=row["refused"], written_back=row["written_back"])
                    if row["scenario"] == "empty" and row["status"] == "ok":
                        seen.update(idle_switches=row["idle_switches"], idle_cpu_s=row["idle_cpu_s"],
                                    idle_settle_s=row["idle_settle_s"], idle_wall_s=row["idle_wall_s"])
                    observations.append(seen)
                summary["runs"].append({"round": round_index + 1, "build": label, "directory": name, "observations": observations})
                expected = {(scenario, count) for scenario in ("empty", "working") for count in (1000, 1500)}
                assert len(observations) == 4 and {(row["scenario"], row["n"]) for row in observations} == expected, observations
                assert all(row["target"] == "zig-loop" for row in observations), observations
                assert all(row["status"] == "ok" for row in observations), observations
                for row in observations:
                    if row["scenario"] == "working":
                        assert row["bad_results"] == 0 and row["refused"] == 0 and row["written_back"] == row["n"], row
                print(f"PASS measured {name}", flush=True)
        medians = {}
        candidate_rows = []
        for label in ("baseline", "candidate"):
            rows = [row for run in summary["runs"] if run["build"] == label for row in run["observations"]]
            medians[label] = {}
            for count in (1000, 1500):
                values = [row["runs_s"] for row in rows if row["scenario"] == "working" and row["n"] == count]
                medians[label][str(count)] = statistics.median(values)
            if label == "candidate":
                candidate_rows = rows
        checks = {
            "working_1500_non_degrading": medians["candidate"]["1500"] >= medians["candidate"]["1000"],
            "state_over_500": all(row["state_s"] > 500 for row in candidate_rows if row["scenario"] == "working"),
            "idle_zero_switches_and_cpu": all(row["idle_switches"] == 0 and row["idle_cpu_s"] == 0 for row in candidate_rows if row["scenario"] == "empty"),
        }
        summary.update(working_median_runs_s=medians, checks=checks, passed=all(checks.values()))
        print(json.dumps({"working_median_runs_s": medians, "checks": checks}, indent=2), flush=True)
        assert summary["passed"], "Stage 2 acceptance condition failed; retain all runs and report the result"
    finally:
        summary["finished"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (output / "acceptance.json").write_text(json.dumps(summary, indent=2))


if __name__ == "__main__":
    main()
