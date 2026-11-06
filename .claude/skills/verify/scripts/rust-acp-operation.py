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
    build = subprocess.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml',
                            '-p', 'oap-adapter-acp', '--example', 'operation'],
                           cwd=ROOT, capture_output=True, text=True, timeout=180)
    (output / 'build.log').write_text(build.stdout + build.stderr)
    assert build.returncode == 0, build.stderr
    binary = ROOT / 'rust/target/debug/examples/operation'
    with tempfile.TemporaryDirectory(prefix='oap-acp-operation-') as temporary:
        fake = Path(temporary) / 'agent.py'
        fake.write_text('''import json, os, sys
from pathlib import Path
mode, directory = sys.argv[1:]
out = Path(directory)
(out / 'pid').write_text(str(os.getpid()))
first = sys.stdin.readline()
(out / 'wire.jsonl').write_text(first)
if mode in ['start-cancel', 'deadline', 'early-cancel']:
    (out / 'ready').touch()
    sys.stdin.read()
    sys.exit(0)
print(json.dumps({'jsonrpc': '2.0', 'id': json.loads(first)['id'], 'result': {'protocolVersion': 1, 'agentCapabilities': {}}}), flush=True)
second = sys.stdin.readline()
with (out / 'wire.jsonl').open('a') as stream:
    stream.write(second)
(out / 'ready').touch()
if mode == 'complete':
    print(json.dumps({'jsonrpc': '2.0', 'id': json.loads(second)['id'], 'result': {'sessionId': 'synthetic-session'}}), flush=True)
sys.stdin.read()
''')
        modes = ['start-cancel', 'call-cancel', 'complete', 'deadline'] + ['early-cancel'] * 20
        for index, mode in enumerate(modes):
            case = output / f'{index:02d}-{mode}'
            case.mkdir()
            command = [str(binary), mode, sys.executable, '-u', str(fake), mode, str(case)]
            with subprocess.Popen(command, cwd=ROOT, stdin=subprocess.PIPE,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  text=True, start_new_session=True) as consumer:
                prefix = ''
                try:
                    if mode.endswith('-cancel') and mode != 'early-cancel':
                        ready, _, _ = select.select([consumer.stdout], [], [], 3)
                        assert ready, 'future poll blocked the consumer'
                        prefix = consumer.stdout.readline()
                        assert prefix == 'pending\n', prefix
                        limit = time.monotonic() + 3
                        while not (case / 'ready').exists() and time.monotonic() < limit:
                            time.sleep(0.005)
                        assert (case / 'ready').exists(), 'native request never reached the agent'
                        stdout, stderr = consumer.communicate('cancel\n', timeout=3)
                    else:
                        stdout, stderr = consumer.communicate(timeout=8)
                finally:
                    if consumer.poll() is None:
                        os.killpg(consumer.pid, signal.SIGKILL)
                        consumer.communicate()
                (case / 'consumer.log').write_text(prefix + stdout + stderr)
                assert consumer.returncode == 0, (mode, stderr)
                if mode in ['start-cancel', 'call-cancel']:
                    observed = json.loads(stdout)
                    assert observed['pending']
                    assert observed['poll_us'] < 100_000, observed
                    assert observed['cancel_ms'] < 1_500, observed
                elif mode == 'complete':
                    assert json.loads(stdout) == {
                        'result': {'sessionId': 'synthetic-session'}, 'close_error': None}
                elif mode == 'deadline':
                    assert 'context deadline exceeded' in stdout, stdout
                else:
                    assert stdout.strip() == 'cancelled', stdout
                if (case / 'pid').exists():
                    pid = int((case / 'pid').read_text())
                    if not absent(pid):
                        os.killpg(consumer.pid, signal.SIGKILL)
                        raise AssertionError(f'native child {pid} remains after consumer completion')
                if mode != 'early-cancel':
                    frames = [json.loads(line) for line in (case / 'wire.jsonl').read_text().splitlines()]
                    expected = ['initialize'] if mode in ['start-cancel', 'deadline'] else ['initialize', 'session/new']
                    assert [frame['method'] for frame in frames] == expected, frames
                    if len(frames) == 2:
                        assert frames[1]['params'] == {'cwd': '/', 'mcpServers': []}, frames
                print(f'PASS {index:02d} {mode}: expected wire/result, child reaped')
    print(f'PASS cancellable async ACP startup/calls. Evidence: {output}')


if __name__ == '__main__':
    main()
