#!/usr/bin/env python3
"""Measure how each OAP hub's resource use grows with the number of agents.

Runs the same scenarios against the Zig, Go and Rust stdio hubs, each serving
its scripted memory adapter, and against oapx's own loop driven by a local
scripted provider. See docs/resource-benchmark.md for what is and is not
measured.
"""
import argparse
import asyncio
import ctypes
import ctypes.util
import http.server
import json
import mmap
import os
import platform
import re
from pathlib import Path
import queue
import shutil
import signal
import socket
import socketserver
import statistics
import struct
import subprocess
import sys
import tempfile
import threading
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
HISTORY_TURNS = 3
SCENARIOS = ("empty", "history", "waiting", "jobs", "burst")
FLIGHTS = ("working", "mixed", "hold-model", "hold-tool", "hold-gate")
TARGETS = ("zig", "go", "rust", "zig-loop")
LOOP_ONLY = {"jobs", *FLIGHTS}
WINDOW = 64
SLOT, SLOT_FIELDS = 64, ("in_model", "served", "calls", "results", "bad")

# Layouts from the macOS SDK's <sys/proc_info.h> and <sys/resource.h>, checked with
# sizeof/offsetof: struct proc_taskinfo is 96 bytes with pti_threadnum at byte 84; in
# struct rusage_info_v4 the 16-byte ri_uuid fills uint64 slots 0-1, so ri_phys_footprint
# is slot 9 and ri_logical_writes slot 29. struct proc_fdinfo is 8 bytes. pti_total_user
# and pti_total_system are at 16 and 24, in mach time units; pti_csw is at 80.
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
            timebase = (ctypes.c_uint32 * 2)()
            ctypes.CDLL(None).mach_timebase_info(timebase)
            self.tick_ns = timebase[0] / timebase[1]

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
        user, system = struct.unpack_from("<QQ", self.task.raw, 16)
        return {"rss": int(self.usage[RUSAGE_PHYS_FOOTPRINT]), "threads": threads, "fds": fds,
                "written": int(self.usage[RUSAGE_LOGICAL_WRITES]), "cpu": (user + system) * self.tick_ns / 1e9,
                "switches": struct.unpack_from("<i", self.task.raw, 80)[0]}

    def _linux(self):
        base = Path("/proc") / str(self.pid)
        status = dict(line.split(":", 1) for line in (base / "status").read_text().splitlines() if ":" in line)
        io = dict(line.split(": ") for line in (base / "io").read_text().splitlines())
        resident = int(status["VmRSS"].split()[0]) + int(status.get("VmSwap", "0 kB").split()[0])
        ticks = (base / "stat").read_text().rsplit(")", 1)[1].split()[11:13]
        return {"rss": resident * 1024, "threads": int(status["Threads"]),
                "fds": len(os.listdir(base / "fd")), "written": int(io["write_bytes"]),
                "cpu": sum(map(int, ticks)) / os.sysconf("SC_CLK_TCK"),
                "switches": int(status["voluntary_ctxt_switches"]) + int(status["nonvoluntary_ctxt_switches"])}


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
        self.process = subprocess.Popen(command, cwd=cwd, env=environment, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=stderr, text=True, bufsize=1, start_new_session=True)
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

    def batch(self, calls):
        """Writes every call at once, then collects the replies, so the hub is never idle between them."""
        first = self.number + 1
        lines = []
        for op, fields in calls:
            self.number += 1
            lines.append(json.dumps({"id": self.number, "op": op, **fields}) + "\n")
        self.process.stdin.write("".join(lines))
        self.process.stdin.flush()
        replies = {}
        while len(replies) < len(calls):
            reply = self.replies.get(timeout=120)
            if reply is None:
                raise RuntimeError("hub exited during a batch")
            replies[reply["id"]] = reply
        return [replies[i] for i in range(first, self.number + 1)]

    def stop(self):
        try:
            self.process.stdin.close()
            self.process.wait(timeout=20)
        except (subprocess.TimeoutExpired, BrokenPipeError, OSError):
            pass
        self.kill()
        self.process.wait(timeout=10)
        self.reader.join(timeout=5)

    def kill(self):
        try:
            os.killpg(self.process.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


def envelope(kind, payload, **head):
    return {"protocol": "open-agent-protocol", "version": "0.1",
            "profile": "open-agent-protocol.agent-control-core", "type": kind,
            "id": str(uuid.uuid4()), "payload": payload, **head}


class Agents:
    """Drives sessions on one hub through the hub's stdio operations."""

    def __init__(self, hub, adapter, metadata):
        self.hub, self.adapter, self.metadata = hub, adapter, metadata

    def open_call(self, session, mode="bypass", workspace=None):
        payload = {"session_id": session}
        if self.metadata is not None:
            payload["metadata"] = {"oapx": dict(self.metadata, permission_mode=mode)}
            if workspace:
                payload["metadata"]["oapx"]["workspace_root"] = workspace
        return "open", {"adapter": self.adapter, "request": envelope("session.open.request", payload)}

    def open(self, session, mode="bypass"):
        op, fields = self.open_call(session, mode)
        self.hub.call(op, **fields)

    def submit_call(self, session, text):
        payload = {"session_id": session, "delivery": "auto", "messages": [{"role": "user", "content": text}]}
        return "submit", {"session_id": session, "request": envelope("session.message.submit.request", payload, session_id=session)}

    def submit(self, session, text):
        op, fields = self.submit_call(session, text)
        self.hub.call(op, **fields)

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

    def resolve_call(self, session, run, interaction):
        head = {"interaction_id": interaction, "session_id": session, "run_id": run,
                "requested_by": "reference.memory" if self.metadata is None else "oapx.agent", "responded_by": "user"}
        if interaction.startswith("permission") or self.metadata is not None:
            request = envelope("action.permission.resolve.request", dict(head, choice_id="approve", granted=True),
                               session_id=session, run_id=run)
        elif interaction.startswith("input"):
            request = envelope("user.input.resolve.request", dict(head, answers=[
                {"question_id": "choice", "selected_option_ids": ["yes"]}]), session_id=session, run_id=run)
        else:
            raise RuntimeError("unexpected interaction " + interaction)
        return "resolve", {"session_id": session, "request": request}

    def resolve(self, session, run, interaction):
        op, fields = self.resolve_call(session, run, interaction)
        self.hub.call(op, **fields)


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


def chunks(items):
    return [items[i:i + WINDOW] for i in range(0, len(items), WINDOW)]


def shape_of(scenario, n, hold):
    """What the model and the driver do in one flight; a hold scenario parks every agent in one phase."""
    busy = {"ttft": 0.5, "gap": 0.05, "text": 20, "tool": (1, 5), "first": 0.5, "gate": 2.0, "every": 0, "rounds": 2}
    quick = {"ttft": 0.01, "gap": 0.0, "text": 2, "tool": (0, 0), "first": 0.01, "gate": 0.0, "every": 0, "rounds": 1}
    return {"working": busy, "mixed": dict(busy, every=3), "hold-model": dict(quick, first=hold),
            "hold-tool": dict(quick, tool=(hold, hold)), "hold-gate": dict(quick, every=1, gate=hold)}[scenario]


def scenario_flight(agents, n, steps, shape, work, poll_s, phases):
    """Keeps all N in flight through one pipelined client: a run settles while others are in every phase."""
    hub, sessions = agents.hub, [f"s{i}" for i in range(n)]
    gated = {s for i, s in enumerate(sessions) if shape["every"] and i % shape["every"] == 0}
    prompts = {}
    for i, s in enumerate(sessions):
        (work / s).mkdir()
        (work / s / "seed.txt").write_text(f"seed-{s}\n")
        low, high = shape["tool"]
        prompts[s] = f"@work {s} {work / s} {shape['first']} {shape['ttft']} {shape['gap']} {shape['text']} {low + i * 7 % (high - low + 1)}"
    for part in chunks(sessions):
        hub.batch([agents.open_call(s, "ask" if s in gated else "bypass", str(work / s)) for s in part])
    started, left, due = {}, {s: shape["rounds"] for s in sessions}, {}
    for part in chunks(sessions):
        hub.batch([agents.submit_call(s, prompts[s]) for s in part])
        started.update((s, time.perf_counter()) for s in part)
    phases["all_in_flight"] = time.perf_counter()
    while left:
        cycle = time.perf_counter()
        for part in chunks(sorted(left)):
            again, answers = [], []
            for s, reply in zip(part, hub.batch([("state", {"session_id": s}) for s in part])):
                phases["polls"] += 1
                state = reply["result"]["payload"]
                runs = state.get("active_runs") or []
                now = time.perf_counter()
                for run in runs:
                    for interaction in run.get("pending_interactions") or []:
                        held = due.setdefault((s, run["run_id"], interaction), now + shape["gate"])
                        if held <= now:
                            answers.append(agents.resolve_call(s, run["run_id"], interaction))
                            del due[(s, run["run_id"], interaction)]
                if not runs and state["status"] == "idle":
                    steps.append(now - started[s])
                    left[s] -= 1
                    if left[s]:
                        again.append(agents.submit_call(s, prompts[s]))
                        started[s] = now
                    else:
                        del left[s]
            phases["gates"] = len(due)
            for reply in hub.batch(again + answers) if again or answers else ():
                phases["refused"] += not reply.get("ok")
        time.sleep(max(0.0, poll_s - (time.perf_counter() - cycle)))


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


def tool_turn(turn, agent, workspace, seconds):
    common = {"description": "benchmark", "workspace_root": workspace}
    if turn == 0:
        return "Read", dict(common, path="seed.txt")
    if turn == 1:
        return "Shell", dict(common, command=f"sleep {seconds}; echo done-{agent}")
    return "Write", dict(common, path="out.txt", content=f"written by {agent}\n")


async def answer(request, slot, trace):
    """Decides the next step from the conversation so far: Read, Shell, Write, then a closing text."""
    received = time.perf_counter()
    messages = request["messages"]
    start = max(i for i, m in enumerate(messages) if m["role"] == "user" and "@work" in json.dumps(m.get("content")))
    agent, workspace, first, ttft, gap, text, seconds = re.search(r"@work (\S+) (\S+) (\S+) (\S+) (\S+) (\S+) (\d+)", json.dumps(messages[start])).groups()
    turn = sum(1 for m in messages[start:] if m["role"] == "tool")
    if turn:
        result = json.dumps(messages[-1].get("content"))
        slot.add("results", 1)
        slot.add("bad", "Tool execution failed" in result or {1: "seed-", 2: "done-"}.get(turn, "") + (agent if turn < 3 else "") not in result)
    slot.add("in_model", 1)
    slot.add("served", 1)
    await asyncio.sleep(float(first if turn == 0 else ttft))
    yield {"role": "assistant", "content": "w"}
    for _ in range(int(text)):
        if float(gap):
            await asyncio.sleep(float(gap))
        yield {"content": "ord "}
    if turn < 3:
        name, arguments = tool_turn(turn, agent, workspace, seconds)
        yield {"tool_calls": [{"index": 0, "id": "call_" + uuid.uuid4().hex[:12], "type": "function",
                               "function": {"name": name, "arguments": json.dumps(arguments)}}]}
        slot.add("calls", 1)
    slot.add("in_model", -1)
    trace.append((agent, turn, received, time.perf_counter(), int(seconds) if turn == 1 else 0))
    yield "tool_calls" if turn < 3 else "stop"


async def connection(reader, writer, slot, trace):
    try:
        while True:
            head = (await reader.readuntil(b"\r\n\r\n")).decode("latin-1")
            size = re.search(r"content-length: *(\d+)", head, re.I)
            body = await reader.readexactly(int(size.group(1))) if size else b""
            if not head.startswith("POST"):
                reply = json.dumps({"data": [{"id": "bench-model"}]}).encode()
                writer.write(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s" % (len(reply), reply))
                continue
            writer.write(b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
            async for part in answer(json.loads(body), slot, trace):
                done = isinstance(part, str)
                choice = {"index": 0, "delta": {} if done else part, "finish_reason": part if done else None}
                frame = "data: " + json.dumps({"id": "bench", "object": "chat.completion.chunk", "model": "bench-model", "choices": [choice]}) + "\n\n"
                frame += "data: [DONE]\n\n" if done else ""
                writer.write(b"%x\r\n%s\r\n" % (len(frame), frame.encode()))
                await writer.drain()
            writer.write(b"0\r\n\r\n")
    except (asyncio.IncompleteReadError, ConnectionError):
        pass
    finally:
        writer.close()


class Slot:
    """One worker's counters in the shared file; only that worker writes them."""

    def __init__(self, shared, index):
        self.shared, self.base = shared, index * SLOT

    def add(self, name, amount):
        at = self.base + 8 * SLOT_FIELDS.index(name)
        struct.pack_into("<q", self.shared, at, struct.unpack_from("<q", self.shared, at)[0] + int(amount))


def stats_of(shared, workers):
    return {name: sum(struct.unpack_from("<q", shared, w * SLOT + 8 * i)[0] for w in range(workers))
            for i, name in enumerate(SLOT_FIELDS)}


def flight_worker(listener, shared, index, directory):
    slot, trace = Slot(shared, index), []

    async def main():
        stop = asyncio.Event()
        asyncio.get_running_loop().add_signal_handler(signal.SIGTERM, stop.set)
        await asyncio.start_server(lambda r, w: connection(r, w, slot, trace), sock=listener, limit=1 << 24)
        await stop.wait()

    asyncio.run(main())
    Path(directory, f"trace-{index}.json").write_text(json.dumps(trace))
    os._exit(0)


def serve_flight(arguments):
    """A forked pool of asyncio workers sharing one listener, outside the hub and the driver."""
    listener = socket.create_server(("127.0.0.1", 0), backlog=4096)
    with open(Path(arguments.directory, "stats"), "wb") as held:
        held.truncate(SLOT * arguments.workers)
    with open(Path(arguments.directory, "stats"), "r+b") as held:
        shared = mmap.mmap(held.fileno(), 0)
    pids = []
    for index in range(arguments.workers):
        pid = os.fork()
        if pid == 0:
            flight_worker(listener, shared, index, arguments.directory)
        pids.append(pid)
    print(listener.getsockname()[1], *pids, flush=True)
    signal.signal(signal.SIGTERM, lambda *_: [os.kill(pid, signal.SIGTERM) for pid in pids])
    for pid in pids:
        os.waitpid(pid, 0)


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


class Timeline(threading.Thread):
    """Samples the hub, the model workers and the driver together, so each reading can be tied to a phase mix."""

    def __init__(self, probe, workers, shared, count, phases, hub, options):
        super().__init__(daemon=True)
        self.probe, self.workers, self.shared, self.count, self.phases = probe, workers, shared, count, phases
        self.hub, self.options = hub, options
        self.rows, self.halt, self.stopped = [], threading.Event(), None

    def run(self):
        while not self.halt.wait(0.25):
            try:
                hub = self.probe.sample()
                model = [worker.sample()["cpu"] for worker in self.workers]
            except (ProcessLookupError, FileNotFoundError):
                return
            counts = stats_of(self.shared, self.count)
            self.rows.append({"t": time.perf_counter(), "threads": hub["threads"], "rss": hub["rss"], "hub_cpu": hub["cpu"],
                              "switches": hub["switches"], "model_cpu": model, "driver_cpu": sum(os.times()[:2]),
                              "in_model": counts["in_model"], "in_tool": counts["calls"] - counts["results"],
                              "at_gate": self.phases["gates"], "polls": self.phases["polls"]})
            self.guard()

    def guard(self):
        recent = self.rows[-20:]
        if user_processes() >= self.options.max_user_processes:
            self.stopped = f"stopped: user processes reached {self.options.max_user_processes}"
        elif len(recent) == 20 and rate(recent, "hub_cpu") > self.options.max_hub_cores:
            self.stopped = f"stopped: hub over {self.options.max_hub_cores} cores for 5 s"
        if self.stopped:
            self.hub.kill()
            self.halt.set()


def user_processes():
    return len(subprocess.run(["ps", "-U", str(os.getuid()), "-o", "pid="], capture_output=True, text=True).stdout.split())


def rate(rows, key):
    span = rows[-1]["t"] - rows[0]["t"]
    return (rows[-1][key] - rows[0][key]) / span if span > 0 else 0.0


def flight_summary(rows, trace, steps, n, shape, stats, phases, scenario, work):
    summary = {"runs": len(steps), "polls": phases["polls"], "refused": phases["refused"], "bad_results": stats["bad"],
               "written_back": sum(1 for path in work.glob("*/out.txt")), "peak_in_model": max(r["in_model"] for r in rows),
               "peak_in_tool": max(r["in_tool"] for r in rows), "peak_at_gate": max(r["at_gate"] for r in rows)}
    span = rows[-1]["t"] - rows[0]["t"]
    summary.update(hub_cores=rate(rows, "hub_cpu"), switches_s=rate(rows, "switches"), driver_cores=rate(rows, "driver_cpu"),
                   model_core_peak=max((b - a) / span for a, b in zip(rows[0]["model_cpu"], rows[-1]["model_cpu"])))
    key = {"hold-model": "in_model", "hold-tool": "in_tool", "hold-gate": "at_gate"}.get(scenario)
    if key:
        parked = [r for r in rows if r[key] >= n * 0.98 and r["in_model"] + r["in_tool"] + r["at_gate"] >= n * 0.98]
        parked = [r for r in parked if r[key] >= n]  or parked
        if len(parked) >= 3:
            summary.update(hold_samples=len(parked), hold_threads=statistics.median(r["threads"] for r in parked),
                           hold_cores=rate(parked, "hub_cpu"), hold_switches_s=rate(parked, "switches"),
                           hold_rss=statistics.median(r["rss"] for r in parked))
    turns, by_agent = [], {}
    for agent, turn, received, finished, tool in sorted(trace, key=lambda t: t[2]):
        last = by_agent.get(agent)
        if last is not None and turn > 0:
            turns.append((received - last[0], received - last[1] - last[2]))
        by_agent[agent] = (received, finished, tool)
    summary.update(turn_p50_ms=percentile([t[0] for t in turns], 0.5) * 1e3, turn_p99_ms=percentile([t[0] for t in turns], 0.99) * 1e3,
                   between_p50_ms=percentile([t[1] for t in turns], 0.5) * 1e3, between_p99_ms=percentile([t[1] for t in turns], 0.99) * 1e3)
    return summary


def run_case(target, binary, scenario, n, scratch, timeout, cap, options):
    root = Path(tempfile.mkdtemp(prefix=f"{target}-{scenario}-{n}-", dir=scratch))
    home, work, data = root / "home", root / "work", root / "data"
    for directory in (home, work, data):
        directory.mkdir()
    environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                   "OAPX_KEYCHAIN_SERVICE": "oap-bench-" + uuid.uuid4().hex}
    adapter, metadata, provider = "memory", None, None
    config = {"adapters": {"memory": {"type": "memory"}}}
    flight = scenario in FLIGHTS
    if target == "zig-loop":
        command = ["flight", "--directory", str(root), "--workers", str(options.model_workers)] if flight else ["provider", "--workspace", str(work)]
        provider = subprocess.Popen([sys.executable, "-I", __file__, *command], stdout=subprocess.PIPE, text=True)
        port, *model_pids = map(int, provider.stdout.readline().split())
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
        runner, phases, timeline = RUNNERS.get(scenario), {"gates": 0, "polls": 0, "refused": 0}, None
        if flight:
            shape = shape_of(scenario, n, options.hold or max(15.0, n / 50))
            runner = lambda a, count, s: scenario_flight(a, count, s, shape, work, options.poll, phases)
            with open(root / "stats", "r+b") as held:
                shared = mmap.mmap(held.fileno(), 0)
            timeline = Timeline(probe, [Probe(pid) for pid in model_pids], shared, len(model_pids), phases, hub, options)
            timeline.start()
        worker = threading.Thread(target=lambda: outcome.setdefault("error", _guard(runner, Agents(hub, adapter, metadata), n, steps)), daemon=True)
        worker.start()
        worker.join(timeout)
        if timeline is not None and timeline.stopped:
            result["status"] = timeline.stopped + f" at {len(timeline.rows) / 4:.0f} s"
        elif sampler.capped:
            result["status"] = f"stopped: RSS over {cap >> 20} MiB at step {len(steps)}"
        elif worker.is_alive():
            result["status"] = f"timeout after {timeout}s at step {len(steps)}"
        elif outcome.get("error"):
            result["status"] = f"failed at step {len(steps)}: {outcome['error']}"[:300]
        result["wall_s"] = time.perf_counter() - started
        result.update(base_rss=base["rss"], steps=len(steps), p50_ms=percentile(steps, 0.5) * 1e3,
                      p99_ms=percentile(steps, 0.99) * 1e3)
        if timeline is not None:
            timeline.halt.set()
            timeline.join(timeout=5)
        if result["status"] == "ok":
            time.sleep(1.0)
        sampler.stop()
        result.update(peak_rss=sampler.peak["rss"], peak_threads=sampler.peak["threads"])
        if hub.process.poll() is None:
            settled = probe.sample()
            result.update(settled_rss=settled["rss"], threads=settled["threads"], fds=settled["fds"],
                          written=settled["written"] - base["written"],
                          stored=stored_bytes(data) + stored_bytes(home) + stored_bytes(work) - stored_before)
        elif result["status"] == "ok":
            result["status"] = "failed: hub exited"
        if timeline is not None and len(timeline.rows) >= 2:
            result["timeline"] = timeline.rows
            provider.terminate()
            provider.wait(timeout=30)
            trace = [row for path in root.glob("trace-*.json") for row in json.loads(path.read_text())]
            result.update(flight_summary(timeline.rows, trace, steps, n, shape, stats_of(shared, len(model_pids)), phases, scenario, work))
            result["runs_s"] = len(steps) / result["wall_s"] if result["wall_s"] else 0.0
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


FLIGHT_COLUMNS = (("peak threads", "peak_threads", 1), ("hold threads", "hold_threads", 1), ("peak footprint MiB", "peak_rss", 1 / 2**20),
                  ("hub cores", "hub_cores", 1), ("hold cores", "hold_cores", 1), ("switches/s", "switches_s", 1), ("hold switches/s", "hold_switches_s", 1),
                  ("turn p50 ms", "turn_p50_ms", 1), ("turn p99 ms", "turn_p99_ms", 1), ("between p50 ms", "between_p50_ms", 1),
                  ("between p99 ms", "between_p99_ms", 1), ("runs/s", "runs_s", 1), ("model worker peak cores", "model_core_peak", 1),
                  ("driver cores", "driver_cores", 1))


def render_flights(results):
    rows = [r for r in results if r["scenario"] in FLIGHTS]
    lines = ["### zig-loop flights", "", "| scenario | N | " + " | ".join(c[0] for c in FLIGHT_COLUMNS) + " | wall s | status |",
             "|---|---:|" + "---:|" * len(FLIGHT_COLUMNS) + "---:|---|"]
    for r in rows:
        cells = [f"{r[key] * scale:.2f}" if key in r else "–" for _, key, scale in FLIGHT_COLUMNS]
        lines.append(f"| {r['scenario']} | {r['n']} | " + " | ".join(cells) + f" | {r.get('wall_s', 0):.1f} | {r['status']} |")
    return "\n".join(lines + [""]) if rows else ""


def render(results, targets):
    lines = []
    for target in targets:
        rows = [r for r in results if r["target"] == target and r["scenario"] not in FLIGHTS]
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
    return "\n".join(lines) + render_flights(results)


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
            if not group or scenario in FLIGHTS:
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
    flight = commands.add_parser("flight")
    flight.add_argument("--directory", required=True)
    flight.add_argument("--workers", type=int, default=4)
    parser.add_argument("--targets", default=",".join(TARGETS))
    parser.add_argument("--scenarios", default=",".join(SCENARIOS))
    parser.add_argument("--n", default="10,100,1000,10000")
    parser.add_argument("--timeout", type=float, default=600, help="seconds per case before it is reported as a timeout")
    parser.add_argument("--rss-cap-mib", type=int, default=4096, help="kill a hub whose RSS passes this and report the step")
    parser.add_argument("--model-workers", type=int, default=4, help="asyncio processes behind a flight's model endpoint")
    parser.add_argument("--poll", type=float, default=0.25, help="seconds a flight waits between rounds of state reads")
    parser.add_argument("--hold", type=float, help="seconds a hold scenario parks each agent (default max(15, N/50))")
    parser.add_argument("--max-user-processes", type=int, default=5000, help="stop a flight when this user runs this many processes")
    parser.add_argument("--max-hub-cores", type=float, default=12, help="stop a flight whose hub holds this many cores for 5 s")
    parser.add_argument("--out", help="directory for results.json and results.md")
    parser.add_argument("--no-build", action="store_true", help="use existing zig-out and rust/target/release binaries")
    parser.add_argument("--go", help="a prebuilt goap binary")
    parser.add_argument("--loop-binary", help="run the zig-loop target on this binary, which takes oapx's serve arguments")
    parser.add_argument("--report", help="write a scaling.jsonl report that `zig build bench-compare` reads")
    parser.add_argument("--host-class", default="local", help="the report's host class, as the micro benchmarks take it")
    parser.add_argument("--git-revision", help="the report's source revision (default: HEAD)")
    arguments = parser.parse_args()
    if arguments.command == "provider":
        return serve_provider(arguments)
    if arguments.command == "flight":
        return serve_flight(arguments)
    targets = [t for t in arguments.targets.split(",") if t]
    scenarios = [s for s in arguments.scenarios.split(",") if s]
    counts = sorted({int(n) for n in arguments.n.split(",")})
    unknown = set(targets) - set(TARGETS) | set(scenarios) - set(SCENARIOS) - set(FLIGHTS)
    if unknown:
        parser.error("unknown target or scenario: " + ", ".join(sorted(unknown)))
    if not re.fullmatch(r"[A-Za-z0-9._-]+", arguments.host_class):
        parser.error("--host-class takes letters, digits, '.', '_' and '-'")
    revision = arguments.git_revision or subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True,
                                                        text=True).stdout.strip() or "unknown"
    scratch = tempfile.mkdtemp(prefix="oap-bench-")
    try:
        built = {} if arguments.no_build else build(targets, scratch)
        sources = {"zig": ROOT / "zig/zig-out/bin/oapx", "zig-loop": Path(arguments.loop_binary) if arguments.loop_binary else ROOT / "zig/zig-out/bin/oapx",
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
                    if scenario in FLIGHTS and user_processes() + n >= arguments.max_user_processes:
                        results.append({"target": target, "scenario": scenario, "n": n, "status": "skipped: N tools at once would pass --max-user-processes"})
                        continue
                    result = run_case(target, binaries[target], scenario, n, scratch, arguments.timeout,
                                      arguments.rss_cap_mib << 20, arguments)
                    results.append(result)
                    print(json.dumps({k: (round(v, 3) if isinstance(v, float) else v) for k, v in result.items() if k != "timeline"}),
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
