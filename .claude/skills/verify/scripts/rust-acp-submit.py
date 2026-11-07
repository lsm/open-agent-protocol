import argparse
import importlib.util
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
spec = importlib.util.spec_from_file_location('ownership', Path(__file__).with_name('rust-acp-session-bridge.py'))
ownership = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ownership)
AGENT = '''import json, os, sys, time
from pathlib import Path
mode, directory = sys.argv[1:]
out = Path(directory)
(out / 'pid').write_text(str(os.getpid()))
def send(value):
    print(json.dumps(dict(jsonrpc='2.0', **value)), flush=True)
def read():
    line = sys.stdin.readline()
    with (out/'wire.jsonl').open('a') as stream: stream.write(line)
    return json.loads(line)
request = read()
send(dict(id=request['id'], result=dict(protocolVersion=1, agentCapabilities={})))
request = read()
send(dict(id=request['id'], result=dict(sessionId='native')))
if 'admission' in mode:
    (out/'ready').touch()
    time.sleep(60)
else:
    request = read()
    assert request['params'] == dict(sessionId='native', prompt=[dict(type='text', text='hi')]), request
    (out/'prompt').touch()
    while not (out/'release').exists(): time.sleep(.005)
    if mode in ['complete', 'overflow']:
        for text in (['x']*100 if mode == 'overflow' else ['Hello', ' 世界']):
            send(dict(method='session/update', params=dict(sessionId='native', update=dict(sessionUpdate='agent_message_chunk', content=dict(type='text',text=text)))))
        send(dict(id=request['id'], result=dict(stopReason='end_turn')))
        (out/'settled').touch()
        sys.stdin.read()
    else:
        time.sleep(60)
'''


def run(command, log):
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=180)
    log.write_text(result.stdout + result.stderr)
    assert result.returncode == 0, log


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output-dir', required=True, type=Path)
    output = parser.parse_args().output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    run(['cargo', 'build', '--manifest-path', 'rust/Cargo.toml', '-p', 'oap-adapter-acp', '--example', 'submit_owner'], output/'build.log')
    with tempfile.TemporaryDirectory(prefix='oap-submit-') as temporary:
        agent = Path(temporary)/'agent.py'
        agent.write_text(AGENT)
        for mode in ['complete', 'overflow', 'drop', 'abort-close', 'cancel-close', 'cancel-admission', 'close-admission']:
            case = output/mode
            case.mkdir()
            child = subprocess.Popen([str(ROOT/'rust/target/debug/examples/submit_owner'), mode, sys.executable, '-u', str(agent), mode, str(case)],
                                     cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
            phases, counts = [], {}
            def phase(name):
                ready, _, _ = select.select([child.stdout], [], [], 10)
                assert ready, f'{mode} did not reach {name}'
                line = child.stdout.readline()
                assert line, f'{mode} exited before {name}'
                value = json.loads(line)
                assert value['phase'] == name, value
                phases.append(value)
                return value
            def advance():
                child.stdin.write('continue\n')
                child.stdin.flush()
            try:
                phase('booted')
                counts['before'], raw = ownership.threads(child.pid)
                (case/'threads-before.txt').write_text(raw)
                advance()
                opened = phase('opened')
                assert opened['state']['status'] == 'idle', opened
                counts['open'], raw = ownership.threads(child.pid)
                (case/'threads-open.txt').write_text(raw)
                if 'admission' in mode: ownership.wait_file(case/'ready')
                advance()
                if 'admission' in mode:
                    assert phase('pending')['pending']
                    advance()
                else:
                    admitted = phase('admitted')
                    assert admitted['response']['accepted'] and admitted['state']['status'] == 'running', admitted
                    ownership.wait_file(case/'prompt')
                    (case/'release').touch()
                    if mode in ['complete', 'overflow']:
                        ownership.wait_file(case/'settled')
                    advance()
                    if mode in ['complete', 'overflow']:
                        settled = phase('settled')
                        assert settled['state']['status'] == 'idle', settled
                        advance()
                    elif mode == 'cancel-close':
                        assert phase('pending')['pending']
                        advance()
                cleaned = phase('cleaned')
                assert ownership.absent(int((case/'pid').read_text())), 'native child remains'
                counts['after'], raw = ownership.threads(child.pid)
                (case/'threads-after.txt').write_text(raw)
                assert counts['before'] == counts['after'], counts
                events = cleaned['events']
                if mode == 'complete':
                    assert [event['type'] for event in events] == ['run.started', 'content.delta', 'content.delta', 'run.completed'], events
                    assert events[-1]['payload']['final_response']['content'] == 'Hello 世界', events
                    template = json.loads((ROOT/'fixtures/valid/core-completed.json').read_text())[:2]
                    for envelope in template:
                        envelope['session_id'] = 'oap-session'
                        envelope['capability_revision'] = opened['revision']
                    template[0]['payload'] = {'session_id':'oap-session','delivery':'auto','messages':[{'role':'user','content':'hi'}], 'allow_degraded_features':['session.message.submit','run.streaming']}
                    template[1]['payload'] = admitted['response']
                    (case/'trace.json').write_text(json.dumps(template + events, indent=2))
                elif mode == 'overflow':
                    assert len(events) == 65 and len(cleaned['errors']) == 1, cleaned
                    assert cleaned['errors'] == ['adapter: event stream consumer fell behind; resume from the last sequence'], cleaned['errors']
                else:
                    assert [event['type'] for event in events] == ([] if 'admission' in mode else ['run.started']), events
                if mode in ['abort-close', 'close-admission']:
                    assert 'shutdown timed out' in cleaned['closeError'], cleaned
                advance()
                stdout, stderr = child.communicate(timeout=5)
                (case/'consumer.stderr').write_text(stderr)
                assert child.returncode == 0 and not stdout, (stdout, stderr)
            finally:
                (case/'phases.json').write_text(json.dumps(phases, indent=2))
                (case/'thread-counts.json').write_text(json.dumps(counts, indent=2))
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.communicate()
                if (case/'pid').exists() and not ownership.absent(int((case/'pid').read_text())):
                    os.killpg(child.pid, signal.SIGKILL)
            print(f'PASS {mode}: public submit, native wire, reaped child and joined threads {counts}', flush=True)
    run(['go','build','-o',str(output/'goap'),'./go/cmd/goap'], output/'goap-build.log')
    run(['cargo','build','--manifest-path','rust/Cargo.toml','-p','oap-validation','--example','validate'], output/'validator-build.log')
    trace = str(output/'complete/trace.json')
    for name, command in [('go',[str(output/'goap'),'validate']), ('rust',[str(ROOT/'rust/target/debug/examples/validate')]), ('zig',[str(ROOT/'zig/zig-out/bin/oapx'),'validate'])]:
        run(command+[trace], output/f'validate-{name}.log')
    print(f'PASS normal Rust submit trace accepted by all three validators. Evidence: {output}', flush=True)


if __name__ == '__main__':
    main()
