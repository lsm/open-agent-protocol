#!/usr/bin/env python3
"""Compile/run public client consumers and compare scoped wire judgments."""
import argparse
import copy
import json
import os
from pathlib import Path
import signal
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def run(command, output, label, environment=None):
    child = subprocess.Popen(command, cwd=ROOT, env=environment, text=True,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    try:
        stdout, stderr = child.communicate(timeout=300)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        stdout, stderr = child.communicate()
        raise
    finally:
        (output / f"{label}.stdout").write_text(stdout)
        (output / f"{label}.stderr").write_text(stderr)
    return child.returncode, stdout, stderr


def consumers(output, expect_success=True):
    output.mkdir(parents=True, exist_ok=False)
    environment = dict(os.environ, OAP_TS_SCHEMA_PRECISION_OUTPUT=str(output / "consumer"))
    commands = [
        ["npm", "--prefix", "clients/ts", "run", "build"],
        ["node", "--test", "clients/ts/dist/test/schema-precision.test.js"],
    ]
    status = 0
    for index, command in enumerate(commands):
        status, stdout, stderr = run(command, output, f"step-{index}", environment)
        if status and (index == 0 or expect_success):
            raise RuntimeError(f"step {index} exited {status}: {stdout} {stderr}")
    results = json.loads((output / "consumer/results.json").read_text())
    if expect_success and (len(results) != 9 or any((row["exit"] == 0) != row["expected"] for row in results)):
        raise RuntimeError(f"unexpected consumer results: {results}")
    print(f"public consumer: exit {status}; results at {output}", flush=True)
    return status, results


def validators(output):
    goap = output / "goap"
    builds = [
        ["go", "build", "-o", str(goap), "./go/cmd/goap"],
        ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"],
        ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"],
    ]
    for index, command in enumerate(builds):
        status, stdout, stderr = run(command, output, f"validator-build-{index}")
        if status:
            raise RuntimeError(f"validator build exited {status}: {stdout} {stderr}")
    return {
        "go": [str(goap), "validate", "--format", "json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }


def judge(commands, trace, output, label, invalid_at=None):
    path = output / f"{label}.trace.json"
    path.write_text(json.dumps(trace, indent=2) + "\n")
    expected = [] if invalid_at is None else [("schema", "schema_invalid", invalid_at)]
    results = {}
    for language, command in commands.items():
        status, stdout, stderr = run(command + [str(path)], output, label + "." + language)
        parsed = json.loads(stdout)
        reports = parsed if isinstance(parsed, list) else parsed.get("reports", [parsed])
        found = sorted((d["phase"], d["code"], d.get("index", 0))
                       for report in reports for d in report.get("diagnostics") or [])
        results[language] = {"exit": status, "diagnostics": found}
        if status != int(invalid_at is not None) or found != expected:
            raise RuntimeError(f"{label}.{language}: expected {expected}; got {status} {found}: {stderr}")
    print(f"{label}: all three exit {int(invalid_at is not None)}, diagnostics {expected}", flush=True)
    return results


def wire_checks(commands, output, results):
    observed = {}
    positives = [row for row in results if row["name"].startswith("auth-") and row["expected"]]
    if len(positives) != 2:
        raise RuntimeError("missing executed auth controls")
    for row in positives:
        observed[row["name"]] = judge(commands, row["wire"], output, row["name"])
    original = positives[0]["wire"]
    for name, index, field, repair in [
        ("empty-kinds", 1, "auth_kinds", ["api_key"]),
        ("missing-url", 4, "url", "https://example.invalid/login"),
        ("missing-message", 5, "message", "Complete the synthetic flow"),
    ]:
        bad = copy.deepcopy(original)
        payload = bad[index]["payload"]
        if index == 1:
            payload = payload["providers"][0]
            payload[field] = []
        else:
            del payload[field]
        observed[name] = judge(commands, bad, output, name, index)
        payload[field] = repair
        observed[name + "-repaired"] = judge(commands, bad, output, name + "-repaired")
    return observed


def mutation_checks(commands, output):
    path = ROOT / "clients/ts/src/protocol.ts"
    original = path.read_text()
    cases = [
        ("auth-empty-kinds", "auth_kinds: [AuthKind, ...AuthKind[]];", "auth_kinds: AuthKind[];", 1),
        ("auth-url-without-url", "{ kind: 'url'; url: string }", "{ kind: 'url'; url?: string }", 4),
        ("auth-progress-without-message", "{ kind: 'progress'; message: string }", "{ kind: 'progress'; message?: string }", 5),
    ]
    observed = {}
    try:
        for name, before, after, invalid_at in cases:
            if original.count(before) != 1:
                raise RuntimeError(f"mutation anchor changed: {name}")
            path.write_text(original.replace(before, after))
            status, results = consumers(output / name, expect_success=False)
            row = next(row for row in results if row["name"] == name)
            if status != 1 or row["expected"] or row["exit"] != 0 or "wire" not in row:
                raise RuntimeError(f"{name}: mutation did not emit the invalid public consumer: {status} {row}")
            observed[name] = judge(commands, row["wire"], output, "mutant-" + name, invalid_at)
            print(f"KILLED {name}: public compiler accepted and emitted schema-invalid wire", flush=True)
    finally:
        path.write_text(original)
    return observed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    _, results = consumers(output / "baseline")
    commands = validators(output)
    observed = wire_checks(commands, output, results)
    if args.mutation_check:
        observed["mutations"] = mutation_checks(commands, output)
        _, restored = consumers(output / "restored")
        observed["restored"] = wire_checks(commands, output, restored)
    (output / "validator-results.json").write_text(json.dumps(observed, indent=2) + "\n")
    print(f"PASS: scoped auth wire checks have zero disagreements; other validator rules are not claimed. Artifacts: {output}")


if __name__ == "__main__":
    main()
