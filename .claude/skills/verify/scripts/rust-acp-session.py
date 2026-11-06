import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[4]


def run(command, output, value=None, expected=0, timeout=180):
    with subprocess.Popen(command, cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, text=True, start_new_session=True) as child:
        try:
            stdout, stderr = child.communicate(value, timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            stdout, stderr = child.communicate()
            output.write_text(stdout + stderr)
            raise
    output.write_text(stdout + stderr)
    assert child.returncode == expected, (command, child.returncode, output)
    return stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp',
         '--example', 'foundation'], output / 'build.log')
    binary = ROOT / 'rust/target/debug/examples/foundation'
    option = {
        'id': 'model', 'name': 'Model', 'type': 'select', 'category': 'model',
        'currentValue': 'luna', 'options': [
            {'value': 'sol', 'name': 'Sol'},
            {'name': 'Group', 'options': [{'value': 'luna', 'name': 'Luna'}]},
        ],
    }
    observed = None
    with tempfile.TemporaryDirectory(prefix='oap-acp-session-') as temporary:
        directory = Path(temporary)
        for mode in ['models', 'no-model', 'placeholder', 'empty-id', 'null', 'bad-id', 'close-timeout']:
            case = output / mode
            case.mkdir()
            native = {'sessionId': 'native-session', 'configOptions': [dict(option)]}
            if mode == 'no-model':
                native.pop('configOptions')
            if mode == 'placeholder':
                native['configOptions'][0]['currentValue'] = 'default'
            if mode == 'empty-id':
                native['sessionId'] = ''
            if mode == 'null':
                native = None
            if mode == 'bad-id':
                native['sessionId'] = 17
            (case / 'native-result.json').write_text(json.dumps(native))
            fake = directory / f'agent-{mode}'
            fake.write_text(f'#!{sys.executable} -u\n' + f'''import json, os, sys, time
from pathlib import Path
out = Path({str(case)!r})
(out / 'pid').write_text(str(os.getpid()))
first = sys.stdin.readline()
(out / 'wire.jsonl').write_text(first)
print(json.dumps({{'jsonrpc':'2.0', 'id':json.loads(first)['id'], 'result':{{'protocolVersion':1, 'agentCapabilities':{{}}}}}}), flush=True)
second = sys.stdin.readline()
with (out / 'wire.jsonl').open('a') as wire:
    wire.write(second)
result = json.loads((out / 'native-result.json').read_text())
print(json.dumps({{'jsonrpc':'2.0', 'id':json.loads(second)['id'], 'result':result}}), flush=True)
sys.stdin.read()
if {mode!r} == 'close-timeout':
    time.sleep(30)
''')
            fake.chmod(0o700)
            request = {
                'path': str(fake), 'dir': str(directory), 'participant': 'synthetic-user',
                'open': True, 'servers': [{'name': 'local', 'command': '/synthetic/mcp', 'args': []}],
            }
            (case / 'consumer-input.json').write_text(json.dumps(request))
            before = time.time_ns() // 1_000_000
            result = json.loads(run([str(binary)], case / 'consumer-output.json', json.dumps(request), timeout=8))
            after = time.time_ns() // 1_000_000
            pid = int((case / 'pid').read_text())
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                pass
            else:
                os.kill(pid, signal.SIGKILL)
                raise AssertionError(f'native child {pid} remains')
            frames = [json.loads(line) for line in (case / 'wire.jsonl').read_text().splitlines()]
            assert [frame['method'] for frame in frames] == ['initialize', 'session/new'], frames
            assert frames[0]['params']['clientInfo'] == {'name': 'open-agent-protocol', 'version': '0.1'}
            assert frames[1]['params'] == {
                'cwd': str(directory), 'mcpServers': [{'name': 'local', 'command': '/synthetic/mcp'}]}, frames
            if mode in ['empty-id', 'null', 'bad-id']:
                expected = 'session/new returned no session id' if mode == 'empty-id' else 'decode session/new response:'
                assert expected in result['openError'], result
            else:
                state = result['state']
                assert state['session_id'] == 'session-1' and state['status'] == 'idle', state
                assert before <= state['updated_at_ms'] <= after
                assert result['nativeId'] == 'native-session'
                assert state['sources'] == [{'id': 'local', 'kind': 'process', 'protocol': 'mcp'}]
                assert result['closedStateError'] == 'adapter: session closed'
                assert result['secondCloseError'] is None
                assert result['closeError'] == ('acp rpc: shutdown timed out' if mode == 'close-timeout' else None)
                if mode == 'no-model':
                    assert result['models'] is None and 'no configOption' in result['modelError'], result
                else:
                    assert [model['id'] for model in result['models']['models']] == ['sol', 'luna']
                    current = '' if mode == 'placeholder' else 'luna'
                    assert state.get('current_model_id', '') == current
                    assert result['models'].get('current_model_id', '') == current
                    assert [model['id'] for model in result['models']['models'] if model.get('default')] == ([] if mode == 'placeholder' else ['luna'])
                if mode == 'models':
                    observed = result
                    request['open'] = False
                    probe = json.loads(run([str(binary)], case / 'probe.json', json.dumps(request)))
            print(f'PASS {mode}: public state/catalog/error and child reap observed')
    common = {'protocol': 'open-agent-protocol', 'version': '0.1',
              'profile': 'open-agent-protocol.agent-control-core'}
    revision = observed['revision']
    session_id = observed['state']['session_id']
    trace = [
        dict(common, id='init', type='protocol.initialize.request', payload={
            'protocol_versions':['0.1'], 'profiles':[common['profile']], 'participant':{'id':'synthetic-user'}}),
        dict(common, id='initialized', type='protocol.initialize.response', in_reply_to='init', payload={
            'protocol_version':'0.1', 'profile':common['profile'], 'endpoint':probe['descriptor']['endpoint']}),
        dict(common, id='caps', type='capabilities.request', payload={}),
        dict(common, id='capabilities', type='capabilities.response', in_reply_to='caps',
             capability_revision=revision, payload=probe['descriptor']),
        dict(common, id='open', type='session.open.request', capability_revision=revision,
             payload={'session_id':session_id}),
        dict(common, id='opened', type='session.open.response', in_reply_to='open',
             session_id=session_id, capability_revision=revision, payload=observed['state']),
        dict(common, id='models', type='models.request', session_id=session_id,
             capability_revision=revision, payload={'session_id':session_id, 'allow_degraded_features':['models.list']}),
        dict(common, id='catalog', type='models.response', in_reply_to='models',
             session_id=session_id, capability_revision=revision, payload=observed['models']),
    ]
    path = output / 'open-models.trace.json'
    path.write_text(json.dumps(trace))
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-validation',
         '--example', 'validate'], output / 'validator-build.log')
    run(['go', 'build', '-o', str(output / 'goap'), './go/cmd/goap'], output / 'go-build.log')
    for name, command in [
        ('go', [str(output / 'goap'), 'validate']),
        ('rust', [str(ROOT / 'rust/target/debug/examples/validate')]),
        ('zig', [str(ROOT / 'zig/zig-out/bin/oapx'), 'validate']),
    ]:
        run(command + [str(path)], output / f'trace-{name}.log')
        print(f'PASS observed opening/catalog trace validates with {name}')
    print(f'PASS Rust ACP session opening. Evidence: {output}')


if __name__ == '__main__':
    main()
