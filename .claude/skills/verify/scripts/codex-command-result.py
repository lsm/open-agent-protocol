#!/usr/bin/env python3
"""Replay Codex command outcomes through both real CLIs without credentials."""
import argparse
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[4]
OUTPUT = "bwrap: No permissions to create a new namespace"
CASES = [("zero", 0), ("nonzero", 1), ("negative", -7),
         ("missing", None), ("null", None), ("native-result", 0),
         ("failed", 0), ("error", 0)]


def native():
    def emit(value):
        print(json.dumps(value), flush=True)

    def event(method, item):
        emit({"method": method, "params": {
            "threadId": "thread-1", "turnId": "turn-1", "item": item}})

    for line in sys.stdin:
        request = json.loads(line)
        method = request.get("method")
        if method == "initialize":
            result = {"userAgent": "codex-fixture", "codexHome": "/fixture",
                      "platformFamily": "unix", "platformOs": "linux"}
        elif method == "thread/start":
            result = {"thread": {"id": "thread-1"}}
        elif method == "turn/start":
            result = {"turn": {"id": "turn-1", "status": "inProgress"}}
        else:
            continue
        emit({"id": request["id"], "result": result})
        if method != "turn/start":
            continue
        emit({"method": "turn/started", "params": {"threadId": "thread-1",
              "turn": {"id": "turn-1", "status": "inProgress"}}})
        for name, code in CASES:
            item = {"type": "commandExecution", "id": name,
                    "command": "git diff HEAD~1", "cwd": "/workspace/repo",
                    "status": "inProgress"}
            event("item/started", item)
            item.update(status="completed", aggregatedOutput=OUTPUT)
            if name != "missing":
                item["exitCode"] = code
            if name == "native-result":
                item["result"] = ["native", {"value": 42}]
            if name == "failed":
                item["status"] = "failed"
            if name == "error":
                item["error"] = {"message": "native refused"}
            event("item/completed", item)
        emit({"method": "turn/completed", "params": {"threadId": "thread-1",
              "turn": {"id": "turn-1", "status": "completed"}}})


def replay(binary, output):
    with tempfile.TemporaryDirectory(prefix="oap-codex-command-") as temporary:
        work = Path(temporary)
        registry = work / "registry.json"
        registry.write_text(json.dumps({"adapters": {"codex": {
            "type": "codex", "executable": sys.executable,
            "args": [str(Path(__file__).resolve()), "--native"],
            "working_directory": temporary, "model": "glm-test",
            "approval_policy": "never", "sandbox": "read-only",
            "environment": []}}}))
        environment = {"HOME": temporary, "PATH": "/usr/bin:/bin",
                       "CODEX_HOME": temporary}
        child = subprocess.Popen([str(binary), "serve", "agent", "--backend", "codex",
                                  "--config", str(registry)], cwd=work, env=environment,
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE, start_new_session=True)
        selector = selectors.DefaultSelector()
        selector.register(child.stdout, selectors.EVENT_READ)
        pending = b""
        trace = []
        try:
            for index, (kind, payload) in enumerate([
                ("protocol.initialize.request", {"protocol_versions": ["0.1"],
                 "profiles": ["open-agent-protocol.agent-control-core"],
                 "participant": {"id": "user"}}),
                ("capabilities.request", {}),
                ("session.open.request", {"session_id": "s1"}),
                ("session.message.submit.request", {"session_id": "s1", "delivery": "auto",
                 "messages": [{"role": "user", "content": "read the code"}]})]):
                request = {"protocol": "open-agent-protocol", "version": "0.1",
                           "profile": "open-agent-protocol.agent-control-core", "type": kind,
                           "id": f"req-{index}", "payload": payload}
                if index == 3:
                    request["session_id"] = "s1"
                trace.append(request)
                child.stdin.write((json.dumps(request) + "\n").encode())
                child.stdin.flush()
                deadline = time.monotonic() + 20
                done = False
                while not done:
                    if not selector.select(max(0, deadline - time.monotonic())):
                        raise TimeoutError(f"no response to {kind}")
                    chunk = os.read(child.stdout.fileno(), 65536)
                    if not chunk:
                        raise RuntimeError(f"stdio ended before {kind}")
                    pending += chunk
                    while b"\n" in pending:
                        line, pending = pending.split(b"\n", 1)
                        frame = json.loads(line)
                        trace.append(frame)
                        if frame["type"] == "error.response":
                            raise RuntimeError(frame)
                        done |= (frame.get("type") == "run.completed" if index == 3
                                 else frame.get("in_reply_to") == request["id"])
            child.stdin.close()
            code = child.wait(timeout=10)
            assert code == 0, f"CLI exit {code}"
        finally:
            selector.close()
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
            (output / "stderr.txt").write_bytes(child.stderr.read())
            (output / "trace.json").write_text(json.dumps(trace, indent=2) + "\n")
        calls = [frame for frame in trace if frame["type"] in
                 ("action.call.completed", "action.call.failed")]
        assert len(calls) == len(CASES), f"expected {len(CASES)} terminal calls, got {len(calls)}"
        for (name, code), frame in zip(CASES, calls):
            payload = frame["payload"]
            if name in ("failed", "error"):
                assert frame["type"] == "action.call.failed", frame
                assert payload["error"]["code"] == "native_action_failed", frame
            else:
                result = payload["result"]
                assert frame["type"] == "action.call.completed", frame
                assert "exitCode" in result and result["exitCode"] == code, frame
                assert result["command"] == "git diff HEAD~1" and result["cwd"] == "/workspace/repo", frame
                assert result["output"] == OUTPUT, frame
                if name == "native-result":
                    assert result["nativeResult"] == ["native", {"value": 42}], frame
            print(f"{binary.name}/{name}: {frame['type']} {json.dumps(payload.get('result', payload.get('error')))}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--oapx", type=Path, default=ROOT / "zig/zig-out/bin/oapx")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    goap = output / "goap"
    subprocess.run(["go", "build", "-o", str(goap), "./go/cmd/goap"], cwd=ROOT, check=True, timeout=120)
    for label, binary in [("go", goap), ("zig", args.oapx.resolve())]:
        destination = output / label
        destination.mkdir()
        replay(binary, destination)
    commands = {
        "go": [str(goap), "validate", "--format", "json"],
        "zig": [str(args.oapx.resolve()), "validate", "--format", "json"],
        "rust": ["cargo", "run", "--quiet", "--manifest-path", "rust/Cargo.toml",
                 "-p", "oap-validation", "--example", "validate", "--"],
    }
    for source in ("go", "zig"):
        for label, command in commands.items():
            result = subprocess.run(command + [str(output / source / "trace.json")],
                                    cwd=ROOT, capture_output=True, timeout=120)
            (output / f"{source}-trace.{label}-validator.log").write_bytes(result.stdout + result.stderr)
            assert result.returncode == 0, f"{source} trace: {label} validator exit {result.returncode}: {result.stdout!r}"
            print(f"{source} trace: {label} validator exit 0")
    print(f"PASS: command exit evidence retained; validators agree; artifacts: {output}")


if __name__ == "__main__":
    if "--native" in sys.argv[1:]:
        native()
    else:
        main()
