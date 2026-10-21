#!/usr/bin/env python3
"""Compare snapshot listings through Rust's public validator, Go and oapx."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
MISMATCH = "session_state_mismatch"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)

    def command(args, label):
        result = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=300)
        (output / f"{label}.stdout").write_text(result.stdout)
        (output / f"{label}.stderr").write_text(result.stderr)
        return result

    built = command(["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p",
                     "oap-validation", "--example", "validate"], "rust-build")
    assert built.returncode == 0, built.stderr
    metadata = command(["cargo", "metadata", "--manifest-path", "rust/Cargo.toml",
                        "--no-deps", "--format-version", "1"], "cargo-metadata")
    assert metadata.returncode == 0, metadata.stderr
    rust = Path(json.loads(metadata.stdout)["target_directory"]) / "debug/examples/validate"
    go = output / "goap"
    built = command(["go", "build", "-o", str(go), "./go/cmd/goap"], "go-build")
    assert built.returncode == 0, built.stderr
    fixtures = ROOT / "fixtures"
    manifest = json.loads((fixtures / "manifest.json").read_text())["fixtures"]
    gaps = set(json.loads((fixtures / "known-gaps.json").read_text())["rust"])
    cases = []
    for entry in manifest:
        if not (entry["id"].startswith(("queue-state-", "queue-open-"))
                or entry["id"] in ["compound-open-queued-without-position",
                                   "state-omits-active-runs-with-pending-permission"]):
            continue
        if entry["id"] in gaps:
            print(f"Known unported claim: {entry['id']}", flush=True)
            continue
        pointers = {
            "queue-state-settles-one-run-twice": "/payload/as_of/settled/1/run_id",
            "queue-state-lists-a-run-it-says-it-settled": "/payload/active_runs/1/run_id",
        }
        cases.append((entry["id"], fixtures / entry["path"], set(entry.get("codes", [])),
                      entry.get("phase", "semantic"), pointers.get(entry["id"])))
    shared = len(cases)

    def fixture(name):
        return json.loads((fixtures / f"valid/{name}.json").read_text())

    def state(trace):
        return next(e for e in trace if e["type"] == "session.state.response")["payload"]

    def add(label, trace, expected, pointer=None):
        path = output / f"{label}.trace.json"
        path.write_text(json.dumps(trace, ensure_ascii=False) + "\n")
        cases.append((label, path, set(expected), "semantic", pointer))

    def mutation(label, name, change, pointer):
        original = fixture(name)
        trace = copy.deepcopy(original)
        change(state(trace))
        add(label, trace, [MISMATCH], pointer)
        add(f"{label}-repaired", original, [])

    base = "queue-state-active-runs"
    mutation("duplicate-run", base,
             lambda p: p["active_runs"][1].update(run_id="r1"),
             "/payload/active_runs/1/run_id")
    mutation("unknown-run", base,
             lambda p: p["active_runs"][1].update(run_id="unadmitted"),
             "/payload/active_runs/1/run_id")
    mutation("terminal-entry", base,
             lambda p: p["active_runs"][1].update(status="completed"),
             "/payload/active_runs/1/status")
    mutation("executing-run-has-a-place", base,
             lambda p: p["active_runs"][0].update(queue_position=1),
             "/payload/active_runs/0/queue_position")
    mutation("queued-at-observed-start", "queue-state-reservation-captured-before-promotion",
             lambda p: p["active_runs"][0].update(as_of_sequence=1),
             "/payload/active_runs/0/status")
    mutation("at-terminal-position", "queue-state-lists-run-settled-in-window",
             lambda p: p["active_runs"][0].update(as_of_sequence=2),
             "/payload/active_runs/0/as_of_sequence")
    original_window = fixture("queue-state-lists-run-settled-in-window")
    stale = copy.deepcopy(original_window)
    read = next(e for e in stale if e["type"] == "session.state.request")
    stale.remove(read)
    terminal = next(i for i, e in enumerate(stale) if e["type"] == "run.completed")
    stale.insert(terminal + 1, read)
    add("settled-before-the-read", stale, [MISMATCH], "/payload/active_runs/0/run_id")
    add("settled-inside-the-read-window", original_window, [])
    mutation("wrong-place", base,
             lambda p: p["active_runs"][1].update(queue_position=2),
             "/payload/active_runs/1/queue_position")
    mutation("missing-place", base,
             lambda p: p["active_runs"][1].pop("queue_position"),
             "/payload/active_runs/1/queue_position")
    mutation("admission-order", base, lambda p: p["active_runs"].reverse(),
             "/payload/active_runs/1/run_id")
    mutation("started-behind-reservation", base, lambda p: p["active_runs"].reverse(),
             "/payload/active_runs/1/status")

    def two_started(p):
        p["active_runs"][1].update(status="running")
        p["active_runs"][1].pop("queue_position")

    mutation("two-started", base, two_started, "/payload/active_runs/1/status")
    mutation("wrong-active-run", base, lambda p: p.update(active_run_id="r2"),
             "/payload/active_run_id")
    mutation("missing-list", base, lambda p: p.pop("active_runs"),
             "/payload/active_runs")
    mutation("empty-list", base, lambda p: p.update(active_runs=[]),
             "/payload/active_runs")
    mutation("missing-reservation", base, lambda p: p["active_runs"].pop(),
             "/payload/active_runs")
    mutation("false-wait", base, lambda p: p.update(status="waiting_for_input"),
             "/payload/status")
    mutation("executing-session-called-queued", base, lambda p: p.update(status="queued"),
             "/payload/status")
    mutation("queued-session-called-idle", "queue-state-only-reservations",
             lambda p: p.update(status="idle"), "/payload/status")
    mutation("reservation-named-active", "queue-state-only-reservations",
             lambda p: p.update(active_run_id="r2"), "/payload/active_run_id")
    mutation("wait-denied", "queue-state-waiting-entry-and-session",
             lambda p: p.update(status="running"), "/payload/status")
    omitted = json.loads((fixtures / "semantic-invalid/state-omits-active-runs-with-pending-permission.json").read_text())
    add("advertised-queue-requires-interaction-list", omitted, [MISMATCH], "/payload/active_runs")
    withdrawn = copy.deepcopy(omitted)
    for e in withdrawn:
        if e["type"] == "capabilities.response":
            e["payload"]["features"].pop("session.message.delivery.queue", None)
    add("unadvertised-queue-does-not-require-interaction-list", withdrawn, [])

    paths = [str(path) for _, path, _, _, _ in cases]
    commands = {
        "rust": [str(rust)],
        "go": [str(go), "validate", "--format=json"],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format=json"],
    }
    for language, args in commands.items():
        result = command(args + paths, language)
        assert result.returncode == int(any(codes for _, _, codes, _, _ in cases)), (language, result.stderr)
        if language == "rust":
            reports = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
        else:
            data = json.loads(result.stdout)
            reports = data if isinstance(data, list) else data.get("reports", [data])
        assert len(reports) == len(cases), (language, len(reports), len(cases))
        for report, (label, _, expected, phase, pointer) in zip(reports, cases):
            diagnostics = report.get("diagnostics") or []
            codes = {d["code"] for d in diagnostics}
            assert codes == expected, (language, label, codes, expected, diagnostics)
            assert all(d["phase"] == phase for d in diagnostics), (language, label, diagnostics)
            if language == "rust" and pointer:
                assert any(d.get("pointer") == pointer for d in diagnostics), (label, pointer, diagnostics)
            print(f"{language}: {label}: {sorted(codes)}", flush=True)
    print(f"PASS: {shared} shared traces and {len(cases) - shared} mutated/repaired traces agree across Rust, Go and Zig", flush=True)


if __name__ == "__main__":
    main()
