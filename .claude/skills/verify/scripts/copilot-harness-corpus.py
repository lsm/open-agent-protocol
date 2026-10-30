#!/usr/bin/env python3
"""Replay token-free Copilot observations in Zig, then validate their OAP trace."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--oapx", default=str(ROOT / "zig/zig-out/bin/oapx"))
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    result = subprocess.run(["zig", "build", "--build-file", "zig/build.zig", "test-copilot-corpus",
                             "-Doptimize=ReleaseSafe"], cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / "zig-replay.txt").write_text(result.stdout + result.stderr)
    assert result.returncode == 0, result.stdout + result.stderr
    decoded = subprocess.run(["go", "test", "-race", "-count=1", "./go/adapter/copilot/..."],
                             cwd=ROOT, capture_output=True, text=True, timeout=120)
    (output / "go-native.txt").write_text(decoded.stdout + decoded.stderr)
    assert decoded.returncode == 0, decoded.stdout + decoded.stderr
    pin = next(v for v in json.loads((ROOT / "harnesses/copilot-cli.json").read_text())["versions"]
               if v["status"] == "current")
    corpus = ROOT / pin["corpus"]
    manifest = json.loads((corpus / "manifest.json").read_text())
    for case in manifest["cases"]:
        directory = corpus / case["path"]
        metadata = json.loads((directory / "case.json").read_text())
        events = json.loads((directory / "expected-oap.json").read_text())
        if metadata.get("codec_only"):
            assert events is None, case["id"]
            print("Zig codec rejection", case["id"], "passed", flush=True)
            continue
        prefix = json.loads((directory / metadata["prefix_oap"]).read_text())
        trace = output / (case["id"] + ".json")
        trace.write_text(json.dumps(prefix + events, indent=2) + "\n")
        for label, command in [("go", ["go", "run", "./go/cmd/goap", "validate", str(trace)]),
                               ("zig", [args.oapx, "validate", str(trace)])]:
            checked = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=60)
            (output / (case["id"] + "-" + label + ".txt")).write_text(checked.stdout + checked.stderr)
            assert checked.returncode == 0, checked.stdout + checked.stderr
            print(label, case["id"], "exit", checked.returncode, flush=True)
    rust = subprocess.run(["cargo", "test", "--manifest-path", "rust/Cargo.toml",
                           "-p", "oap-adapter-copilot", "--all-features"],
                          cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / "rust-native.txt").write_text(rust.stdout + rust.stderr)
    assert rust.returncode == 0, rust.stdout + rust.stderr
    print("PASS: Zig/Go replay and both public validators accepted the synthetic corpus; Rust consumed the shared native wire; synthetic peers only")


if __name__ == "__main__":
    main()
