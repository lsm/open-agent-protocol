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
use oxap_validation::pack::PackLoader;
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
    parser.add_argument("--complete", action="store_true")
    options = parser.parse_args()
    output = Path(options.output_dir).resolve()
    stage = "complete-loader" if options.complete else "corpus-expectations"
    output.mkdir(parents=True, exist_ok=False)

    def run(command, label):
        return HELPERS["run"](command, output, label)

    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="pack-corpus-entries-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noxap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    source = CONSUMER
    if options.complete:
        source = source.replace(
            "PackLoader::read(&paths).and_then(PackLoader::read_resources)\n        .and_then(|p| p.compile()).and_then(|p| p.check_corpus_entries())",
            "PackLoader::load(&paths).map(|_| ())")
    (project / "src/main.rs").write_text(source.replace("corpus-expectations", stage))
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"),
                 "--target-dir", str(ROOT / "rust/target")], "consumer-build")
    assert build.returncode == 0, build.stderr
    docs = run(["cargo", "doc", "--manifest-path", "rust/Cargo.toml", "-p", "oxap-validation", "--no-deps"], "docs-build")
    assert docs.returncode == 0, docs.stderr
    page = (ROOT / "rust/target/doc/oap_validation/pack/struct.CompiledPacks.html").read_text()
    (output / "compiled-packs.html").write_text(page)
    words = html.unescape(re.sub(r"<[^>]+>", "", page))
    assert "Agent mode and provenance validation" in words, words
    assert "Nested pack declarations and empty agent units" in words, words
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
        assert data["stage"] == stage, data
        assert data["accepted"] == accepted, (label, data)
        assert rust.returncode == go.returncode == (0 if accepted else 1), (label, rust.stdout, go.stdout, go.stderr)
        if not accepted:
            assert data["codes"] == [], (label, data)
        results.append({"id": label, "accepted": accepted})
    differences = []
    deferred = [
        ("deferred-agent-mode", dict(positive, mode="other"), "unsupported validation mode"),
        ("deferred-empty-provenance", dict(positive, provenance=[""]), "empty provenance"),
        ("deferred-nested-packs", dict(positive, packs=["other"]), "cannot load further packs"),
        ("deferred-empty-units", dict(positive, units=[]), "no conformance units"),
        ("deferred-empty-id", dict(positive, id=""), "lacks id or path"),
        ("deferred-empty-path", dict(positive, path=""), "lacks id or path"),
        ("deferred-escaping-path", dict(positive, path="../outside.json"), "path outside fixture root"),
        ("deferred-core-unit", dict(positive, units=["core"]), "pack_fixture_claims_core_unit"),
        ("deferred-unloaded-unit", dict(positive, units=["ext:com.example.absent/1"]), "ext_claim_without_pack"),
        ("deferred-empty-capability", dict(negative, covers=[{"capability":"", "aspect":"gate"}]), "empty capability"),
        ("deferred-coverage-aspect", dict(negative, covers=[{"capability":"com.example.corpus.key", "aspect":"other"}]), "unknown aspect"),
        ("deferred-positive-coverage", dict(positive, covers=[{"capability":"com.example.corpus.key", "aspect":"gate"}]), "coverage is asserted by semantic-invalid fixtures"),
    ]
    for label, entry, reason in deferred:
        directory = output / label
        directory.mkdir()
        (directory / "pack.json").write_text(json.dumps({"id": "com.example.corpus", "version": "1", "schemas": ["types.json"], "fixtures": "manifest.json"}))
        (directory / "types.json").write_text("{}")
        (directory / "manifest.json").write_text(json.dumps({"version": 1, "fixtures": [entry]}))
        rust = run([str(consumer), str(directory)], label + ".rust")
        go = run([str(goap), "validate", "--format", "json", "--pack", str(directory), str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
        data = json.loads(rust.stdout)
        assert go.returncode == 1 and reason in go.stderr, (label, go.stdout, go.stderr)
        if options.complete:
            assert rust.returncode == 1 and not data["accepted"], (label, data)
            go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
            assert data["codes"] == go_codes, (label, data, go.stderr)
            results.append({"id": label, "accepted": False, "codes": data["codes"]})
            continue
        assert rust.returncode == 0 and data["accepted"], (label, data)
        differences.append({"id": label, "rust": "accepted at staged boundary", "go": "refused by complete loader", "reason": reason, "deferred_to": "full constructor"})
    if options.complete:
        for fixture in fixtures:
            if fixture["kind"] != "load-invalid":
                continue
            label = fixture["id"]
            directories = [ROOT / "fixtures" / path for path in [fixture["path"], *fixture.get("packs", [])]]
            rust = run([str(consumer), *map(str, directories)], label + ".rust")
            command = [str(goap), "validate", "--format", "json"]
            for directory in directories:
                command += ["--pack", str(directory)]
            go = run(command + [str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
            data = json.loads(rust.stdout)
            go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
            assert rust.returncode == go.returncode == 1, (label, data, go.stderr)
            assert data["codes"] == go_codes == sorted(set(fixture["codes"])), (label, data, go.stderr)
            results.append({"id": label, "accepted": False, "codes": data["codes"]})
        policy_cases = [
            ("missing-manifest", None, ["com.example.corpus.key"], False),
            ("no-manifest-no-keys", None, [], True),
            ("empty-corpus-with-keys", [], ["com.example.corpus.key"], False),
            ("duplicate-id", [positive, dict(positive, path="other.json")], [], False),
            ("duplicate-path", [positive, dict(positive, id="other")], [], False),
            ("agent-normalized-paths-remain-distinct", [dict(positive, path="a/../positive.json"), dict(positive, id="other")], [], True),
            ("provider-normalized-paths-collide", [dict(provider, path="a/../positive.json"), dict(provider, id="other")], [], False),
            ("contained-normalized-path", [dict(positive, path="a/../positive.json")], [], True),
            ("absolute-path", [dict(positive, path="/outside.json")], [], False),
            ("null-units", [dict(positive, units=None)], [], False),
            ("load-entry-nested-packs", [dict(negative, kind="load-invalid", phase="load", codes=["pack_external_ref"], packs=["other"])], [], False),
            ("load-entry-no-units", [dict(negative, kind="load-invalid", phase="load", codes=["pack_external_ref"], units=[])], [], False),
            ("earlier-owner-error-before-later-expectation", [dict(positive, units=["core"]), dict(negative, id="second", path="second.json", codes=["unknown"])], [], False),
        ]
        for aspects in [["gate"], ["honour"], ["gate", "honour"]]:
            entries = [dict(negative, id=aspect, path=aspect + ".json", covers=[{"capability":"com.example.corpus.key", "aspect":aspect}]) for aspect in aspects]
            policy_cases.append(("coverage-" + "-".join(aspects), entries, ["com.example.corpus.key"], len(aspects) == 2))
        for label, entries, keys, accepted in policy_cases:
            directory = output / label
            directory.mkdir()
            descriptor = {"id":"com.example.corpus", "version":"1", "schemas":["types.json"], "capability_keys":keys}
            if entries is not None:
                descriptor["fixtures"] = "manifest.json"
                (directory / "manifest.json").write_text(json.dumps({"version":1, "fixtures":entries}))
            (directory / "pack.json").write_text(json.dumps(descriptor))
            (directory / "types.json").write_text("{}")
            rust = run([str(consumer), str(directory)], label + ".rust")
            go = run([str(goap), "validate", "--format", "json", "--pack", str(directory), str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
            data = json.loads(rust.stdout)
            assert rust.returncode == go.returncode == (0 if accepted else 1), (label, data, go.stdout, go.stderr)
            if not accepted:
                go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
                assert data["codes"] == go_codes, (label, data, go.stderr)
            results.append({"id":label, "accepted":accepted})
    (output / "staged-differences.json").write_text(json.dumps(differences, indent=2) + "\n")
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"PASS: {len(results)} public typed corpus expectation/profile/code cases agree with Go")
    if options.complete:
        assert not differences, differences
        print("PASS: all 12 staged policy differences close through the complete constructor; all shared load refusals judged")
    else:
        print("STAGED: 12 explicit context/ownership/coverage differences recorded; full constructor follows")


if __name__ == "__main__":
    main()
