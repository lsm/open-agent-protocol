#!/usr/bin/env python3
"""Drives a real `oapx serve --stdio` against a scripted model and reports when a
job's result reaches the session. Usage: shell-job-wake.py <oapx binary>."""
import http.server
import json
import os
import pathlib
import queue
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid

NOTICE = "SYSTEM NOTIFICATION"
OUTPUT = re.compile(r"\.oxap/jobs/job-[0-9a-z-]+\.log")
requests = []
plan = []


class Model(http.server.BaseHTTPRequestHandler):
    workspace = ""

    def log_message(self, *_):
        pass

    def do_GET(self):
        self.reply("application/json", json.dumps({"data": [{"id": "probe-model"}]}).encode())

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = request.get("messages") or [{}]
        last_reply = max((i for i, m in enumerate(messages) if m.get("role") == "assistant"), default=-1)
        requests.append({"at": time.monotonic(), "notice": any(NOTICE in json.dumps(m.get("content", "")) for m in messages[last_reply + 1:])})
        if plan:
            arguments = dict(plan.pop(0), description="probe", workspace_root=self.workspace)
            name = "Shell"
            if "path" in arguments:
                name = "ShellJob"
                arguments.update(path=OUTPUT.findall(json.dumps(messages))[-1], wake_on="ready")
            deltas = [{"role": "assistant", "tool_calls": [{"index": 0, "id": "call_" + uuid.uuid4().hex[:12], "type": "function",
                       "function": {"name": name, "arguments": json.dumps(arguments)}}]}]
            finish = "tool_calls"
        else:
            deltas, finish = [{"role": "assistant", "content": "ok"}], "stop"
        chunks = [{"choices": [{"index": 0, "delta": d, "finish_reason": None}]} for d in deltas]
        chunks.append({"choices": [{"index": 0, "delta": {}, "finish_reason": finish}]})
        body = "".join("data: " + json.dumps(dict(c, id="probe", object="chat.completion.chunk", model="probe-model")) + "\n\n" for c in chunks)
        self.reply("text/event-stream", (body + "data: [DONE]\n\n").encode())

    def reply(self, kind, body):
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def envelope(kind, payload, **head):
    return {"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core",
            "type": kind, "id": str(uuid.uuid4()), "payload": payload, **head}


class Hub:
    def __init__(self, binary, root, environment, work):
        command = [str(binary), "serve", "--stdio", "--config", str(root / "hub.json"), "--session-history", str(root / "sessions.jsonl")]
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                        env=environment, cwd=work, text=True, bufsize=1)
        self.replies, self.events, self.number = queue.Queue(), queue.Queue(), 0
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        for line in self.process.stdout:
            message = json.loads(line)
            if message.get("event") == "envelope":
                self.events.put((time.monotonic(), message["envelope"]))
            elif "ok" in message:
                self.replies.put(message)

    def call(self, op, **fields):
        self.number += 1
        self.process.stdin.write(json.dumps({"id": self.number, "op": op, **fields}) + "\n")
        self.process.stdin.flush()
        while True:
            reply = self.replies.get(timeout=30)
            if reply.get("id") == self.number:
                if not reply["ok"]:
                    raise RuntimeError(json.dumps(reply)[:300])
                return reply["result"]

    def until(self, wanted, timeout):
        limit = time.monotonic() + timeout
        while True:
            at, event = self.events.get(timeout=max(limit - time.monotonic(), 0.01))
            if wanted(event):
                return at, event


def submit(hub, session, calls):
    plan.extend(calls)
    requests.clear()
    payload = {"session_id": session, "delivery": "auto", "messages": [{"role": "user", "content": "go"}]}
    hub.call("submit", session_id=session, request=envelope("session.message.submit.request", payload, session_id=session))
    return time.monotonic()


def is_type(*kinds):
    return lambda event: event["type"] in kinds


