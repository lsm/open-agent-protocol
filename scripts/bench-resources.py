#!/usr/bin/env python3
"""Measure how each OAP hub's resource use grows with the number of agents.

Runs the same scenarios against the Zig, Go and Rust stdio hubs, each serving
its scripted memory adapter, and against oapx's own loop driven by a local
scripted provider. See docs/resource-benchmark.md for what is and is not
measured.
"""
import argparse
import ctypes
import ctypes.util
import http.server
import json
import os
import platform
import re
from pathlib import Path
import queue
import shutil
import socketserver
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
HISTORY_TURNS = 3
SCENARIOS = ("empty", "history", "waiting", "jobs", "burst")
TARGETS = ("zig", "go", "rust", "zig-loop")
LOOP_ONLY = {"jobs"}

# Layouts from the macOS SDK's <sys/proc_info.h> and <sys/resource.h>, checked with
# sizeof/offsetof: struct proc_taskinfo is 96 bytes with pti_threadnum at byte 84; in
# struct rusage_info_v4 the 16-byte ri_uuid fills uint64 slots 0-1, so ri_phys_footprint
# is slot 9 and ri_logical_writes slot 29. struct proc_fdinfo is 8 bytes.
TASKINFO_SIZE, TASKINFO_THREADS = 96, 84
RUSAGE_PHYS_FOOTPRINT, RUSAGE_LOGICAL_WRITES, FDINFO_SIZE = 9, 29, 8


class Probe:
    """Reads RSS, thread count, open descriptors and bytes written for one pid."""

    def __init__(self, pid):
        self.pid = pid
        if sys.platform == "darwin":
            self.lib = ctypes.CDLL(ctypes.util.find_library("proc") or "/usr/lib/libproc.dylib")
            self.lib.proc_pidinfo.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64, ctypes.c_void_p, ctypes.c_int]
            self.lib.proc_pid_rusage.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_void_p]
            self.task = ctypes.create_string_buffer(TASKINFO_SIZE)
            self.usage = (ctypes.c_uint64 * 64)()
            self.fds = ctypes.create_string_buffer(FDINFO_SIZE * 65536)

    def sample(self):
        if sys.platform == "darwin":
            return self._darwin()
        return self._linux()

    def _darwin(self):
        if self.lib.proc_pidinfo(self.pid, 4, 0, self.task, TASKINFO_SIZE) != TASKINFO_SIZE:
            raise ProcessLookupError(self.pid)
        threads = int.from_bytes(self.task.raw[TASKINFO_THREADS:TASKINFO_THREADS + 4], "little")
        fds = self.lib.proc_pidinfo(self.pid, 1, 0, self.fds, len(self.fds)) // FDINFO_SIZE
        if self.lib.proc_pid_rusage(self.pid, 4, self.usage) != 0:
            raise ProcessLookupError(self.pid)
        return {"rss": int(self.usage[RUSAGE_PHYS_FOOTPRINT]), "threads": threads, "fds": fds,
                "written": int(self.usage[RUSAGE_LOGICAL_WRITES])}

    def _linux(self):
        base = Path("/proc") / str(self.pid)
        status = dict(line.split(":", 1) for line in (base / "status").read_text().splitlines() if ":" in line)
        io = dict(line.split(": ") for line in (base / "io").read_text().splitlines())
        resident = int(status["VmRSS"].split()[0]) + int(status.get("VmSwap", "0 kB").split()[0])
        return {"rss": resident * 1024, "threads": int(status["Threads"]),
                "fds": len(os.listdir(base / "fd")), "written": int(io["write_bytes"])}


class Sampler(threading.Thread):
    def __init__(self, probe, process, cap, interval=0.01):
        super().__init__(daemon=True)
        self.probe, self.process, self.cap, self.interval = probe, process, cap, interval
        self.peak = {"rss": 0, "threads": 0, "fds": 0}
        self.capped = False
        self.halt = threading.Event()

    def run(self):
        while not self.halt.is_set():
            try:
                now = self.probe.sample()
            except (ProcessLookupError, FileNotFoundError):
                return
            for key in self.peak:
                self.peak[key] = max(self.peak[key], now[key])
            if now["rss"] > self.cap:
                self.capped = True
                self.process.kill()
                return
            self.halt.wait(self.interval)

    def stop(self):
        self.halt.set()
        self.join(timeout=5)


