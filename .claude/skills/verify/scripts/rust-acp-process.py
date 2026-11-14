import argparse
import json
import os
import signal
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def run(command, output, expected=0, timeout=120):
    with subprocess.Popen(command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          text=True, start_new_session=True) as child:
        try:
            stdout, stderr = child.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            stdout, stderr = child.communicate()
            output.write_text(stdout + stderr)
            raise
        result = subprocess.CompletedProcess(command, child.returncode, stdout, stderr)
    output.write_text(result.stdout + result.stderr)
    assert result.returncode == expected, (command, result.returncode, output)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml',
         '-p', 'oxap-adapter-acp', '--example', 'process'], output / 'build.log')
    run(['cargo', 'test', '--manifest-path', 'rust/Cargo.toml',
         '-p', 'oxap-adapter', '--test', 'jsonrpc_backpressure', '--', '--nocapture'],
        output / 'backpressure.log')
    binary = ROOT / 'rust/target/debug/examples/process'
    with tempfile.TemporaryDirectory(prefix='oap-rust-acp-') as temporary:
        fake = Path(temporary) / 'agent.py'
        fake.write_text('''import json, os, sys, time
from pathlib import Path
mode, directory = sys.argv[1:]
out = Path(directory)
(out / 'pid.txt').write_text(str(os.getpid()))
request = sys.stdin.readline()
(out / 'wire.jsonl').write_text(request)
if mode == 'quiet':
    for line in sys.stdin:
        pass
    sys.exit(0)
response = {
    'protocolVersion': 1,
    'agentCapabilities': {'loadSession': True},
    'authMethods': [{'id': 'synthetic'}],
    'agentInfo': {'name': 'fixture', 'version': '1'},
}
if mode == 'version':
    response['protocolVersion'] = 2
if mode == 'missing':
    del response['agentCapabilities']
if mode == 'null':
    response['agentCapabilities'] = None
if mode == 'stderr':
    print('token=synthetic-secret', file=sys.stderr, flush=True)
    sys.exit(3)
print(json.dumps({'jsonrpc': '2.0', 'id': json.loads(request)['id'], 'result': response}), flush=True)
for line in sys.stdin:
    with (out / 'wire.jsonl').open('a') as stream:
        stream.write(line)
if mode == 'stubborn':
    time.sleep(10)
''')
        for mode in ['ok', 'version', 'missing', 'null', 'quiet', 'stderr', 'stubborn']:
            case = output / mode
            case.mkdir(exist_ok=True)
            success = mode in ['ok', 'stubborn']
            result = run([str(binary), sys.executable, '-u', str(fake), mode, str(case)],
                         case / 'consumer.log', expected=0 if success else 1, timeout=8)
            wire = (case / 'wire.jsonl').read_text()
            frames = [json.loads(line) for line in wire.splitlines()]
            assert frames == [{
                'jsonrpc': '2.0',
                'id': 1,
                'method': 'initialize',
                'params': {
                    'protocolVersion': 1,
                    'clientCapabilities': {},
                    'clientInfo': {'name': 'synthetic<&>', 'version': '0.1'},
                },
            }], wire
            assert r'synthetic\u003c\u0026\u003e' in wire, wire
            if success:
                observed = json.loads(result.stdout)
                assert observed['protocolVersion'] == 1
                assert observed['agentCapabilities'] == {'loadSession': True}
                assert observed['agentInfo']['name'] == 'fixture'
                assert observed['authMethods'] == [{'id': 'synthetic'}]
                assert observed['closeError'] == (None if mode == 'ok' else 'acp rpc: shutdown timed out')
            else:
                expected = {
                    'version': 'unsupported protocol version 2',
                    'missing': 'agentCapabilities is required',
                    'null': 'agentCapabilities is required',
                    'quiet': 'context deadline exceeded',
                    'stderr': 'token=[REDACTED]',
                }[mode]
                assert expected in result.stderr, result.stderr
                assert 'synthetic-secret' not in result.stderr
            pid = int((case / 'pid.txt').read_text())
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                pass
            else:
                os.kill(pid, signal.SIGKILL)
                raise AssertionError(f'synthetic agent {pid} was not reaped')
            print(f'PASS {mode}: one initialize frame, expected consumer result, child reaped')
    print(f'PASS ACP process/handshake and shared backpressure. Evidence: {output}')


if __name__ == '__main__':
    main()
