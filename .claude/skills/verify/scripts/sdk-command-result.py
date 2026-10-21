#!/usr/bin/env python3
"""Check native command exit evidence through all public SDK streams and clients/ts."""
import argparse
import json
import os
import shutil
import signal
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--oapx", type=Path, default=ROOT / "zig/zig-out/bin/oapx")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)

    def run(label, command, environment=None):
        child = subprocess.Popen(command, cwd=ROOT, env=environment, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE, start_new_session=True)
        try:
            stdout, stderr = child.communicate(timeout=240)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            stdout, stderr = child.communicate()
            (output / f"{label}.log").write_bytes(stdout + stderr)
            raise
        (output / f"{label}.log").write_bytes(stdout + stderr)
        assert child.returncode == 0, f"{label}: exit {child.returncode}; see {output / (label + '.log')}"
        return stdout

    run("wire", [sys.executable, str(ROOT / ".claude/skills/verify/scripts/codex-command-result.py"),
                 "--output-dir", str(output / "wire"), "--oapx", str(args.oapx.resolve())])
    go = output / "go-consumer"
    run("go-build", ["go", "build", "-o", str(go), "./go/sdk/examples/command-result"])
    run("rust-build", ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-sdk", "--example", "command_result"])
    run("ts-build", ["npm", "run", "build:sdk"])
    run("client-build", ["npm", "--prefix", "clients/ts", "run", "build"])
    parse = """
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const {parseEnvelope, payload} = await import(process.argv[1]);
const trace = JSON.parse(readFileSync(process.argv[2], 'utf8'));
for (const frame of trace) {
  assert.deepEqual(payload(parseEnvelope(JSON.stringify(frame))), frame.payload);
}
console.log('clients/ts preserved every payload, including numeric and null exitCode');
"""
    for source in ("go", "zig"):
        run(f"client-{source}", ["node", "--input-type=module", "-e", parse,
            (ROOT / "clients/ts/dist/src/protocol.js").as_uri(), str(output / "wire" / source / "trace.json")])
    with tempfile.TemporaryDirectory(prefix="oap-sdk-command-") as temporary:
        work = Path(temporary)
        registry = work / "registry.json"
        registry.write_text(json.dumps({"adapters": {"codex": {
            "type": "codex", "executable": sys.executable,
            "args": [str(ROOT / ".claude/skills/verify/scripts/codex-command-result.py"), "--native"],
            "working_directory": temporary, "model": "glm-test", "approval_policy": "never",
            "sandbox": "read-only", "environment": []}}}))
        environment = {"HOME": temporary, "CODEX_HOME": temporary, "PATH": os.defpath}
        consumers = {
            "go": [str(go)],
            "rust": [str(ROOT / "rust/target/debug/examples/command_result")],
            "ts": [shutil.which("node"), "sdk/typescript/test/fixtures/command-result-consumer.cjs"],
        }
        for sdk, command in consumers.items():
            raw = run(f"non-codex-{sdk}", command + [shutil.which("node"), "--non-codex"], environment)
            event, = json.loads(raw)
            assert event.get("is_error", event.get("IsError")) is False, (sdk, event)
            result = event.get("result", event.get("Result"))
            assert result == {"command": "git diff HEAD~1", "cwd": "/workspace/repo", "output": "read completed"}, (sdk, event)
            print(f"{sdk}: non-Codex Bash without exitCode retains its completed outcome", flush=True)
        for source, binary in (("go", output / "wire/goap"), ("zig", args.oapx.resolve())):
            observed = []
            for sdk, command in consumers.items():
                raw = run(f"{source}-{sdk}", command + [str(binary), str(registry)], environment)
                events = json.loads(raw)
                normalized = [{"is_error": event.get("is_error", event.get("IsError")),
                               "result": event.get("result", event.get("Result"))} for event in events]
                assert len(normalized) == 8, (source, sdk, normalized)
                for i, event in enumerate(normalized):
                    assert event["is_error"] is (i not in (0, 5)), (source, sdk, i, event)
                    if i < 6:
                        expected = {"command": "git diff HEAD~1", "cwd": "/workspace/repo",
                                    "output": "bwrap: No permissions to create a new namespace",
                                    "exitCode": [0, 1, -7, None, None, 0][i]}
                        if i == 5:
                            expected["nativeResult"] = ["native", {"value": 42}]
                        assert event["result"] == expected, (source, sdk, i, event)
                observed.append(normalized)
                print(f"{source}/{sdk}: eight command outcomes retain exit evidence; unknown is not success", flush=True)
            assert observed[0] == observed[1] == observed[2], f"SDK disagreement for {source}"
    print(f"PASS: SDK parity, clients/ts preservation, six validator passes; artifacts: {output}")


if __name__ == "__main__":
    main()