class Hub:
    def __init__(self, command, environment, cwd, stderr):
        self.process = subprocess.Popen(command, cwd=cwd, env=environment, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=stderr, text=True, bufsize=1)
        self.replies = queue.Queue()
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()
        self.number = 0

    def _read(self):
        for line in self.process.stdout:
            message = json.loads(line)
            if "id" in message:
                self.replies.put(message)
        self.replies.put(None)

    def call(self, op, **fields):
        self.number += 1
        self.process.stdin.write(json.dumps({"id": self.number, "op": op, **fields}) + "\n")
        self.process.stdin.flush()
        while True:
            reply = self.replies.get(timeout=120)
            if reply is None:
                raise RuntimeError("hub exited during " + op)
            if reply["id"] == self.number:
                if not reply.get("ok"):
                    raise RuntimeError(f"{op} failed: {json.dumps(reply)[:400]}")
                return reply["result"]

    def stop(self):
        try:
            self.process.stdin.close()
            self.process.wait(timeout=20)
        except (subprocess.TimeoutExpired, BrokenPipeError, OSError):
            pass
        if self.process.poll() is None:
            self.process.kill()
            self.process.wait(timeout=10)
        self.reader.join(timeout=5)


def envelope(kind, payload, **head):
    return {"protocol": "open-agent-protocol", "version": "0.1",
            "profile": "open-agent-protocol.agent-control-core", "type": kind,
            "id": str(uuid.uuid4()), "payload": payload, **head}


class Agents:
    """Drives sessions on one hub through the hub's stdio operations."""

    def __init__(self, hub, adapter, metadata):
        self.hub, self.adapter, self.metadata = hub, adapter, metadata

    def open(self, session, mode="bypass"):
        payload = {"session_id": session}
        if self.metadata is not None:
            payload["metadata"] = {"oapx": dict(self.metadata, permission_mode=mode)}
        self.hub.call("open", adapter=self.adapter, request=envelope("session.open.request", payload))

    def submit(self, session, text):
        payload = {"session_id": session, "delivery": "auto", "messages": [{"role": "user", "content": text}]}
        self.hub.call("submit", session_id=session, request=envelope("session.message.submit.request", payload, session_id=session))

    def state(self, session):
        return self.hub.call("state", session_id=session)["payload"]

    def pending(self, session):
        runs = self.state(session).get("active_runs") or []
        return [(run["run_id"], interaction) for run in runs for interaction in run.get("pending_interactions") or []]

    def settle(self, session, stop_at_gate=False):
        while True:
            state = self.state(session)
            runs = state.get("active_runs") or []
            if not runs and state["status"] == "idle":
                return
            gates = [(run["run_id"], i) for run in runs for i in run.get("pending_interactions") or []]
            if gates and stop_at_gate:
                return
            for run, interaction in gates:
                self.resolve(session, run, interaction)

    def resolve(self, session, run, interaction):
        head = {"interaction_id": interaction, "session_id": session, "run_id": run,
                "requested_by": "reference.memory", "responded_by": "user"}
        if interaction.startswith("permission"):
            request = envelope("action.permission.resolve.request", dict(head, choice_id="approve", granted=True),
                               session_id=session, run_id=run)
        elif interaction.startswith("input"):
            request = envelope("user.input.resolve.request", dict(head, answers=[
                {"question_id": "choice", "selected_option_ids": ["yes"]}]), session_id=session, run_id=run)
        else:
            raise RuntimeError("unexpected interaction " + interaction)
        self.hub.call("resolve", session_id=session, request=request)


def timed(steps, action, *args):
    started = time.perf_counter()
    action(*args)
    steps.append(time.perf_counter() - started)


def scenario_empty(agents, n, steps):
    for i in range(n):
        timed(steps, agents.open, f"s{i}")


def scenario_history(agents, n, steps):
    def turn(session, text):
        agents.submit(session, text)
        agents.settle(session)
    for i in range(n):
        agents.open(f"s{i}")
        for t in range(HISTORY_TURNS):
            timed(steps, turn, f"s{i}", f"turn {t} of agent {i}")


