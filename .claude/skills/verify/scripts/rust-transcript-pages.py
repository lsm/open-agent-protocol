#!/usr/bin/env python3
"""Replay synthetic transcript response pages through the public validators."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/validation/src/state/transcript.rs"
OLD = {
    "transcript-load-over-limit", "transcript-load-over-the-default-limit",
    "transcript-load-duplicate-entry", "transcript-load-returns-an-entry-twice-out-of-order",
    "transcript-load-gap-without-cursor", "transcript-load-gap-elsewhere",
    "transcript-load-paged", "transcript-load-gap",
}

def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / f"{label}.stdout").write_text(result.stdout)
    (output / f"{label}.stderr").write_text(result.stderr)
    return result


def build(language, output, label):
    command = {
        "go": ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"],
        "rust": ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"],
        "zig": ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"],
    }[language]
    if run(command, output, label).returncode != 0:
        raise RuntimeError(f"{label}: build failed; compiler failures are not mutation catches")


def cases():
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    result = {}
    for fixture in manifest:
        name = fixture["id"]
        if name not in OLD and not name.startswith("transcript-page-"):
            continue
        trace = json.loads((ROOT / "fixtures" / fixture["path"]).read_text())
        result[name] = (trace, fixture["codes"])
        if fixture["valid"]:
            continue
        repaired = copy.deepcopy(trace)
        requests = {e["id"]: e["payload"] for e in repaired if e["type"] == "transcript.load.request"}
        for e in repaired:
            if e["type"] != "transcript.load.response":
                continue
            p, request = e["payload"], requests[e["in_reply_to"]]
            p["entries"] = list({entry["entry_id"]: entry for entry in p["entries"]}.values())
            if "limit" in request:
                request["limit"] = max(request["limit"], len(p["entries"]))
            else:
                p["entries"] = p["entries"][:100]
            if "gap" in p:
                if not request.get("cursor"):
                    del p["gap"]
                elif p["entries"]:
                    p["gap"]["resumed_at"] = p["entries"][0]["entry_id"]
                else:
                    p["gap"].pop("resumed_at", None)
        result["repair-" + name] = (repaired, [])
    base = copy.deepcopy(result["transcript-load-gap"][0])
    base[2]["payload"].pop("cursor")
    base[3]["payload"].pop("gap")
    entry = base[3]["payload"]["entries"][0]
    for count, limit in [(100, None), (101, 101), (500, 500), (0, 1)]:
        trace = copy.deepcopy(base)
        if limit is not None:
            trace[2]["payload"]["limit"] = limit
        trace[3]["payload"]["entries"] = [dict(copy.deepcopy(entry), entry_id=f"entry-{i}") for i in range(count)]
        result[f"boundary-{count}-limit-{limit}"] = (trace, [])
    return result

def replay(language, output, label, case):
    trace, codes = case
    path = output / f"{label}.input.json"
    path.write_text(json.dumps(trace, indent=2) + "\n")
    command = {
        "go": [str(output / "goap"), "validate", "--format", "json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }[language]
    result = run(command + [str(path)], output, label)
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else data.get("reports", [data])
    diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
    got = {"exit": result.returncode, "codes": sorted({d["code"] for d in diagnostics}),
           "phases": sorted({d["phase"] for d in diagnostics})}
    want = {"exit": int(bool(codes)), "codes": sorted(codes), "phases": ["semantic"] if codes else []}
    return got, want


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    corpus = cases()
    for language in ("go", "rust", "zig"):
        build(language, output, language + "-build")
    results, wrong = [], []
    for name, case in corpus.items():
        for language in ("go", "rust", "zig"):
            got, want = replay(language, output, language + "." + name, case)
            results.append({"case": name, "language": language, "got": got, "want": want})
            if got != want:
                wrong.append(f"{language}.{name}: got {got}, want {want}")
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    if wrong:
        raise RuntimeError("\n".join(wrong))
    print(f"PASS: {len(corpus)} cases × 3 public CLIs, 0 disagreements; artifacts: {output}")


if __name__ == "__main__":
    main()
