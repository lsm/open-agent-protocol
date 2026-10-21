#!/usr/bin/env python3
"""Replay response-local model catalog shapes through three public validator CLIs."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/validation/src/state/models.rs"
IDS = {
    "models-duplicate-id", "models-duplicate-id-apart", "models-two-defaults",
    "models-two-defaults-around-the-current-model", "models-current-not-listed",
    "models-empty-catalog",
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


def catalog(trace):
    return next(e["payload"] for e in trace if e["type"] == "models.response")


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
        page = catalog(repair)
        if "duplicate-id" in fixture["id"]:
            page["models"][-1]["id"] = "m3"
        elif "two-defaults" in fixture["id"]:
            for model in page["models"]:
                model.pop("default", None)
        else:
            page["models"].append({"id": page["current_model_id"]})
        result["repair-" + fixture["id"]] = (repair, [])
    base = result["models-empty-catalog"][0]
    for name, models, current in [
        ("no-default", [{"id":"m1"},{"id":"m2"}], None),
        ("listed-nondefault-current", [{"id":"m1"},{"id":"m2"}], "m1"),
        ("another-model-is-default", [{"id":"m1"},{"id":"m2","default":True}], "m1"),
        ("case-sensitive-ids", [{"id":"m1"},{"id":"M1"}], "m1"),
    ]:
        trace = copy.deepcopy(base)
        catalog(trace)["models"] = models
        if current is not None:
            catalog(trace)["current_model_id"] = current
        result[name] = (trace, [])
    combined = copy.deepcopy(base)
    catalog(combined).update(models=[{"id":"m1","default":True},{"id":"m1","default":True}], current_model_id="absent")
    codes = ["duplicate_model_id", "ambiguous_default_model", "model_not_in_catalog"]
    result["all-shape-faults"] = (combined, codes)
    stale = copy.deepcopy(combined)
    stale[3]["capability_revision"] = "stale"
    result["stale-all-shape-faults"] = (stale, codes + ["stale_capability_revision"])
    repeated = copy.deepcopy(result["listed-nondefault-current"][0])
    request, response = copy.deepcopy(repeated[2]), copy.deepcopy(repeated[3])
    request["id"] = "query2"
    response.update(id="response2", in_reply_to="query2")
    repeated.extend([request, response])
    result["ids-repeat-across-responses"] = (repeated, [])
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
        ("duplicate-ids", "if let Some(duplicate) = duplicate {", "if let Some(duplicate) = None::<&str> {", "models-duplicate-id", []),
        ("separated-duplicate-ids", "if let Some(duplicate) = duplicate {", "if let Some(duplicate) = None::<&str> {", "models-duplicate-id-apart", []),
        ("multiple-defaults", "if defaults > 1 {", "if defaults > usize::MAX {", "models-two-defaults", []),
        ("defaults-around-current", "if defaults > 1 {", "if defaults > usize::MAX {", "models-two-defaults-around-the-current-model", []),
        ("single-default-boundary", "if defaults > 1 {", "if defaults > 0 {", "another-model-is-default", ["ambiguous_default_model"]),
        ("current-membership", "!ids.contains(p.current_model_id.as_str())", "false", "models-current-not-listed", []),
        ("stale-shape-gate", "let p: ModelsResponse = payload(e);", 'if e.capability_revision.as_deref() == Some("stale") { return; }\n        let p: ModelsResponse = payload(e);', "stale-all-shape-faults", ["stale_capability_revision"]),
    ]
    original = SOURCE.read_bytes()
    try:
        for label, before, after, target, changed_codes in changes:
            text = original.decode()
            if text.count(before) != 1:
                raise RuntimeError(f"{label}: mutation target is not unique")
            SOURCE.write_text(text.replace(before, after, 1))
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