def scenario_waiting(agents, n, steps):
    def wait(session):
        agents.submit(session, "@fg run a short command")
        agents.settle(session, stop_at_gate=True)
        if not agents.pending(session):
            raise RuntimeError(session + " never reached a pending interaction")
    for i in range(n):
        agents.open(f"s{i}", mode="ask")
        timed(steps, wait, f"s{i}")


def scenario_jobs(agents, n, steps):
    def job(session):
        agents.submit(session, "@bg run a short background command")
        agents.settle(session)
    for i in range(n):
        agents.open(f"s{i}")
        timed(steps, job, f"s{i}")


def scenario_burst(agents, n, steps):
    for i in range(n):
        agents.open(f"s{i}")
    for i in range(n):
        timed(steps, agents.submit, f"s{i}", f"small command {i}")
    for i in range(n):
        agents.settle(f"s{i}")


RUNNERS = {"empty": scenario_empty, "history": scenario_history, "waiting": scenario_waiting,
           "jobs": scenario_jobs, "burst": scenario_burst}


class Provider(http.server.BaseHTTPRequestHandler):
    """A scripted openai-completions endpoint: text, or one Shell call when asked."""

    protocol_version = "HTTP/1.1"
    workspace = ""

    def log_message(self, *_):
        pass

    def do_GET(self):
        self.reply("application/json", json.dumps({"data": [{"id": "bench-model"}]}).encode())

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = request.get("messages") or [{}]
        last = messages[-1]
        text = json.dumps(last.get("content", ""))
        call = None
        if request.get("tools") and last.get("role") == "user":
            if "@bg" in text:
                call = {"command": "true", "run_in_background": True}
            elif "@fg" in text:
                call = {"command": "true"}
        if call is None:
            deltas = [{"role": "assistant", "content": "ok"}]
            finish = "stop"
        else:
            arguments = dict(call, description="benchmark", workspace_root=self.workspace)
            deltas = [{"role": "assistant", "tool_calls": [{"index": 0, "id": "call_" + uuid.uuid4().hex[:12],
                       "type": "function", "function": {"name": "Shell", "arguments": json.dumps(arguments)}}]}]
            finish = "tool_calls"
        chunks = [{"choices": [{"index": 0, "delta": d, "finish_reason": None}]} for d in deltas]
        chunks.append({"choices": [{"index": 0, "delta": {}, "finish_reason": finish}]})
        body = "".join("data: " + json.dumps(dict(c, id="bench", object="chat.completion.chunk", model="bench-model")) + "\n\n"
                       for c in chunks) + "data: [DONE]\n\n"
        self.reply("text/event-stream", body.encode())

    def reply(self, kind, body):
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class ProviderServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    request_queue_size = 1024


def serve_provider(arguments):
    Provider.workspace = arguments.workspace
    server = ProviderServer(("127.0.0.1", 0), Provider)
    print(server.server_port, flush=True)
    server.serve_forever()


def sandboxed(command, root):
    if sys.platform != "darwin":
        return command
    profile = root / "isolation.sb"
    profile.write_text("(version 1) (allow default)\n(deny network*)\n"
                       '(allow network-outbound (remote ip "localhost:*"))\n'
                       '(deny mach-lookup (global-name-regex #".*[Ss]ecurity.*"))\n'
                       f"(deny file-read* (subpath {json.dumps(str(Path.home()))}))\n")
    return ["/usr/bin/sandbox-exec", "-f", str(profile), *command]


def stored_bytes(root):
    return sum(p.stat().st_size for p in root.rglob("*") if p.is_file() and p.name != "hub-stderr.txt")


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(fraction * len(ordered)))] if ordered else 0.0


def cpu_seconds(pid):
    text = subprocess.run(["ps", "-o", "cputime=", "-p", str(pid)], capture_output=True, text=True).stdout.strip()
    if not text:
        return 0.0
    total = 0.0
    for part in text.replace("-", ":").split(":"):
        total = total * 60 + float(part)
    return total


