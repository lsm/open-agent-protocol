#!/usr/bin/env python3
"""Check staged typed corpus expectations through an external Rust consumer."""
import argparse
from copy import deepcopy
import json
import html
import re
from pathlib import Path
import runpy

ROOT = Path(__file__).resolve().parents[4]
HELPERS = runpy.run_path(str(Path(__file__).with_name("rust-pack-declarations.py")))
CONSUMER = '''use std::path::PathBuf;
use std::process::ExitCode;
use oap_validation::pack::PackLoader;
fn main() -> ExitCode {
    let paths: Vec<PathBuf> = std::env::args_os().skip(1).map(PathBuf::from).collect();
    let checked = PackLoader::read(&paths).and_then(PackLoader::read_resources)
        .and_then(|p| p.compile()).and_then(|p| p.check_corpus_entries());
    match checked {
        Ok(()) => {
            println!("{}", serde_json::json!({"stage":"corpus-expectations", "accepted":true}));
            ExitCode::SUCCESS
        }
        Err(error) => {
            println!("{}", serde_json::json!({"stage":"corpus-expectations", "accepted":false, "codes":error.codes(), "refusals":error.refusals}));
            ExitCode::FAILURE
        }
    }
}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)

    def run(command, label):
        return HELPERS["run"](command, output, label)

    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="pack-corpus-entries-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(CONSUMER)
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"),
                 "--target-dir", str(ROOT / "rust/target")], "consumer-build")
    assert build.returncode == 0, build.stderr
    docs = run(["cargo", "doc", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--no-deps"], "docs-build")
    assert docs.returncode == 0, docs.stderr
    page = (ROOT / "rust/target/doc/oap_validation/pack/struct.CompiledPacks.html").read_text()
    (output / "compiled-packs.html").write_text(page)
    words = html.unescape(re.sub(r"<[^>]+>", "", page))
    assert "Agent mode and provenance validation" in words, words
    goap = output / "goap"
    build = run(["go", "build", "-o", str(goap), "./go/cmd/goap"], "go-build")
    assert build.returncode == 0, build.stderr
    consumer = ROOT / "rust/target/debug/pack-corpus-entries-consumer"
    fixtures = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    own = "ext:com.example.corpus/1"
    positive = {"id": "positive", "path": "positive.json", "kind": "positive", "valid": True, "units": [own]}
    negative = dict(positive, kind="semantic-invalid", valid=False, phase="semantic", codes=["unavailable_capability"])
    provider = dict(positive, profile="model-provider-core", scope="frame", units=["provider-core"])
    cases = []

    def case(label, entry, accepted):
        cases.append((label, {"version": 1, "fixtures": [entry]}, accepted))

    case("positive", positive, True)
    case("negative", negative, True)
    case("provider-positive", provider, True)
    for field in ["id", "path", "kind", "valid", "phase", "codes", "units", "provenance", "covers", "mode", "profile", "scope", "packs", "rules"]:
        case("wrong-type-" + field, dict(positive, **{field: 7}), False)
    for field in ["phase", "codes", "provenance", "covers", "mode", "profile", "scope", "packs", "rules"]:
        case("null-" + field, dict(positive, **{field: None}), True)
    case("null-valid-on-negative", dict(negative, valid=None), True)
    case("null-rule-element", dict(positive, rules=[None]), True)
    case("unknown-field", dict(positive, future={"value": True}), True)
    for label, patch in [
        ("unknown-kind", {"kind": "other"}),
        ("positive-phase", {"phase": "schema"}),
        ("positive-codes", {"codes": ["schema_invalid"]}),
        ("invalid-positive", {"valid": False}),
        ("unknown-profile", {"profile": "other"}),
        ("scope-outside-provider", {"scope": "frame"}),
    ]:
        case(label, dict(positive, **patch), False)
    for label, patch in [
        ("negative-missing-codes", {"codes": []}),
        ("negative-missing-phase", {"phase": ""}),
        ("negative-invalid-phase", {"phase": "load"}),
        ("pack-defined-code", {"codes": ["com.example.corpus.error"]}),
        ("load-code-as-diagnostic", {"codes": ["pack_external_ref"]}),
        ("provider-only-agent-code", {"codes": ["credential_in_trace"]}),
    ]:
        case(label, dict(negative, **patch), False)
    for label, patch in [
        ("provider-no-scope", {"scope": ""}),
        ("provider-wrong-scope", {"scope": "trace"}),
        ("provider-no-units", {"units": []}),
        ("provider-agent-unit", {"units": [own]}),
        ("provider-mode", {"mode": "strict"}),
        ("provider-packs", {"packs": ["other"]}),
        ("provider-coverage", {"covers": [{"capability": "com.example.corpus.key", "aspect": "gate"}]}),
    ]:
        case(label, dict(provider, **patch), False)
    agent_codes = sorted({c for f in fixtures if not f.get("profile") and f.get("phase") in ["decode", "schema", "semantic"] for c in f.get("codes", [])})
    for code in agent_codes:
        case("agent-code-" + code, dict(negative, codes=[code]), True)
    for code in sorted({c for f in fixtures if f["kind"] == "load-invalid" for c in f["codes"]}):
        case("load-code-" + code, dict(negative, kind="load-invalid", phase="load", codes=[code]), True)
    case("fixture-kind-as-load-code", dict(negative, kind="load-invalid", phase="load", codes=["load-invalid"]), False)
    trace_codes = {"sequence_gap", "sequence_regression", "event_after_terminal", "terminal_not_assembly"}
    for code in ["malformed_json", "duplicate_key", "payload_decode", "schema_invalid", "credential_in_trace", "credential_in_headers", *sorted(trace_codes)]:
        entry = dict(provider, kind="semantic-invalid", valid=False, phase="semantic", codes=[code], scope="trace" if code in trace_codes else "frame")
        case("provider-code-" + code, entry, True)
    case("provider-agent-code", dict(provider, kind="semantic-invalid", valid=False, phase="semantic", codes=["missing_response"]), False)
    case("provider-mixed-scope", dict(provider, kind="semantic-invalid", valid=False, phase="semantic", codes=["sequence_gap", "schema_invalid"]), True)
    cases.extend([
        ("null-fixtures", {"version": 1, "fixtures": None}, True),
        ("empty-fixtures", {"version": 1, "fixtures": []}, True),
        ("wrong-version", {"version": 2, "fixtures": [positive]}, False),
        ("wrong-version-type", {"version": "1", "fixtures": [positive]}, False),
        ("wrong-fixtures-type", {"version": 1, "fixtures": {}}, False),
    ])
    results = []
    for label, manifest, accepted in cases:
        directory = output / label
        directory.mkdir()
        (directory / "pack.json").write_text(json.dumps({"id": "com.example.corpus", "version": "1", "schemas": ["types.json"], "fixtures": "manifest.json"}))
        (directory / "types.json").write_text("{}")
        (directory / "manifest.json").write_text(json.dumps(deepcopy(manifest)))
        rust = run([str(consumer), str(directory)], label + ".rust")
        go = run([str(goap), "validate", "--format", "json", "--pack", str(directory), str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
        data = json.loads(rust.stdout)
        assert data["stage"] == "corpus-expectations", data
        assert data["accepted"] == accepted, (label, data)
        assert rust.returncode == go.returncode == (0 if accepted else 1), (label, rust.stdout, go.stdout, go.stderr)
        if not accepted:
            assert data["codes"] == [], (label, data)
        results.append({"id": label, "accepted": accepted})
    differences = []
    for label, patch in [("deferred-agent-mode", {"mode": "other"}), ("deferred-empty-provenance", {"provenance": [""]})]:
        directory = output / label
        directory.mkdir()
        (directory / "pack.json").write_text(json.dumps({"id": "com.example.corpus", "version": "1", "schemas": ["types.json"], "fixtures": "manifest.json"}))
        (directory / "types.json").write_text("{}")
        (directory / "manifest.json").write_text(json.dumps({"version": 1, "fixtures": [dict(positive, **patch)]}))
        rust = run([str(consumer), str(directory)], label + ".rust")
        go = run([str(goap), "validate", "--format", "json", "--pack", str(directory), str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
        assert rust.returncode == 0 and json.loads(rust.stdout)["accepted"], (label, rust.stdout)
        assert go.returncode == 1 and "fixture manifest" in go.stderr, (label, go.stdout, go.stderr)
        differences.append({"id": label, "rust": "accepted at staged boundary", "go": "refused by complete loader", "deferred_to": "full constructor"})
    (output / "staged-differences.json").write_text(json.dumps(differences, indent=2) + "\n")
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"PASS: {len(results)} public typed corpus expectation/profile/code cases agree with Go")
    print("STAGED: two explicit mode/provenance differences recorded; identity, path, owner and completeness checks follow in the full constructor")


if __name__ == "__main__":
    main()
