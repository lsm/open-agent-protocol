#!/usr/bin/env python3
"""Compare the Rust and Go validator CLIs diagnostic by diagnostic over the whole manifest."""
import argparse
import collections
import json
import runpy
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
PACKED = runpy.run_path(str(Path(__file__).with_name("rust-packed-traces.py")))


def build(output):
    goap = output / "goap"
    subprocess.run(["go", "build", "-o", str(goap), "./go/cmd/goap"], cwd=ROOT, check=True,
                   timeout=600)
    return goap, PACKED["build_native"](output)



def shape(diagnostics, packed=False):
    if packed:
        return sorted((d["phase"], d["code"], (d.get("index", 0), d.get("envelope_id", "")), d.get("pointer", "")) for d in diagnostics or [])
    return sorted((d["phase"], d["code"], d.get("envelope_id", ""), d.get("pointer", ""))
                  for d in diagnostics or [])


def judge(goap, rust, fixtures, provider, packs, mode, output, label):
    paths = [str(ROOT / "fixtures" / f["path"]) for f in fixtures]
    go_args = [str(goap), "validate", "--format", "json", "--mode", mode]
    rust_args = [str(rust)]
    if mode == "tolerant":
        rust_args.append("--tolerant")
    if provider:
        go_args.append("--provider")
        rust_args.append("--provider")
    for pack in packs:
        go_args += ["--pack", str(ROOT / "fixtures" / pack)]
        rust_args += ["--pack", str(ROOT / "fixtures" / pack)]
    go = subprocess.run(go_args + paths, cwd=ROOT, capture_output=True, text=True, timeout=600)
    rs = subprocess.run(rust_args + paths, cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / f"{label}.go.json").write_text(go.stdout + go.stderr)
    (output / f"{label}.rust.json").write_text(rs.stdout + rs.stderr)
    go_reports = json.loads(go.stdout)
    rust_reports = [json.loads(line) for line in rs.stdout.splitlines() if line.strip()]
    assert len(go_reports) == len(rust_reports) == len(fixtures), label
    assert all("diagnostics" in report for report in rust_reports), (label, rust_reports)
    expected_exit = int(any(report.get("diagnostics") for report in rust_reports))
    assert go.returncode == rs.returncode == expected_exit, (label, go.stderr, rs.stderr)
    return [(f, shape(g.get("diagnostics"), bool(packs)), shape(r.get("diagnostics"), bool(packs)))
            for f, g, r in zip(fixtures, go_reports, rust_reports)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    goap, rust = build(output)
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    gaps = set(json.loads((ROOT / "fixtures/known-gaps.json").read_text())["rust"])
    locations = json.loads(Path(__file__).with_name("rust-location-gaps.json").read_text())
    located = set(locations["rust"])
    go_pointers = set(locations.get("go", []))
    groups = collections.defaultdict(list)
    skipped = collections.Counter()
    load_fixtures = []
    for fixture in manifest:
        if fixture["kind"] == "load-invalid":
            load_fixtures.append(fixture)
        elif fixture["id"] in gaps:
            skipped["known gap"] += 1
        else:
            key = (fixture.get("profile") == "model-provider-core",
                   tuple(fixture.get("packs", [])), fixture.get("mode", "strict"))
            groups[key].append(fixture)
    disagreements = []
    listed_agreeing = []
    agreed = 0
    for fixture in load_fixtures:
        PACKED["load_refusal"](goap, rust, fixture, output)
        agreed += 1
    for index, ((provider, packs, mode), fixtures) in enumerate(sorted(groups.items())):
        for fixture, go, rs in judge(goap, rust, fixtures, provider, packs, mode, output,
                                     f"group-{index}"):
            declared = sorted(set(fixture.get("codes", [])))
            codes_agree = sorted({code for _, code, _, _ in go}) == declared == sorted(
                {code for _, code, _, _ in rs})
            if go == rs and codes_agree:
                agreed += 1
                if fixture["id"] in located | go_pointers:
                    listed_agreeing.append(fixture["id"])
            elif codes_agree and fixture["id"] in located:
                agreed += 1
            elif fixture["id"] in go_pointers and codes_agree and [d[:3] for d in go] == [d[:3] for d in rs]:
                agreed += 1
            else:
                disagreements.append({"id": fixture["id"], "go": go, "rust": rs})
    (output / "disagreements.json").write_text(json.dumps(disagreements, indent=2))
    for item in disagreements:
        print(f"{item['id']}:\n  go   {item['go']}\n  rust {item['rust']}", flush=True)
    print(f"{agreed} fixtures agree ({len(located)} on codes only and {len(go_pointers)} Go pointer differences, as listed); "
          f"{len(disagreements)} disagree; skipped {dict(skipped)}", flush=True)
    assert not listed_agreeing, f"location gaps that now agree, delete them: {listed_agreeing}"
    assert not disagreements


if __name__ == "__main__":
    main()
