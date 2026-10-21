#!/usr/bin/env python3
"""Replay Decision 0050 redelivery judgments through all three public validators."""
import argparse
import json
import re
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCES = {
    "go": ROOT / "go/validation/redelivery.go",
    "rust": ROOT / "rust/validation/src/state/redelivery.rs",
    "zig": ROOT / "zig/src/validation/semantic.zig",
}


def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / f"{label}.stdout").write_text(result.stdout)
    (output / f"{label}.stderr").write_text(result.stderr)
    return result


def build(language, output, label):
    commands = {
        "go": ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"],
        "rust": ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"],
        "zig": ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"],
    }
    result = run(commands[language], output, label + "-build")
    if result.returncode != 0:
        raise RuntimeError(f"{label}: build failed; this is not a mutation kill")


def replay(language, fixture, output, label):
    commands = {
        "go": [str(output / "goap"), "validate", "--format", "json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }
    result = run(commands[language] + [str(ROOT / "fixtures" / fixture["path"])], output, label)
    document = json.loads(result.stdout)
    reports = document if isinstance(document, list) else document.get("reports", [document])
    diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
    codes = sorted({d["code"] for d in diagnostics})
    phases = {d["phase"] for d in diagnostics}
    observed = (result.returncode, codes, phases)
    expected = (int(not fixture["valid"]), sorted(fixture["codes"]), {fixture["phase"]} if not fixture["valid"] else set())
    return observed, expected


def mutations(language):
    if language == "go":
        return [
            ("pairing", "duplicate-previous", "previous[prev] ||", "false ||"),
            ("roots", "duplicate-root", "roots[root] ||", "false ||"),
            ("root-alias", "root-belongs-to-another-pair", "entry.RootRunID != entry.PreviousRunID", "entry.RootRunID != entry.RootRunID"),
            ("new-pairing", "duplicate-new", "position == 0 || position <= last", "position < 0 || last < 0"),
            ("queued-membership", "missing-queued-member", "position == 0 || position <= last", "position < 0 || last < 0"),
            ("new-order", "reversed-new-order", "position == 0 || position <= last", "position < 0 || last < 0"),
            ("held-pairing", "held-extra", "if st.heldKnown || len(held) > 0 {", "if false {"),
            ("root-chain", "root-changed", 'if expected != "" && root != expected {', "if false {"),
            ("first-root", "wrong-first-root", 'if expected != "" && root != expected {', "if false {"),
            ("root-head", "root-fork", "known && head != prev", "known && head != head"),
            ("old-carrier", "self-replacement", "active.RunID == prev || active.RunID == root", "active.RunID == active.RunID && false"),
            ("old-order", "queue-order-conflicts-with-held", "r.order <= priorOrder", "r.order < priorOrder && false"),
            ("close-carriers", "chain", "r.terminal, r.terminalAt = true, i", "r.terminal, r.terminalAt = false, i"),
            ("live-open", "live-open-before-restart", "if p.Recovery == nil || !p.Recovery.Recovered {", "if false {"),
        ]
    if language == "rust":
        return [
            ("pairing", "duplicate-previous", "!previous.insert(prev) ||", "{ previous.insert(prev); false } ||"),
            ("roots", "duplicate-root", "!roots.insert(root) ||", "{ roots.insert(root); false } ||"),
            ("root-alias", "root-belongs-to-another-pair", "entry.root_run_id != entry.previous_run_id", "entry.root_run_id != entry.root_run_id"),
            ("new-pairing", "duplicate-new", "position == 0 || position <= last", "position < last && false"),
            ("queued-membership", "missing-queued-member", "position == 0 || position <= last", "position < last && false"),
            ("new-order", "reversed-new-order", "position == 0 || position <= last", "position < last && false"),
            ("held-pairing", "held-extra", "if history.held.is_some() || !held.is_empty() {", "if false {"),
            ("root-chain", "root-changed", "root != expected", "expected != expected"),
            ("first-root", "wrong-first-root", "root != expected", "expected != expected"),
            ("root-head", "root-fork", "head != prev", "head != head"),
            ("old-carrier", "self-replacement", "active.run_id.as_str() == prev || active.run_id.as_str() == root", "false"),
            ("old-order", "queue-order-conflicts-with-held", "run.order <= order", "order != order"),
            ("close-carriers", "chain", "run.terminal = true;", "run.terminal = false;"),
            ("live-open", "live-open-before-restart", "if !p.recovery.as_ref().is_some_and(|r| r.recovered) {", "if false {"),
        ]
    return [
        ("live-open", "live-open-before-restart", 'if (!memberBool(recovery, "recovered")) {', "if (false) {"),
        ("pairing", "duplicate-previous", "previous.contains(prev) or", "false or"),
        ("roots", "duplicate-root", "roots.contains(root) or", "false or"),
        ("root-alias", "root-belongs-to-another-pair", '!std.mem.eql(u8, memberString(entry, "root_run_id"), memberString(entry, "previous_run_id"))', "false"),
        ("new-pairing", "duplicate-new", "position == 0 or position <= last", "position < last and false"),
        ("queued-membership", "missing-queued-member", "position == 0 or position <= last", "position < last and false"),
        ("new-order", "reversed-new-order", "position == 0 or position <= last", "position < last and false"),
        ("held-pairing", "held-extra", "if (holder.redelivery_held != null or held.items.len > 0) {", "if (false) {"),
        ("root-chain", "root-changed", "!std.mem.eql(u8, root, known)", "!std.mem.eql(u8, known, known)"),
        ("first-root", "wrong-first-root", "!std.mem.eql(u8, root, known)", "!std.mem.eql(u8, known, known)"),
        ("root-head", "root-fork", "!std.mem.eql(u8, head, prev)", "!std.mem.eql(u8, head, head)"),
        ("old-carrier", "self-replacement", "std.mem.eql(u8, id, prev) or std.mem.eql(u8, id, root)", "false"),
        ("old-order", "queue-order-conflicts-with-held", "run.order <= order", "order != order"),
        ("close-carriers", "chain", "run.terminal = true;\n                    run.terminal_at = index;", "run.terminal = false;\n                    run.terminal_at = index;"),
    ]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    parser.add_argument("--language", choices=list(SOURCES))
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    fixtures = [f for f in manifest if f["id"].startswith("redelivery-state-") or f["id"] == "session-reopen-reports-redelivered-runs"]
    languages = [args.language] if args.language else list(SOURCES)
    for language in languages:
        build(language, output, language + "-baseline")
        for fixture in fixtures:
            label = language + "." + fixture["id"]
            observed, expected = replay(language, fixture, output, label)
            if observed != expected:
                raise RuntimeError(f"{label}: got {observed}, want {expected}")
            print(f"PASS {label}: exit {observed[0]}, codes {observed[1]}", flush=True)
        if not args.mutation_check:
            continue
        path = SOURCES[language]
        original = path.read_bytes()
        try:
            for label, suffix, before, after in mutations(language):
                text = original.decode()
                if text.count(before) != 1:
                    raise RuntimeError(f"{language}.{label}: mutation target is not unique")
                path.write_text(text.replace(before, after, 1))
                label = language + "." + label
                fixture = next(f for f in fixtures if f["id"] == "redelivery-state-" + suffix)
                if language == "zig":
                    result = run(["zig", "build", "--build-file", "zig/build.zig", "test-unit-validation", "-Doptimize=ReleaseSafe"], output, label)
                    pattern = re.escape(fixture["id"]) + r"\s+want \[(.*?)\]\s+got  \[(.*?)\]"
                    match = re.search(pattern, result.stdout + result.stderr)
                    if result.returncode == 0 or match is None:
                        raise RuntimeError(f"{label}: no executed fixture disagreement; compiler errors do not count")
                    got = match[2].split()
                    if bool(got) != fixture["valid"]:
                        raise RuntimeError(f"{label}: mutation failed inconclusively: {got}")
                    print(f"KILLED {label}: manifest fixture codes {got}", flush=True)
                else:
                    build(language, output, label)
                    observed, expected = replay(language, fixture, output, label)
                    if observed == expected or observed[0] != int(fixture["valid"]):
                        raise RuntimeError(f"{label}: mutation survived or failed inconclusively: {observed}")
                    print(f"KILLED {label}: exit {observed[0]}, codes {observed[1]}", flush=True)
        finally:
            path.write_bytes(original)
            build(language, output, language + "-restored")
        for fixture in fixtures:
            observed, expected = replay(language, fixture, output, language + ".restored." + fixture["id"])
            if observed != expected:
                raise RuntimeError(f"{language}: restored replay failed: {fixture['id']}")
    print(f"PASS: {len(fixtures)} redelivery traces agree on {', '.join(languages)}", flush=True)


if __name__ == "__main__":
    main()
