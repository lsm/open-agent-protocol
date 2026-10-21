#!/usr/bin/env python3
"""Observe token counts through a standalone public Rust SDK consumer."""
import argparse
import json
import re
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = r'''use futures::StreamExt;
use oap_sdk::{AgentEvent, ClientBuilder, ExecutionRequest, ProviderEvent};
use serde_json::{json, Value};

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let arguments: Vec<String> = std::env::args().collect();
    let client = ClientBuilder::new().command(&arguments[1])
        .args([format!("--usage={}", arguments[2])]).env_clear().connect().await?;
    let request = ExecutionRequest::prompt("fixture/openai-responses@mock", "usage");
    let mut observed = Vec::new();
    for agent in [false, true] {
        let result = if agent { client.agent().run(request.clone()).await }
                     else { client.provider().complete(request.clone()).await };
        observed.push(match result {
            Ok(response) => json!({"agent":agent,"stream":false,"usage":response.usage}),
            Err(error) => json!({"agent":agent,"stream":false,"error":error.code()}),
        });
        let mut terminal: Option<Result<Value, oap_sdk::Error>> = None;
        if agent {
            let mut events = Box::pin(client.agent().stream(request.clone()));
            while let Some(event) = events.next().await {
                match event {
                    Ok(AgentEvent::AgentEnd { usage, .. }) => terminal = Some(Ok(json!(usage))),
                    Err(error) => { terminal = Some(Err(error)); break; }
                    _ => {}
                }
            }
        } else {
            let mut events = Box::pin(client.provider().stream(request.clone()));
            while let Some(event) = events.next().await {
                match event {
                    Ok(ProviderEvent::MessageEnd { usage, .. }) => terminal = Some(Ok(json!(usage))),
                    Err(error) => { terminal = Some(Err(error)); break; }
                    _ => {}
                }
            }
        }
        observed.push(match terminal.ok_or("missing stream terminal")? {
            Ok(usage) => json!({"agent":agent,"stream":true,"usage":usage}),
            Err(error) => json!({"agent":agent,"stream":true,"error":error.code()}),
        });
    }
    client.close().await;
    println!("{}", json!(observed));
    Ok(())
}
'''


def run(command, output, label):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
    (output / f"{label}.out").write_text(result.stdout)
    (output / f"{label}.err").write_text(result.stderr)
    assert result.returncode == 0, (command, result.stderr)
    return result.stdout


def validators(output):
    template = json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())
    commands = {
        "go": ["go", "run", "./go/cmd/goap", "validate", "--format", "json"],
        "rust": ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
                 "-p", "oap-validation", "--example", "validate", "--"],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate"],
    }
    cases = [({}, True), ({"input_tokens":0}, True),
             ({"total_tokens":18446744073709551615}, True),
             ({"input_tokens":-1}, False), ({"total_tokens":"3"}, False), ({"input_tokens":None}, False), (None, False), ([], False)]
    for index, (usage, valid) in enumerate(cases):
        trace = json.loads(json.dumps(template))
        for frame in trace:
            if frame["type"] == "run.completed":
                frame["payload"]["usage"] = usage
        path = output / f"boundary-{index}.json"
        path.write_text(json.dumps(trace))
        for name, command in commands.items():
            result = subprocess.run(command + [str(path)], cwd=ROOT, capture_output=True,
                                    text=True, timeout=300)
            (output / f"boundary-{index}.{name}.out").write_text(result.stdout)
            (output / f"boundary-{index}.{name}.err").write_text(result.stderr)
            assert result.returncode == int(not valid), (name, usage, result.stdout, result.stderr)
            if not valid:
                if name == "zig":
                    codes = set(re.findall(r"\b(?:decode|schema|semantic) ([a-z_]+) at \d+",
                                           result.stdout + result.stderr))
                else:
                    reports = json.loads(result.stdout)
                    if not isinstance(reports, list):
                        reports = [reports]
                    codes = {diagnostic["code"] for report in reports
                             for diagnostic in report.get("diagnostics") or []}
                assert codes == {"schema_invalid"}, (name, usage, codes)
        print(f"Go/Rust/Zig agree for usage {usage}: {'valid' if valid else 'schema_invalid'}", flush=True)
    print("usage boundary validator disagreements: 0", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    consumer = output / "consumer"
    (consumer / "src").mkdir(parents=True)
    (consumer / "src/main.rs").write_text(SOURCE)
    (consumer / "Cargo.toml").write_text(
        '[package]\nname="usage-consumer"\nversion="0.0.0"\nedition="2021"\n'
        '[dependencies]\nfutures="0.3"\nserde_json="1"\ntokio={version="1",features=["macros","rt"]}\n'
        + f'oap-sdk={{path={json.dumps(str(ROOT / "rust/sdk"))}}}\n')
    run(["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-sdk",
         "--bin", "oap-protocol-fake"], output, "fixture-build")
    run(["cargo", "build", "--offline", "--manifest-path", str(consumer / "Cargo.toml"),
         "--target-dir", str(ROOT / "rust/target")], output, "consumer-build")
    fields = {"input_tokens":"input", "output_tokens":"output", "total_tokens":"total",
              "cache_read_input_tokens":"cache_read", "cache_write_input_tokens":"cache_write",
              "reasoning_tokens":"reasoning"}
    cases = [("absent", None), ("{}", {})]
    cases += [(json.dumps({wire:0}), {name:0}) for wire, name in fields.items()]
    cases += [(json.dumps({wire:18446744073709551615 for wire in fields}),
               {name:18446744073709551615 for name in fields.values()})]
    for index, (raw, expected) in enumerate(cases):
        (output / f"{index}.input").write_text(raw)
        values = json.loads(run([str(ROOT / "rust/target/debug/usage-consumer"),
                                 str(ROOT / "rust/target/debug/oap-protocol-fake"), raw],
                                output, str(index)))
        assert len(values) == 4 and all(value.get("usage", "missing") == expected for value in values), values
        print(f"public Rust consumer {raw}: {values}", flush=True)
    for index, raw in enumerate(["null", '{"input_tokens":null}', '{"input_tokens":-1}', '{"total_tokens":"3"}',
                                 '{"output_tokens":18446744073709551616}', "[]"]):
        values = json.loads(run([str(ROOT / "rust/target/debug/usage-consumer"),
                                 str(ROOT / "rust/target/debug/oap-protocol-fake"), raw],
                                output, f"invalid-{index}"))
        assert len(values) == 4 and all(value.get("error") == "malformed_response" for value in values), values
        print(f"public Rust consumer rejects {raw}: {values}", flush=True)
    validators(output)
    print(f"PASS: absence, independent zeros and uint64 maximum preserved; malformed usage refused. Artifacts: {output}")


if __name__ == "__main__":
    main()