def run_case(target, binary, scenario, n, scratch, timeout, cap):
    root = Path(tempfile.mkdtemp(prefix=f"{target}-{scenario}-{n}-", dir=scratch))
    home, work, data = root / "home", root / "work", root / "data"
    for directory in (home, work, data):
        directory.mkdir()
    environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                   "OAPX_KEYCHAIN_SERVICE": "oap-bench-" + uuid.uuid4().hex}
    adapter, metadata, provider = "memory", None, None
    config = {"adapters": {"memory": {"type": "memory"}}}
    if target == "zig-loop":
        provider = subprocess.Popen([sys.executable, "-I", __file__, "provider", "--workspace", str(work)],
                                    stdout=subprocess.PIPE, text=True)
        port = int(provider.stdout.readline())
        oapx = home / ".oapx"
        oapx.mkdir()
        (oapx / "providers.json").write_text(json.dumps({"providers": [{"id": "bench", "api": "openai-completions",
            "base_url": f"http://127.0.0.1:{port}/v1", "auth": "none", "models": ["bench-model"]}]}))
        (oapx / "config.json").write_text(json.dumps({"model": "bench-model", "provider": "bench",
            "api": "openai-completions", "workspace": str(work)}))
        adapter, metadata = "loop", {"workspace_root": str(work)}
        config = {"adapters": {"loop": {"type": "oapx"}}}
    (root / "hub.json").write_text(json.dumps(config))
    command = sandboxed([str(binary), "serve", "--stdio", "--config", str(root / "hub.json"),
                         "--session-history", str(data / "sessions.jsonl")], root)
    stderr = (root / "hub-stderr.txt").open("w")
    hub = Hub(command, environment, work, stderr)
    result = {"target": target, "scenario": scenario, "n": n, "status": "ok"}
    sampler = None
    try:
        hub.call("adapters")
        probe = Probe(hub.process.pid)
        base = probe.sample()
        stored_before = stored_bytes(data) + stored_bytes(home) + stored_bytes(work)
        sampler = Sampler(probe, hub.process, cap)
        sampler.start()
        steps = []
        started = time.perf_counter()
        outcome = {}
        worker = threading.Thread(target=lambda: outcome.setdefault("error", _guard(RUNNERS[scenario], Agents(hub, adapter, metadata), n, steps)), daemon=True)
        worker.start()
        worker.join(timeout)
        if sampler.capped:
            result["status"] = f"stopped: RSS over {cap >> 20} MiB at step {len(steps)}"
        elif worker.is_alive():
            result["status"] = f"timeout after {timeout}s at step {len(steps)}"
        elif outcome.get("error"):
            result["status"] = f"failed at step {len(steps)}: {outcome['error']}"[:300]
        result["wall_s"] = time.perf_counter() - started
        result.update(base_rss=base["rss"], steps=len(steps), p50_ms=percentile(steps, 0.5) * 1e3,
                      p99_ms=percentile(steps, 0.99) * 1e3)
        result["busy_cpu_s"] = cpu_seconds(hub.process.pid)
        if result["status"] == "ok":
            time.sleep(1.0)
        sampler.stop()
        before_idle = cpu_seconds(hub.process.pid)
        time.sleep(5.0)
        result["idle_cpu_pct"] = (cpu_seconds(hub.process.pid) - before_idle) / 5.0 * 100
        result.update(peak_rss=sampler.peak["rss"], peak_threads=sampler.peak["threads"])
        if hub.process.poll() is None:
            settled = probe.sample()
            result.update(settled_rss=settled["rss"], threads=settled["threads"], fds=settled["fds"],
                          written=settled["written"] - base["written"],
                          stored=stored_bytes(data) + stored_bytes(home) + stored_bytes(work) - stored_before)
        elif result["status"] == "ok":
            result["status"] = "failed: hub exited"
    except Exception as error:
        result["status"] = f"failed: {error}"[:300]
    finally:
        if sampler is not None:
            sampler.stop()
        hub.stop()
        stderr.close()
        if provider is not None:
            provider.kill()
            provider.wait(timeout=10)
        shutil.rmtree(root, ignore_errors=True)
    return result


def _guard(runner, agents, n, steps):
    try:
        runner(agents, n, steps)
    except Exception as error:
        return str(error) or type(error).__name__
    return None


def slope(points):
    if len(points) < 2:
        return None
    xs, ys = zip(*points)
    mean_x, mean_y = statistics.fmean(xs), statistics.fmean(ys)
    spread = sum((x - mean_x) ** 2 for x in xs)
    return sum((x - mean_x) * (y - mean_y) for x, y in zip(xs, ys)) / spread if spread else None


