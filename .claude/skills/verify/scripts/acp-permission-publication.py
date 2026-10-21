import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[4]
AGENT = r'''
import json
import socket
import sys

def send(value):
    print(json.dumps(dict(jsonrpc="2.0", **value)), flush=True)

for line in sys.stdin:
    message = json.loads(line)
    method = message.get("method")
    if method == "initialize":
        send(dict(id=message["id"], result=dict(protocolVersion=1, agentCapabilities={})))
    elif method == "session/new":
        send(dict(id=message["id"], result=dict(sessionId="native-session")))
    elif method == "session/prompt":
        for number in (1, 2):
            send(dict(id=f"permission-{number}", method="session/request_permission", params=dict(
                sessionId="native-session", toolCall=dict(toolCallId=f"call-{number}", title="Act"),
                options=[dict(optionId="allow_once", name="Allow", kind="allow_once")])))
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as gate:
            gate.connect(sys.argv[1])
            assert gate.recv(1) == b"x"
        send(dict(id=message["id"], result=dict(stopReason="end_turn")))
'''
CONSUMER = r'''package main

import (
    "context"
    "encoding/json"
    "fmt"
    "net"
    "os"
    "time"

    base "github.com/lsm/open-agent-protocol/go/adapter"
    "github.com/lsm/open-agent-protocol/go/adapter/acp"
    "github.com/lsm/open-agent-protocol/go/protocol"
)

func must(err error) { if err != nil { panic(err) } }
func frame(kind protocol.EnvelopeType, id protocol.EnvelopeID, payload any, reply protocol.EnvelopeID, revision string, session protocol.SessionID) protocol.Envelope {
    event, err := protocol.NewEnvelope(kind, id, payload); must(err)
    event.InReplyTo, event.CapabilityRevision, event.SessionID = reply, revision, session
    return event
}

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second); defer cancel()
    gate, err := net.ListenUnix("unix", &net.UnixAddr{Name:os.Args[3], Net:"unix"}); must(err)
    defer gate.Close()
    must(gate.SetDeadline(time.Now().Add(10*time.Second)))
    adapter, err := acp.New(acp.Config{Executable:os.Args[1], Args:[]string{os.Args[2], os.Args[3]}, Environment:[]string{}, WorkingDirectory:os.Args[4]}); must(err)
    descriptor, err := adapter.Probe(ctx); must(err)
    session, err := adapter.Open(ctx, base.OpenRequest{SessionID:"session", Participant:protocol.Participant{ID:"user"}}); must(err)
    defer session.Close(context.Background())
    state, err := session.State(ctx); must(err)
    request := protocol.MessageSubmitRequest{SessionID:"session", Delivery:protocol.DeliveryAuto, Messages:[]protocol.Message{{Role:protocol.RoleUser, Content:protocol.TextContent("permissions")}}}
    admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request:request}); must(err)
    revision := descriptor.CapabilityRevision
    trace := []protocol.Envelope{
        frame(protocol.TypeProtocolInitializeRequest,"init-q",protocol.InitializeRequest{ProtocolVersions:[]string{protocol.Version}, Profiles:[]string{protocol.Profile}, Participant:&protocol.Participant{ID:"user"}},"","",""),
        frame(protocol.TypeProtocolInitializeResponse,"init-r",protocol.InitializeResponse{ProtocolVersion:protocol.Version, Profile:protocol.Profile, Endpoint:descriptor.Capabilities.Endpoint},"init-q","",""),
        frame(protocol.TypeCapabilitiesRequest,"caps-q",protocol.CapabilitiesRequest{},"","",""),
        frame(protocol.TypeCapabilitiesResponse,"caps-r",descriptor.Capabilities,"caps-q",revision,""),
        frame(protocol.TypeSessionOpenRequest,"open-q",protocol.SessionOpenRequest{SessionID:"session"},"",revision,""),
        frame(protocol.TypeSessionOpenResponse,"open-r",state,"open-q",revision,"session"),
        frame(protocol.TypeSessionMessageSubmitRequest,"submit-q",request,"",revision,"session"),
        frame(protocol.TypeSessionMessageSubmitResponse,"submit-r",admission,"submit-q",revision,"session"),
    }
    requested, resolved, terminal := 0, 0, 0
    for {
        select {
        case result, ok := <-stream:
            if !ok {
                if requested != 2 || resolved != 2 || terminal != 1 { panic(fmt.Sprintf("requests/resolutions/terminals: %d/%d/%d",requested,resolved,terminal)) }
                must(json.NewEncoder(os.Stdout).Encode(trace)); return
            }
            must(result.Error)
            event := result.Envelope
            trace = append(trace,event)
            switch event.Type {
            case protocol.TypeActionPermissionRequested:
                requested++
                if requested == 2 {
                    peer, err := gate.Accept(); must(err)
                    _, err = peer.Write([]byte("x")); must(err); must(peer.Close())
                }
            case protocol.TypeActionPermissionResolved:
                if terminal != 0 { panic("permission resolved after terminal") }; resolved++
            case protocol.TypeRunCompleted:
                if resolved != requested { panic("terminal left a permission unresolved") }; terminal++
            }
        case <-ctx.Done(): panic(ctx.Err())
        }
    }
}
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", type=Path, required=True)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="oap-acp-permission-", dir="/tmp") as temporary:
        work = Path(temporary)
        (work / "go.mod").write_text(f"module acp-permission-consumer\n\ngo 1.26\n\nrequire github.com/lsm/open-agent-protocol v0.0.0\nreplace github.com/lsm/open-agent-protocol => {ROOT}\n")
        (work / "main.go").write_text(CONSUMER)
        (work / "agent.py").write_text(AGENT)
        built = subprocess.run(["go", "build", "-mod=mod", "-o", str(work / "consumer"), "."], cwd=work, capture_output=True, text=True, timeout=300)
        (output / "consumer-build.log").write_text(built.stdout + built.stderr)
        assert built.returncode == 0, built.stderr
        process = subprocess.Popen([str(work / "consumer"), sys.executable, str(work / "agent.py"), str(work / "gate.sock"), str(work)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
        try:
            stdout, stderr = process.communicate(timeout=30)
        finally:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            if process.poll() is None:
                process.communicate(timeout=5)
        (output / "consumer.stdout").write_text(stdout)
        (output / "consumer.stderr").write_text(stderr)
        assert process.returncode == 0, (process.returncode, stderr)
    trace = json.loads(stdout)
    complete = output / "complete.json"
    complete.write_text(json.dumps(trace, indent=2) + "\n")
    removed = False
    broken = []
    sequence = 0
    for event in trace:
        if not removed and event["type"] == "action.permission.resolved":
            removed = True
            continue
        if "sequence" in event:
            sequence += 1
            event["sequence"] = sequence
        broken.append(event)
    assert removed
    missing = output / "missing-resolution.json"
    missing.write_text(json.dumps(broken, indent=2) + "\n")
    for name, command in [
        ("go", ["go", "run", "./go/cmd/goap", "validate", "--format=json"]),
        ("rust", ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate", "--"]),
        ("zig", [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format=json"]),
    ]:
        for label, path, expected in [("complete", complete, []), ("missing-resolution", missing, ["pending_interaction_at_terminal"])]:
            result = subprocess.run([*command, str(path)], cwd=ROOT, capture_output=True, text=True, timeout=300)
            (output / f"{label}-{name}.stdout").write_text(result.stdout)
            (output / f"{label}-{name}.stderr").write_text(result.stderr)
            reports = json.loads(result.stdout)
            if isinstance(reports, dict):
                reports = [reports]
            codes = sorted({d["code"] for report in reports for d in report.get("diagnostics") or []})
            assert result.returncode == int(bool(expected)) and codes == expected, (label, name, result.returncode, codes, result.stderr)
            print(f"{label}-{name}: exit {result.returncode}, codes {codes}", flush=True)
    print("PASS: public Go adapter over real synthetic ACP stdio resolved both permissions before terminal; mutation rejected by all three validators")


if __name__ == "__main__":
    main()
