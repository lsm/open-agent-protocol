import argparse
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile


PEER = r'''
import json,os,sys
log=sys.argv[1]
def emit(value): print(json.dumps(value),flush=True)
for line in sys.stdin:
 request=json.loads(line)
 with open(log,'a') as output: output.write(json.dumps({'cwd':os.getcwd(),'request':request})+'\n')
 method=request.get('method')
 if 'id' not in request: continue
 if method=='initialize': result={'userAgent':'synthetic','codexHome':'synthetic','platformFamily':'unix','platformOs':'synthetic'}
 elif method in ['thread/start','thread/resume']:
  thread=request.get('params',{}).get('threadId','native-'+os.path.basename(os.getcwd()))
  result={'thread':{'id':thread},'model':'synthetic-model','cwd':os.getcwd(),'approvalPolicy':'on-request',
          'sandbox':{'type':'workspaceWrite','writableRoots':[],'networkAccess':False}}
 elif method=='turn/start': result={'turn':{'id':'turn-1','status':'inProgress'}}
 else: result={}
 emit({'id':request['id'],'result':result})
 if method=='turn/start':
  thread=request['params']['threadId']
  emit({'method':'turn/started','params':{'threadId':thread,'turn':{'id':'turn-1','status':'inProgress'}}})
  emit({'method':'item/agentMessage/delta','params':{'threadId':thread,'turnId':'turn-1','itemId':'text-1','delta':'synthetic reply'}})
  emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':'turn-1','status':'completed'}}})
'''