COLUMNS = (("peak RSS MiB", "peak_rss", 1 / 2**20, "KiB", 1 / 2**10),
           ("settled RSS MiB", "settled_rss", 1 / 2**20, "KiB", 1 / 2**10),
           ("threads", "threads", 1, "", 1),
           ("FDs", "fds", 1, "", 1),
           ("written KiB", "written", 1 / 2**10, "B", 1),
           ("stored KiB", "stored", 1 / 2**10, "B", 1),
           ("step p50 ms", "p50_ms", 1, "µs", 1e3),
           ("step p99 ms", "p99_ms", 1, "µs", 1e3))


def render(results, targets):
    lines = []
    for target in targets:
        rows = [r for r in results if r["target"] == target]
        if not rows:
            continue
        lines += [f"### {target}", "", "| scenario | N | " + " | ".join(c[0] for c in COLUMNS) + " | wall s | status |",
                  "|---|---:|" + "---:|" * len(COLUMNS) + "---:|---|"]
        for scenario in SCENARIOS:
            group = [r for r in rows if r["scenario"] == scenario]
            for r in group:
                cells = [f"{r[key] * scale:.1f}" if key in r else "–" for _, key, scale, _, _ in COLUMNS]
                lines.append(f"| {scenario} | {r['n']} | " + " | ".join(cells) +
                             f" | {r.get('wall_s', 0):.1f} | {r['status']} |")
            fitted = [r for r in group if r["status"] == "ok"]
            if len(fitted) >= 2:
                cells = []
                for _, key, _, unit, scale in COLUMNS:
                    value = slope([(r["n"], r[key]) for r in fitted])
                    cells.append("–" if value is None else f"{value * scale:.2f}{(' ' + unit) if unit else ''}")
                lines.append(f"| {scenario} | per agent | " + " | ".join(cells) + " | | slope over ok rows |")
        lines.append("")
    return "\n".join(lines)


METRICS = (("peak_rss", "peak_rss", 1), ("settled_rss", "settled_rss", 1), ("threads", "threads", 1),
           ("fds", "fds", 1), ("written", "written", 1), ("stored", "stored", 1),
           ("p50_ns", "p50_ms", 1e6), ("p99_ns", "p99_ms", 1e6), ("wall_ns", "wall_s", 1e9))


def cpu_model():
    if sys.platform == "darwin":
        return subprocess.run(["sysctl", "-n", "machdep.cpu.brand_string"], capture_output=True, text=True).stdout.strip()
    for line in Path("/proc/cpuinfo").read_text().splitlines():
        if line.startswith("model name"):
            return line.split(":", 1)[1].strip()
    return platform.processor() or "unknown"


def toolchain(target, binary):
    if target == "go":
        command = ["go", "version", str(binary)]
    elif target == "rust":
        command = ["rustc", "--version"]
    else:
        command = ["zig", "version"]
    try:
        answer = subprocess.run(command, capture_output=True, text=True, timeout=60).stdout.strip()
    except OSError:
        answer = ""
    if target == "go":
        answer = answer.split(": ", 1)[-1]
    return answer or "unknown"


def report_lines(results, targets, scenarios, counts, host_class, revision, cap, timeout, toolchains):
    identity = {"schema_version": 1, "git_revision": revision, "host_class": host_class,
                "target": f"{platform.machine()}-{platform.system().lower()}", "cpu_model": cpu_model(),
                "mode": "scaling"}
    for target in targets:
        for scenario in scenarios:
            group = [r for r in results if r["target"] == target and r["scenario"] == scenario]
            if not group:
                continue
            rows = [dict({"n": r["n"], "status": r["status"]},
                         **{name: round(r[key] * scale) if key in r else None for name, key, scale in METRICS})
                    for r in group]
            fitted = [r for r in rows if r["status"] == "ok"]
            per_agent = {name: slope([(r["n"], r[name]) for r in fitted]) for name, _, _ in METRICS[:8]}
            yield json.dumps(dict(identity, workload=f"{target}/{scenario}", toolchain=toolchains[target], fixture_version=1, agent_counts=counts,
                                  history_turns=HISTORY_TURNS, rss_cap_bytes=cap,
                                  timeout_ms=round(timeout * 1000), rows=rows, per_agent=per_agent),
                             separators=(",", ":"))


