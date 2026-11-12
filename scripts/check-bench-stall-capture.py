#!/usr/bin/env python3
"""Exercise stall capture through the resource benchmark CLI."""
import argparse
import json
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile


def main():
    cases = [('capture', 'hold-model', True), ('capture-error', 'hold-model', True),
             ('diagnostic-bytes', 'hold-model', True), ('disabled', 'hold-model', False), ('completed', 'empty', True)]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--allow-unavailable-stack', action='store_true')
    parser.add_argument('--case', choices=[name for name, _, _ in cases])
    args = parser.parse_args()
    if args.case:
        cases = [case for case in cases if case[0] == args.case]
    binary = args.binary.resolve()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    benchmark = Path(__file__).with_name('bench-resources.py')
    observations = []
    with tempfile.TemporaryDirectory(prefix='oap-stall-check-') as temporary:
        root = Path(temporary)
        home, scratch = root / 'home', root / 'scratch'
        home.mkdir()
        scratch.mkdir()
        environment = {'HOME': str(home), 'TMPDIR': str(scratch), 'NODE_ENV': 'test',
                       'PATH': '/usr/bin:/bin:/usr/sbin:/sbin'}
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            listener.listen(1)
            outbound = socket.create_connection(listener.getsockname())
            inbound, _ = listener.accept()
            with inbound, outbound:
                inbound.shutdown(socket.SHUT_WR)
                assert outbound.recv(1) == b''
                outbound.close()
                assert inbound.recv(1) == b''
        commands = root / 'commands'
        commands.mkdir()
        process_listing = commands / 'ps'
        process_listing.write_text(
            '#!/bin/sh\n'
            'case "$*" in\n'
            '*command*)\n'
            '  printf "PID PPID PGID ELAPSED STAT COMMAND\\n"\n'
            '  printf "999999 1 1 00:01 S bench-unrelated-private-argument\\n"\n'
            '  ;;\n'
            '*) exec /bin/ps "$@";;\n'
            'esac\n')
        process_listing.chmod(0o755)
        environment['PATH'] = str(commands) + ':' + environment['PATH']
        if sys.platform.startswith('linux'):
            netstat = commands / 'netstat'
            netstat.write_text('#!/bin/sh\necho netstat-unavailable >&2\nexit 1\n')
            netstat.chmod(0o755)
        for name, scenario, enabled in cases:
            result_dir = output / (name + '-result')
            capture_dir = output / (name + '-capture')
            command = [sys.executable, str(benchmark), '--targets', 'zig-loop',
                       '--scenarios', scenario, '--n', '1', '--hold', '120',
                       '--timeout', '30' if scenario == 'empty' else '1',
                       '--model-workers', '1', '--no-build', '--loop-binary', str(binary),
                       '--git-revision', 'verification', '--out', str(result_dir)]
            if name == 'capture-error':
                capture_dir.write_text('capture-unavailable')
            if enabled:
                command += ['--capture-stalls', str(capture_dir)]
            case_environment = environment.copy()
            if name == 'diagnostic-bytes':
                malformed = root / 'malformed'
                malformed.mkdir()
                lsof = malformed / 'lsof'
                lsof.write_text("#!/bin/sh\nprintf '\\377'\n")
                lsof.chmod(0o755)
                case_environment['PATH'] = str(malformed) + ':' + environment['PATH']
            child = subprocess.run(command, env=case_environment, capture_output=True, text=True, timeout=240)
            (output / (name + '.log')).write_text(child.stdout + child.stderr)
            assert child.returncode == 0, name + ' CLI failed'
            rows = json.loads((result_dir / 'results.json').read_text())
            row = rows[0]
            observation = {'case': name, 'cli_exit': child.returncode, 'status': row['status']}
            if scenario == 'empty':
                assert row['status'] == 'ok', 'ordinary completion failed'
            else:
                assert row['status'].startswith('timeout after '), 'timeout status was lost'
            if name in ('capture', 'diagnostic-bytes'):
                capture = Path(row['stall_capture'])
                files = ['stuck.json', 'processes.txt', 'sockets.txt', 'time-wait.txt', 'sample.txt']
                assert all((capture / item).stat().st_size > 0 for item in files), 'missing diagnostic'
                processes = (capture / 'processes.txt').read_text()
                assert 'bench-unrelated-private-argument' not in processes, 'unrelated process arguments were captured'
                assert '--stdio' not in processes, 'hub arguments were captured'
                process_rows = [line for line in processes.splitlines() if re.match(r'^\s*\d+\s', line)]
                assert len(process_rows) == 1 and 'bin-zig-loop' in process_rows[0], 'process capture exceeded the hub'
                if name == 'diagnostic-bytes':
                    assert '\ufffd' in (capture / 'sockets.txt').read_text(encoding='utf-8'), 'invalid diagnostic bytes were lost'
                time_wait_count = int((capture / 'time-wait.txt').read_text().splitlines()[0])
                assert time_wait_count > 0, 'TIME_WAIT diagnostic reported a false zero'
                stuck = json.loads((capture / 'stuck.json').read_text())
                assert 's0' in stuck['left'] and stuck['left']['s0'] > 0, 'unfinished session was not retained'
                assert stuck['states']['s0']['active_runs'], 'last state omitted the active run'
                assert (capture / 'run/hub.json').is_file(), 'run directory was deleted'
                assert (capture / 'run/hub-stderr.txt').is_file(), 'hub log was deleted'
                sample = (capture / 'sample.txt').read_text()
                stack = 'Call graph:' in sample or re.search(r'^#0\s', sample, re.MULTILINE) is not None
                assert stack or args.allow_unavailable_stack, 'stack sampling unavailable'
                observation.update(diagnostic_files=len(files), run_retained=True,
                                   live_stack_captured=stack, time_wait_count=time_wait_count)
            elif name == 'capture-error':
                assert row.get('stall_capture_error'), 'capture error was omitted'
                retained = Path(row['stall_run'])
                assert (retained / 'hub-stderr.txt').is_file(), 'hub log was deleted after capture failure'
                assert (retained / 'data/sessions.jsonl').is_file(), 'session history was deleted after capture failure'
                kept = output / 'capture-error-run'
                assert retained.parent.parent == scratch, 'retained run escaped the test scratch directory'
                shutil.move(str(retained.parent), kept)
                observation['run_retained'] = True
                assert capture_dir.read_text() == 'capture-unavailable', 'capture path was overwritten'
            else:
                assert 'stall_capture' not in row, 'capture recorded for an ineligible case'
                assert not capture_dir.exists(), 'diagnostics were written for an ineligible case'
            assert not any(scratch.iterdir()), 'benchmark scratch directory leaked'
            observations.append(observation)
    (output / 'checks.json').write_text(json.dumps(observations, indent=2) + '\n')
    print(json.dumps({'checks_passed': len(observations), 'checks_total': len(cases),
                      'live_stack_captured': observations[0].get('live_stack_captured')}))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