class Hub:
    def __init__(self, binary, root, config, phase, out):
        self.serial = 0
        self.output = (out / f'{phase}-wire.jsonl').open('w')
        self.error = (out / f'{phase}-stderr.txt').open('w')
        env = {'PATH': os.environ['PATH'], 'HOME': str(root / 'home'),
               'XDG_DATA_HOME': str(root / 'data'), 'XDG_CONFIG_HOME': str(root / 'config')}
        self.process = subprocess.Popen([binary, 'serve', '--stdio', '--config', str(config),
                                         '--session-history', str(root / 'history.jsonl')],
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.error,
                                        env=env, start_new_session=True, bufsize=0)

    def call(self, op, **members):
        self.serial += 1
        request = {'id': self.serial, 'op': op, **members}
        self.process.stdin.write((json.dumps(request) + '\n').encode())
        self.process.stdin.flush()
        while True:
            assert select.select([self.process.stdout], [], [], 10)[0], f'{op} timed out'
            line = self.process.stdout.readline()
            assert line, f'{op} ended without a response'
            self.output.write(line.decode())
            self.output.flush()
            answer = json.loads(line)
            if answer.get('id') == self.serial and 'event' not in answer:
                return answer

    def close(self, expect_failure=False):
        try:
            self.process.stdin.close()
            self.process.stdin = None
            remaining, _ = self.process.communicate(timeout=10)
            self.output.write(remaining.decode())
            assert (self.process.returncode != 0) == expect_failure, f'unexpected hub exit {self.process.returncode}'
        finally:
            try:
                os.killpg(self.process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            self.process.wait()
            self.output.close()
            self.error.close()


def envelope(session, **payload):
    return {'protocol': 'open-agent-protocol', 'version': '0.1',
            'profile': 'open-agent-protocol.agent-control-core',
            'type': 'session.open.request', 'id': 'open-' + session,
            'payload': {'session_id': session, **payload}}


def opened(answer, session):
    assert 'error' not in answer, answer
    state = answer['result']['payload']
    assert state['session_id'] == session, state
    return state


def refused(answer, code='invalid_request'):
    assert answer.get('error', {}).get('code') == code, answer


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--output-dir', required=True)
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    out = Path(args.output_dir).resolve()
    out.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix='hub-directory-') as temporary:
        root = Path(temporary).resolve()
        for name in ['a', 'b', 'c', 'base', 'changed', 'home']:
            (root / name).mkdir()
        peer = root / 'peer.py'
        peer.write_text(PEER)
        log = out / 'native.jsonl'
        config = root / 'registry.json'

        def configure(base='base', any_directory=True):
            entry = {'type': 'codex', 'executable': sys.executable,
                     'args': [str(peer), str(log)], 'environment': ['PATH'],
                     'working_directory': str(root / base), 'model': 'synthetic-model',
                     'approval_policy': 'on-request', 'sandbox': 'workspace-write'}
            config.write_text(json.dumps({'adapters': {
                'placed': {**entry, 'any_directory': any_directory}, 'fixed': entry,
                'scripted': {'type': 'memory', 'working_directory': str(root / base), 'any_directory': True}}}))

        configure()
        hub = Hub(binary, root, config, 'initial', out)
        try:
            for session, place in [('named-one', 'a'), ('named-two', 'b')]:
                opened(hub.call('open', adapter='placed', directory=str(root / place),
                                request=envelope(session)), session)
                state = hub.call('work.status', session_id=session)['result']
                assert state['directory'] == str(root / place), state
            opened(hub.call('open', adapter='placed', directory=str(root / 'c'),
                            request=envelope('compound',
                                             message={'messages': [{'role': 'user', 'content': 'synthetic'}], 'delivery': 'auto'})), 'compound')
            opened(hub.call('open', adapter='scripted', directory=str(root / 'c'),
                            request=envelope('subscribed', subscribe=True)), 'subscribed')
            assert hub.call('work.status', session_id='subscribed')['result']['directory'] == str(root / 'c')
            opened(hub.call('open', adapter='placed', directory=None,
                            request=envelope('default')), 'default')
            opened(hub.call('open', adapter='fixed', request=envelope('fixed-default')), 'fixed-default')
            for index, directory in enumerate(['relative', '', str(root / 'missing'), str(peer)]):
                refused(hub.call('open', adapter='placed', directory=directory,
                                 request=envelope(f'refused-{index}')))
            refused(hub.call('open', adapter='fixed', directory=str(root / 'base'),
                             request=envelope('refused-fixed')))
            refused(hub.call('open', adapter='absent', directory=str(root / 'a'),
                             request=envelope('refused-adapter')), 'unknown_adapter')
            refused(hub.call('sessions', directory=None))
            sessions = hub.call('sessions')['result']['sessions']
            assert not any(row['session_id'].startswith('refused-') for row in sessions)
            hub.call('close', session_id='named-one')
            refused(hub.call('open', adapter='placed', directory=str(root / 'b'),
                             request=envelope('named-one', reopen=True)))
            opened(hub.call('open', adapter='placed', directory=str(root / 'a'),
                            request=envelope('named-one', reopen=True)), 'named-one')
        finally:
            hub.close()

        configure(base='changed')
        hub = Hub(binary, root, config, 'restart', out)
        try:
            recovered = opened(hub.call('open', adapter='placed', request=envelope('named-one', reopen=True)), 'named-one')
            assert recovered['recovery']['recovered'], recovered
            assert hub.call('work.status', session_id='named-one')['result']['directory'] == str(root / 'a')
            page = hub.call('history')['result']
            assert any(row['session_id'] == 'named-one' and row['directory'] == str(root / 'a') for row in page['sessions']), page
        finally:
            hub.close()

        configure(base='changed', any_directory=False)
        hub = Hub(binary, root, config, 'fixed-restart', out)
        try:
            refused(hub.call('open', adapter='placed', request=envelope('named-one', reopen=True)))
            assert not hub.call('sessions')['result']['sessions']
        finally:
            hub.close()
        hub = Hub(binary, root, config, 'malformed', out)
        request = {'id': 1, 'op': 'open', 'adapter': 'placed', 'directory': 7,
                   'request': envelope('wrong-type')}
        try:
            hub.process.stdin.write((json.dumps(request) + '\n').encode())
            hub.process.stdin.flush()
        finally:
            hub.close(expect_failure=True)
        assert not (out / 'malformed-wire.jsonl').read_text()
        native = [json.loads(line) for line in log.read_text().splitlines()]
        starts = [row for row in native if row['request'].get('method') == 'thread/start']
        assert len(starts) == 5, starts
        assert {row['cwd'] for row in starts} == {str(root / name) for name in ['a', 'b', 'c', 'base']}, starts
        for row in starts:
            assert row['request']['params']['cwd'] == row['cwd'], row
        resumed = [row for row in native if row['request'].get('method') == 'thread/resume']
        assert len(resumed) == 2 and all(row['cwd'] == str(root / 'a') for row in resumed), resumed
        assert all(row['request']['params']['threadId'] == 'native-a' for row in resumed), resumed
        (out / 'history.jsonl').write_text((root / 'history.jsonl').read_text())
    print('PASS: host-named open placed real synthetic children in selected directories, refused invalid placement, and reopened from history after restart')


if __name__ == '__main__':
    main()
