#!/usr/bin/env python3
"""Observe URI parsing through the three public consumers with escaped controls."""
import argparse
import json
from pathlib import Path
import re
import runpy

ROOT = Path(__file__).resolve().parents[4]
RESOURCES = runpy.run_path(str(Path(__file__).with_name("rust-pack-resources.py")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    helpers = runpy.run_path(str(Path(__file__).with_name("rust-pack-declarations.py")))
    def run(command, label):
        return helpers["run"](command, output, label)
    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="pack-uri-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(RESOURCES["CONSUMER"])
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"), "--target-dir", str(ROOT / "rust/target")], "consumer-build")
    assert build.returncode == 0, build.stderr
    goap = output / "goap"
    build = run(["go", "build", "-o", str(goap), "./go/cmd/goap"], "go-build")
    assert build.returncode == 0, build.stderr
    build = run(["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"], "zig-build")
    assert build.returncode == 0, build.stderr
    results = []
    for label, identifier, reference in [
        ("fragment-pipe", "", "#/$defs/value|"),
        ("fragment-escaped", "", "#/$defs/value%7C"),
        ("id-pipe", "alias|.json", "#/$defs/value"),
        ("id-pipe-escaped", "alias%7C.json", "#/$defs/value"),
        ("id-space", "alias space.json", "#/$defs/value"),
        ("id-space-escaped", "alias%20space.json", "#/$defs/value"),
    ]:
        directory = output / label
        directory.mkdir()
        event = {
            "type": "object",
            "properties": {"type": {"const": "com.example.compat.event"}, "payload": {"$ref": reference}},
        }
        document = {
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "$defs": {"event": event, "value|": {"type": "object"}, "value": {"type": "object"}},
        }
        if identifier:
            document["$id"] = identifier
        (directory / "types.json").write_text(json.dumps(document))
        descriptor = {
            "id": "com.example.compat",
            "version": "1",
            "schemas": ["types.json"],
            "envelope_types": [{"type": "com.example.compat.event", "role": "event", "schema": "types.json#/$defs/event"}],
            "gates": [{"type": "com.example.compat.event", "ungated": True}],
        }
        (directory / "pack.json").write_text(json.dumps(descriptor))
        rust = run([str(ROOT / "rust/target/debug/pack-uri-consumer"), str(directory)], label + ".rust")
        go = run([str(goap), "validate", "--pack", str(directory), "--format", "json", str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
        zig = run([str(ROOT / "zig/zig-out/bin/oxap"), "validate", "--pack", str(directory), "--format", "json", str(ROOT / "fixtures/valid/core-completed.json")], label + ".zig")
        rust_data = json.loads(rust.stdout)
        go_reports = json.loads(go.stdout)
        assert len(go_reports) == 1 and go_reports[0]["valid"], (label, go.stdout)
        if rust.returncode == 0:
            assert rust_data["stage"] == "resources" and rust_data["ids"] == ["com.example.compat"], rust_data
            assert len(rust_data["documents"]) == 1, rust_data
        results.append({
            "case": label,
            "rust_exit": rust.returncode,
            "rust_codes": rust_data.get("codes", []),
            "rust_messages": [r["message"] for r in rust_data.get("refusals", [])],
            "go_exit": go.returncode,
            "go_codes": sorted(set(re.findall(r"\bpack_[a-z_]+\b", go.stderr))),
            "zig_exit": zig.returncode,
            "zig_reports": json.loads(zig.stdout),
        })
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    for result in results:
        escaped = result["case"].endswith("-escaped")
        assert result["go_exit"] == 0 and result["go_codes"] == [], result
        assert len(result["zig_reports"]) == 1, result
        if result["case"] == "fragment-escaped":
            assert result["zig_exit"] == 1, result
            assert any(r.get("unjudged") == "the schema interpreter cannot judge this trace" for r in result["zig_reports"]), result
            label = "DOCUMENTED ZIG UNJUDGED "
        else:
            assert result["zig_exit"] == 0 and all(r["valid"] for r in result["zig_reports"]), result
            label = "AGREE " if escaped else "DOCUMENTED RUST DIFFERENCE "
        assert all("Ok(" not in message and "Err(" not in message for message in result["rust_messages"]), result
        assert result["rust_exit"] == (0 if escaped else 1), result
        assert result["rust_codes"] == ([] if escaped else ["pack_external_ref"]), result
        print(label + result["case"])
    print("PASS: three Rust URI differences and one Zig unjudged control pinned. Two escaped ID controls agree")


if __name__ == "__main__":
    main()
