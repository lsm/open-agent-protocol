#!/usr/bin/env python3
"""Drive the public Go hub through a token-free Copilot-shaped child."""
import argparse
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parents[4]
PROFILE = "open-agent-protocol.agent-control-core"


def envelope(kind, identifier, payload, session=None, revision=None):
    value = dict(protocol="open-agent-protocol", version="0.1", profile=PROFILE,
                 type=kind, id=identifier, payload=payload)
    if session:
        value["session_id"] = session
    if revision:
        value["capability_revision"] = revision
    return value


class Hub:
    def __init__(self, binary, config, env, output):
        self.lines = queue.Queue()
        self.received = []
        self.counter = 0
        self.stderr = open(output / "hub-stderr.txt", "w")
        self.process = subprocess.Popen([str(binary), "hub", "--stdio", "--config", str(config)],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.stderr,
                                        env=env, text=True, encoding="utf-8", start_new_session=True)
        def read():
            try:
                for line in self.process.stdout:
                    self.lines.put(json.loads(line))
            finally:
                self.lines.put(None)
        self.reader = threading.Thread(target=read, daemon=True)
        self.reader.start()

    def next(self):
        value = self.lines.get(timeout=10)
        assert value is not None, "hub exited before replying"
        self.received.append(value)
        return value

    def call(self, op, **fields):
        self.counter += 1
        identifier = self.counter
        self.process.stdin.write(json.dumps(dict(id=identifier, op=op, **fields), ensure_ascii=False) + "\n")
        self.process.stdin.flush()
        while True:
            value = self.next()
            if value.get("id") == identifier and "ok" in value:
                assert value["ok"], value
                return value["result"]

    def events(self, session, run):
        self.call("events", session_id=session, run_id=run, after=0)
        result = []
        while True:
            line = self.next()
            if line.get("event") == "envelope":
                value = line["envelope"]
                assert value["run_id"] == run, value
                result.append(value)
                if value["type"] in ("run.completed", "run.failed"):
                    return result
            else:
                assert line.get("event") == "oap-subscribed", line

    def close(self):
        try:
            self.process.stdin.close()
            self.process.wait(timeout=5)
        except (BrokenPipeError, subprocess.TimeoutExpired):
            if self.process.poll() is None:
                os.killpg(self.process.pid, signal.SIGTERM)
                try:
                    self.process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(self.process.pid, signal.SIGKILL)
                    self.process.wait(timeout=3)
        finally:
            self.process.stdout.close()
            self.reader.join(timeout=2)
            self.stderr.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--oapx", default=str(ROOT / "zig/zig-out/bin/oapx"))
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    binary = output / "goap"
    built = subprocess.run(["go", "build", "-o", str(binary), "./go/cmd/goap"], cwd=ROOT,
                           capture_output=True, text=True, timeout=180)
    (output / "build.txt").write_text(built.stdout + built.stderr)
    assert built.returncode == 0, built.stdout + built.stderr
    pin = next(x for x in json.loads((ROOT / "harnesses/copilot-cli.json").read_text())["versions"]
               if x["status"] == "current")
    native_log = output / "native.jsonl"
    traces = []
    with tempfile.TemporaryDirectory(prefix="oap-copilot-home-") as home:
        env = {"HOME": home, "PATH": os.defpath, "OAP_SYNTHETIC_LOG": str(native_log),
               "OAP_SYNTHETIC_VERSION": pin["endpoint_version"]}
        refusal = subprocess.run([args.oapx, "hub", "--stdio", "--no-save-oapx-sessions",
                                  "--config", str(ROOT / "examples/oap-serve-go-copilot.json")],
                                 cwd=ROOT, env={"HOME": home, "PATH": os.defpath},
                                 input="", capture_output=True, text=True, timeout=15)
        (output / "zig-registry-refusal.txt").write_text(refusal.stdout + refusal.stderr)
        assert refusal.returncode != 0 and '"copilot"' in refusal.stderr and "does not know" in refusal.stderr, refusal.stderr
        config = output / "registry.json"
        config.write_text(json.dumps({"adapters": {"copilot": {
            "type": "copilot", "executable": sys.executable,
            "args": [str(Path(__file__).with_name("copilot-synthetic-server.py"))],
            "working_directory": home, "environment": list(env), "journal_capacity": 256}}}, indent=2))
        hub = Hub(binary, config, env, output)
        try:
            caps = hub.call("capabilities", adapter="copilot")
            revision = caps["capability_revision"]
            assert revision == pin["capability_revision"]
            cap_request = envelope("capabilities.request", caps["in_reply_to"], {})
            for name, texts in [("text", ["hello", "world"]), ("unicode", ["\u4f60\u597d"]), ("tool", ["tool"])]:
                session = "proof-" + name
                opening = envelope("session.open.request", "open-" + name, {"session_id": session}, session, revision)
                opened = hub.call("open", adapter="copilot", request=opening)
                request = envelope("session.message.submit.request", "submit-" + name,
                                   {"session_id": session, "delivery": "auto", "messages": [
                                       {"role": "user", "content": text} for text in texts]}, session, revision)
                ack = hub.call("submit", session_id=session, request=request)
                events = hub.events(session, ack["payload"]["run_id"])
                terminal = events[-1]
                assert terminal["type"] == "run.completed", terminal
                assert terminal["payload"]["final_response"]["content"][0]["text"] == "\n\n".join(texts)
                assert terminal["payload"]["usage"]["input_tokens"] == 9007199254740993
                if name == "tool":
                    assert {"action.call.requested", "action.call.started", "action.call.progress", "action.call.completed"} <= {e["type"] for e in events}
                traces.append((name, [cap_request, caps, opening, opened, request, ack] + events))
                assert hub.call("state", session_id=session)["payload"]["status"] == "idle"
                hub.call("close", session_id=session)
            session = "proof-queue"
            opening = envelope("session.open.request", "open-queue", {"session_id": session}, session, revision)
            opened = hub.call("open", adapter="copilot", request=opening)
            requests, acks = [], []
            for index, text in enumerate(["hold-first", "finish-queue"]):
                request = envelope("session.message.submit.request", f"queue-{index}",
                                   {"session_id": session, "delivery": "auto", "messages": [{"role": "user", "content": text}]}, session, revision)
                requests.append(request)
                acks.append(hub.call("submit", session_id=session, request=request))
            assert acks[1]["payload"]["admission"] == "queued", acks[1]
            first, second = [hub.events(session, ack["payload"]["run_id"]) for ack in acks]
            assert [e["type"] for e in first] == ["run.started", "run.completed"]
            traces.append(("queue", [cap_request, caps, opening, opened, requests[0], acks[0], first[0],
                                     requests[1], acks[1]] + first[1:] + second))
            hub.call("close", session_id=session)
        finally:
            hub.close()
            (output / "hub-wire.json").write_text(json.dumps(hub.received, indent=2, ensure_ascii=False) + "\n")
    assert hub.process.returncode == 0, hub.process.returncode
    for name, trace in traces:
        path = output / (name + ".json")
        path.write_text(json.dumps(trace, indent=2, ensure_ascii=False) + "\n")
        for label, command in [("go", [str(binary), "validate", str(path)]),
                               ("zig", [args.oapx, "validate", str(path)])]:
            result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=60)
            (output / f"{name}-{label}.txt").write_text(result.stdout + result.stderr)
            assert result.returncode == 0, result.stdout + result.stderr
            print(label, name, "accepted", flush=True)
    for entry in map(json.loads, native_log.read_text().splitlines()):
        if "pid" in entry:
            try:
                os.kill(entry["pid"], 0)
            except ProcessLookupError:
                continue
            raise AssertionError("synthetic native process survived shutdown")
    print("PASS: public Go hub -> registered Copilot adapter -> synthetic Content-Length process; text boundaries, Unicode, tools, exact usage and queue promotion observed; both validators accepted; temporary HOME removed and all children stopped")


if __name__ == "__main__":
    main()
