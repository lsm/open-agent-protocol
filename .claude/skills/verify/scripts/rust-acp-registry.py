import argparse
import binascii
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile


PEER = '''import json,os,sys
from pathlib import Path
out=Path(sys.argv[1])
(out/'native-pid').write_text(str(os.getpid()))
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
models=[dict(id='model',name='Model',type='select',category='model',currentValue='synthetic-model',options=[dict(value='synthetic-model',name='Synthetic')])]
for line in sys.stdin:
 request=json.loads(line)
 with (out/'native.jsonl').open('a') as log:
  log.write(json.dumps(dict(cwd=os.getcwd(),synthetic=os.getenv('SYNTHETIC'),resolved=os.getenv('REGISTRY_TEST_VALUE'),inherited_home='HOME' in os.environ,request=request))+'\\n')
 method=request.get('method')
 if method=='initialize': result=dict(protocolVersion=1,agentCapabilities={})
 elif method=='session/new': result=dict(sessionId='synthetic-native',configOptions=models)
 elif method=='session/prompt':
  assert request['params']['prompt']==[dict(type='text',text='synthetic')],request
  prompt=request['id']
  send(dict(method='session/update',params=dict(sessionId='synthetic-native',update=dict(sessionUpdate='agent_message_chunk',content=dict(type='text',text='synthetic reply')))))
  send(dict(id='permission',method='session/request_permission',params=dict(sessionId='synthetic-native',toolCall=dict(toolCallId='native-tool',title='Read'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])) )
  continue
 elif method is None:
  assert request['id']=='permission' and request['result']['outcome']==dict(outcome='selected',optionId='allow'),request
  send(dict(id=prompt,result=dict(stopReason='end_turn')))
  continue
 else: raise AssertionError(request)
 send(dict(id=request['id'],result=result))
'''


