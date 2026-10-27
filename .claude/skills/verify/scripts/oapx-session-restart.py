#!/usr/bin/env python3
"""Observe served oapx history across process restarts without real credentials."""
import argparse
import http.server
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid

ROOT = Path(__file__).resolve().parents[4]
PROMPT = "synthetic first turn 1170"
REPLY = "synthetic saved reply 1170"
FOLLOWUP = "synthetic second turn 1170"


class Provider(http.server.BaseHTTPRequestHandler):
    requests = []

    def log_message(self, *args):
        pass

    def do_GET(self):
        body = json.dumps({"data": [{"id": "synthetic-model"}]}).encode()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.requests.append(body)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for delta, finish in [({"role": "assistant", "content": REPLY}, None), ({}, "stop")]:
            data = {"id": "synthetic-completion", "object": "chat.completion.chunk",
                    "model": "synthetic-model", "choices": [{"index": 0, "delta": delta,
                                                              "finish_reason": finish}]}
            self.wfile.write(("data: " + json.dumps(data) + "\n\n").encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


class Hub:
    def __init__(self, command, environment, root, output, label, flags=()):
        self.log = (output / (label + ".jsonl")).open("w")
        self.errors = (output / (label + "-stderr.txt")).open("w")
        self.process = subprocess.Popen(command + ["serve", "--stdio", "--config", str(root / "hub.json"), *flags],
                                        cwd=root, env=environment, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=self.errors, text=True)
        self.lines = queue.Queue()
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()
        self.number = 0

    def read(self):
        for line in self.process.stdout:
            self.log.write(line)
            self.log.flush()
            self.lines.put(json.loads(line))
        self.lines.put(None)

    def call(self, op, **fields):
        self.number += 1
        request = {"id": self.number, "op": op, **fields}
        self.log.write(json.dumps({"sent": request}) + "\n")
        self.log.flush()
        self.process.stdin.write(json.dumps(request) + "\n")
        self.process.stdin.flush()
        while True:
            response = self.lines.get(timeout=30)
            assert response is not None, "hub exited; inspect stderr"
            if response.get("id") == self.number:
                assert "error" not in response, response
                return response["result"]

    def stop(self):
        try:
            self.process.stdin.close()
            self.process.wait(timeout=15)
        finally:
            if self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=10)
            self.reader.join(timeout=5)
            self.process.stdout.close()
            self.errors.close()
            self.log.close()
        assert self.process.returncode == 0, self.process.returncode


def envelope(kind, payload):
    return {"protocol": "open-agent-protocol", "version": "0.1",
            "profile": "open-agent-protocol.agent-control-core", "type": kind,
            "id": str(uuid.uuid4()), "payload": payload}


def open_session(hub, session_id, reopen=False):
    result = hub.call("open", adapter="loop", request=envelope("session.open.request", {
        "session_id": session_id, "reopen": reopen}))
    assert "error" not in result.get("payload", {}), result
    return result


def submit(hub, session_id, text):
    request = envelope("session.message.submit.request", {"session_id": session_id,
        "delivery": "auto", "messages": [{"role": "user", "content": text}]})
    request["session_id"] = session_id
    result = hub.call("submit", session_id=session_id, request=request)
    assert "error" not in result.get("payload", {}), result
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        state = hub.call("state", session_id=session_id)
        if state["payload"].get("status") == "idle":
            return
        time.sleep(0.05)
    raise AssertionError("run did not return to idle")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--binary", default=str(ROOT / "zig/zig-out/bin/oapx"))
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="oap-session-restart-") as temporary:
        root = Path(temporary).resolve()
        home = root / "home"
        data = home / ".oapx"
        data.mkdir(parents=True)
        binary = root / "oapx"
        shutil.copy2(args.binary, binary)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider)
        server.daemon_threads = True
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{server.server_port}/v1"
        (data / "providers.json").write_text(json.dumps({"providers": [{"id": "synthetic",
            "api": "openai-completions", "base_url": base, "auth": "none",
            "models": ["synthetic-model"]}]}))
        (data / "config.json").write_text(json.dumps({"model": "synthetic-model",
            "provider": "synthetic", "api": "openai-completions", "workspace": str(root)}))
        (root / "hub.json").write_text(json.dumps({"adapters": {"loop": {"type": "oapx"}}}))
        environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                       "OAPX_KEYCHAIN_SERVICE": "oap-verify-" + str(uuid.uuid4())}
        command = [str(binary)]
        if sys.platform == "darwin":
            profile = root / "isolation.sb"
            profile.write_text('(version 1) (allow default)\n'
                '(deny network*)\n'
                '(allow network-outbound (remote ip "localhost:*"))\n'
                '(deny mach-lookup (global-name-regex #".*[Ss]ecurity.*"))\n'
                f'(deny file-read* (subpath {json.dumps(str(Path.home()))}))\n')
            command = ["/usr/bin/sandbox-exec", "-f", str(profile), str(binary)]
        hub = None
        try:
            hub = Hub(command, environment, root, output, "opt-out", flags=("--no-save-oapx-sessions",))
            assert "session.open.reopen" not in json.dumps(hub.call("capabilities", adapter="loop"))
            open_session(hub, "unsaved-proof")
            submit(hub, "unsaved-proof", "synthetic unsaved turn")
            hub.stop()
            hub = None
            assert not any(p.is_file() for p in (data / "sessions").rglob("*")), "opt-out saved transcript"
            hub = Hub(command, environment, root, output, "before")
            capabilities = hub.call("capabilities", adapter="loop")
            assert "session.open.reopen" in json.dumps(capabilities), capabilities
            open_session(hub, "restart-proof")
            submit(hub, "restart-proof", PROMPT)
            before = hub.call("work.read", session_id="restart-proof")
            assert PROMPT in json.dumps(before) and REPLY in json.dumps(before), before
            hub.stop()
            hub = None
            saved = list((data / "sessions").rglob("*"))
            assert any(p.is_file() for p in saved), "no saved session files"
            hub = Hub(command, environment, root, output, "after")
            restored = hub.call("work.read", session_id="restart-proof")
            assert PROMPT in json.dumps(restored) and REPLY in json.dumps(restored), restored
            assert [(t["role"], t["text"]) for t in restored["turns"]] == [(t["role"], t["text"]) for t in before["turns"]], restored
            open_session(hub, "restart-proof", reopen=True)
            assert hub.call("state", session_id="restart-proof")["payload"]["recovery"]["recovered"] is True
            submit(hub, "restart-proof", FOLLOWUP)
            transcript = hub.call("work.read", session_id="restart-proof")
            assert all(text in json.dumps(transcript) for text in (PROMPT, REPLY, FOLLOWUP)), transcript
            assert any(all(text in json.dumps(r.get("messages", [])) for text in (PROMPT, REPLY, FOLLOWUP))
                       for r in Provider.requests), "reopened loop did not send prior history to provider"
            hub.stop()
            hub = None
            assert not list(home.rglob("auth.json")), "credential file was written"
            (output / "provider-requests.json").write_text(json.dumps(Provider.requests, indent=2))
            (output / "restored.json").write_text(json.dumps(transcript, indent=2))
            print("PASS: serve saved synthetic user/reply; process exited 0; fresh serve read and reopened history; next provider request contained both old turns and new prompt; no credential file")
        finally:
            if hub is not None:
                hub.stop()
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == "__main__":
    main()
