#!/usr/bin/env python3
"""Compare public Rust/Go compaction and live-settings diagnostic payloads."""
import argparse
import json
from pathlib import Path
import runpy
import subprocess

ROOT = Path(__file__).resolve().parents[4]
FIELDS = ("phase", "code", "envelope_id", "pointer", "expected", "actual")


def shape(report):
    return sorted(tuple(d.get(field, "") for field in FIELDS)
                  + (tuple(d.get("related_ids", [])),)
                  for d in report.get("diagnostics") or [])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    parity = runpy.run_path(str(Path(__file__).with_name("rust-go-parity.py")))
    goap, rust = parity["build"](output)
    fixtures = [f for f in json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
                if f["id"].startswith(("compaction-", "session-settings-live-"))]
    results = []
    for fixture in fixtures:
        path = str(ROOT / "fixtures" / fixture["path"])
        reports = []
        for language, command in (("go", [str(goap), "validate", "--format", "json"]),
                                  ("rust", [str(rust)])):
            run = subprocess.run(command + [path], cwd=ROOT, capture_output=True, text=True, timeout=60)
            (output / f"{fixture['id']}.{language}.json").write_text(run.stdout + run.stderr)
            data = json.loads(run.stdout)
            report = data[0] if language == "go" else data
            assert run.returncode == int(not fixture["valid"]), (fixture["id"], language, run)
            assert sorted({d["code"] for d in report.get("diagnostics") or []}) == sorted(set(fixture["codes"])), (fixture["id"], language, report)
            reports.append(shape(report))
        results.append({"id": fixture["id"], "go": reports[0], "rust": reports[1]})
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    wrong = [r for r in results if r["go"] != r["rust"]]
    assert not wrong, wrong
    print(f"PASS: {len(results)} public Rust/Go traces agree on diagnostics and expected/actual/related_ids")


if __name__ == "__main__":
    main()