def idle(hub, session):
    began = submit(hub, session, [{"command": "sleep 7; echo finished"}])
    hub.until(is_type("action.call.completed"), 30)
    returned = time.monotonic()
    settled, _ = hub.until(is_type("run.completed"), 30)
    woke, started = hub.until(is_type("run.started"), 30)
    trigger = started["payload"].get("trigger")
    assert trigger and trigger["kind"] == "background_task", started
    hub.until(is_type("run.completed"), 30)
    assert 4.5 < returned - began < 6.5, returned - began
    assert requests[-1]["notice"], "the model was not told"
    print(f"idle: call returned at {returned - began:.2f}s, run settled at {settled - began:.2f}s, the job's run started at {woke - began:.2f}s, model told: yes")


def mid_turn(hub, session):
    began = submit(hub, session, [{"command": "sleep 7; echo finished"}, {"command": "sleep 4"}])
    hub.until(is_type("action.call.completed"), 30)
    returned = time.monotonic()
    limit = time.monotonic() + 3
    while len(requests) < 2 and time.monotonic() < limit:
        time.sleep(0.005)
    second = requests[1]["at"] - began
    settled, _ = hub.until(is_type("run.completed"), 30)
    assert 4.5 < returned - began < 6.5, returned - began
    assert 4.5 < second < 6.5, second
    told = [r["at"] - began for r in requests if r["notice"]]
    assert told, "the model was not told"
    try:
        hub.until(is_type("run.started"), 3)
        raise AssertionError("a run followed a folded notice")
    except queue.Empty:
        pass
    print(f"mid-turn: call returned at {returned - began:.2f}s, model working again at {second:.2f}s, told at {told[0]:.2f}s, run settled at {settled - began:.2f}s, no further run")


def match_wake(hub, session):
    began = submit(hub, session, [{"command": "sleep 6; echo server ready; echo x; sleep 3; echo done"}, {"path": None}])
    hub.until(is_type("run.completed"), 30)
    woke, started = hub.until(is_type("run.started"), 30)
    assert started["payload"].get("trigger"), started
    hub.until(is_type("run.completed"), 30)
    told = requests[-1]["notice"]
    ended, last = hub.until(is_type("run.started"), 30)
    hub.until(is_type("run.completed"), 30)
    assert told and requests[-1]["notice"], "the model was not told"
    print(f"match: woken at {woke - began:.2f}s, woken again at the job's end at {ended - began:.2f}s")


def main():
    binary = pathlib.Path(sys.argv[1]).resolve()
    root = pathlib.Path(tempfile.mkdtemp(prefix="oap-job-wake-"))
    home, work = root / "home", root / "work"
    (home / ".oapx").mkdir(parents=True)
    work.mkdir()
    Model.workspace = str(work)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    port = server.server_address[1]
    (home / ".oapx" / "providers.json").write_text(json.dumps({"providers": [{"id": "probe", "api": "openai-completions",
        "base_url": f"http://127.0.0.1:{port}/v1", "auth": "none", "models": ["probe-model"]}]}))
    (home / ".oapx" / "config.json").write_text(json.dumps({"model": "probe-model", "provider": "probe", "api": "openai-completions", "workspace": str(work)}))
    (root / "hub.json").write_text(json.dumps({"adapters": {"loop": {"type": "oapx"}}}))
    environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root), "OAPX_KEYCHAIN_SERVICE": "oap-probe-" + uuid.uuid4().hex}
    hub = Hub(binary, root, environment, work)
    try:
        metadata = {"oapx": {"permission_mode": "bypass", "workspace_root": str(work)}}
        hub.call("open", adapter="loop", request=envelope("session.open.request", {"session_id": "probe", "metadata": metadata}))
        hub.call("events", session_id="probe")
        idle(hub, "probe")
        mid_turn(hub, "probe")
        match_wake(hub, "probe")
    finally:
        hub.process.terminate()
        hub.process.wait(timeout=10)
        server.shutdown()
        shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    main()
