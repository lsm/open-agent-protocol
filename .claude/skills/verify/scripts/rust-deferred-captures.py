import argparse
import copy
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
NEGATIVE = [
    "queue-state-started-admission-queued-before-start",
    "queue-state-settled-terminal-never-arrives",
    "queue-state-omits-pending-interaction",
    "queue-state-unpositioned-entry-omits-pending-without-queue",
    "queue-state-pending-without-as-of",
    "queue-state-as-of-never-reached",
    "queue-state-queued-at-forthcoming-start",
    "queue-state-cancelling-reservation-past-its-promotion",
    "queue-state-cancelling-run-left-the-queue-early",
    "queue-state-cancelling-run-drops-a-place-it-never-left",
    "queue-state-executes-a-run-that-never-starts",
    "queue-state-false-settled-claim",
    "queue-state-settled-wrong-sequence",
    "queue-state-settled-claim-then-activity",
    "queue-open-keeps-a-resolved-interaction-pending",
    "queue-open-adds-an-interaction-the-entry-denied",
    "state-omits-active-runs-with-pending-permission",
    "control-call-recovered-state-keeps-resolved",
    "steer-state-position-never-reached",
]
POSITIVE = [
    "queue-state-interaction-resolved-in-flight",
    "queue-state-queued-position-reached-without-start",
    "queue-state-queued-before-start",
    "queue-state-cancelling-reservation-before-its-promotion",
    "queue-state-cancelling-run-past-its-promotion",
    "queue-state-executes-a-run-whose-start-follows",
    "queue-state-reflects-early-settlement",
    "queue-state-omits-settled-before-terminal",
    "queue-state-recovered-run-names-an-unseen-interaction",
    "queue-open-recovers-a-blocked-run",
    "queue-open-resolves-a-recovered-interaction",
]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)

    def run(command, label):
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
        (output / f"{label}.stdout").write_text(result.stdout)
        (output / f"{label}.stderr").write_text(result.stderr)
        return result

    for name, command in [
        ("go", ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"]),
        ("rust", ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"]),
        ("zig", ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"]),
    ]:
        result = run(command, f"{name}-build")
        assert result.returncode == 0, (name, result.stderr)
    commands = {
        "go": [str(output / "goap"), "validate", "--format=json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format=json"],
    }
    manifest = {f["id"]: f for f in json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]}
    cases = []
    for name in NEGATIVE + POSITIVE:
        f = manifest[name]
        cases.append((name, json.loads((ROOT / "fixtures" / f["path"]).read_text()), sorted(set(f.get("codes", [])))))

    def state(trace):
        return next(e["payload"] for e in trace if e["type"] == "session.state.response")

    base = next(trace for name, trace, _ in cases if name == "queue-state-interaction-resolved-in-flight")
    for position, pending in [(1, []), (1, ["i1"]), (2, []), (2, ["i1"]), (3, []), (3, ["i1"])]:
        trace = copy.deepcopy(base)
        payload = state(trace)
        payload["active_runs"][0].update(as_of_sequence=position, pending_interactions=pending)
        payload["status"] = "waiting_for_input" if pending else "running"
        expected = [] if pending == (["i1"] if position == 2 else []) else ["session_state_mismatch"]
        cases.append((f"pending-position-{position}-listed-{len(pending)}", trace, expected))
        resolution = next(e for e in trace if e["type"] == "action.permission.resolved")
        trace = copy.deepcopy(trace)
        resolution = next(e for e in trace if e["id"] == resolution["id"])
        trace.remove(resolution)
        trace.insert(next(i for i, e in enumerate(trace) if e["type"] == "session.state.response"), resolution)
        cases.append((f"historical-position-{position}-listed-{len(pending)}", trace, expected))
    started = copy.deepcopy(next(trace for name, trace, _ in cases if name == "queue-state-started-admission-queued-before-start"))
    state(started)["active_runs"][0]["status"] = "running"
    cases.append(("started-admission-running-repaired", started, []))
    missing = copy.deepcopy(next(trace for name, trace, _ in cases if name == "queue-state-settled-terminal-never-arrives"))
    missing.append(json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())[-1])
    cases.append(("settled-terminal-arrives-repaired", missing, []))
    no_queue = copy.deepcopy(next(trace for name, trace, _ in cases if name == "state-omits-active-runs-with-pending-permission"))
    next(e for e in no_queue if e["type"] == "capabilities.response")["payload"]["features"].pop("session.message.delivery.queue", None)
    cases.append(("decision-0057-without-queue", no_queue, []))

    observations = []
    failures = []
    for label, trace, expected in cases:
        path = output / f"{label}.json"
        path.write_text(json.dumps(trace) + "\n")
        for language, command in commands.items():
            result = run([*command, str(path)], f"{label}-{language}")
            reports = json.loads(result.stdout)
            if isinstance(reports, dict):
                reports = [reports]
            diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
            codes = sorted({d["code"] for d in diagnostics})
            record = {"case": label, "language": language, "exit": result.returncode, "codes": codes}
            observations.append(record)
            okay = codes == expected and result.returncode == int(bool(expected)) and all(d["phase"] == "semantic" for d in diagnostics)
            if language == "rust":
                snapshots = {e["id"] for e in trace if e["type"] in ("session.open.response", "session.state.response", "session.state.updated")}
                assert all(d.get("envelope_id") in snapshots and d.get("pointer", "").startswith("/payload/") for d in diagnostics if d["code"] == "session_state_mismatch"), diagnostics
            if not okay:
                failures.append(record)
            print(("PASS" if okay else "FAIL") + ": " + json.dumps(record), flush=True)
    (output / "observations.json").write_text(json.dumps(observations, indent=2) + "\n")
    assert not failures, failures
    print(f"PASS: {len(cases)} deferred capture/repair traces agree across Go, Rust and Zig; 0 disagreements", flush=True)
    if args.mutation_check:
        module = ROOT / "rust/validation/src/state/snapshot_claims.rs"
        mutations = [
            (module, "expected == listed", "true", "queue-state-omits-pending-interaction"),
            (ROOT / "rust/validation/src/state.rs", "        self.close_snapshot_claims();", "", "queue-state-as-of-never-reached"),
            (module, "capture >= start", "false", "queue-state-queued-at-forthcoming-start"),
            (module, "*held != (capture < start)", "false", "queue-state-cancelling-reservation-past-its-promotion"),
            (module, "!terminal || e.sequence != claim.sequence", "false", "queue-state-settled-claim-then-activity"),
            (ROOT / "rust/validation/src/state/interactions.rs", "opened_at: e.sequence.unwrap_or_default()", "opened_at: 0", "historical-position-1-listed-1"),
            (ROOT / "rust/validation/src/state/interactions.rs", "x.resolved_at = x.resolved_at.or(e.sequence);", "x.resolved_at = None;", "historical-position-2-listed-1"),
        ]
        for index, (source, old, new, target) in enumerate(mutations):
            original = source.read_text()
            assert original.count(old) == 1, (source, old)
            try:
                source.write_text(original.replace(old, new))
                build = run(["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"], f"mutation-{index}-build")
                assert build.returncode == 0, build.stderr
                result = run([*commands["rust"], str(output / f"{target}.json")], f"mutation-{index}-result")
                report = json.loads(result.stdout)
                codes = sorted({d["code"] for d in report.get("diagnostics") or []})
                baseline = next(r for r in observations if r["case"] == target and r["language"] == "rust")
                assert (result.returncode, codes) != (baseline["exit"], baseline["codes"]), (target, report)
                assert result.returncode in (0, 1) and all(d["phase"] == "semantic" for d in report.get("diagnostics") or []), report
                print(f"PASS: mutation {index} caught by {target}", flush=True)
            finally:
                source.write_text(original)
        build = run(["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"], "restored-build")
        assert build.returncode == 0, build.stderr
        for label, _, expected in cases:
            result = run([*commands["rust"], str(output / f"{label}.json")], f"restored-{label}")
            report = json.loads(result.stdout)
            codes = sorted({d["code"] for d in report.get("diagnostics") or []})
            assert result.returncode == int(bool(expected)) and codes == expected, (label, report)
        print(f"PASS: {len(mutations)} source mutations caught; sources restored and all Rust cases replayed", flush=True)


if __name__ == "__main__":
    main()
