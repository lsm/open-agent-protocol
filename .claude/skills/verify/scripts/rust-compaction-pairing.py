#!/usr/bin/env python3
"""Replay synthetic per-run compaction pairs through the three public CLIs."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/validation/src/state/compaction.rs"
OLD = {
    "compaction-ended-without-start", "compaction-ended-names-a-compaction-that-never-started",
    "compaction-unpaired", "compaction-started-again-before-the-first-ended",
    "compaction-open-at-terminal", "compaction-open-when-the-run-fails",
    "compaction-failed-without-error", "compaction-failed-without-error-in-a-requested-run",
    "compaction-requested-run", "compaction-threshold-inside-run",
}

def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / f"{label}.stdout").write_text(result.stdout)
    (output / f"{label}.stderr").write_text(result.stderr)
    return result


def build(language, output, label):
    command = {
        "go": ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"],
        "rust": ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"],
        "zig": ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"],
    }[language]
    if run(command, output, label).returncode != 0:
        raise RuntimeError(f"{label}: build failed; compiler failures are not mutation catches")


def repair(trace):
    requests = {e["id"] for e in trace if e["type"] == "session.compact.request"}
    compaction_runs = {e["payload"].get("run_id") for e in trace if e["type"] == "session.compact.response" and e.get("in_reply_to") in requests}
    result, opened, ended = [], {}, set()
    for e in copy.deepcopy(trace):
        t, rid = e["type"], e.get("run_id")
        if t == "run.compaction.started":
            if rid in opened:
                continue
            opened[rid] = e["payload"]["compaction_id"]
        elif t == "run.compaction.ended":
            cid = e["payload"]["compaction_id"]
            if rid in opened and opened[rid] != cid:
                continue
            if rid not in opened:
                if (rid, cid) in ended:
                    continue
                start = copy.deepcopy(e)
                start.update(type="run.compaction.started", id="repair-start-" + e["id"])
                start["payload"] = {"session_id": e["payload"]["session_id"], "run_id": rid, "compaction_id": cid, "reason": "requested" if rid in compaction_runs else "threshold"}
                result.append(start)
            if e["payload"]["outcome"] == "failed":
                e["payload"]["error"] = {"code": "run_failed", "message": "synthetic failure"}
            opened.pop(rid, None)
            ended.add((rid, cid))
        elif t in ("run.completed", "run.failed", "run.cancelled") and rid in opened:
            end = copy.deepcopy(e)
            end.update(type="run.compaction.ended", id="repair-end-" + e["id"])
            end["payload"] = {"session_id": e["payload"]["session_id"], "run_id": rid, "compaction_id": opened.pop(rid), "outcome": "completed"}
            result.append(end)
        result.append(e)
    sequences = {}
    for e in result:
        if "sequence" in e:
            rid = e["run_id"]
            sequences[rid] = sequences.get(rid, 0) + 1
            e["sequence"] = sequences[rid]
    return result


def cases():
    result = {}
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    for fixture in manifest:
        name = fixture["id"]
        if name not in OLD and not name.startswith("compaction-pair-"):
            continue
        trace = json.loads((ROOT / "fixtures" / fixture["path"]).read_text())
        result[name] = (trace, fixture["codes"])
        if not fixture["valid"]:
            result["repair-" + name] = (repair(trace), [])
    for kind, index in [("started", 5), ("ended", 6)]:
        trace = copy.deepcopy(result["compaction-threshold-inside-run"][0])
        late = copy.deepcopy(trace[index])
        late.update(id="late-compaction-" + kind, sequence=5)
        trace.append(late)
        result["after-terminal-" + kind] = (trace, ["event_after_terminal"])
    return result


def replay(language, output, label, case):
    trace, codes = case
    path = output / f"{label}.input.json"
    path.write_text(json.dumps(trace, indent=2) + "\n")
    command = {
        "go": [str(output / "goap"), "validate", "--format", "json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }[language]
    result = run(command + [str(path)], output, label)
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else data.get("reports", [data])
    diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
    got = {"exit": result.returncode, "codes": sorted({d["code"] for d in diagnostics}),
           "phases": sorted({d["phase"] for d in diagnostics})}
    want = {"exit": int(bool(codes)), "codes": sorted(codes), "phases": ["semantic"] if codes else []}
    if any(e["id"] == "wrong-end" for e in trace):
        index = next(i for i, e in enumerate(trace) if e["id"] == "wrong-end")
        got["locations"] = sorted([d["code"], d["index"]] for d in diagnostics)
        want["locations"] = sorted([code, index] for code in codes)
    return got, want


def mutation_check(output, corpus):
    runs = ROOT / "rust/validation/src/state/runs.rs"
    start_guard = 'let p: RunCompactionStartedPayload = payload(e);\n                if !run.open_compaction.is_empty() {'
    terminal_guard = 'if !run.open_compaction.is_empty() =>'
    changes = [
        ("double-start", SOURCE, start_guard, start_guard.replace("!run.open_compaction.is_empty()", "false"), "compaction-unpaired", [], None),
        ("preserve-first-start", SOURCE, start_guard, start_guard + '\n                    run.open_compaction = p.compaction_id.as_str().to_owned();', "compaction-pair-second-start-preserves-first", ["compaction_unpaired", "compaction_ended_without_start", "compaction_open_at_terminal"], None),
        ("matching-end", SOURCE, "if run.open_compaction != p.compaction_id.as_str() {", "if false {", "compaction-ended-without-start", [], None),
        ("wrong-end-preserves-first", SOURCE, "if run.open_compaction != p.compaction_id.as_str() {", "if run.open_compaction.is_empty() {", "compaction-pair-wrong-failed-end-preserves-first", ["compaction_ended_without_start", "compaction_failed_without_error"], [["compaction_ended_without_start", 7], ["compaction_failed_without_error", 6]]),
        ("clear-ended-pair", SOURCE, "run.open_compaction.clear();", "let _ = &run.open_compaction;", "compaction-threshold-inside-run", ["compaction_open_at_terminal"], None),
        ("failed-needs-error", SOURCE, "p.outcome == CompactionOutcome::FAILED && p.error.is_none()", "p.outcome == CompactionOutcome::FAILED && p.error.is_none() && false", "compaction-failed-without-error", [], None),
        ("completed-needs-no-error", SOURCE, "p.outcome == CompactionOutcome::FAILED && p.error.is_none()", "p.error.is_none()", "compaction-threshold-inside-run", ["compaction_failed_without_error"], None),
        ("completed-terminal", SOURCE, terminal_guard, terminal_guard.replace("!run.open_compaction.is_empty()", "false"), "compaction-open-at-terminal", [], None),
        ("failed-terminal", SOURCE, terminal_guard, terminal_guard.replace("!run.open_compaction.is_empty()", "false"), "compaction-open-when-the-run-fails", [], None),
        ("cancelled-terminal", SOURCE, '\"run.completed\" | \"run.failed\" | \"run.cancelled\"', '\"run.completed\" | \"run.failed\"', "compaction-pair-open-at-cancelled", [], None),
        ("per-run-state", SOURCE, ".get_mut(run_id)", '.get_mut("r1")', "compaction-pair-interleaved-runs", ["compaction_unpaired", "compaction_ended_without_start"], None),
        ("error-does-not-skip-pairing", SOURCE, 'let p: RunCompactionEndedPayload = payload(e);', 'let p: RunCompactionEndedPayload = payload(e);\n                if p.outcome == CompactionOutcome::FAILED && p.error.is_none() { self.add(codes::COMPACTION_FAILED_WITHOUT_ERROR, at, e, \"/payload/error\", \"missing error\"); return; }', "compaction-pair-wrong-failed-end-preserves-first", ["compaction_failed_without_error"], [["compaction_failed_without_error", 6]]),
    ]
    originals = {SOURCE: SOURCE.read_bytes(), runs: runs.read_bytes()}
    results = []
    try:
        for label, path, before, after, target, changed_codes, locations in changes:
            text = originals[path].decode()
            if text.count(before) != 1:
                raise RuntimeError(f"{label}: mutation target is not unique")
            path.write_text(text.replace(before, after, 1))
            build("rust", output, label + "-build")
            got, want = replay("rust", output, label, corpus[target])
            changed = {"exit": int(bool(changed_codes)), "codes": sorted(changed_codes), "phases": ["semantic"] if changed_codes else []}
            if locations is not None:
                changed["locations"] = sorted(locations)
            if got == want or got != changed:
                raise RuntimeError(f"{label}: mutation survived or failed inconclusively: {got}; expected change {changed}")
            results.append({"mutation": label, "case": target, "got": got, "original": want})
            print(f"KILLED {label}: {got}", flush=True)
            path.write_bytes(originals[path])
    finally:
        for path, data in originals.items():
            path.write_bytes(data)
        build("rust", output, "restored-build")
        (output / "mutations.json").write_text(json.dumps(results, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    corpus = cases()
    for language in ("go", "rust", "zig"):
        build(language, output, language + "-build")
    if args.mutation_check:
        mutation_check(output, corpus)
    results, wrong = [], []
    for name, case in corpus.items():
        for language in ("go", "rust", "zig"):
            got, want = replay(language, output, language + "." + name, case)
            results.append({"case": name, "language": language, "got": got, "want": want})
            if got != want:
                wrong.append(f"{language}.{name}: got {got}, want {want}")
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    if wrong:
        raise RuntimeError("\n".join(wrong))
    print(f"PASS: {len(corpus)} cases × 3 public CLIs, 0 disagreements; artifacts: {output}")


if __name__ == "__main__":
    main()
