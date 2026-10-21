#!/usr/bin/env python3
"""Observe synthetic open-time settings through the three public validators."""
import argparse
import copy
import json
import re
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/validation/src/state/controls.rs"
OLD = {
    "session-settings-unadvertised-admitted", "session-settings-unadvertised-wrong-refusal",
    "session-settings-live-only-admitted-at-open", "session-settings-degraded-admitted-without-optin",
    "session-settings-level-rounded", "session-settings-policy-rounded",
    "session-settings-at-open", "session-settings-share-at-open",
    "session-settings-unadvertised-refused", "session-settings-live-only-refused-at-open",
    "session-settings-degraded-with-optin", "session-settings-degraded-refused-without-optin",
    "session-settings-state-omits-unknown",
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
    result = {}
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    for fixture in manifest:
        name = fixture["id"]
        if name not in OLD and not name.startswith("open-settings-"):
            continue
        trace = json.loads((ROOT / "fixtures" / fixture["path"]).read_text())
        codes = fixture["codes"]
        locations = [[code, len(trace) - 1] for code in codes]
        if name == "open-settings-gates-before-fidelity":
            locations *= 2
        result[name] = (trace, codes, locations)
        if fixture["valid"]:
            continue
        fixed = copy.deepcopy(trace)
        requests = {e["id"]: e["payload"] for e in fixed if e["type"] == "session.open.request"}
        for e in fixed:
            if e["type"] not in ("session.open.response", "error.response") or e.get("in_reply_to") not in requests:
                continue
            request = requests[e["in_reply_to"]]
            if "session_state_mismatch" in codes:
                for field in ("reasoning_level", "compaction_policy"):
                    if field in request:
                        e["payload"][field] = copy.deepcopy(request[field])
            else:
                feature = "session.reasoning" if "reasoning_level" in request else "session.compaction.policy"
                code = "capability_degraded" if "degraded_without_optin" in codes else "unsupported_feature"
                if name == "open-settings-mixed-defects-wrong-refusal":
                    feature = "session.compaction.policy"
                details = {"feature": feature}
                if code == "unsupported_feature":
                    details["reason"] = "unadvertised"
                e["type"] = "error.response"
                e["payload"] = {"error": {"code": code, "message": "synthetic repaired refusal", "details": details}}
        result["repair-" + name] = (fixed, [], [])
    base = result["session-settings-at-open"][0]
    for level in ("native", "emulated"):
        for policy in ({"kind":"auto"}, {"kind":"off"}, {"kind":"share", "share_percent":1}, {"kind":"share", "share_percent":100}, {"kind":"tokens", "tokens":1}, {"kind":"tokens", "tokens":9007199254740993}):
            trace = copy.deepcopy(base)
            trace[1]["payload"]["features"]["session.reasoning"]["level"] = level
            trace[1]["payload"]["features"]["session.compaction.policy"]["level"] = level
            trace[2]["payload"]["compaction_policy"] = copy.deepcopy(policy)
            trace[3]["payload"]["compaction_policy"] = copy.deepcopy(policy)
            result[f"matching-{level}-{policy}"] = (trace, [], [])
    omitted = copy.deepcopy(base)
    omitted[3]["payload"].pop("reasoning_level")
    omitted[3]["payload"].pop("compaction_policy")
    result["unknown-settings-stay-absent"] = (omitted, [], [])
    unsolicited = copy.deepcopy(base)
    unsolicited[2]["payload"].pop("reasoning_level")
    unsolicited[2]["payload"].pop("compaction_policy")
    unsolicited[1]["payload"]["features"] = {}
    result["unrequested-settings-do-not-elect-features"] = (unsolicited, [], [])
    return result


def replay(language, output, label, case):
    trace, codes, locations = case
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
    got["locations"] = sorted([d["code"], d["index"]] for d in diagnostics)
    want["locations"] = sorted(locations)
    return got, want


def method_span(text, name):
    pattern = re.compile(r"^    (?:pub(?:\([^)]*\))? )?fn " + re.escape(name) + r"\(", re.M)
    found = list(pattern.finditer(text))
    if len(found) != 1:
        raise RuntimeError(f"{name}: method target is not unique")
    start = found[0].start()
    next_method = re.search(r"^    (?:pub(?:\([^)]*\))? )?fn ", text[found[0].end():], re.M)
    end = found[0].end() + next_method.start() if next_method else len(text)
    return start, end


def mutation_check(output, corpus):
    attribution = 'self.pending_open_settings\n                    .get(reply)'
    changes = [
        ("affirmative-feature", "open_settings_gate", "!affirmative(&level) || !disclosed", "false", "session-settings-unadvertised-admitted", [], []),
        ("open-mode", "open_settings_gate", "!affirmative(&level) || !disclosed", "!affirmative(&level)", "open-settings-reasoning-missing-mode", [], []),
        ("degraded-optin", "open_settings_gate", "level == SupportLevel::DEGRADED && !p.allows_degraded(key)", "false", "session-settings-degraded-admitted-without-optin", [], []),
        ("optin-own-feature", "open_settings_gate", "p.allows_degraded(key)", "p.allows_degraded(FEATURE_SESSION_REASONING)", "open-settings-policy-degraded-wrong-optin", [], []),
        ("refusal-feature", "conformed_by", "if err.code != self.code {", "if err.code == self.code { return true; }\n        if err.code != self.code {", "open-settings-refusal-missing-feature", [], []),
        ("refusal-reason", "conformed_by", "if err.code != self.code {", "if err.code == self.code { return true; }\n        if err.code != self.code {", "open-settings-refusal-wrong-reason", [], []),
        ("reasoning-value", "open_settings_response", "p.reasoning_level != pending.level", "false", "session-settings-level-rounded", [], []),
        ("share-value", "open_settings_response", "asked != reported", "asked.kind != reported.kind", "open-settings-share-rounded", [], []),
        ("token-value", "open_settings_response", "asked != reported", "asked.kind != reported.kind", "open-settings-tokens-rounded", [], []),
        ("unknown-reasoning", "open_settings_response", "!p.reasoning_level.is_empty()", "true", "unknown-settings-stay-absent", ["session_state_mismatch"], [["session_state_mismatch", 3]]),
        ("unknown-policy", "open_settings_response", "(&pending.policy, &p.compaction_policy)", "(&pending.policy, &Some(p.compaction_policy.clone().unwrap_or_default()))", "unknown-settings-stay-absent", ["session_state_mismatch"], [["session_state_mismatch", 3]]),
        ("reply-correlation", "open_settings_response", ".remove(&os(&e.in_reply_to))", '.remove("op2")', "open-settings-reversed-replies-rounded", [], []),
        ("diagnostic-location", "open_settings_response", "codes::SESSION_STATE_MISMATCH,\n                at,", "codes::SESSION_STATE_MISMATCH,\n                (at.0 + 1, at.1),", "session-settings-level-rounded", ["session_state_mismatch"], [["session_state_mismatch", 4]]),
        ("retained-settings", "attribute", attribution, attribution.replace(".get(reply)", '.get("")'), "open-settings-setting-refusal-discharges-provide", ["unavailable_capability", "undisclosed_provide_limit"], [["unavailable_capability", 5], ["undisclosed_provide_limit", 5]]),
        ("retain-every-setting", "attribute", ".flat_map(|pending| &pending.expectations)", ".flat_map(|pending| pending.expectations.iter().take(1))", "repair-open-settings-mixed-defects-wrong-refusal", ["degraded_without_optin"], [["degraded_without_optin", 3]]),
        ("refusal-rank", "attribute", ".min_by(|a, b| a.order(b))", ".max_by(|a, b| a.order(b))", "open-settings-mixed-defects-wrong-refusal", ["degraded_without_optin"], [["degraded_without_optin", 3]]),
        ("refusal-diagnostic", "open_settings_refusal", "if !discharged {", "if false {", "session-settings-unadvertised-wrong-refusal", [], []),
        ("gates-before-fidelity", "open_settings_response", "            return;\n        }\n        let p: SessionState", "        }\n        let p: SessionState", "open-settings-gates-before-fidelity", ["session_state_mismatch", "unavailable_capability"], [["unavailable_capability", 3], ["unavailable_capability", 3], ["session_state_mismatch", 3], ["session_state_mismatch", 3]]),
    ]
    original = SOURCE.read_bytes()
    results = []
    try:
        for label, method, before, after, target, changed_codes, locations in changes:
            text = original.decode()
            start, end = method_span(text, method)
            body = text[start:end]
            if body.count(before) != 1:
                raise RuntimeError(f"{label}: mutation target is not unique")
            SOURCE.write_text(text[:start] + body.replace(before, after, 1) + text[end:])
            build("rust", output, label + "-build")
            got, want = replay("rust", output, label, corpus[target])
            changed = {"exit": int(bool(changed_codes)), "codes": sorted(changed_codes), "phases": ["semantic"] if changed_codes else [], "locations": sorted(locations)}
            if got == want or got != changed:
                raise RuntimeError(f"{label}: mutation survived or failed inconclusively: {got}; expected {changed}")
            results.append({"mutation": label, "case": target, "got": got, "original": want})
            print(f"KILLED {label}: {got}", flush=True)
    finally:
        SOURCE.write_bytes(original)
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
