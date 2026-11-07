#!/usr/bin/env python3
"""Observe strict Rust and lenient Go URI parsing with escaped controls."""
import argparse
import json
from pathlib import Path
import re
import runpy

ROOT = Path(__file__).resolve().parents[4]
PREVIOUS = runpy.run_path(str(Path(__file__).with_name("rust-pack-resources.py")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    run = lambda command, label: PREVIOUS["PREVIOUS"]["run"](command, output, label)
    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="pack-uri-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(PREVIOUS["CONSUMER"])
    build = run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"), "--target-dir", str(ROOT / "rust/target")], "consumer-build")
    assert build.returncode == 0, build.stderr
    goap = output / "goap"
    build = run(["go", "build", "-o", str(goap), "./go/cmd/goap"], "go-build")
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
        event = {"type": "object", "properties": {"type": {"const": "com.example.compat.event"}, "payload": {"$ref": reference}}}
        document = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": {"event": event, "value|": {"type": "object"}, "value": {"type": "object"}}}
        if identifier:
            document["$id"] = identifier
        (directory / "types.json").write_text(json.dumps(document))
        descriptor = {"id": "com.example.compat", "version": "1", "schemas": ["types.json"], "envelope_types": [{"type": "com.example.compat.event", "role": "event", "schema": "types.json#/$defs/event"}], "gates": [{"type": "com.example.compat.event", "ungated": True}]}
        (directory / "pack.json").write_text(json.dumps(descriptor))
        rust = run([str(ROOT / "rust/target/debug/pack-uri-consumer"), str(directory)], label + ".rust")
        go = run([str(goap), "validate", "--pack", str(directory), "--format", "json", str(ROOT / "fixtures/valid/core-completed.json")], label + ".go")
        results.append({"case": label, "rust_exit": rust.returncode, "rust_codes": json.loads(rust.stdout).get("codes", []), "go_exit": go.returncode, "go_codes": sorted(set(re.findall(r"\bpack_[a-z_]+\b", go.stderr)))})
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    for result in results:
        assert result["rust_exit"] == result["go_exit"] == 0, result
        assert result["rust_codes"] == result["go_codes"] == [], result
    print("PASS: URI parsing cases agree")


if __name__ == "__main__":
    main()
