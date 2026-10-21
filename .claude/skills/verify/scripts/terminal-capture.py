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
    for name, command in [
        ("go", ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"]),
        ("rust", ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"]),
        ("zig", ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"]),
    ]:
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
        (output / f"{name}-build.log").write_text(result.stdout + result.stderr)
        assert result.returncode == 0, (name, result.stderr)
    commands = {
        "go": [str(output / "goap"), "validate", "--format=json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format=json"],
    }
    base = json.loads((ROOT / "fixtures/valid/queue-state-lists-run-settled-in-window.json").read_text())
    late = json.loads((ROOT / "fixtures/semantic-invalid/queue-state-terminal-capture-after-late-event.json").read_text())
    mismatch = "session_state_mismatch"
    event_after = "event_after_terminal"
    cases = [("before-terminal", base, []), ("late-event-at-terminal", late, [event_after, mismatch])]
    for position in (1, 3):
        trace = copy.deepcopy(late)
        trace[-1]["payload"]["active_runs"][0]["as_of_sequence"] = position
        cases.append((f"late-event-capture-{position}", trace, [event_after] + ([mismatch] if position >= 2 else [])))
    repaired = copy.deepcopy(late)
    repaired.pop(-2)
    cases.append(("remove-late-event", copy.deepcopy(repaired), [mismatch]))
    repaired[-1]["payload"]["active_runs"][0]["as_of_sequence"] = 1
    cases.append(("repair-capture-and-event", repaired, []))
    twice = copy.deepcopy(late)
    extra = copy.deepcopy(twice[-2])
    extra.update(id="another-late-event", sequence=4)
    twice.insert(-1, extra)
    cases.append(("two-late-events", twice, [event_after, mismatch]))
    duplicate = copy.deepcopy(late)
    extra = copy.deepcopy(duplicate[-3])
    extra.update(id="duplicate-terminal", sequence=3)
    duplicate[-2] = extra
    cases.append(("duplicate-terminal-keeps-first-boundary", duplicate, ["duplicate_run_terminal", mismatch]))
    after_capture = copy.deepcopy(late)
    extra = after_capture.pop(-2)
    after_capture.append(extra)
    cases.append(("late-event-after-snapshot", after_capture, [event_after, mismatch]))
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
            if result.returncode != int(bool(expected)) or codes != sorted(expected):
                failures.append(observation)
            if mismatch in expected and name != "zig" and mismatch in codes:
                assert all(d["pointer"] == "/payload/active_runs/0/as_of_sequence" for d in diagnostics if d["code"] == mismatch), diagnostics
            assert all(d["phase"] == "semantic" for d in diagnostics), diagnostics
    (output / "observations.json").write_text(json.dumps(observations, indent=2) + "\n")
    assert not failures, failures
    print(f"PASS: {len(cases)} public capture mutations/repairs agree across Go, Rust and Zig; 0 disagreements")


if __name__ == "__main__":
    main()
