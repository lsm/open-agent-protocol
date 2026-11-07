#!/usr/bin/env python3
"""Exercise the explicitly staged Rust descriptor API from an external consumer."""
import argparse
import html
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[4]
CODES = {
    "pack_unprefixed_name", "pack_foreign_prefix", "pack_id_collision", "pack_type_duplicate",
    "pack_role_undeclared", "pack_reply_target_unknown", "pack_reply_target_ambiguous",
    "pack_refusal_undeclared", "pack_response_gated", "pack_ungated_type",
}
CONSUMER = '''use std::path::PathBuf;
use std::process::ExitCode;
use oap_validation::pack::PackLoader;
fn main() -> ExitCode {
    let paths: Vec<PathBuf> = std::env::args_os().skip(1).map(PathBuf::from).collect();
    match PackLoader::read(&paths) {
        Ok(loader) => {
            let ids: Vec<_> = loader.descriptors().map(|(_, d)| d["id"].as_str().unwrap()).collect();
            println!("{}", serde_json::json!({"stage":"declarations", "ids":ids}));
            ExitCode::SUCCESS
        }
        Err(error) => {
            println!("{}", serde_json::json!({"stage":"declarations", "codes":error.codes(), "refusals":error.refusals}));
            ExitCode::FAILURE
        }
    }
}
'''


def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=900)
    (output / f"{label}.stdout").write_text(result.stdout)
    (output / f"{label}.stderr").write_text(result.stderr)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="pack-stage-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(CONSUMER)
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"),
                 "--target-dir", str(ROOT / "rust/target")], output, "consumer-build")
    assert build.returncode == 0, build.stderr
    documentation = run(["cargo", "doc", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--no-deps"], output, "docs-build")
    assert documentation.returncode == 0, documentation.stderr
    page = (ROOT / "rust/target/doc/oap_validation/pack/struct.PackLoader.html").read_text()
    (output / "pack-loader.html").write_text(page)
    words = html.unescape(re.sub(r"<[^>]+>", "", page))
    assert "Staged pack-loader construction" in words and "six loader slices" in words, words
    goap = output / "goap"
    build = run(["go", "build", "-o", str(goap), "./go/cmd/goap"], output, "go-build")
    assert build.returncode == 0, build.stderr
    consumer = ROOT / "rust/target/debug/pack-stage-consumer"
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    cases = [{"id": f["id"], "codes": f["codes"],
              "directories": [ROOT / "fixtures" / p for p in [f["path"], *f.get("packs", [])]]}
             for f in manifest if f["kind"] == "load-invalid" and set(f["codes"]) <= CODES]
    name = "com.example.stage.ping"
    branch = {"type": name, "role": "event", "schema": "types.json#/$defs/ping"}
    gate = {"type": name, "ungated": True}
    base = {"id": "com.example.stage", "version": "1", "schemas": ["types.json"],
            "envelope_types": [branch], "gates": [gate]}
    malformed = [
        ("unknown-field", {"unknown": True}, []),
        ("invalid-role", {"envelope_types": [dict(branch, role="other")]}, []),
        ("missing-branch", {"envelope_types": [{"type": name, "role": "event"}]}, []),
        ("missing-gate-mode", {"gates": [{"type": name}]}, ["pack_ungated_type"]),
        ("both-gate-modes", {"gates": [dict(gate, capability="com.example.stage.key")]}, ["pack_ungated_type"]),
        ("duplicate-gate", {"gates": [gate, gate]}, []),
        ("undeclared-gate-target", {"gates": [gate, {"type": name + ".other", "ungated": True}]}, []),
        ("undeclared-gate-key", {"gates": [{"type": name, "capability": "com.example.stage.key"}]}, ["pack_ungated_type"]),
        ("core-root-key", {"capability_keys": ["session.open.feature"]}, ["pack_unprefixed_name"]),
        ("two-label-run-root", {"capability_keys": ["run.started.extension"]}, ["pack_unprefixed_name"]),
        ("two-label-content-root", {"capability_keys": ["content.delta.extension"]}, ["pack_unprefixed_name"]),
        ("foreign-key", {"capability_keys": ["org.example.feature"]}, ["pack_foreign_prefix"]),
    ]
    for label, patch, codes in malformed:
        directory = output / label
        directory.mkdir()
        (directory / "pack.json").write_text(json.dumps(dict(base, **patch)))
        cases.append({"id": label, "codes": codes, "directories": [directory]})
    results = []
    for fixture in cases:
        paths = fixture["directories"]
        rust = run([str(consumer), *map(str, paths)], output, fixture["id"] + ".rust")
        data = json.loads(rust.stdout)
        args = [str(goap), "validate", "--format", "json"]
        for path in paths:
            args += ["--pack", str(path)]
        go = run(args + [str(ROOT / "fixtures/valid/core-completed.json")], output, fixture["id"] + ".go")
        go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
        assert rust.returncode == go.returncode == 1, fixture["id"]
        assert data["stage"] == "declarations", data
        assert data["codes"] == go_codes == sorted(set(fixture["codes"])), (fixture["id"], data, go.stderr)
        results.append({"id": fixture["id"], "codes": data["codes"]})
    control = run([str(consumer), str(ROOT / "fixtures/packs/storage"),
                   str(ROOT / "fixtures/packs/index")], output, "accepted-descriptors")
    data = json.loads(control.stdout)
    assert control.returncode == 0 and len(data["ids"]) == 2, data
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"PASS: {len(results)} declaration refusals (12 shared, 12 synthetic) agree with Go; two descriptors read by an external Rust consumer")
    print("PASS: generated public API documentation exposes the staged contract")
    print("STAGED: schema documents, dependencies, references, branches and corpora are not loaded by this API")


if __name__ == "__main__":
    main()
