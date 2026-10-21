#!/usr/bin/env python3
"""Observe synthetic open-time settings through the three public validators."""
import argparse
import copy
import json
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    corpus = cases()
    for language in ("go", "rust", "zig"):
        build(language, output, language + "-build")
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
