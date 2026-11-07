import argparse
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('permissions', Path(__file__).with_name('rust-acp-session-permissions.py'))
permissions = importlib.util.module_from_spec(spec)
spec.loader.exec_module(permissions)
peer = permissions.outbound


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    built = subprocess.run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp', '--example', 'session_permission_ownership'], cwd=ROOT, capture_output=True, text=True, timeout=180)
    (output/'build.log').write_text(built.stdout+built.stderr)
    assert built.returncode == 0, built.stderr
    with tempfile.TemporaryDirectory(prefix='oap-cancel-owner-') as temporary:
        agent = Path(temporary)/'agent.py'
        agent.write_text(permissions.AGENT.replace("if mode.startswith('resolve'):", "if mode=='gate':\n (out/'ready').touch()\n cancel=read()\n assert cancel==dict(jsonrpc='2.0',method='session/cancel',params=dict(sessionId='native')),cancel\n (out/'cancel.json').write_text(json.dumps(cancel))\nif mode.startswith('resolve') or mode=='gate':"))
        for mode in ['cancel-drop', 'cancel-close', 'cancel-gate-drop', 'cancel-gate-close']:
            case = output/mode
            case.mkdir()
            gate = mode.startswith('cancel-gate')
            command = [str(ROOT/'rust/target/debug/examples/session_permission_ownership'), mode, sys.executable, '-u', str(agent), 'gate' if gate else 'resolve-cancel', str(case)]
            child = subprocess.Popen(command, cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
            observations = []
            communicated = False
            try:
                observations.append(peer.read(child))
                before, raw = peer.ownership.threads(child.pid)
                (case/'threads-before.txt').write_text(raw)
                peer.send(child)
                observations.append(peer.read(child))
                opened, raw = peer.ownership.threads(child.pid)
                (case/'threads-open.txt').write_text(raw)
                peer.send(child)
                observations.append(peer.read(child))
                deadline = time.monotonic()+10
                while not (case/'ready').exists() and time.monotonic() < deadline:
                    time.sleep(.005)
                assert (case/'ready').exists(), 'native response did not reach the write boundary'
                if not gate:
                    assert len((case/'partial-write').read_bytes()) == 64
                peer.send(child)
                pending = peer.read(child)
                observations.append(pending)
                assert pending['cancelling'] and pending['poll_us'] < 50000, pending
                if gate:
                    deadline = time.monotonic()+10
                    while not (case/'partial-write').exists() and time.monotonic() < deadline:
                        time.sleep(.005)
                    assert len((case/'partial-write').read_bytes()) == 64
                    assert json.loads((case/'cancel.json').read_text())['method'] == 'session/cancel'
                native = int((case/'pid').read_text())
                assert not peer.ownership.absent(native), 'peer exited before cancellation cleanup'
                _, raw = peer.ownership.threads(child.pid)
                (case/'threads-cancel-pending.txt').write_text(raw)
                peer.send(child)
                result = peer.read(child)
                observations.append(result)
                assert peer.ownership.absent(native), 'native child survived cancellation cleanup'
                deadline = time.monotonic()+5
                after, raw = peer.ownership.threads(child.pid)
                while after != before and time.monotonic() < deadline:
                    time.sleep(.01)
                    after, raw = peer.ownership.threads(child.pid)
                (case/'threads-after.txt').write_text(raw)
                assert after == before, (before, opened, after)
                assert result['cleanup_ms'] < 3000 and result['poll_us'] < 50000, result
                assert not any(event['type'] == 'run.cancelled' for event in result['events']), result
                statuses = [event for event in result['events'] if event['type'] == 'run.status.updated']
                assert len(statuses) == int(gate), result
                if mode.endswith('-close'):
                    assert len(result['events']) == (4 if gate else 3), result
                peer.send(child)
                stdout, stderr = child.communicate(timeout=5)
                communicated = True
                (case/'stderr.txt').write_text(stderr)
                assert child.returncode == 0, (stdout, stderr)
                (case/'observed.json').write_text(json.dumps(dict(observations=observations, threads=dict(before=before, opened=opened, after=after)), indent=2))
                print(f'PASS {mode}: 64-byte native partial write before/after intent={"after" if gate else "before"}; peer reaped, no invented terminal, joined OS threads {before}->{opened}->{after}', flush=True)
            finally:
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGKILL)
                if not communicated:
                    stdout, stderr = child.communicate(timeout=5)
                    (case/'failure-stdout.txt').write_text(stdout)
                    (case/'stderr.txt').write_text(stderr)
                    (case/'failure-observations.json').write_text(json.dumps(observations, indent=2))
                if (case/'pid').exists() and not peer.ownership.absent(int((case/'pid').read_text())):
                    os.kill(int((case/'pid').read_text()), signal.SIGKILL)
    print(f'PASS public Session cancel ownership. Synthetic native inputs only. Evidence: {output}')


if __name__ == '__main__':
    main()