def build(targets, scratch):
    binaries = {}
    if "go" in targets:
        binaries["go"] = Path(scratch) / "goap"
        subprocess.run(["go", "build", "-o", str(binaries["go"]), "./go/cmd/goap"], cwd=ROOT, check=True)
    if "rust" in targets:
        subprocess.run(["cargo", "build", "--release", "-p", "oap-serve", "--bin", "oap-rust-hub"], cwd=ROOT / "rust", check=True)
    if {"zig", "zig-loop"} & set(targets):
        subprocess.run(["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"], cwd=ROOT, check=True)
    return binaries


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command")
    provider = commands.add_parser("provider")
    provider.add_argument("--workspace", required=True)
    parser.add_argument("--targets", default=",".join(TARGETS))
    parser.add_argument("--scenarios", default=",".join(SCENARIOS))
    parser.add_argument("--n", default="10,100,1000,10000")
    parser.add_argument("--timeout", type=float, default=600, help="seconds per case before it is reported as a timeout")
    parser.add_argument("--rss-cap-mib", type=int, default=4096, help="kill a hub whose RSS passes this and report the step")
    parser.add_argument("--out", help="directory for results.json and results.md")
    parser.add_argument("--no-build", action="store_true", help="use existing zig-out and rust/target/release binaries")
    parser.add_argument("--go", help="a prebuilt goap binary")
    parser.add_argument("--report", help="write a scaling.jsonl report that `zig build bench-compare` reads")
    parser.add_argument("--host-class", default="local", help="the report's host class, as the micro benchmarks take it")
    parser.add_argument("--git-revision", help="the report's source revision (default: HEAD)")
    arguments = parser.parse_args()
    if arguments.command == "provider":
        return serve_provider(arguments)
    targets = [t for t in arguments.targets.split(",") if t]
    scenarios = [s for s in arguments.scenarios.split(",") if s]
    counts = sorted({int(n) for n in arguments.n.split(",")})
    unknown = set(targets) - set(TARGETS) | set(scenarios) - set(SCENARIOS)
    if unknown:
        parser.error("unknown target or scenario: " + ", ".join(sorted(unknown)))
    if not re.fullmatch(r"[A-Za-z0-9._-]+", arguments.host_class):
        parser.error("--host-class takes letters, digits, '.', '_' and '-'")
    revision = arguments.git_revision or subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True,
                                                        text=True).stdout.strip() or "unknown"
    scratch = tempfile.mkdtemp(prefix="oap-bench-")
    try:
        built = {} if arguments.no_build else build(targets, scratch)
        sources = {"zig": ROOT / "zig/zig-out/bin/oapx", "zig-loop": ROOT / "zig/zig-out/bin/oapx",
                   "rust": ROOT / "rust/target/release/oap-rust-hub",
                   "go": Path(arguments.go) if arguments.go else built.get("go")}
        binaries = {}
        for target in targets:
            source = sources[target]
            if source is None or not Path(source).is_file():
                parser.error(f"no binary for {target}; drop --no-build or pass --go")
            binaries[target] = Path(scratch) / ("bin-" + target)
            shutil.copy2(source, binaries[target])
        results = []
        for target in targets:
            for scenario in scenarios:
                if scenario in LOOP_ONLY and target != "zig-loop":
                    continue
                for n in counts:
                    result = run_case(target, binaries[target], scenario, n, scratch, arguments.timeout,
                                      arguments.rss_cap_mib << 20)
                    results.append(result)
                    print(json.dumps({k: (round(v, 3) if isinstance(v, float) else v) for k, v in result.items()}),
                          file=sys.stderr, flush=True)
        table = render(results, targets)
        print(table)
        if arguments.out:
            out = Path(arguments.out)
            out.mkdir(parents=True, exist_ok=True)
            (out / "results.json").write_text(json.dumps(results, indent=2))
            (out / "results.md").write_text(table)
        if arguments.report:
            lines = report_lines(results, targets, scenarios, counts, arguments.host_class, revision,
                                 arguments.rss_cap_mib << 20, arguments.timeout,
                                 {target: toolchain(target, binaries[target]) for target in targets})
            Path(arguments.report).write_text("".join(line + "\n" for line in lines))
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    main()
