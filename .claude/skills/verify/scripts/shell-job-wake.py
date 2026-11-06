#!/usr/bin/env python3
"""Drives a real `oapx serve --stdio` against a scripted model and reports when a
job's result reaches the session. Usage: shell-job-wake.py <oapx binary>."""
import http.server
import json
import os
import pathlib
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid

NOTICE = "SYSTEM NOTIFICATION"
requests = []


class Model(http.server.BaseHTTPRequestHandler):
    workspace = ""

    def log_message(self, *_):
        pass

    def do_GET(self):
        self.reply("application/json", json.dumps({"data": [{"id": "probe-model"}]}).encode())

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = request.get("messages") or [{}]
        content = messages[-1].get("content", "")
        text = content if isinstance(content, str) else " ".join(part.get("text", "") for part in content or [])
        requests.append({"at": time.monotonic(), "notice": any(NOTICE in json.dumps(m.get("content", "")) for m in messages[-2:])})
        marker = text.find("@shell ")
        if marker >= 0 and messages[-1].get("role") == "user":
            spec, _ = json.JSONDecoder().raw_decode(text[marker + 7:])
            arguments = dict(spec, description="probe", workspace_root=self.workspace)
            deltas = [{"role": "assistant", "tool_calls": [{"index": 0, "id": "call_" + uuid.uuid4().hex[:12], "type": "function",
                       "function": {"name": "Shell", "arguments": json.dumps(arguments)}}]}]
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


def submit(hub, session, spec):
    text = "@shell " + json.dumps(spec)
    payload = {"session_id": session, "delivery": "auto", "messages": [{"role": "user", "content": text}]}
    hub.call("submit", session_id=session, request=envelope("session.message.submit.request", payload, session_id=session))


def is_type(*kinds):
    return lambda event: event["type"] in kinds


def idle_wake(hub, session, seconds):
    began = time.monotonic()
    submit(hub, session, {"command": f"sleep {seconds}; echo finished", "run_in_background": True})
    settled, _ = hub.until(is_type("run.completed"), 30)
    woke, started = hub.until(is_type("run.started"), seconds + 30)
    trigger = started["payload"].get("trigger")
    assert trigger and trigger["kind"] == "background_task", started
    calls = len(requests)
    hub.until(is_type("run.completed"), 30)
    assert any(r["notice"] for r in requests[calls - 1:]), "the model was not told"
    print(f"idle: run settled at {settled - began:.2f}s, job's run started at {woke - began:.2f}s, trigger {trigger}")


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
        idle_wake(hub, "probe", 2)
    finally:
        hub.process.terminate()
        hub.process.wait(timeout=10)
        server.shutdown()
        shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    main()
