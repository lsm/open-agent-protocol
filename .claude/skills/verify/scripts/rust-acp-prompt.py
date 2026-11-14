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
def reply(frame, result):
    send(dict(id=frame["id"], result=result))
reply(read(), dict(protocolVersion=1, agentCapabilities={}))
reply(read(), dict(sessionId="synthetic-native"))
frame = read()
(out / "ready").touch()
if mode in ["cancel", "close"]:
    time.sleep(60)
elif mode == "eof":
    sys.exit(0)
elif mode == "remote-error":
    send(dict(id=frame["id"], error=dict(code=-32000, message="synthetic refusal")))
elif mode == "malformed":
    reply(frame, [])
elif mode == "wrong-id":
    send(dict(id=999, result=dict(stopReason="end_turn")))
else:
    for index in range(256):
        send(dict(method="session/update", params=dict(sessionId="synthetic-native", index=index)))
        if index == 127:
            send(dict(id="native-request", method="fixture/request", params=dict(index=index)))
            response = read()
            assert response == dict(jsonrpc="2.0", id="native-request", result=dict(accepted=True)), response
    reply(frame, dict(stopReason="end_turn", usage=dict(inputTokens=3)))
sys.stdin.read()
'''


def absent(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return True
    return False


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    built = subprocess.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml',
                            '-p', 'oxap-adapter-acp', '--example', 'prompt'],
                           cwd=ROOT, capture_output=True, text=True, timeout=180)
    (output / 'build.log').write_text(built.stdout + built.stderr)
    assert built.returncode == 0, built.stderr
    binary = ROOT / 'rust/target/debug/examples/prompt'
    with tempfile.TemporaryDirectory(prefix='oap-acp-prompt-') as temporary:
        fake = Path(temporary) / 'agent.py'
        fake.write_text(AGENT)
        modes = ['complete', 'cancel', 'close', 'remote-error', 'malformed', 'wrong-id', 'eof']
        for mode in modes:
            case = output / mode
            case.mkdir()
            command = [str(binary), mode, sys.executable, '-u', str(fake), mode, str(case)]
            consumer = subprocess.Popen(command, cwd=ROOT, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                        text=True, start_new_session=True)
            prefix = ''
            try:
                if mode in ['cancel', 'close']:
                    readable, _, _ = select.select([consumer.stdout], [], [], 10)
                    assert readable, 'native write admission did not wake its public consumer'
                    prefix = consumer.stdout.readline()
                    assert json.loads(prefix) == {'admitted': True}, prefix
                    limit = time.monotonic() + 5
                    while not (case / 'ready').exists() and time.monotonic() < limit:
                        time.sleep(0.005)
                    assert (case / 'ready').exists(), 'agent did not observe the actual prompt'
                    stdout, stderr = consumer.communicate('finish\n', timeout=5)
                else:
                    stdout, stderr = consumer.communicate(timeout=15)
                (case / 'consumer.log').write_text(prefix + stdout + stderr)
                assert consumer.returncode == 0, stderr
                lines = (prefix + stdout).splitlines()
                assert len(lines) == 2, lines
                assert json.loads(lines[0]) == {'admitted': True}, lines[0]
                observed = json.loads(lines[1])
                assert observed['dispatcher_released'], observed
                if mode == 'complete':
                    assert observed['result'] == {'stopReason': 'end_turn', 'usage': {'inputTokens': 3}}, observed
                    assert observed['error'] is None and observed['close_error'] is None, observed
                    events = observed['events']
                    assert len(events) == 257, len(events)
                    assert [event['params']['index'] for event in events if event['method'] == 'session/update'] == list(range(256)), events
                    assert events[128] == {'id':'native-request', 'method':'fixture/request', 'params':{'index':127}}, events[128]
                elif mode in ['cancel', 'close']:
                    assert observed['pending'] and observed['poll_us'] < 100_000, observed
                    assert observed['events'] == [], observed
                    if mode == 'cancel':
                        assert observed['cancel_ms'] < 1_500, observed
                    else:
                        assert 'shutdown timed out' in observed['close_error'] and observed['error'], observed
                else:
                    expected = {'remote-error':'-32000', 'malformed':'decode', 'wrong-id':'response id is not pending', 'eof':'EOF'}[mode]
                    assert expected in observed['error'] and observed['result'] is None, observed
                frames = [json.loads(line) for line in (case / 'wire.jsonl').read_text().splitlines()]
                assert [frame['method'] for frame in frames[:3]] == ['initialize', 'session/new', 'session/prompt'], frames
                assert frames[1]['params'] == {'cwd':'/', 'mcpServers':[]}, frames[1]
                assert frames[2]['params'] == {'sessionId':'synthetic-native', 'prompt':[{'type':'text', 'text':'synthetic prompt'}]}, frames[2]
                pid = int((case / 'pid').read_text())
                assert absent(pid), f'native child {pid} remains after cleanup'
            finally:
                if consumer.poll() is None:
                    os.killpg(consumer.pid, signal.SIGKILL)
                    consumer.communicate()
                if (case / 'pid').exists() and not absent(int((case / 'pid').read_text())):
                    os.killpg(consumer.pid, signal.SIGKILL)
            print(f'PASS {mode}: native wire/admission/order/result observed, child reaped, dispatcher released', flush=True)
    print(f'PASS native ACP prompt bridge. Native-layer ownership proof. Evidence: {output}')


if __name__ == '__main__':
    main()
