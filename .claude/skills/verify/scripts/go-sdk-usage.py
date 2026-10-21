#!/usr/bin/env python3
"""Observe token counts through a standalone public Go SDK consumer."""
import argparse
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = r'''package main

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "os"

    "github.com/lsm/open-agent-protocol/go/sdk"
)

func observe(client *sdk.Client, agent, stream bool) (*sdk.Usage, error) {
    ctx := context.Background()
    messages := []sdk.Message{sdk.UserMessage("usage")}
    if agent {
        request := sdk.AgentRequest{ModelRef:"fixture/other:test@ok", Messages:messages}
        if !stream {
            response, err := client.Agent.Run(ctx, request)
            if err != nil { return nil, err }
            return response.Usage, nil
        }
        events, err := client.Agent.Stream(ctx, request)
        if err != nil { return nil, err }
        defer events.Close()
        var usage *sdk.Usage
        for events.Next() {
            if terminal, ok := events.Event().(*sdk.AgentEnd); ok { usage = terminal.Usage }
        }
        return usage, events.Err()
    }
    request := sdk.CompletionRequest{ModelRef:"fixture/other:test@ok", Messages:messages}
    if !stream {
        response, err := client.Provider.Complete(ctx, request)
        if err != nil { return nil, err }
        return response.Usage, nil
    }
    events, err := client.Provider.Stream(ctx, request)
    if err != nil { return nil, err }
    defer events.Close()
    var usage *sdk.Usage
    for events.Next() {
        if terminal, ok := events.Event().(*sdk.MessageEnd); ok { usage = terminal.Usage }
    }
    return usage, events.Err()
}

func main() {
    client, err := sdk.New(context.Background(), &sdk.Options{
        BinaryPath:os.Args[1], Args:[]string{}, Env:[]string{
            "OAP_SDK_GO_FAKE_HOST=oap-combined", "OAP_SDK_GO_FAKE_USAGE="+os.Args[2], "GORACE=atexit_sleep_ms=0"}})
    if err != nil { panic(err) }
    defer client.Close()
    var output []map[string]any
    for _, agent := range []bool{false,true} {
        for _, stream := range []bool{false,true} {
            usage, err := observe(client,agent,stream)
            entry := map[string]any{"agent":agent,"stream":stream}
            if err != nil {
                var typed *sdk.ProtocolError
                if errors.As(err,&typed) { entry["error"] = typed.Code
                } else { entry["error"] = fmt.Sprint(err) }
            } else { entry["usage"] = usage }
            output = append(output,entry)
        }
    }
    if err := json.NewEncoder(os.Stdout).Encode(output); err != nil { panic(err) }
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
    source = output / "consumer.go"
    source.write_text(SOURCE)
    fixture, consumer = output / "fixture", output / "consumer"
    run(["go", "test", "-c", "-o", str(fixture), "./go/sdk"], output, "fixture-build")
    run(["go", "build", "-o", str(consumer), str(source)], output, "consumer-build")
    fields = {"input_tokens":"input", "output_tokens":"output", "total_tokens":"total",
              "cache_read_input_tokens":"cache_read", "cache_write_input_tokens":"cache_write",
              "reasoning_tokens":"reasoning"}
    cases = [("absent", None), ("{}", {})]
    cases += [(json.dumps({wire:0}), {name:0}) for wire, name in fields.items()]
    for maximum in [9007199254740993, 18446744073709551615]:
        cases.append((json.dumps({wire:maximum for wire in fields}), {name:maximum for name in fields.values()}))
    for index, (raw, expected) in enumerate(cases):
        (output / f"{index}.input").write_text(raw)
        values = json.loads(run([str(consumer),str(fixture),raw],output,str(index)))
        assert len(values) == 4 and all(value.get("usage", "missing") == expected for value in values), values
        print(f"public Go SDK consumer {raw}: {values}", flush=True)
    for index, raw in enumerate(["null", '{"input_tokens":null}', '{"input_tokens":-1}',
                                 '{"total_tokens":"3"}', '{"output_tokens":18446744073709551616}', "[]"]):
        values = json.loads(run([str(consumer),str(fixture),raw],output,f"invalid-{index}"))
        assert len(values) == 4 and all(value.get("error") == "malformed_response" for value in values), values
        print(f"public Go SDK consumer rejects {raw}: {values}", flush=True)
    validators(output)
    print(f"PASS: absence, independent zeros and uint64 maximum preserved; malformed usage refused. Artifacts: {output}")


if __name__ == "__main__":
    main()
