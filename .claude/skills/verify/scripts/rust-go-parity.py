#!/usr/bin/env python3
"""Compare the Rust and Go validator CLIs diagnostic by diagnostic over the whole manifest."""
import argparse
import collections
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def build(output):
    goap = output / "goap"
    subprocess.run(["go", "build", "-o", str(goap), "./go/cmd/goap"], cwd=ROOT, check=True,
                   timeout=600)
    subprocess.run(["cargo", "build", "--quiet", "--manifest-path", "rust/Cargo.toml",
                    "-p", "oap-validation", "--example", "validate"], cwd=ROOT, check=True,
                   timeout=900)
    return goap, ROOT / "rust/target/debug/examples/validate"


def shape(diagnostics):
    return sorted((d["phase"], d["code"], d.get("envelope_id", ""), d.get("pointer", ""))
                  for d in diagnostics or [])


def judge(goap, rust, fixtures, provider, packs, mode, output, label):
    paths = [str(ROOT / "fixtures" / f["path"]) for f in fixtures]
    go_args = [str(goap), "validate", "--format", "json", "--mode", mode]
    rust_args = [str(rust)]
    if provider:
        go_args.append("--provider")
        rust_args.append("--provider")
    for pack in packs:
        go_args += ["--pack", str(ROOT / "fixtures" / pack)]
    go = subprocess.run(go_args + paths, cwd=ROOT, capture_output=True, text=True, timeout=600)
    rs = subprocess.run(rust_args + paths, cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / f"{label}.go.json").write_text(go.stdout + go.stderr)
    (output / f"{label}.rust.json").write_text(rs.stdout + rs.stderr)
    go_reports = json.loads(go.stdout)
    rust_reports = [json.loads(line) for line in rs.stdout.splitlines() if line.strip()]
    assert len(go_reports) == len(rust_reports) == len(fixtures), label
    return [(f, shape(g.get("diagnostics")), shape(r.get("diagnostics")))
            for f, g, r in zip(fixtures, go_reports, rust_reports)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    goap, rust = build(output)
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    gaps = set(json.loads((ROOT / "fixtures/known-gaps.json").read_text())["rust"])
    located = set(json.loads(Path(__file__).with_name("rust-location-gaps.json").read_text())["rust"])
    groups = collections.defaultdict(list)
    skipped = collections.Counter()
    for fixture in manifest:
        if fixture["kind"] == "load-invalid":
            skipped["load-invalid"] += 1
        elif fixture["id"] in gaps:
            skipped["known gap"] += 1
        else:
            key = (fixture.get("profile") == "model-provider-core",
                   tuple(fixture.get("packs", [])), fixture.get("mode", "strict"))
            groups[key].append(fixture)
    disagreements = []
    listed_agreeing = []
    agreed = 0
    for index, ((provider, packs, mode), fixtures) in enumerate(sorted(groups.items())):
        for fixture, go, rs in judge(goap, rust, fixtures, provider, packs, mode, output,
                                     f"group-{index}"):
            declared = sorted(set(fixture.get("codes", [])))
            codes_agree = sorted({code for _, code, _, _ in go}) == declared == sorted(
                {code for _, code, _, _ in rs})
            if go == rs and codes_agree:
                agreed += 1
                if fixture["id"] in located:
                    listed_agreeing.append(fixture["id"])
            elif codes_agree and fixture["id"] in located:
                agreed += 1
            else:
                disagreements.append({"id": fixture["id"], "go": go, "rust": rs})
    (output / "disagreements.json").write_text(json.dumps(disagreements, indent=2))
    for item in disagreements:
        print(f"{item['id']}:\n  go   {item['go']}\n  rust {item['rust']}", flush=True)
    print(f"{agreed} fixtures agree ({len(located)} on codes only, as listed); "
          f"{len(disagreements)} disagree; skipped {dict(skipped)}", flush=True)
    assert not listed_agreeing, f"location gaps that now agree, delete them: {listed_agreeing}"
    assert not disagreements


if __name__ == "__main__":
    main()
