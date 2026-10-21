import argparse
import copy
import json
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    builds = [
        ("go", ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"]),
        ("rust", ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"]),
        ("zig", ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"]),
    ]
    for name, command in builds:
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
        (output / f"{name}-build.log").write_text(result.stdout + result.stderr)
        assert result.returncode == 0, (name, result.stderr)
    commands = {
        "go": [str(output / "goap"), "validate", "--format=json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format=json"],
    }
    base = json.loads((ROOT / "fixtures/valid/control-tool-roundtrip.json").read_text())
    cases = [("declared-agent", base, [])]
    for requester in ("control", "stranger"):
        trace = copy.deepcopy(base)
        for event in trace:
            if "requested_by" in event["payload"]:
                event["payload"]["requested_by"] = requester
        cases.append((f"requester-{requester}", trace, ["scope_mismatch"]))
    renamed = copy.deepcopy(base)
    for event in renamed:
        if "endpoint" in event["payload"]:
            event["payload"]["endpoint"]["id"] = "renamed-agent"
    cases.append(("endpoint-renamed-with-stale-requester", copy.deepcopy(renamed), ["scope_mismatch"]))
    for event in renamed:
        if "requested_by" in event["payload"]:
            event["payload"]["requested_by"] = "renamed-agent"
    cases.append(("endpoint-and-requester-renamed", renamed, []))
    prefix = copy.deepcopy(base)
    prefix = [event for event in prefix if not event["type"].startswith("protocol.initialize.")]
    cases.append(("unobserved-initialization", prefix, ["unmatched_interaction"]))
    failures = []
    observations = []
    for label, trace, expected in cases:
        path = output / f"{label}.json"
        path.write_text(json.dumps(trace, indent=2) + "\n")
        for name, command in commands.items():
            result = subprocess.run([*command, str(path)], cwd=ROOT, capture_output=True, text=True, timeout=60)
            (output / f"{label}-{name}.stdout").write_text(result.stdout)
            (output / f"{label}-{name}.stderr").write_text(result.stderr)
            reports = json.loads(result.stdout)
            if isinstance(reports, dict):
                reports = [reports]
            diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
            codes = sorted({d["code"] for d in diagnostics})
            observation = {"case": label, "validator": name, "exit": result.returncode, "codes": codes}
            observations.append(observation)
            print(json.dumps(observation), flush=True)
            if result.returncode != int(bool(expected)) or codes != expected:
                failures.append(observation)
            if expected == ["scope_mismatch"] and name != "zig" and codes == expected:
                assert all(d["pointer"] == "/payload/requested_by" for d in diagnostics), diagnostics
    (output / "observations.json").write_text(json.dumps(observations, indent=2) + "\n")
    assert not failures, failures
    print(f"PASS: {len(cases)} public traces agree across Go, Rust and Zig; 0 disagreements")


if __name__ == "__main__":
    main()
