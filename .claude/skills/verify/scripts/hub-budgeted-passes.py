#!/usr/bin/env python3
"""Exercise many memory sessions through the real HTTP Hub."""
import argparse
import concurrent.futures
import importlib.util
import http.client
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location("bench_resources", ROOT / "scripts/bench-resources.py")
bench = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--binary", default=str(ROOT / "zig/zig-out/bin/oapx"))
    parser.add_argument("--hold-expiry", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="oap-hub-passes-") as temporary:
        root = Path(temporary)
        home = root / "home"
        home.mkdir()
        binary = root / "oapx"
        shutil.copy2(args.binary, binary)
        config = root / "hub.json"
        config.write_text(json.dumps({"adapters": {"memory": {"type": "memory"}}}))
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        command = bench.sandboxed([str(binary), "serve", "--addr", f"127.0.0.1:{port}",
                                   "--config", str(config), "--no-save-oapx-sessions"], root)
        profile = root / "isolation.sb"
        if profile.exists():
            with profile.open("a") as rules:
                rules.write('(allow network-bind (local ip "localhost:*"))\n'
                            '(allow network-inbound (local ip "localhost:*"))\n')
        environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                       "OAPX_KEYCHAIN_SERVICE": "oap-hub-passes-" + uuid.uuid4().hex}
        with (output / "server.log").open("w") as log:
            process = subprocess.Popen(command, cwd=root, env=environment, stdout=log,
                                       stderr=log, start_new_session=True)
            try:
                def request(path, payload=None, expected_status=200):
                    data = json.dumps(payload).encode() if payload is not None else None
                    req = urllib.request.Request(f"http://127.0.0.1:{port}" + path, data=data,
                                                 headers={"Content-Type": "application/json"})
                    with urllib.request.urlopen(req, timeout=10) as response:
                        assert response.status == expected_status
                        return None if response.status == 204 else json.load(response)

                deadline = time.monotonic() + 15
                while True:
                    try:
                        request("/adapters")
                        break
                    except OSError:
                        assert process.poll() is None
                        if time.monotonic() >= deadline:
                            raise
                        time.sleep(0.05)
                sessions = [f"passes-{index}" for index in range(80)]
                for session in sessions:
                    opened = request("/adapters/memory/sessions", bench.envelope(
                        "session.open.request", {"session_id": session, "subscribe": args.hold_expiry}))
                    assert opened["payload"]["session_id"] == session

                def submit(session):
                    payload = {"session_id": session, "delivery": "auto",
                               "messages": [{"role": "user", "content": "run"}]}
                    reply = request(f"/sessions/{session}/submit", bench.envelope(
                        "session.message.submit.request", payload, session_id=session))
                    assert reply["payload"]["accepted"]

                with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
                    list(pool.map(submit, sessions))
                states = {}
                deadline = time.monotonic() + 15
                while len(states) < len(sessions):
                    for session in sessions:
                        if session in states:
                            continue
                        state = request(f"/sessions/{session}/state")["payload"]
                        pending = [item for run in state.get("active_runs", [])
                                   for item in run.get("pending_interactions", [])]
                        if pending:
                            states[session] = state
                    assert time.monotonic() < deadline, sorted(set(sessions) - set(states))
                (output / "states.json").write_text(json.dumps(states, indent=2))
                if args.hold_expiry:
                    with socket.create_connection(("127.0.0.1", port), timeout=10) as pending:
                        pending.sendall(f"GET /sessions/{sessions[0]}/events HTTP/1.1\r\nHost: 127.0.0.1\r\n".encode())
                        request("/adapters")
                        os.kill(process.pid, signal.SIGSTOP)
                        try:
                            pending.sendall(b"\r\n")
                            time.sleep(31)
                        finally:
                            os.kill(process.pid, signal.SIGCONT)
                        with http.client.HTTPResponse(pending) as response:
                            response.begin()
                            assert response.status == 200
                            event = []
                            for _ in range(64):
                                line = response.readline().decode()
                                event.append(line)
                                if line.startswith("data:"):
                                    break
                            observed = "".join(event)
                            (output / "expired-stream.txt").write_text(observed)
                            assert "event: oap-subscribed" in observed, observed
                            joined = json.loads(line.removeprefix("data:"))
                            assert joined["joined_after"] > 0, joined
                    print("PASS an events request accepted after the stopped Hub's 30-second hold deadline starts after the expired backlog")
                for session, state in states.items():
                    run = state["active_runs"][0]["run_id"]
                    request(f"/sessions/{session}/cancel", bench.envelope(
                        "run.cancel.request", {"session_id": session, "run_id": run},
                        session_id=session, run_id=run))
                for session in sessions:
                    deadline = time.monotonic() + 10
                    while request(f"/sessions/{session}/state")["payload"].get("active_runs"):
                        assert time.monotonic() < deadline, session
                    request(f"/sessions/{session}/close", {}, expected_status=204)
                print(f"PASS HTTP Hub accepted all {len(sessions)} concurrent submissions, published every permission gate, and cancelled and closed them all")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
                (output / "cleanup.json").write_text(json.dumps({"exit": process.returncode,
                                                                 "reaped": process.poll() is not None}))


if __name__ == "__main__":
    main()
