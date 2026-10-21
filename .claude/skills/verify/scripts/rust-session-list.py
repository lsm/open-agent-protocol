#!/usr/bin/env python3
"""Observe session-list page judgments through the three public validator CLIs."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/validation/src/state/session_list.rs"
IDS = {
    "session-list-over-limit", "session-list-over-the-default-limit",
    "session-list-out-of-order", "session-list-tie-out-of-order",
    "session-list-duplicate-entry", "session-list-lists-one-session-twice-apart",
    "session-list-paged", "session-list-ties-by-session-id",
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


def cases():
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    result = {}
    for fixture in manifest:
        if fixture["id"] not in IDS:
            continue
        trace = json.loads((ROOT / "fixtures" / fixture["path"]).read_text())
        result[fixture["id"]] = (trace, fixture["codes"])
        if fixture["valid"]:
            continue
        repair = copy.deepcopy(trace)
        page = repair[3]["payload"]["sessions"]
        if fixture["id"] == "session-list-over-limit":
            repair[2]["payload"]["limit"] = len(page)
        elif fixture["id"] == "session-list-over-the-default-limit":
            page.pop()
        elif "order" in fixture["id"]:
            page.sort(key=lambda entry: (-entry["updated_at_ms"], entry["session_id"]))
        else:
            repair[3]["payload"]["sessions"] = sorted(
                {entry["session_id"]: entry for entry in page}.values(),
                key=lambda entry: (-entry["updated_at_ms"], entry["session_id"]))
        result["repair-" + fixture["id"]] = (repair, [])
    base = result["session-list-paged"][0]
    empty = copy.deepcopy(base[:4])
    empty[3]["payload"]["sessions"] = []
    result["empty-page"] = (empty, [])
    repeated = copy.deepcopy(base)
    repeated[5]["payload"]["sessions"][0]["session_id"] = repeated[3]["payload"]["sessions"][0]["session_id"]
    result["same-session-on-another-page"] = (repeated, [])
    small, large = copy.deepcopy(base[2]), copy.deepcopy(base[2])
    small["payload"]["limit"] = 1
    large["id"] = "large"
    large_response, small_response = copy.deepcopy(base[3]), copy.deepcopy(base[3])
    large_response.update(id="large-response", in_reply_to="large")
    small_response["payload"]["sessions"].pop()
    correlated = copy.deepcopy(base[:2]) + [small, large, large_response, small_response]
    result["correlated-out-of-order-responses"] = (correlated, [])
    oversized = copy.deepcopy(correlated)
    oversized[5]["payload"]["sessions"] = copy.deepcopy(oversized[4]["payload"]["sessions"])
    result["correlated-over-limit"] = (oversized, ["session_list_over_limit"])
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
    return got, want


def mutation_check(output, corpus):
    changes = [
        ("explicit-limit", "if p.sessions.len() as i64 > limit {", "if false {", "session-list-over-limit"),
        ("default-limit", "SESSION_LIST_DEFAULT_LIMIT", "100", "session-list-over-the-default-limit"),
        ("limit-boundary", "if p.sessions.len() as i64 > limit {", "if p.sessions.len() as i64 >= limit {", "repair-session-list-over-limit"),
        ("time-order", "previous.updated_at_ms < entry.updated_at_ms", "false", "session-list-out-of-order"),
        ("tie-order", "previous.session_id > entry.session_id", "false", "session-list-tie-out-of-order"),
        ("duplicates", "if !seen.insert(entry.session_id.as_str()) {", "if !seen.insert(entry.session_id.as_str()) && false {", "session-list-duplicate-entry"),
        ("separated-duplicates", "if !seen.insert(entry.session_id.as_str()) {", "if !seen.insert(entry.session_id.as_str()) && false {", "session-list-lists-one-session-twice-apart"),
        ("request-correlation", ".get(&os(&e.in_reply_to))", '.get("large")', "correlated-over-limit"),
    ]
    original = SOURCE.read_bytes()
    try:
        for label, before, after, target in changes:
            text = original.decode()
            if text.count(before) != 1:
                raise RuntimeError(f"{label}: mutation target is not unique")
            SOURCE.write_text(text.replace(before, after, 1))
            build("rust", output, label + "-build")
            got, want = replay("rust", output, label, corpus[target])
            if got == want or got["exit"] != 1 - want["exit"] or any(phase != "semantic" for phase in got["phases"]):
                raise RuntimeError(f"{label}: mutation survived or failed inconclusively: {got}")
            print(f"KILLED {label}: {got}", flush=True)
    finally:
        SOURCE.write_bytes(original)
        build("rust", output, "restored-build")


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
