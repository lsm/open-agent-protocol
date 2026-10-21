#!/usr/bin/env python3
"""Replay every provider fixture through the Rust, Go and Zig validator CLIs."""
import argparse
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[4]
RUST = ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
        "-p", "oap-validation", "--example", "validate", "--", "--provider"]
GO = ["go", "run", "./go/cmd/goap", "validate", "--provider", "--format", "json"]
ZIG = ["./zig/zig-out/bin/oapx", "validate"]


def run(command, path, output, label):
    result = subprocess.run(command + [str(path)], cwd=ROOT, capture_output=True,
                            text=True, timeout=300)
    (output / f"{label}.out").write_text(result.stdout)
    (output / f"{label}.err").write_text(result.stderr)
    return result


def detail(result):
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else [data]
    found = sorted((d["phase"], d["code"], d.get("envelope_id", ""), d.get("pointer", ""))
                   for report in reports for d in report.get("diagnostics") or [])
    assert result.returncode == int(bool(found)), (result.returncode, found)
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())
    gaps = set(json.loads((ROOT / "fixtures/known-gaps.json").read_text())["rust"])
    agreed = 0
    for fixture in manifest["fixtures"]:
        if fixture.get("profile") != "model-provider-core":
            continue
        assert fixture["id"] not in gaps, fixture["id"]
        path = ROOT / "fixtures" / fixture["path"]
        rust = detail(run(RUST, path, output, f"{fixture['id']}.rust"))
        go = detail(run(GO, path, output, f"{fixture['id']}.go"))
        zig = run(ZIG, path, output, f"{fixture['id']}.zig")
        zig_codes = sorted(set(re.findall(r"\b(?:decode|schema|semantic) ([a-z_]+) at \d+",
                                          zig.stdout + zig.stderr)))
        declared = sorted(set(fixture.get("codes", [])))
        print(f"{fixture['id']}: {rust}", flush=True)
        assert rust == go, (fixture["id"], rust, go)
        assert sorted({code for _, code, _, _ in rust}) == declared, fixture["id"]
        assert zig.returncode == int(bool(declared)), (fixture["id"], zig.returncode)
        assert zig_codes == declared, (fixture["id"], zig_codes, declared)
        agreed += 1
    print(f"{agreed} provider fixtures agree across the Rust, Go and Zig CLIs", flush=True)
    assert agreed > 0


if __name__ == "__main__":
    main()
