#!/usr/bin/env python3
"""Replay redelivery wire fixtures, or mutation-check schema and typed preservation."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / f"{label}.stdout").write_text(result.stdout)
    (output / f"{label}.stderr").write_text(result.stderr)
    return result


def mutations(output):
    schema = ROOT / "schema/v0.1/common.schema.json"
    go = ROOT / "go/protocol/common.go"
    rust = ROOT / "rust/protocol/src/content.rs"
    originals = {p: p.read_text() for p in [schema, go, rust]}
    go_command = ["go", "test", "./go/validation", "-run", "^TestManifest$", "-count=1"]
    go_types = ["go", "test", "./go/protocol", "-run", "^TestRecoveryRedeliveriesSurviveTypedRoundTrip$", "-count=1"]
    rust_types = ["cargo", "test", "--manifest-path", "rust/Cargo.toml", "-p", "oap-protocol", "--test", "payloads"]
    assert run(["npm", "--prefix", "clients/ts", "run", "build"], output, "baseline-ts-build").returncode == 0
    ts_mirror = ["node", "--test", "clients/ts/dist/test/schema.test.js"]
    assert run(ts_mirror, output, "baseline-ts-mirror").returncode == 0
    for label, command in [("baseline-schema", go_command), ("baseline-go-types", go_types), ("baseline-rust-types", rust_types)]:
        assert run(command, output, label).returncode == 0, label
    edits = [
        ("omit-field", lambda d: d["recovery"]["properties"].pop("redelivered_runs")),
        ("omit-previous", lambda d: d["redeliveredRun"]["required"].remove("previous_run_id")),
        ("omit-root", lambda d: d["redeliveredRun"]["required"].remove("root_run_id")),
        ("omit-new-run", lambda d: d["redeliveredRun"]["oneOf"][0].pop("required")),
        ("allow-withdrawn-run", lambda d: d["redeliveredRun"]["oneOf"][1].pop("not")),
        ("allow-empty-run", lambda d: d["redeliveredRun"]["properties"].update(run_id={"type": "string"})),
    ]
    try:
        for label, edit in edits:
            document = json.loads(originals[schema])
            edit(document["$defs"])
            schema.write_text(json.dumps(document))
            result = run(go_command, output, label)
            assert result.returncode != 0 and "FAIL: TestManifest" in result.stdout, label
            print(f"KILLED {label}", flush=True)
            schema.write_text(originals[schema])
        line = next(l for l in originals[go].splitlines(True) if "RedeliveredRuns" in l)
        go.write_text(originals[go].replace(line, "", 1))
        result = run(go_types, output, "drop-go-field")
        assert result.returncode != 0 and "FAIL: TestRecovery" in result.stdout
        print("KILLED drop-go-field", flush=True)
        go.write_text(originals[go])
        field = '    #[serde(skip_serializing_if = "Vec::is_empty")]\n    pub redelivered_runs: Vec<RedeliveredRun>,\n'
        assert originals[rust].count(field) == 1
        rust.write_text(originals[rust].replace(field, "", 1))
        result = run(rust_types, output, "drop-rust-field")
        assert result.returncode != 0 and "test result: FAILED" in result.stdout
        print("KILLED drop-rust-field", flush=True)
        rust.write_text(originals[rust])
        document = json.loads(originals[schema])
        document["$defs"]["recovery"]["properties"].pop("redelivered_runs")
        schema.write_text(json.dumps(document))
        result = run(ts_mirror, output, "drop-ts-mirror-field")
        assert result.returncode != 0 and "RecoveryMetadata: interface field redelivered_runs absent" in result.stdout
        print("KILLED drop-ts-mirror-field", flush=True)
    finally:
        for path, original in originals.items():
            path.write_text(original)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    if args.mutation_check:
        mutations(output)
        return
    commands = {
        "go": ["go", "run", "./go/cmd/goap", "validate", "--format", "json"],
        "rust": ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate", "--"],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())
    for fixture in manifest["fixtures"]:
        if fixture["id"] != "session-reopen-reports-redelivered-runs" and not fixture["id"].startswith("redelivery-"):
            continue
        for language, command in commands.items():
            result = run(command + [str(ROOT / "fixtures" / fixture["path"])], output, fixture["id"] + "." + language)
            data = json.loads(result.stdout)
            reports = data if isinstance(data, list) else data.get("reports", [data])
            diagnostics = [d for r in reports for d in r.get("diagnostics") or []]
            found = sorted({d["code"] for d in diagnostics})
            print(f"{fixture['id']}.{language}: exit {result.returncode}, codes {found}", flush=True)
            assert result.returncode == int(not fixture["valid"]), result.stderr
            assert found == sorted(set(fixture["codes"])), diagnostics
            assert all(d["phase"] == fixture["phase"] for d in diagnostics), diagnostics
    print("PASS: redelivery wire fixtures agree; stateful redelivery semantics remain unverified")


if __name__ == "__main__":
    main()
