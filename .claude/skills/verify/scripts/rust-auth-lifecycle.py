#!/usr/bin/env python3
"""Judge synthetic standalone auth lifecycles with all three public validators."""
import argparse
import copy
import json
import re
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/validation/src/state/auth.rs"
IDS = {"auth-event-before-start", "auth-completed-before-start", "auth-missing-terminal",
       "auth-progress-without-a-terminal", "auth-login-interactive", "auth-login-cancelled"}


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
        if fixture["id"] not in IDS and not fixture["id"].startswith("auth-flow-"):
            continue
        trace = json.loads((ROOT / "fixtures" / fixture["path"]).read_text())
        result[fixture["id"]] = (trace, fixture["codes"])
        if fixture["id"] not in {"auth-flow-first-sequence-gap", "auth-flow-terminal-sequence-gap", "auth-flow-sequence-regression"}:
            continue
        repaired, sequences = copy.deepcopy(trace), {}
        for event in repaired:
            if event["type"] in {"auth.login.event", "auth.login.completed"}:
                flow = event["payload"]["flow_id"]
                sequences[flow] = sequences.get(flow, 0) + 1
                event["sequence"] = sequences[flow]
        result["repair-" + fixture["id"]] = (repaired, [])
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
    unknown = 'self.add( codes::AUTH_FLOW_ORDER, at, e, "/payload/flow_id", "auth flow event or command preceded its start response", );'
    changes = [
        ("active-id-reuse", "if self.auth_flows.contains_key(&id) {", "if false {", "auth-flow-reuses-active-id", []),
        ("completed-id-reuse", "if self.auth_flows.contains_key(&id) {", "if false {", "auth-flow-reuses-completed-id", ["missing_auth_terminal"]),
        ("unstarted-event", unknown, "{}", "auth-event-before-start", []),
        ("unstarted-cancel", unknown, "{}", "auth-flow-cancel-before-start", []),
        ("first-sequence", "next: 1,", "next: 2,", "auth-flow-immediate-completion", ["sequence_regression"]),
        ("sequence-gap", "sequence > expected", "false", "auth-flow-first-sequence-gap", []),
        ("sequence-regression", "sequence < expected", "false", "auth-flow-sequence-regression", []),
        ("advance-sequence", "flow.next = sequence.wrapping_add(1);", "flow.next = sequence;", "auth-login-interactive", ["sequence_gap"]),
        ("event-after-terminal", "if flow.terminal {", "if false {", "auth-flow-event-after-terminal", []),
        ("second-terminal", "if flow.terminal {", "if false {", "auth-flow-duplicate-terminal", []),
        ("completion-closes", "flow.terminal = true;", "flow.terminal = false;", "auth-flow-immediate-completion", ["missing_auth_terminal"]),
        ("missing-completion", "if !flow.terminal {", "if false {", "auth-progress-without-a-terminal", []),
        ("flow-sequence-domain", ".get_mut(&id)", '.get_mut("flow-1")', "auth-flow-interleaved", ["sequence_regression", "event_after_terminal", "missing_auth_terminal"]),
        ("cancel-is-not-completion", "if e.envelope_type != EnvelopeType::AUTH_LOGIN_EVENT && !completed { return; }", "if e.envelope_type != EnvelopeType::AUTH_LOGIN_EVENT && !completed { flow.terminal = true; return; }", "auth-flow-cancel-without-completion", []),
    ]
    original = SOURCE.read_bytes()
    try:
        for label, before, after, target, changed_codes in changes:
            text = original.decode()
            pattern = r"\s+".join(re.escape(part) for part in before.split())
            matches = list(re.finditer(pattern, text))
            if len(matches) != 1:
                raise RuntimeError(f"{label}: mutation target is not unique")
            match = matches[0]
            SOURCE.write_text(text[:match.start()] + after + text[match.end():])
            build("rust", output, label + "-build")
            got, want = replay("rust", output, label, corpus[target])
            changed = {"exit": int(bool(changed_codes)), "codes": sorted(changed_codes),
                       "phases": ["semantic"] if changed_codes else []}
            if got == want or got != changed:
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