class Client:
    def __init__(self, binary, root, config, output):
        self.serial = 0
        self.revision = None
        self.trace = []
        self.events = []
        self.refused = set()
        self.output = output
        self.wire = (output / 'wire.jsonl').open('w')
        self.stderr = (output / 'stderr.txt').open('w')
        self.process = subprocess.Popen(
            [binary, 'serve', 'agent', '--backend=fixture', '--config', str(config),
             '--session-history', str(root / 'history.jsonl')],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.stderr,
            cwd=root, env={'HOME': str(root / 'home'), 'PATH': os.environ['PATH'],
                           'REGISTRY_TEST_VALUE': 'resolved'},
            start_new_session=True, bufsize=0)

    def read(self):
        assert select.select([self.process.stdout], [], [], 10)[0], 'CLI answer timed out'
        line = self.process.stdout.readline()
        assert line, f'CLI ended early; see {self.output / "stderr.txt"}'
        answer = json.loads(line)
        self.trace.append(answer)
        self.wire.write(json.dumps(dict(direction='agent', envelope=answer)) + '\n')
        self.wire.flush()
        if 'sequence' in answer and not answer['type'].endswith('.response'):
            self.events.append(answer)
        return answer

    def call(self, kind, expected_error=None, **payload):
        self.serial += 1
        request = dict(protocol='open-agent-protocol', version='0.1',
                       profile='open-agent-protocol.agent-control-core',
                       type=kind, id=str(self.serial), payload=payload)
        for key in ['session_id', 'run_id']:
            if key in payload:
                request[key] = payload[key]
        if self.revision:
            request['capability_revision'] = self.revision
        self.trace.append(request)
        self.wire.write(json.dumps(dict(direction='host', envelope=request)) + '\n')
        self.process.stdin.write((json.dumps(request) + '\n').encode())
        self.process.stdin.flush()
        while True:
            answer = self.read()
            if answer.get('in_reply_to') == request['id']:
                break
        if expected_error:
            assert answer['type'] == 'error.response', answer
            assert answer['payload']['error']['code'] == expected_error, answer
            self.refused.add(request['id'])
        else:
            assert answer['type'] != 'error.response', answer
        return answer

    def close(self):
        try:
            self.process.stdin.close()
            self.process.stdin = None
            stdout, _ = self.process.communicate(timeout=10)
            assert self.process.returncode == 0, stdout
            native = self.output / 'native-pid'
            if native.exists():
                try:
                    os.kill(int(native.read_text()), 0)
                except ProcessLookupError:
                    pass
                else:
                    raise AssertionError('native child survived normal EOF cleanup')
        finally:
            try:
                os.killpg(self.process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            self.process.communicate(timeout=10)
            self.wire.close()
            self.stderr.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--output-dir', required=True, type=Path)
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix='oap-acp-registry-') as temporary:
        root = Path(temporary).resolve()
        workspace = root / 'workspace'
        workspace.mkdir()
        (root / 'home').mkdir()
        peer = root / 'peer.py'
        peer.write_text(PEER)
        config = root / 'registry.json'
        config.write_text(json.dumps(dict(adapters=dict(fixture=dict(
            type='acp', executable=sys.executable, args=['-u', str(peer), str(output)],
            environment=['SYNTHETIC=only', 'REGISTRY_TEST_VALUE'], journal_capacity=0,
            working_directory=str(workspace))))))
        client = Client(str(args.binary.resolve()), root, config, output)
        try:
            initialized = client.call('protocol.initialize.request', protocol_versions=['0.1'],
                                      profiles=['open-agent-protocol.agent-control-core'],
                                      participant=dict(id='synthetic-user'))
            descriptor = client.call('capabilities.request')
            client.revision = descriptor['capability_revision']
            assert client.revision == initialized['capability_revision']
            catalog = json.loads((Path(__file__).resolve().parents[4] / 'harnesses/acp.json').read_text())
            pin = next(version for version in catalog['versions'] if version['status'] == 'current')
            assert client.revision == pin['capability_revision'], client.revision
            features = descriptor['payload']['features']
            assert features['models.list']['level'] == 'degraded', descriptor
            assert features['session.message.submit']['level'] == 'emulated', descriptor
            assert features['run.streaming']['level'] == 'native', descriptor
            (output / 'descriptor.json').write_text(json.dumps(descriptor, indent=2))
            opened = client.call('session.open.request', session_id='host-session')
            assert opened['payload']['session_id'] == 'host-session', opened
            client.call('models.request', expected_error='capability_degraded', session_id='host-session')
            models = client.call('models.request', session_id='host-session',
                                 allow_degraded_features=['models.list'])
            assert [model['id'] for model in models['payload']['models']] == ['synthetic-model'], models
            submit = dict(session_id='host-session', delivery='auto',
                          messages=[dict(role='user', content='synthetic')])
            admission = client.call('session.message.submit.request', **submit)
            assert admission['payload']['accepted'], admission
            while not any(event['type'] == 'action.permission.requested' for event in client.events):
                client.read()
            permission = next(event for event in client.events if event['type'] == 'action.permission.requested')
            answer = {key: permission['payload'][key] for key in
                      ['session_id', 'run_id', 'interaction_id', 'requested_by', 'responded_by']}
            assert answer['responded_by'] == 'synthetic-user', answer
            answer.update(choice_id='allow', granted=True)
            wrong = dict(answer, responded_by='another-user')
            client.call('action.permission.resolve.request', expected_error='resolution_rejected', **wrong)
            client.call('action.permission.resolve.request', **answer)
            while not any(event['type'] == 'run.completed' for event in client.events):
                client.read()
            assert any(event['type'] == 'content.delta' for event in client.events), client.events
            assert any(event['type'] == 'action.permission.resolved' for event in client.events), client.events
            assert [event['sequence'] for event in client.events] == list(range(1, len(client.events) + 1)), client.events
            assert all(event['run_id'] == admission['payload']['run_id'] for event in client.events), client.events
            assert client.trace.index(admission) < client.trace.index(client.events[0]), client.trace
            state = client.call('session.state.request', session_id='host-session')
            assert state['payload']['status'] == 'idle', state
        finally:
            client.close()
        native = [json.loads(line) for line in (output / 'native.jsonl').read_text().splitlines()]
        assert [line['request'].get('method') for line in native] == ['initialize', 'session/new', 'session/prompt', None], native
        assert all(line['cwd'] == str(workspace) for line in native), native
        assert all(line['synthetic'] == 'only' and line['resolved'] == 'resolved' and not line['inherited_home'] for line in native), native
        assert native[1]['request']['params']['cwd'] == str(workspace), native
        (output / 'history.jsonl').write_bytes((root / 'history.jsonl').read_bytes())
        history = []
        for line in (root / 'history.jsonl').read_bytes().splitlines():
            checksum, payload = line.split(b' ', 1)
            assert int(checksum, 16) == binascii.crc32(payload), line
            history.append(json.loads(payload))
        (output / 'history-decoded.json').write_text(json.dumps(history, indent=2))
        assert [entry['action'] for entry in history] == ['opened', 'closed'], history
        assert all(entry['record']['native_session_id'] == 'synthetic-native' and
                   entry['record']['directory'] == str(workspace) for entry in history), history
        accepted = [frame for frame in client.trace if frame['id'] not in client.refused and
                    frame.get('in_reply_to') not in client.refused]
        (output / 'accepted.trace.json').write_text(json.dumps(accepted, indent=2))
        for label, fields, diagnostic in [
            ('journal', dict(journal_capacity=1), 'nonzero journal_capacity'),
            ('directory', dict(any_directory=True), 'any_directory'),
            ('relative', dict(working_directory='relative'), 'absolute working directory'),
            ('executable', dict(executable=''), 'executable is required'),
        ]:
            document = json.loads(config.read_text())
            document['adapters']['fixture'].update(fields)
            refused = root / f'{label}.json'
            refused.write_text(json.dumps(document))
            result = subprocess.run([str(args.binary.resolve()), 'serve', '--stdio', '--config', str(refused)],
                                    input='', capture_output=True, text=True, timeout=10,
                                    env={'HOME': str(root / 'home'), 'REGISTRY_TEST_VALUE': 'resolved'})
            (output / f'{label}-refusal.txt').write_text(result.stdout + result.stderr)
            assert result.returncode != 0 and diagnostic in result.stderr, result
    print('PASS: configured ACP CLI preserved fixed cwd/native identity, the models opt-in, admission without an opt-in before events, sequenced permissions, participant refusal and cleanup; unsupported config refused')


if __name__ == '__main__':
    main()
