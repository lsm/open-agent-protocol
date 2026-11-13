#!/usr/bin/env python3
"""Observe SIGTERM and two-stage SIGINT shutdown with stdin left open."""
import argparse
import importlib.util
import json
from pathlib import Path
import shutil
import signal
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location("bench_resources", ROOT / "scripts/bench-resources.py")
bench = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench)


def run_case(binary, output, gated):
    label = "gate" if gated else "idle"
    with tempfile.TemporaryDirectory(prefix="oap-hub-signals-") as temporary:
        root = Path(temporary)
        home = root / "home"
        home.mkdir()
        copied = root / "oapx"
        shutil.copy2(binary, copied)
        config = root / "hub.json"
        config.write_text(json.dumps({"adapters": {"memory": {"type": "memory"}}}))
        environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                       "OAPX_KEYCHAIN_SERVICE": "oap-idle-waits-" + uuid.uuid4().hex}
        command = bench.sandboxed([str(copied), "serve", "--stdio", "--config", str(config),
                                   "--no-save-oapx-sessions"], root)
        log_path = output / (label + "-stderr.txt")
        with log_path.open("w") as errors:
            hub = bench.Hub(command, environment, root, errors)
            try:
                adapters = hub.call("adapters")
                (output / (label + "-adapters.json")).write_text(json.dumps(adapters, indent=2))
                if gated:
                    agents = bench.Agents(hub, "memory", None)
                    agents.open("signal-gate")
                    agents.submit("signal-gate", "hold at the scripted permission gate")
                    agents.settle("signal-gate", stop_at_gate=True)
                    before = agents.state("signal-gate")
                    assert any(run.get("pending_interactions") for run in before["active_runs"]), before
                    hub.process.send_signal(signal.SIGINT)
                    deadline = time.monotonic() + 5
                    while "signal again to force" not in log_path.read_text():
                        assert hub.process.poll() is None
                        assert time.monotonic() < deadline, "first SIGINT was not observed"
                        time.sleep(0.02)
                    during = agents.state("signal-gate")
                    assert during["active_runs"], during
                    (output / "gate-states.json").write_text(json.dumps({"before": before, "draining": during}, indent=2))
                    hub.process.send_signal(signal.SIGINT)
                else:
                    time.sleep(0.2)
                    hub.process.send_signal(signal.SIGTERM)
                assert hub.process.wait(timeout=5) == 0
            finally:
                hub.stop()
                (output / (label + "-cleanup.json")).write_text(json.dumps({
                    "pid": hub.process.pid, "exit": hub.process.returncode,
                    "reaped": hub.process.poll() is not None}, indent=2))
        text = log_path.read_text()
        assert "shutting down gracefully" in text, text
        if gated:
            assert "forced shutdown" in text, text
        assert not list(home.rglob("auth.json")), "child wrote a credential file"
    print(f"PASS {label}: stdin stayed open; signals reached the Hub; exit 0 and child reaped")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--binary", default=str(ROOT / "zig/zig-out/bin/oxap"))
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for gated in (False, True):
        run_case(Path(args.binary).resolve(), output, gated)


if __name__ == "__main__":
    main()
