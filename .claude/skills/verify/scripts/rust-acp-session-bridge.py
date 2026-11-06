import argparse
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[4]
AGENT = '''import json, os, sys, time
from pathlib import Path
mode, directory = sys.argv[1:]
out = Path(directory)
(out / "pid").write_text(str(os.getpid()))
def read():
    line = sys.stdin.readline()
    with (out / "wire.jsonl").open("a") as stream:
        stream.write(line)
    return json.loads(line)
def send(value):
    print(json.dumps(dict(jsonrpc="2.0", **value)), flush=True)
first = read()
send(dict(id=first["id"], result=dict(protocolVersion=1, agentCapabilities={})))
second = read()
send(dict(id=second["id"], result=dict(sessionId="native-session", configOptions=[dict(id="model", category="model", type="select", currentValue="sol", options=[dict(value="sol", name="Sol"), dict(value="luna", name="Luna")])])))
limit = time.monotonic() + 10
while not (out / "start-inbound").exists():
    assert time.monotonic() < limit, "consumer never opened"
    time.sleep(0.005)
for index in range(256):
    send(dict(method="session/update", params=dict(sessionId="native-session", update=dict(sessionUpdate="session_info_update", title=str(index)))))
send(dict(id="idle-query", method="fixture/unsupported", params={}))
response = read()
assert response == dict(jsonrpc="2.0", id="idle-query", error=dict(code=-32601, message="method not supported")), response
(out / "idle-ready").touch()
if mode == "cancel-close":
    time.sleep(60)
elif mode in ["cancel-switch", "close-with-switch"]:
    switch = read()
    assert switch["method"] == "session/set_config_option", switch
    assert switch["params"] == dict(sessionId="native-session", configId="model", value="luna"), switch
    (out / "switch-ready").touch()
    time.sleep(60)
else:
    sys.stdin.read()
'''


def threads(pid):
    if sys.platform == 'darwin':
        result = subprocess.run(['ps', '-M', '-p', str(pid)], capture_output=True, text=True, timeout=5)
        assert result.returncode == 0, result.stderr
        count = len([line for line in result.stdout.splitlines()[1:] if line.strip()])
        return count, result.stdout
    entries = sorted(path.name for path in Path(f'/proc/{pid}/task').iterdir())
    return len(entries), json.dumps(entries)


def absent(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return True
    return False


def wait_file(path):
    deadline = time.monotonic() + 10
    while not path.exists() and time.monotonic() < deadline:
        time.sleep(0.005)
    assert path.exists(), f'agent never reached {path.name}'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    built = subprocess.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp', '--example', 'session_owner'],
                           cwd=ROOT, capture_output=True, text=True, timeout=180)
    (output / 'build.log').write_text(built.stdout + built.stderr)
    assert built.returncode == 0, built.stderr
    binary = ROOT / 'rust/target/debug/examples/session_owner'
    with tempfile.TemporaryDirectory(prefix='oap-session-owner-') as temporary:
        fake = Path(temporary) / 'agent.py'
        fake.write_text(AGENT)
        for mode in ['close', 'drop', 'cancel-switch', 'cancel-close', 'close-with-switch']:
            case = output / mode
            case.mkdir()
            consumer = subprocess.Popen([str(binary), mode, sys.executable, '-u', str(fake), mode, str(case)],
                                        cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, text=True, start_new_session=True)
            phases = []
            counts = {}
            def phase(expected):
                readable, _, _ = select.select([consumer.stdout], [], [], 10)
                assert readable, f'public consumer did not reach {expected}'
                line = consumer.stdout.readline()
                assert line, f'public consumer exited before {expected}'
                value = json.loads(line)
                assert value['phase'] == expected, value
                phases.append(value)
                return value
            def advance():
                consumer.stdin.write('continue\n')
                consumer.stdin.flush()
            try:
                phase('booted')
                counts['before'], raw = threads(consumer.pid)
                (case / 'threads-before.txt').write_text(raw)
                advance()
                opened = phase('opened')
                assert opened['state']['status'] == 'idle' and opened['state']['current_model_id'] == 'sol', opened
                (case / 'start-inbound').touch()
                wait_file(case / 'idle-ready')
                counts['open'], raw = threads(consumer.pid)
                (case / 'threads-open.txt').write_text(raw)
                assert counts['open'] > counts['before'], counts
                advance()
                if mode in ['cancel-switch', 'cancel-close', 'close-with-switch']:
                    pending = phase('pending')
                    assert pending['pending'], pending
                    if mode != 'cancel-close':
                        wait_file(case / 'switch-ready')
                    advance()
                cleaned = phase('cleaned')
                assert absent(int((case / 'pid').read_text())), 'native child survived cleanup'
                if mode != 'drop':
                    assert cleaned['stateError'] == 'adapter: session closed', cleaned
                if mode == 'close':
                    assert cleaned['closeError'] is None, cleaned
                if mode == 'close-with-switch':
                    assert 'shutdown timed out' in cleaned['closeError'] and cleaned['switchError'], cleaned
                deadline = time.monotonic() + 5
                counts['after'], raw = threads(consumer.pid)
                while counts['after'] > counts['before'] and time.monotonic() < deadline:
                    time.sleep(0.01)
                    counts['after'], raw = threads(consumer.pid)
                (case / 'threads-after.txt').write_text(raw)
                assert counts['after'] == counts['before'], counts
                frames = [json.loads(line) for line in (case / 'wire.jsonl').read_text().splitlines()]
                assert [frame['method'] for frame in frames[:2]] == ['initialize', 'session/new'], frames
                assert frames[2] == {'jsonrpc':'2.0', 'id':'idle-query', 'error':{'code':-32601, 'message':'method not supported'}}, frames
                assert len(frames) == (4 if mode in ['cancel-switch', 'close-with-switch'] else 3), frames
                advance()
                stdout, stderr = consumer.communicate(timeout=5)
                assert consumer.returncode == 0 and not stdout, (stdout, stderr)
                (case / 'consumer.stderr').write_text(stderr)
            finally:
                (case / 'phases.json').write_text(json.dumps(phases, indent=2))
                (case / 'thread-counts.json').write_text(json.dumps(counts, indent=2))
                if consumer.poll() is None:
                    os.killpg(consumer.pid, signal.SIGKILL)
                    consumer.communicate()
                if (case / 'pid').exists() and not absent(int((case / 'pid').read_text())):
                    os.killpg(consumer.pid, signal.SIGKILL)
            print(f'PASS {mode}: idle inbound/reverse wire observed, native PID absent, threads {counts}', flush=True)
    print(f'PASS session PromptBridge ownership. Evidence: {output}')


if __name__ == '__main__':
    main()
