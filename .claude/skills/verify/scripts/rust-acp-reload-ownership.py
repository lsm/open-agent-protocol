import argparse
import importlib.util
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('owner', Path(__file__).with_name('rust-acp-session-bridge.py'))
owner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(owner)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    build = subprocess.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp', '--example', 'reload_owner'], cwd=ROOT, capture_output=True, text=True, timeout=180)
    (output / 'build.log').write_text(build.stdout + build.stderr)
    assert build.returncode == 0, build.stderr
    agent = Path(__file__).with_name('fixtures') / 'acp-reload-agent.py'
    for mode in ['cancel', 'partial', 'deadline']:
        case = output / mode
        case.mkdir()
        (case / 'native.json').write_text(json.dumps(dict(capabilities=dict(loadSession=True), result={}, quiet=True, history=320 if mode == 'cancel' else 0, blocked_reply=mode != 'cancel')))
        consumer = subprocess.Popen([str(ROOT / 'rust/target/debug/examples/reload_owner'), mode, sys.executable, str(agent), str(case)], cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
        phases, counts = [], {}
        completed = False

        def phase(expected):
            readable, _, _ = select.select([consumer.stdout], [], [], 10)
            assert readable, f'consumer did not reach {expected}'
            line = consumer.stdout.readline()
            assert line, f'consumer exited before {expected}'
            result = json.loads(line)
            phases.append(result)
            assert result['phase'] == expected, result
            return result

        def advance():
            consumer.stdin.write('continue\n')
            consumer.stdin.flush()

        try:
            phase('booted')
            counts['before'], raw = owner.threads(consumer.pid)
            (case / 'threads-before.txt').write_text(raw)
            advance()
            assert phase('pending')['pending']
            counts['pending'], raw = owner.threads(consumer.pid)
            (case / 'threads-pending.txt').write_text(raw)
            assert counts['pending'] > counts['before'], counts
            pid = int((case / 'pid').read_text())
            assert not owner.absent(pid), 'native was not alive at the pending phase'
            if mode != 'cancel':
                assert len((case / 'partial.bin').read_bytes()) == 64
            advance()
            cleaned = phase('cleaned')
            assert cleaned['elapsed_ms'] < 3000, cleaned
            assert owner.absent(pid), 'native survived operation completion/drop'
            if mode == 'deadline':
                assert 'deadline exceeded' in cleaned['error'], cleaned
            else:
                assert cleaned['error'] is None, cleaned
            deadline = time.monotonic() + 5
            counts['after'], raw = owner.threads(consumer.pid)
            while counts['after'] > counts['before'] and time.monotonic() < deadline:
                time.sleep(.01)
                counts['after'], raw = owner.threads(consumer.pid)
            (case / 'threads-after.txt').write_text(raw)
            assert counts['after'] == counts['before'], counts
            frames = [json.loads(line) for line in (case / 'wire.jsonl').read_text().splitlines()]
            host = [value['frame'] for value in frames if value['from'] == 'host']
            assert [frame.get('method') for frame in host[:2]] == ['initialize', 'session/load'], host[:2]
            if mode == 'cancel':
                assert host[-1] == dict(jsonrpc='2.0', id='reload-query', error=dict(code=-32601, message='method not supported during session reload'))
            advance()
            stdout, stderr = consumer.communicate(timeout=5)
            completed = True
            (case / 'consumer.stderr').write_text(stderr)
            assert consumer.returncode == 0 and not stdout, (stdout, stderr)
        finally:
            (case / 'phases.json').write_text(json.dumps(phases, indent=2))
            (case / 'thread-counts.json').write_text(json.dumps(counts, indent=2))
            if not completed:
                if consumer.poll() is None:
                    os.killpg(consumer.pid, signal.SIGKILL)
                stdout, stderr = consumer.communicate()
                (case / 'failed-consumer.log').write_text(stdout + stderr)
            if (case / 'pid').exists():
                pid = int((case / 'pid').read_text())
                if not owner.absent(pid):
                    os.kill(pid, signal.SIGKILL)
        print(f'PASS {mode}: actual native reload/partial reply, child reaped, workers joined; OS threads {counts}', flush=True)
    print(f'PASS reload ownership. Evidence: {output}', flush=True)


if __name__ == '__main__':
    main()
