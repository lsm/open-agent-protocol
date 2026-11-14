#!/usr/bin/env python3
"""Drives a real `oxap serve --stdio` to show which state directory and which
environment spelling it reads. Usage: oxap-state-env.py <oxap binary>."""
import http.server
import json
import pathlib
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import uuid


class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.reply("application/json", json.dumps({"data": [{"id": "probe-model"}]}).encode())

    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        self.server.hits += 1
        chunks = [{"choices": [{"index": 0, "delta": {"role": "assistant", "content": "ok"}, "finish_reason": None}]},
                  {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}]
        body = "".join("data: " + json.dumps(dict(c, id="probe", object="chat.completion.chunk", model="probe-model")) + "\n\n" for c in chunks)
        self.reply("text/event-stream", (body + "data: [DONE]\n\n").encode())

    def reply(self, kind, body):
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def model_server():
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    server.hits = 0
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def envelope(kind, payload, **head):
    return {"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core",
            "type": kind, "id": str(uuid.uuid4()), "payload": payload, **head}


def write_state(directory, server, work):
    directory.mkdir(parents=True)
    port = server.server_address[1]
    (directory / "providers.json").write_text(json.dumps({"providers": [{"id": "probe", "api": "openai-completions",
        "base_url": f"http://127.0.0.1:{port}/v1", "auth": "none", "models": ["probe-model"]}]}))
    (directory / "config.json").write_text(json.dumps({"model": "probe-model", "provider": "probe", "api": "openai-completions", "workspace": str(work)}))


def one_turn(binary, root, home, work, extra, settings):
    (root / "hub.json").write_text(json.dumps({"adapters": {"loop": {"type": "oapx"}}}))
    environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                   "OXAP_KEYCHAIN_SERVICE": "oxap-probe-" + uuid.uuid4().hex, "OAPX_KEYCHAIN_SERVICE": "oapx-probe-" + uuid.uuid4().hex, **extra}
    command = [str(binary), "serve", "--stdio", "--config", str(root / "hub.json"), "--session-history", str(root / "sessions.jsonl")]
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                               env=environment, cwd=work, text=True, bufsize=1)
    replies, events, number = queue.Queue(), queue.Queue(), [0]

    def read():
        for line in process.stdout:
            message = json.loads(line)
            (events if message.get("event") == "envelope" else replies).put(message)

    def call(op, **fields):
        number[0] += 1
        process.stdin.write(json.dumps({"id": number[0], "op": op, **fields}) + "\n")
        process.stdin.flush()
        while True:
            reply = replies.get(timeout=30)
            if reply.get("id") == number[0]:
                if not reply["ok"]:
                    raise RuntimeError(json.dumps(reply)[:300])
                return reply["result"]

    threading.Thread(target=read, daemon=True).start()
    try:
        metadata = {"oapx": {"permission_mode": "bypass", "workspace_root": str(work)}, **settings}
        try:
            call("open", adapter="loop", request=envelope("session.open.request", {"session_id": "probe", "metadata": metadata}))
        except RuntimeError as refused:
            return "open refused: " + ("disagree" if "disagree" in str(refused) else str(refused))
        call("events", session_id="probe")
        payload = {"session_id": "probe", "delivery": "auto", "messages": [{"role": "user", "content": "go"}]}
        call("submit", session_id="probe", request=envelope("session.message.submit.request", payload, session_id="probe"))
        while True:
            kind = events.get(timeout=30)["envelope"]["type"]
            if kind in ("run.completed", "run.failed"):
                return kind
    finally:
        process.terminate()
        process.wait(timeout=10)


def case(binary, name, layout, extra=lambda root: {}, settings={}):
    root = pathlib.Path(tempfile.mkdtemp(prefix="oxap-state-env-"))
    home, work = root / "home", root / "work"
    work.mkdir()
    servers = {}
    try:
        for directory in layout:
            servers[directory] = model_server()
            write_state(home / directory, servers[directory], work)
        settled = one_turn(binary, root, home, work, extra(root), settings)
        hits = {directory: server.hits for directory, server in servers.items()}
        present = sorted(p.name for p in home.iterdir())
        dumps = sorted(p.name for p in root.glob("*.payload"))
        print(f"{name}: {settled}; requests by state dir {hits}; home holds {present}; payload dumps {dumps}")
        return settled, hits, present, dumps
    finally:
        for server in servers.values():
            server.shutdown()
        shutil.rmtree(root, ignore_errors=True)


def empty_token(binary):
    root = pathlib.Path(tempfile.mkdtemp(prefix="oxap-state-env-"))
    try:
        (root / "home").mkdir()
        environment = {"PATH": "/usr/bin:/bin", "HOME": str(root / "home"), "TMPDIR": str(root),
                       "OXAP_KEYCHAIN_SERVICE": "oxap-probe-" + uuid.uuid4().hex,
                       "OXAP_PROVIDER_SERVICE_URL": "http://127.0.0.1:9", "OXAP_PROVIDER_SERVICE_SECURITY": "loopback",
                       "OXAP_PROVIDER_SERVICE_TOKEN": "", "OAPX_PROVIDER_SERVICE_TOKEN": "probe-token"}
        done = subprocess.run([str(binary), "serve", "agent", "--stdio"], stdin=subprocess.DEVNULL, capture_output=True,
                              env=environment, cwd=root, text=True, timeout=30)
        refused = "InvalidProviderServiceToken" in done.stderr
        print(f"empty new token: exit {done.returncode}, refused as invalid: {refused}")
        return done.returncode != 0 and refused
    finally:
        shutil.rmtree(root, ignore_errors=True)


def main():
    binary = pathlib.Path(sys.argv[1]).resolve()
    assert case(binary, "new home", [".oxap"])[:3] == ("run.completed", {".oxap": 1}, [".oxap"])
    assert case(binary, "old home", [".oapx"])[:3] == ("run.completed", {".oapx": 1}, [".oapx"])
    assert case(binary, "both", [".oxap", ".oapx"])[:3] == ("run.completed", {".oxap": 1, ".oapx": 0}, [".oapx", ".oxap"])
    both = lambda root: {"OXAP_DEBUG_PROVIDER_PAYLOAD": str(root / "new.payload"), "OAPX_DEBUG_PROVIDER_PAYLOAD": str(root / "old.payload")}
    assert case(binary, "both env names", [".oapx"], both)[3] == ["new.payload"]
    old = lambda root: {"OAPX_DEBUG_PROVIDER_PAYLOAD": str(root / "old.payload")}
    assert case(binary, "old env name", [".oapx"], old)[3] == ["old.payload"]
    empty = lambda root: {"OXAP_DEBUG_PROVIDER_PAYLOAD": "", "OAPX_DEBUG_PROVIDER_PAYLOAD": str(root / "old.payload")}
    assert case(binary, "empty new env name", [".oapx"], empty)[3] == ["old.payload"]
    split = {"oxap": {"permission_mode": "ask", "workspace_root": "/"}}
    assert case(binary, "disagreeing metadata", [".oxap"], settings=split)[:2] == ("open refused: disagree", {".oxap": 0})
    assert empty_token(binary)
    print("ok")


if __name__ == "__main__":
    main()
