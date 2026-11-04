import json, os, socket, subprocess, sys, tempfile, threading, time
root = tempfile.mkdtemp(); home = root + "/home"; os.makedirs(home + "/.oapx")
mode = {"next": "stall"}
def frame(d, fin=None):
    c = {"choices": [{"index": 0, "delta": d, "finish_reason": fin}]}
    b = "data: " + json.dumps(c) + "\n\n"
    return b"%x\r\n%s\r\n" % (len(b), b.encode())
def serve(conn):
    data = b""
    try:
        while True:
            while b"\r\n\r\n" not in data:
                x = conn.recv(65536)
                if not x: return
                data += x
            head, rest = data.split(b"\r\n\r\n", 1)
            size = int([l for l in head.split(b"\r\n") if l.lower().startswith(b"content-length")][0].split(b":")[1])
            while len(rest) < size: rest += conn.recv(65536)
            data = rest[size:]
            conn.sendall(b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n")
            m = mode["next"]
            if m == "stall":
                time.sleep(60); return
            if m == "text":
                conn.sendall(frame({"content": "ok"}, "stop") + b"0\r\n\r\n")
            else:
                call = {"tool_calls": [{"index": 0, "id": "c1", "function": {"name": "Shell", "arguments": json.dumps({"command": "sleep 300"})}}]}
                conn.sendall(frame(call, "tool_calls") + b"0\r\n\r\n")
    except OSError:
        pass
srv = socket.create_server(("127.0.0.1", 0)); port = srv.getsockname()[1]
threading.Thread(target=lambda: [threading.Thread(target=serve, args=(srv.accept()[0],), daemon=True).start() for _ in iter(int, 1)], daemon=True).start()
open(home + "/.oapx/providers.json", "w").write(json.dumps({"base_url": f"http://127.0.0.1:{port}/v1"}))
hub = subprocess.Popen([sys.argv[1], "serve"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1, env={"HOME": home, "TMPDIR": root, "PATH": "/usr/bin:/bin"})
n = [0]
def call(op, **f):
    n[0] += 1
    hub.stdin.write(json.dumps({"id": n[0], "op": op, **f}) + "\n"); hub.stdin.flush()
    return json.loads(hub.stdout.readline())
def opened(s, mode_):
    return call("open", request={"payload": {"session_id": s, "metadata": {"oapx": {"workspace_root": root, "permission_mode": mode_}}}})
def submit(s): return call("submit", session_id=s, request={"payload": {"messages": [{"content": "go"}]}})
def state(s): return call("state", session_id=s)["result"]["payload"]
def wait(s, test, seconds=5):
    end = time.time() + seconds
    while time.time() < end:
        st = state(s)
        if test(st): return st
        time.sleep(0.02)
    raise SystemExit(f"timed out on {s}: {st}")
idle = lambda st: st["status"] == "idle"
gated = lambda st: any(r.get("pending_interactions") for r in st["active_runs"])
children = lambda: subprocess.run(["pgrep", "-f", "sleep 300"], capture_output=True, text=True).stdout.split()
before = set(children())
opened("model", "bypass"); mode["next"] = "stall"; submit("model"); time.sleep(0.2)
started = time.time(); call("cancel", session_id="model"); wait("model", idle); print("cancel in model: idle after %.0f ms" % ((time.time() - started) * 1000))
mode["next"] = "text"; submit("model"); wait("model", idle); print("the same session answers after a cancel")
mode["next"] = "tool"; opened("gate", "ask"); submit("gate"); wait("gate", gated)
started = time.time(); call("cancel", session_id="gate"); wait("gate", idle); print("cancel at gate: idle after %.0f ms" % ((time.time() - started) * 1000))
opened("tool", "bypass"); submit("tool"); time.sleep(0.5)
spawned = set(children()) - before
assert spawned, "no child ran"
started = time.time(); call("cancel", session_id="tool"); wait("tool", idle)
end = time.time() + 3
while time.time() < end and set(children()) & spawned: time.sleep(0.02)
print("cancel in tool: idle, child gone:", not (set(children()) & spawned), "after %.0f ms" % ((time.time() - started) * 1000))
hub.kill()
