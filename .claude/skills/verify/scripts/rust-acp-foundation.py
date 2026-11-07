import argparse
import copy
import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def run(command, output, value=None):
    result = subprocess.run(command, cwd=ROOT, input=value, text=True,
                            capture_output=True, timeout=180)
    output.write_text(result.stdout + result.stderr)
    assert result.returncode == 0, (command, output, result.returncode)
    return result.stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml',
         '-p', 'oap-adapter-acp', '--example', 'foundation'], output / 'build.log')
    binary = ROOT / 'rust/target/debug/examples/foundation'
    catalog = json.loads((ROOT / 'harnesses/acp.json').read_text())
    pin = next(version for version in catalog['versions'] if version['status'] == 'current')
    observed = None
    with tempfile.TemporaryDirectory(prefix='oap-acp-foundation-') as temporary:
        directory = Path(temporary)
        marker = directory / 'unexpected-spawn'
        executable = directory / 'synthetic-agent'
        executable.write_text(f'#!/bin/sh\ntouch "{marker}"\nexit 99\n')
        executable.chmod(0o700)
        valid = {
            'path': str(executable),
            'dir': str(directory),
            'participant': 'synthetic-user',
            'servers': [{'name': 'local', 'command': '/synthetic/mcp', 'args': []}],
        }
        for name, patch, expected in [
            ('probe', {}, None),
            ('participant', {'participant': ''}, 'participant identity is required'),
            ('reasoning', {'reasoning_level': 'high'}, 'session.reasoning'),
            ('empty-executable', {'path': ''}, 'executable is required'),
            ('relative-directory', {'dir': 'relative'}, 'absolute working directory is required'),
            ('unnamed-mcp', {'servers': [{}]}, 'needs a name'),
            ('duplicate-mcp', {'servers': [{'name': 'x'}, {'name': 'x'}]}, 'configured twice'),
        ]:
            case = copy.deepcopy(valid)
            case.update(patch)
            (output / f'{name}.input.json').write_text(json.dumps(case))
            result = json.loads(run([str(binary)], output / f'{name}.output.json', json.dumps(case)))
            if 'configError' in result:
                assert expected in result['configError'], result
            else:
                assert result['admitError'] == result['openError'], result
                if expected is None:
                    assert result['openError'] is None, result
                else:
                    assert expected in result['openError'], result
                assert result['descriptor']['endpoint']['version'] == pin['endpoint_version']
                assert result['revision'] == pin['capability_revision'] + '-rust-cancel'
                assert result['corpus'] == pin['corpus']
                assert result['journalReplay'] == 'unavailable'
                assert result['descriptor']['sources'] == [
                    {'id': 'local', 'kind': 'process', 'protocol': 'mcp'}]
                for key, support in result['descriptor']['features'].items():
                    level = {
                        'protocol.initialize': 'emulated', 'capabilities': 'emulated',
                        'session.open': 'native', 'session.state': 'emulated',
                        'action.permissions': 'native', 'run.cancel': 'native',
                        'models.list': 'degraded', 'session.model.switch': 'degraded',
                        'session.message.submit': 'degraded', 'run.streaming': 'degraded',
                        'session.message.delivery.auto': 'emulated', 'run.status': 'emulated',
                        'action.tools': 'degraded', 'action.tools.execute': 'degraded',
                    }.get(key, 'unavailable')
                    assert support['level'] == level, (key, support)
                observed = result
            assert not marker.exists(), 'probe/admission unexpectedly launched the configured process'
            print(f'PASS {name}: expected public result, no native process launched')
    common = {
        'protocol': 'open-agent-protocol', 'version': '0.1',
        'profile': 'open-agent-protocol.agent-control-core',
    }
    trace = [
        dict(common, id='cap-request', type='capabilities.request', payload={}),
        dict(common, id='cap-response', type='capabilities.response',
             in_reply_to='cap-request', capability_revision=observed['revision'],
             payload=observed['descriptor']),
    ]
    path = output / 'capabilities.trace.json'
    path.write_text(json.dumps(trace))
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml',
         '-p', 'oap-validation', '--example', 'validate'], output / 'validator-build.log')
    run(['go', 'build', '-o', str(output / 'goap'), './go/cmd/goap'], output / 'go-build.log')
    for name, command in [
        ('go', [str(output / 'goap'), 'validate']),
        ('rust', [str(ROOT / 'rust/target/debug/examples/validate')]),
        ('zig', [str(ROOT / 'zig/zig-out/bin/oapx'), 'validate']),
    ]:
        run(command + [str(path)], output / f'capabilities-{name}.log')
        print(f'PASS public descriptor capabilities exchange validates with {name}')
    print(f'PASS ACP foundation. Evidence: {output}')


if __name__ == '__main__':
    main()
