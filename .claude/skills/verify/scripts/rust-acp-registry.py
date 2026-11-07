import argparse
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
for line in sys.stdin:
 request=json.loads(line)
 with (out/'native.jsonl').open('a') as log:
  log.write(json.dumps(dict(cwd=os.getcwd(),synthetic=os.getenv('SYNTHETIC'),request=request))+'\\n')
 method=request.get('method')
 if method=='initialize': result=dict(protocolVersion=1,agentCapabilities={})
 elif method=='session/new': result=dict(sessionId='synthetic-native')
 else: raise AssertionError(request)
 print(json.dumps(dict(jsonrpc='2.0',id=request['id'],result=result)),flush=True)
'''


class Client:
    def __init__(self, binary, root, config, output):
        self.serial = 0
        self.output = output
        self.wire = (output / 'wire.jsonl').open('w')
        self.stderr = (output / 'stderr.txt').open('w')
        self.process = subprocess.Popen(
            [binary, 'serve', 'agent', '--backend=fixture', '--config', str(config),
             '--session-history', str(root / 'history.jsonl')],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.stderr,
            cwd=root, env={'HOME': str(root / 'home'), 'PATH': os.environ['PATH']},
            start_new_session=True, bufsize=0)

    def call(self, kind, **payload):
        self.serial += 1
        request = dict(protocol='open-agent-protocol', version='0.1',
                       profile='open-agent-protocol.agent-control-core',
                       type=kind, id=str(self.serial), session_id='host-session', payload=payload)
        self.wire.write(json.dumps(dict(direction='host', envelope=request)) + '\n')
        self.process.stdin.write((json.dumps(request) + '\n').encode())
        self.process.stdin.flush()
        assert select.select([self.process.stdout], [], [], 10)[0], 'CLI answer timed out'
        line = self.process.stdout.readline()
        assert line, f'CLI ended before {kind}; see {self.output / "stderr.txt"}'
        answer = json.loads(line)
        self.wire.write(json.dumps(dict(direction='agent', envelope=answer)) + '\n')
        self.wire.flush()
        assert answer['in_reply_to'] == request['id'], answer
        assert answer['type'] != 'error.response', answer
        return answer

    def close(self):
        try:
            self.process.stdin.close()
            self.process.stdin = None
            stdout, _ = self.process.communicate(timeout=10)
            assert self.process.returncode == 0, stdout
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
            environment=['SYNTHETIC=only'], working_directory=str(workspace))))))
        client = Client(str(args.binary.resolve()), root, config, output)
        try:
            opened = client.call('session.open.request', session_id='host-session')
            assert opened['payload']['session_id'] == 'host-session', opened
            client.call('session.close.request', session_id='host-session')
        finally:
            client.close()
        native = [json.loads(line) for line in (output / 'native.jsonl').read_text().splitlines()]
        assert [line['request']['method'] for line in native] == ['initialize', 'session/new'], native
        assert all(line['cwd'] == str(workspace) for line in native), native
        (output / 'history.jsonl').write_bytes((root / 'history.jsonl').read_bytes())
    print('PASS: configured ACP CLI opens and closes a host-named session in its fixed directory')


if __name__ == '__main__':
    main()
