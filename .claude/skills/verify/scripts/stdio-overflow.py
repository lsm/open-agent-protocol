#!/usr/bin/env python3
"""Observe a slow stdio reader's overflow and recover its terminal through public APIs."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
SOURCE = r'''
package main

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "time"

    base "github.com/lsm/open-agent-protocol/go/adapter"
    "github.com/lsm/open-agent-protocol/go/protocol"
    "github.com/lsm/open-agent-protocol/go/serve"
    "github.com/lsm/open-agent-protocol/go/serve/servestdio"
)

func check(err error) { if err != nil { panic(err) } }

func drive(ctx context.Context, hub *serve.Hub, entry *serve.Session) (protocol.RunID, uint64) {
    driver, err := hub.Subscribe(ctx, entry.ID())
    check(err)
    defer driver.Close()
    admission, err := entry.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: entry.ID(), Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
    check(err)
    for {
        event, err := driver.Next()
        check(err)
        switch event.Type {
        case protocol.TypeActionPermissionRequested:
            var p protocol.PermissionRequestedPayload
            check(event.DecodePayload(&p))
            check(entry.Resolve(ctx, base.InteractionResolution{RunID: p.RunID, RespondedBy: p.RespondedBy, Permission: &protocol.PermissionResolveRequest{InteractionID: p.InteractionID, SessionID: p.SessionID, RunID: p.RunID, RequestedBy: p.RequestedBy, RespondedBy: p.RespondedBy, ChoiceID: "approve", Granted: true}}))
        case protocol.TypeUserInputRequested:
            var p protocol.UserInputRequestedPayload
            check(event.DecodePayload(&p))
            check(entry.Resolve(ctx, base.InteractionResolution{RunID: p.RunID, RespondedBy: p.RespondedBy, Input: &protocol.UserInputResolveRequest{InteractionID: p.InteractionID, SessionID: p.SessionID, RunID: p.RunID, RequestedBy: p.RequestedBy, RespondedBy: p.RespondedBy, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}}))
        case protocol.TypeRunCompleted:
            return admission.RunID, *event.Sequence
        }
    }
}

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    registry := serve.NewRegistry()
    check(registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: 64})))
    hub := serve.New(registry, serve.Options{StreamQueue: 8})
    entry, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "flooded", Participant: protocol.Participant{ID: "user"}})
    check(err)
    server, err := servestdio.New(hub, servestdio.Options{WriteQueue: 1})
    check(err)
    input, request := io.Pipe()
    output, response := io.Pipe()
    done := make(chan error, 1)
    go func() { done <- server.Run(ctx, input, response); response.Close() }()
    _, err = fmt.Fprintln(request, `{"id":9,"op":"events","session_id":"flooded"}`)
    check(err)
    reader := bufio.NewReader(output)
    line, err := reader.ReadString('\n')
    check(err)
    var ack struct { OK bool `json:"ok"` }
    check(json.Unmarshal([]byte(line), &ack))
    if !ack.OK { panic("events refused") }
    fmt.Print(line)
    run, terminal := drive(ctx, hub, entry)
    var delivered uint64
    var cursor uint64
    found := false
    for n := 0; n < 64; n++ {
        line, err := reader.ReadString('\n')
        check(err)
        fmt.Print(line)
        var event struct {
            Event string `json:"event"`
            RunID string `json:"run_id"`
            SessionID string `json:"session_id"`
            Sequence *uint64 `json:"sequence"`
            LastSequence uint64 `json:"last_sequence"`
        }
        check(json.Unmarshal([]byte(line), &event))
        if event.Event == "envelope" && event.Sequence != nil { delivered = *event.Sequence }
        if event.Event == "oap-overflow" {
            if event.RunID != string(run) || event.SessionID != string(entry.ID()) || event.LastSequence < delivered || event.LastSequence >= terminal { panic("overflow lost its run or cursor") }
            cursor, found = event.LastSequence, true
            break
        }
    }
    if !found { panic("no overflow signal") }
    _, err = fmt.Fprintf(request, "{\"id\":10,\"op\":\"events\",\"session_id\":\"flooded\",\"run_id\":%q,\"after\":%d}\n", run, cursor)
    check(err)
    line, err = reader.ReadString('\n')
    check(err)
    fmt.Print(line)
    check(json.Unmarshal([]byte(line), &ack))
    if !ack.OK { panic("replay refused") }
    var recovered uint64
    for n := 0; n < 64; n++ {
        line, err := reader.ReadString('\n')
        check(err)
        fmt.Print(line)
        var event struct {
            Event string `json:"event"`
            ID int `json:"id"`
            Envelope protocol.Envelope `json:"envelope"`
        }
        check(json.Unmarshal([]byte(line), &event))
        if event.Event != "envelope" || event.ID != 10 || event.Envelope.RunID != run || event.Envelope.Sequence == nil || *event.Envelope.Sequence != cursor+1 { panic("replay lost continuity") }
        cursor = *event.Envelope.Sequence
        if event.Envelope.Type == protocol.TypeRunCompleted { recovered = cursor; break }
    }
    if recovered != terminal { panic("replay lost the terminal") }
    check(request.Close())
    check(<-done)
    check(entry.Close(ctx))
    summary, err := json.Marshal(map[string]any{"run_id": run, "healthy_terminal_sequence": terminal, "replayed_terminal_sequence": recovered, "last_delivered_sequence": delivered})
    check(err)
    fmt.Fprintln(os.Stderr, string(summary))
}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    source = output / "consumer.go"
    source.write_text(SOURCE)
    binary = output / "consumer"
    with (output / "build.log").open("wb") as log:
        subprocess.run(["go", "build", "-o", str(binary), str(source)], cwd=ROOT,
                       stdout=log, stderr=subprocess.STDOUT, timeout=120, check=True)
    with tempfile.TemporaryDirectory(prefix="oap-stdio-consumer-") as home:
        result = subprocess.run([str(binary)], env={"HOME": home, "PATH": os.defpath},
                                capture_output=True, timeout=45)
    (output / "wire.jsonl").write_bytes(result.stdout)
    (output / "result.json").write_bytes(result.stderr)
    result.check_returncode()
    frames = [json.loads(line) for line in result.stdout.splitlines()]
    summary = json.loads(result.stderr)
    assert any(frame.get("event") == "oap-overflow" for frame in frames), frames
    assert frames[-1]["envelope"]["type"] == "run.completed", frames[-1]
    assert summary["healthy_terminal_sequence"] == summary["replayed_terminal_sequence"] == 12, summary
    print(f"PASS: healthy reader received terminal 12; stdio overflow names {summary['run_id']}; cursor replay recovered terminal 12; artifacts: {output}")


if __name__ == "__main__":
    main()
