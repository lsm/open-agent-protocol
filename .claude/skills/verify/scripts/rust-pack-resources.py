#!/usr/bin/env python3
"""Exercise staged member declarations, contained resources and dependencies."""
import argparse
import json
from pathlib import Path
import re
import runpy

ROOT = Path(__file__).resolve().parents[4]
PREVIOUS = runpy.run_path(str(Path(__file__).with_name("rust-pack-declarations.py")))
CODES = PREVIOUS["CODES"] | {
    "pack_member_duplicate", "pack_member_target_unknown", "pack_restates_core_member",
    "pack_schema_path_escape", "pack_dependency_missing",
}
CONSUMER = '''use std::path::PathBuf;
use std::process::ExitCode;
use oap_validation::pack::PackLoader;
fn main() -> ExitCode {
    let paths: Vec<PathBuf> = std::env::args_os().skip(1).map(PathBuf::from).collect();
    match PackLoader::read(&paths).and_then(PackLoader::read_resources) {
        Ok(resources) => {
            let ids: Vec<_> = resources.descriptors().map(|(_, d)| d["id"].as_str().unwrap()).collect();
            let documents: Vec<_> = resources.documents().keys().collect();
            println!("{}", serde_json::json!({"stage":"resources", "ids":ids, "documents":documents, "manifests":resources.manifests().len()}));
            ExitCode::SUCCESS
        }
        Err(error) => {
            println!("{}", serde_json::json!({"stage":"resources", "codes":error.codes(), "refusals":error.refusals}));
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
    run = lambda command, label: PREVIOUS["run"](command, output, label)
    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="pack-resource-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(CONSUMER)
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"), "--target-dir", str(ROOT / "rust/target")], "consumer-build")
    assert build.returncode == 0, build.stderr
    goap = output / "goap"
    build = run(["go", "build", "-o", str(goap), "./go/cmd/goap"], "go-build")
    assert build.returncode == 0, build.stderr
    consumer = ROOT / "rust/target/debug/pack-resource-consumer"
    fixtures = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    cases = [(f["id"], [ROOT / "fixtures" / p for p in [f["path"], *f.get("packs", [])]], f["codes"])
             for f in fixtures if f["kind"] == "load-invalid" and set(f["codes"]) <= CODES]
    base = {"id": "com.example.resources", "version": "1", "schemas": ["types.json"]}
    outside = output / "outside.json"
    outside.write_text("{}")
    for label, schemas, manifest, link, codes in [
        ("missing-schema", ["missing.json"], "", "", ["pack_schema_path_escape"]),
        ("schema-link-escape", ["linked.json"], "", "outside", ["pack_schema_path_escape"]),
        ("manifest-link-escape", ["types.json"], "linked.json", "outside", ["pack_schema_path_escape"]),
        ("contained-link", ["linked.json"], "", "inside", []),
        ("normalized-parent", ["nested/../types.json"], "", "", []),
    ]:
        directory = output / label
        directory.mkdir()
        (directory / "types.json").write_text("{}")
        if link:
            (directory / "linked.json").symlink_to(outside if link == "outside" else directory / "types.json")
        descriptor = dict(base, schemas=schemas)
        if manifest:
            descriptor["fixtures"] = manifest
        (directory / "pack.json").write_text(json.dumps(descriptor))
        cases.append((label, [directory], codes))
    for label, mixed in [("member-gate-both-modes", False), ("member-gate-also-names-type", True)]:
        directory = output / label
        directory.mkdir()
        (directory / "types.json").write_text("{}")
        member = "com.example.resources.extra"
        gate = {"payload_type": "run.completed", "member": member, "ungated": True}
        descriptor = dict(base, payload_members=[{"payload_type": "run.completed", "member": member, "schema": {}}])
        if mixed:
            gate["type"] = "com.example.resources.event"
            descriptor["envelope_types"] = [{"type": gate["type"], "role": "event", "schema": "types.json"}]
        else:
            gate["capability"] = "com.example.resources.feature"
            descriptor["capability_keys"] = [gate["capability"]]
        descriptor["gates"] = [gate]
        (directory / "pack.json").write_text(json.dumps(descriptor))
        cases.append((label, [directory], ["pack_ungated_type"]))
    for label, version_error in [("combined-declaration-refusals", False), ("dependency-version-mismatch", True)]:
        paths = []
        for index, namespace in enumerate(["com.example.alpha", "com.other.beta"]):
            directory = output / f"{label}-{index}"
            directory.mkdir()
            (directory / "types.json").write_text("{}")
            descriptor = dict(base, id=namespace)
            if version_error and index == 1:
                descriptor["depends_on"] = [{"id": "com.example.alpha", "version": "2"}]
            elif not version_error and index == 0:
                member = {"payload_type": "run.completed", "member": namespace + ".extra", "schema": {}}
                descriptor["payload_members"] = [member, member]
                descriptor["gates"] = [{"payload_type": "run.completed", "member": member["member"], "ungated": True}]
            elif not version_error:
                descriptor["capability_keys"] = ["storage.objects"]
            (directory / "pack.json").write_text(json.dumps(descriptor))
            paths.append(directory)
        codes = ["pack_dependency_missing"] if version_error else ["pack_member_duplicate", "pack_unprefixed_name"]
        cases.append((label, paths, codes))
    results = []
    for label, paths, codes in cases:
        rust = run([str(consumer), *map(str, paths)], label + ".rust")
        data = json.loads(rust.stdout)
        args = [str(goap), "validate", "--format", "json"]
        for path in paths:
            args += ["--pack", str(path)]
        go = run(args + [str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
        go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
        assert rust.returncode == go.returncode == int(bool(codes)), (label, data, go.stderr)
        assert data.get("codes", []) == go_codes == sorted(set(codes)), (label, data, go.stderr)
        if not codes:
            assert len(data["documents"]) == 1, data
        results.append({"id": label, "codes": codes})
    positive = run([str(consumer), str(ROOT / "fixtures/packs/storage"), str(ROOT / "fixtures/packs/index")], "shared-positive")
    data = json.loads(positive.stdout)
    assert positive.returncode == 0 and len(data["documents"]) == 2 and data["manifests"] == 1, data
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"PASS: {len(results)} public Rust/Go cases agree, with shared positive resources retained")
    print("STAGED: reference ownership, branch compilation and corpus checks follow in loader stage three")


if __name__ == "__main__":
    main()
