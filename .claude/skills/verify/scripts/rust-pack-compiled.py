#!/usr/bin/env python3
"""Exercise compiled pack schemas through an external public Rust consumer."""
import argparse
import json
from pathlib import Path
import re
import runpy

ROOT = Path(__file__).resolve().parents[4]
RESOURCES = runpy.run_path(str(Path(__file__).with_name("rust-pack-resources.py")))
HELPERS = runpy.run_path(str(Path(__file__).with_name("rust-pack-declarations.py")))
CODES = RESOURCES["CODES"] | {"pack_external_ref", "pack_branch_unpinned", "pack_branch_undeclared_type"}
CONSUMER = '''use std::path::PathBuf;
use std::process::ExitCode;
use oap_validation::pack::PackLoader;
use serde_json::Value;
fn main() -> Result<ExitCode, Box<dyn std::error::Error>> {
    let paths: Vec<PathBuf> = std::env::args_os().skip(1).map(PathBuf::from).collect();
    let compiled = match PackLoader::read(&paths[1..]).and_then(PackLoader::read_resources).and_then(|p| p.compile()) {
        Ok(compiled) => compiled,
        Err(error) => {
            println!("{}", serde_json::json!({"stage":"compiled-schemas", "codes":error.codes(), "refusals":error.refusals}));
            return Ok(ExitCode::FAILURE);
        }
    };
    let trace: Value = serde_json::from_slice(&std::fs::read(&paths[0])?)?;
    let diagnostics: Vec<_> = trace.as_array().unwrap().iter().enumerate().filter_map(|(index, envelope)| {
        compiled.validate_schema(envelope, "external-schema-consumer").err()
            .map(|diagnostic| serde_json::json!({"index":index, "diagnostic":diagnostic}))
    }).collect();
    let valid = diagnostics.is_empty();
    let ids: Vec<_> = compiled.resources().descriptors().map(|(_, d)| d["id"].as_str().unwrap()).collect();
    println!("{}", serde_json::json!({"stage":"compiled-schemas", "schema_valid":valid, "diagnostics":diagnostics, "pack_ids":ids}));
    Ok(if valid { ExitCode::SUCCESS } else { ExitCode::FAILURE })
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
        '[package]\nname="pack-compiled-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(CONSUMER)
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"), "--target-dir", str(ROOT / "rust/target")], "consumer-build")
    assert build.returncode == 0, build.stderr
    goap = output / "goap"
    build = run(["go", "build", "-o", str(goap), "./go/cmd/goap"], "go-build")
    assert build.returncode == 0, build.stderr
    consumer = ROOT / "rust/target/debug/pack-compiled-consumer"
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    core = ROOT / "fixtures/valid/core-completed.json"
    cases = []
    for fixture in manifest:
        if fixture["kind"] == "load-invalid" and set(fixture["codes"]) <= CODES:
            directories = [ROOT / "fixtures" / p for p in [fixture["path"], *fixture.get("packs", [])]]
            cases.append((fixture["id"], core, directories, False, sorted(set(fixture["codes"]))))
        elif fixture.get("packs") and (fixture["valid"] or fixture.get("phase") == "schema"):
            directories = [ROOT / "fixtures" / p for p in fixture["packs"]]
            cases.append((fixture["id"], ROOT / "fixtures" / fixture["path"], directories, fixture["valid"], None))
    for label, citation, schema, codes in [
        ("whole-document", "types.json", {"type":"object", "properties":{"type":{"const":"com.example.compiled.event"}}}, None),
        ("root-fragment", "types.json#", {"type":"object", "properties":{"type":{"const":"com.example.compiled.event"}}}, None),
        ("noncontributing-file", "missing.json", {}, []),
        ("dangling-pointer", "types.json#/$defs/missing", {}, []),
        ("unknown-core-resource", "types.json", {"properties":{"type":{"const":"com.example.compiled.event"}, "payload":{"$ref":"https://open-agent-protocol.local/v0.1/missing.schema.json"}}}, ["pack_external_ref"]),
        ("invalid-schema-keyword", "types.json", {"properties":{"type":{"const":"com.example.compiled.event"}, "payload":{"type":17}}}, []),
    ]:
        directory = output / label
        directory.mkdir()
        (directory / "types.json").write_text(json.dumps(schema))
        descriptor = {
            "id":"com.example.compiled", "version":"1", "schemas":["types.json"],
            "envelope_types":[{"type":"com.example.compiled.event", "role":"event", "schema":citation}],
            "gates":[{"type":"com.example.compiled.event", "ungated":True}],
        }
        (directory / "pack.json").write_text(json.dumps(descriptor))
        cases.append((label, core, [directory], codes is None, codes))
    for label, identifier, reference in [
        ("compiled-fragment-escaped", "", "#/$defs/value%7C"),
        ("compiled-id-pipe-escaped", "alias%7C.json", "#/$defs/value"),
        ("compiled-id-space-escaped", "alias%20space.json", "#/$defs/value"),
    ]:
        directory = output / label
        directory.mkdir()
        value = {"type": "object", "required": ["ok"], "properties": {"ok": {"const": True}}}
        event = {"properties": {"type": {"const": "com.example.compiled.event"}, "payload": {"$ref": reference}}}
        document = {"$defs": {"event": event, "value": value, "value|": value}}
        if identifier:
            document["$id"] = identifier
        (directory / "types.json").write_text(json.dumps(document))
        (directory / "pack.json").write_text(json.dumps({
            "id": "com.example.compiled", "version": "1", "schemas": ["types.json"],
            "envelope_types": [{"type": "com.example.compiled.event", "role": "event", "schema": "types.json#/$defs/event"}],
            "gates": [{"type": "com.example.compiled.event", "ungated": True}],
        }))
        for valid in [True, False]:
            trace = directory / ("valid.json" if valid else "invalid.json")
            trace.write_text(json.dumps([{
                "protocol": "open-agent-protocol", "version": "0.1",
                "profile": "open-agent-protocol.agent-control-core",
                "type": "com.example.compiled.event", "id": "synthetic", "payload": {"ok": valid},
            }]))
            cases.append((label + ("-valid" if valid else "-invalid"), trace, [directory], valid, None))
    directory = output / "escaped-member-pointer"
    directory.mkdir()
    member = "com.example.pointer.a/b~c"
    (directory / "types.json").write_text("{}")
    (directory / "pack.json").write_text(json.dumps({
        "id":"com.example.pointer", "version":"1", "schemas":["types.json"],
        "payload_members":[{"payload_type":"run.completed", "member":member, "schema":{"type":"object", "required":["ok"], "properties":{"ok":{"const":True}}}}],
        "gates":[{"payload_type":"run.completed", "member":member, "ungated":True}],
    }))
    for valid in [True, False]:
        trace = json.loads(core.read_text())
        terminal = next(envelope for envelope in trace if envelope["type"] == "run.completed")
        terminal["payload"][member] = {"ok":valid}
        path = directory / ("valid.json" if valid else "invalid.json")
        path.write_text(json.dumps(trace))
        cases.append(("escaped-member-pointer-" + str(valid).lower(), path, [directory], valid, None))
    descriptor = json.loads((directory / "pack.json").read_text())
    second = "com.example.pointer.ab"
    descriptor["payload_members"].append(dict(descriptor["payload_members"][0], member=second))
    descriptor["gates"].append({"payload_type":"run.completed", "member":second, "ungated":True})
    (directory / "pack.json").write_text(json.dumps(descriptor))
    terminal["payload"][second] = {"ok":False}
    path = directory / "two-invalid-members.json"
    path.write_text(json.dumps(trace))
    cases.append(("escaped-member-order", path, [directory], False, None))
    results = []
    diagnostic_differences = []
    for label, trace, directories, valid, codes in cases:
        rust = run([str(consumer), str(trace), *map(str, directories)], label + ".rust")
        data = json.loads(rust.stdout)
        args = [str(goap), "validate", "--format", "json"]
        for directory in directories:
            args += ["--pack", str(directory)]
        go = run(args + [str(trace)], label + ".go")
        assert rust.returncode == go.returncode == int(not valid), (label, data, go.stdout, go.stderr)
        if codes is not None:
            go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
            assert data["codes"] == go_codes == codes, (label, data, go.stderr)
        else:
            reports = json.loads(go.stdout)
            assert len(reports) == 1 and reports[0]["valid"] == valid, (label, reports)
            assert data["stage"] == "compiled-schemas" and data["schema_valid"] == valid, (label, data)
            go_diagnostics = reports[0].get("diagnostics") or []
            expected = {(d.get("index", 0), d.get("pointer", ""), d["code"]) for d in go_diagnostics}
            actual = {(d["index"], d["diagnostic"].get("pointer", ""), d["diagnostic"]["code"]) for d in data["diagnostics"]}
            assert {d[0] for d in actual} == {d[0] for d in expected}, (label, data, go_diagnostics)
            if actual != expected:
                diagnostic_differences.append({"id":label, "rust":data["diagnostics"], "go":go_diagnostics})
            if label == "escaped-member-order":
                assert actual and all(d[1] == "/payload/com.example.pointer.ab/ok" for d in actual), actual
            if label == "escaped-member-pointer-false":
                assert any(d[1] == "/payload/com.example.pointer.a~1b~0c/ok" for d in actual), actual
        results.append({"id":label, "valid":valid, "codes":codes, "rust":data})
    (output / "diagnostic-differences.json").write_text(json.dumps(diagnostic_differences, indent=2) + "\n")
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    assert not diagnostic_differences, diagnostic_differences
    print(f"PASS: {len(results)} public compiled-schema/load cases agree with Go")
    print("PASS: primary diagnostic pointers agree; diagnostic-differences.json is empty")
    print("SCHEMA: parsed-envelope operation verified; trace semantics are checked by rust-packed-traces.py and rust-tolerant.py")


if __name__ == "__main__":
    main()
