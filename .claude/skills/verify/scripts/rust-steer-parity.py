#!/usr/bin/env python3
"""Replay every steer fixture through the Rust and Go validator CLIs and compare codes."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
RUST = ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
        "-p", "oap-validation", "--example", "validate", "--"]
GO = ["go", "run", "./go/cmd/goap", "validate", "--format", "json"]


def codes(command, path, output, label):
    result = subprocess.run(command + [str(path)], cwd=ROOT, capture_output=True,
                            text=True, timeout=300)
    (output / f"{label}.json").write_text(result.stdout)
    (output / f"{label}.stderr").write_text(result.stderr)
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else data.get("reports", [data])
    found = sorted({d["code"] for report in reports for d in report.get("diagnostics") or []})
    assert result.returncode == int(bool(found)), (label, result.returncode, found)
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())
    gaps = set(json.loads((ROOT / "fixtures/known-gaps.json").read_text())["rust"])
    agreed = skipped = 0
    for fixture in manifest["fixtures"]:
        if "steer" not in fixture["id"]:
            continue
        if fixture["id"] in gaps:
            skipped += 1
            continue
        path = ROOT / "fixtures" / fixture["path"]
        rust = codes(RUST, path, output, f"{fixture['id']}.rust")
        go = codes(GO, path, output, f"{fixture['id']}.go")
        print(f"{fixture['id']}: rust {rust} go {go}", flush=True)
        assert rust == go == sorted(fixture.get("codes", [])), fixture["id"]
        agreed += 1
    print(f"{agreed} steer fixtures agree; {skipped} listed as Rust gaps", flush=True)
    assert agreed > 0


if __name__ == "__main__":
    main()
