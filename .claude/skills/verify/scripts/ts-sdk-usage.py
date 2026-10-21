#!/usr/bin/env python3
"""Observe public TypeScript SDK usage and compare agreed validator boundaries."""
import argparse
import json
import os
import re
import signal
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
FIELDS = {"input_tokens": "input", "output_tokens": "output", "total_tokens": "total",
          "cache_read_input_tokens": "cache_read", "cache_write_input_tokens": "cache_write",
          "reasoning_tokens": "reasoning"}
SOURCE = r'''const { createOapClient, MakaiProtocolError } = require(process.argv[2]);
(async () => {
  const request = { model_ref: "fixture/openai-responses@mock", messages: [{ role: "user", content: "usage" }] };
  const output = [];
  for (const agent of [false, true]) for (const stream of [false, true]) {
    const client = await createOapClient({ command: process.execPath, args: [process.argv[3]], env: { OAP_FIXTURE_USAGE: process.argv[4] } });
    const entry = { agent, stream };
    try {
        const api = agent ? client.agent : client.provider;
        if (!stream) entry.usage = (await (agent ? api.run(request) : api.complete(request))).usage ?? null;
        else {
          let completed = false;
          for await (const event of api.stream(request)) {
            if (event.type === (agent ? "agent_end" : "message_end")) {
              entry.usage = event.usage ?? null;
              completed = true;
            }
          }
          if (!completed) throw new Error("missing terminal");
        }
      } catch (error) {
        if (!(error instanceof MakaiProtocolError)) throw error;
        entry.error = error.code;
    } finally { await client.close(); }
    output.push(entry);
  }
  console.log(JSON.stringify(output));
})().catch(error => { console.error(error); process.exitCode = 1; });
'''


def run(command, output, label, expected=0):
    child = subprocess.Popen(command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                             text=True, start_new_session=True)
    try:
        stdout, stderr = child.communicate(timeout=300)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        stdout, stderr = child.communicate()
        raise
    finally:
        (output / f"{label}.out").write_text(stdout)
        (output / f"{label}.err").write_text(stderr)
    assert child.returncode == expected, (command, child.returncode, stdout, stderr)
    return stdout


def validators(output):
    template = json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())
    commands = {
        "go": ["go", "run", "./go/cmd/goap", "validate", "--format", "json"],
        "rust": ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
                 "-p", "oap-validation", "--example", "validate", "--"],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate"],
    }
    cases = [({}, True), ({"input_tokens": 0}, True)]
    cases += [({"input_tokens": count}, True) for count in [2**53-1, 2**53, 2**64-1]]
    cases += [(value, False) for value in [{"input_tokens": -1}, {"input_tokens": None},
                                          {"input_tokens": 1.5}, {"input_tokens": True},
                                          {"input_tokens": "3"}, None, []]]
    results = []
    for index, (usage, valid) in enumerate(cases):
        trace = json.loads(json.dumps(template))
        for frame in trace:
            if frame["type"] == "run.completed":
                frame["payload"]["usage"] = usage
        path = output / f"boundary-{index}.json"
        path.write_text(json.dumps(trace))
        outcomes = {}
        for name, command in commands.items():
            label = f"boundary-{index}.{name}"
            stdout = run(command + [str(path)], output, label, int(not valid))
            if name == "zig":
                codes = set(re.findall(r"\b(?:decode|schema|semantic) ([a-z_]+) at \d+",
                                       stdout + (output / f"{label}.err").read_text()))
            else:
                reports = json.loads(stdout)
                if not isinstance(reports, list):
                    reports = [reports]
                codes = {diagnostic["code"] for report in reports
                         for diagnostic in report.get("diagnostics") or []}
            assert codes == (set() if valid else {"schema_invalid"}), (name, usage, codes)
            outcomes[name] = {"exit": int(not valid), "codes": sorted(codes)}
        results.append({"usage": usage, "outcomes": outcomes})
    (output / "validators.json").write_text(json.dumps(results))
    print("agreed usage-boundary validator disagreements: 0; focused subset; full Decision 0065 raw-token matrix runs in usage-count-validation.py", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    source = output / "consumer.cjs"
    source.write_text(SOURCE)
    run(["npm", "run", "build:sdk"], output, "build")
    command = ["node", str(source), str(ROOT / "dist/src/index.js"),
               str(ROOT / "sdk/typescript/test/fixtures/oap-server.js")]
    cases = [("absent", None), ("{}", {})]
    for field, name in FIELDS.items():
        for value in [0, 2**53-1, 2**53, 2**64-1]:
            expected = {name: value} if value <= 2**53-1 else {
                "unrepresentable_counts": [{"code": "unrepresentable_count", "field": field, "value": str(value)}]}
            cases.append((json.dumps({field: value}), expected))
    cases.append(('{"input_tokens":18446744073709551615,"output_tokens":0}', {
        "output": 0, "unrepresentable_counts": [{"code": "unrepresentable_count", "field": "input_tokens", "value": "18446744073709551615"}]}))
    for index, (raw, expected) in enumerate(cases):
        (output / f"valid-{index}.input").write_text(raw)
        values = json.loads(run(command + [raw], output, f"valid-{index}"))
        assert len(values) == 4 and all(value.get("usage", "missing") == expected for value in values), values
        print(f"public TypeScript consumer {raw}: {values}", flush=True)
    invalid = ["null", "[]", "true", "3", '\"usage\"']
    for field in FIELDS:
        for raw in ["null", "-1", "1.5", "1.0", "1e3", "true", '\"3\"', "[]", "{}", str(2**64)]:
            invalid.append('{"' + field + '":' + raw + '}')
    for index, raw in enumerate(invalid):
        (output / f"invalid-{index}.input").write_text(raw)
        values = json.loads(run(command + [raw], output, f"invalid-{index}"))
        assert len(values) == 4 and all(value.get("error") == "malformed_response" for value in values), values
    validators(output)
    print(f"PASS: safe counts exact, unsafe counts non-fatal and lossless, invalid usage refused. Artifacts: {output}")


if __name__ == "__main__":
    main()
