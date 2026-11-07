#!/usr/bin/env python3
"""Observe idle HTTP waits, header expiry, native SSE keepalive and shutdown."""
import argparse
import http.client
import importlib.util
import json
import os
from pathlib import Path
import select
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location("bench_resources", ROOT / "scripts/bench-resources.py")
bench = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench)


def stop(process):
    if process.poll() is None:
        process.terminate()
    try:
        process.wait(timeout=15)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)
    return {"exit": process.returncode, "reaped": process.poll() is not None}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--binary", default=str(ROOT / "zig/zig-out/bin/oapx"))
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="oap-http-deadlines-") as temporary:
        root = Path(temporary)
        home, work = root / "home", root / "work"
        home.mkdir()
        work.mkdir()
        environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                       "OAPX_KEYCHAIN_SERVICE": "oap-http-deadlines-" + uuid.uuid4().hex}
        with (output / "provider.log").open("w") as provider_log, (output / "server.log").open("w") as server_log:
            provider = subprocess.Popen([sys.executable, "-I", str(ROOT / "scripts/bench-resources.py"),
                                         "provider", "--workspace", str(work)], env=environment,
                                        stdout=subprocess.PIPE, stderr=provider_log, text=True, start_new_session=True)
            server = None
            cleanup = {}
            try:
                assert select.select([provider.stdout], [], [], 15)[0], "provider did not announce a port"
                provider_port = int(provider.stdout.readline())
                settings = home / ".oapx"
                settings.mkdir()
                (settings / "providers.json").write_text(json.dumps({"providers": [{"id": "bench", "api": "openai-completions",
                    "base_url": f"http://127.0.0.1:{provider_port}/v1", "auth": "none", "models": ["bench-model"]}]}))
                (settings / "config.json").write_text(json.dumps({"model": "bench-model", "provider": "bench",
                    "api": "openai-completions", "workspace": str(work)}))
                config = root / "hub.json"
                config.write_text(json.dumps({"adapters": {"loop": {"type": "oapx"}}}))
                copied = root / "oapx"
                shutil.copy2(args.binary, copied)
                with socket.socket() as reservation:
                    reservation.bind(("127.0.0.1", 0))
                    port = reservation.getsockname()[1]
                command = bench.sandboxed([str(copied), "serve", "--addr", f"127.0.0.1:{port}", "--config", str(config)], root)
                profile = root / "isolation.sb"
                if profile.exists():
                    with profile.open("a") as rules:
                        rules.write('(allow network-bind (local ip "localhost:*"))\n'
                                    '(allow network-inbound (local ip "localhost:*"))\n')
                server = subprocess.Popen(command, cwd=root, env=environment, stdin=subprocess.DEVNULL,
                                          stdout=server_log, stderr=server_log, start_new_session=True)

                def request(path, payload=None):
                    data = json.dumps(payload).encode() if payload is not None else None
                    req = urllib.request.Request(f"http://127.0.0.1:{port}" + path, data=data,
                                                 headers={"Content-Type": "application/json"})
                    with urllib.request.urlopen(req, timeout=10) as response:
                        return None if response.status == 204 else json.load(response)

                deadline = time.monotonic() + 15
                while True:
                    try:
                        request("/adapters")
                        break
                    except OSError:
                        assert server.poll() is None
                        assert time.monotonic() < deadline, "Hub did not listen"
                        time.sleep(0.02)
                time.sleep(10)
                probe = bench.Probe(server.pid)
                before = probe.sample()
                idle_started = time.monotonic()
                time.sleep(5)
                after = probe.sample()
                idle = {"idle_settle_s": 10, "idle_wall_s": time.monotonic() - idle_started,
                        "idle_switches": after["switches"] - before["switches"],
                        "idle_cpu_s": after["cpu"] - before["cpu"]}
                (output / "idle.json").write_text(json.dumps(idle, indent=2))
                assert idle["idle_switches"] == 0 and idle["idle_cpu_s"] == 0, idle
                with socket.create_connection(("127.0.0.1", port), timeout=35) as partial:
                    partial.sendall(f"GET /adapters HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n".encode())
                    started = time.monotonic()
                    head = b""
                    while b"\r\n\r\n" not in head:
                        chunk = partial.recv(4096)
                        assert chunk, head
                        head += chunk
                    (output / "header-timeout.txt").write_bytes(head)
                    assert head.startswith(b"HTTP/1.1 408 "), head
                    (output / "header-timing.json").write_text(json.dumps({"seconds": time.monotonic() - started}))
                opened = request("/adapters/loop/sessions", bench.envelope("session.open.request", {"session_id": "deadline-session", "subscribe": True}))
                (output / "opened.json").write_text(json.dumps(opened, indent=2))
                connection = http.client.HTTPConnection("127.0.0.1", port, timeout=20)
                try:
                    connection.request("GET", "/sessions/deadline-session/events")
                    response = connection.getresponse()
                    assert response.status == 200
                    lines = []
                    while True:
                        line = response.readline()
                        assert line, lines
                        lines.append(line.decode())
                        if line == b": keepalive\n":
                            break
                    (output / "keepalive.txt").write_text("".join(lines))
                finally:
                    connection.close()
                request("/sessions/deadline-session/close", {})
                server.send_signal(signal.SIGTERM)
                assert server.wait(timeout=10) == 0
                assert not list(home.rglob("auth.json")), "child wrote a credential file"
                print("PASS HTTP: zero settled idle CPU/switches; partial header expired; native SSE keepalive arrived; SIGTERM exited 0")
            finally:
                if server is not None:
                    cleanup["hub"] = stop(server)
                cleanup["provider"] = stop(provider)
                (output / "cleanup.json").write_text(json.dumps(cleanup, indent=2))


if __name__ == "__main__":
    main()
