#!/usr/bin/env python3
import argparse
import contextlib
import http.server
import json
from pathlib import Path
import queue
import subprocess
import tempfile
import threading
import time

TOKEN = 'synthetic.A_b-9+/=~'
MODEL = 'synthetic/openai-responses@model'
PROFILE = 'open-agent-protocol.agent-control-core'


def envelope(kind, identifier, payload, profile=PROFILE, **fields):
    return dict(protocol='open-agent-protocol', version='0.1', profile=profile,
                type=kind, id=identifier, payload=payload, **fields)


class Service(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, mode='normal', target=None):
        super().__init__(('127.0.0.1', 0), Handler)
        self.mode, self.target = mode, target
        self.requests = []
        self.cancelled = threading.Event()
        self.created = threading.Event()

    @property
    def url(self):
        return f'http://127.0.0.1:{self.server_port}'


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.server.requests.append(({'type': 'redirected-get'}, self.headers.get('Authorization')))
        self.send_error(400)

    def do_POST(self):
        body = self.rfile.read(int(self.headers['Content-Length']))
        request = json.loads(body)
        service = self.server
        service.requests.append((request, self.headers.get('Authorization')))
        if service.mode == 'redirect':
            self.send_response(303)
            self.send_header('Location', service.target + '/oap/v0.1/provider')
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        if service.mode == 'error':
            raw = TOKEN.encode()
            self.send_response(401)
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        kind = request['type']
        if kind == 'provider.describe.request':
            payload = {'providers': [{'id': 'synthetic', 'wire': 'openai-responses',
                        'framing': 'sse', 'endpoint': 'https://model.example',
                        'credential_grant': 'none', 'allows_anonymous': True}]}
        elif kind == 'provider.models.list.request':
            payload = {'models': [{'model_ref': MODEL, 'model_id': 'model',
                        'provider_id': 'synthetic', 'wire': 'openai-responses',
                        'auth_status': 'authenticated'}]}
        elif kind == 'inference.cancel.request':
            payload = {'accepted': True}
            service.cancelled.set()
        elif kind == 'inference.create.request':
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            self.event(request, 'inference.create.response', {'accepted': True})
            self.event(request, 'inference.started', {'model_ref': MODEL, 'started_at_ms': 1}, 1)
            service.created.set()
            service.cancelled.wait(10)
            self.event(request, 'inference.failed', {'error': {'code': 'cancelled',
                       'message': 'synthetic cancellation'}}, 2)
            return
        else:
            raise AssertionError('unexpected service request')
        response = envelope(kind.replace('.request', '.response'), 'answer-' + request['id'],
                            payload, 'open-agent-protocol.model-provider-core',
                            in_reply_to=request['id'], capability_revision='synthetic-r1')
        if 'inference_id' in request:
            response['inference_id'] = request['inference_id']
        raw = json.dumps(response).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def event(self, request, kind, payload, sequence=None):
        fields = {'inference_id': 'i-1'}
        if sequence is None:
            fields['in_reply_to'] = request['id']
        else:
            fields['sequence'] = sequence
        frame = envelope(kind, kind, payload, 'open-agent-protocol.model-provider-core', **fields)
        try:
            self.wfile.write(('data: ' + json.dumps(frame) + '\n\n').encode())
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass


@contextlib.contextmanager
def running(service):
    worker = threading.Thread(target=service.serve_forever, daemon=True)
    worker.start()
    try:
        yield service
    finally:
        service.cancelled.set()
        service.shutdown()
        service.server_close()
        worker.join()


def environment(home, url=None, token=None):
    env = {'HOME': str(home), 'PATH': '/usr/bin:/bin', 'NODE_ENV': 'test',
           'OAPX_KEYCHAIN_SERVICE': 'oap-synthetic-service-token'}
    if url is not None:
        env.update(OAPX_PROVIDER_SERVICE_URL=url, OAPX_PROVIDER_SERVICE_SECURITY='loopback')
    if token is not None:
        env['OAPX_PROVIDER_SERVICE_TOKEN'] = token
    return env


def startup(binary, home, url, token):
    return subprocess.run([binary, 'serve', 'agent', '--stdio'], input=b'',
                          env=environment(home, url, token), cwd=home,
                          capture_output=True, timeout=15)


def no_token(raw):
    assert TOKEN.encode() not in raw, 'transport credential escaped into output'


def dialogue(binary, home, service, token, output):
    child = subprocess.Popen([binary, 'serve', 'agent', '--stdio', '--model', MODEL],
                             env=environment(home, service.url, token), cwd=home,
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    received = queue.Queue()
    trace = []

    def reader():
        for line in child.stdout:
            received.put(line)
        received.put(None)

    worker = threading.Thread(target=reader, daemon=True)
    worker.start()
    revision = None

    def send(kind, identifier, payload, **fields):
        request = envelope(kind, identifier, payload, **fields)
        if revision:
            request['capability_revision'] = revision
        trace.append(request)
        child.stdin.write((json.dumps(request) + '\n').encode())
        child.stdin.flush()

    def until(predicate):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            line = received.get(timeout=max(.01, deadline - time.monotonic()))
            assert line is not None, 'agent exited before the expected response'
            no_token(line)
            item = json.loads(line)
            trace.append(item)
            if predicate(item):
                return item
        raise AssertionError('missing agent response')

    try:
        send('protocol.initialize.request', 'init', {'protocol_versions': ['0.1'],
             'profiles': [PROFILE], 'participant': {'id': 'synthetic-user'}})
        until(lambda item: item.get('in_reply_to') == 'init')
        send('capabilities.request', 'caps', {})
        revision = until(lambda item: item.get('in_reply_to') == 'caps')['capability_revision']
        send('session.open.request', 'open', {'session_id': 's1'}, session_id='s1')
        assert until(lambda item: item.get('in_reply_to') == 'open')['type'] == 'session.open.response'
        send('session.message.submit.request', 'submit', {'session_id': 's1', 'delivery': 'auto',
             'messages': [{'role': 'user', 'content': 'hello'}]}, session_id='s1')
        response = until(lambda item: item.get('in_reply_to') == 'submit')
        assert response['type'] == 'session.message.submit.response', 'submission refused'
        assert service.created.wait(10), 'inference did not reach the service'
        run = response['payload']['run_id']
        send('run.cancel.request', 'cancel', {'session_id': 's1', 'run_id': run},
             session_id='s1', run_id=run)
        until(lambda item: item.get('in_reply_to') == 'cancel')
        assert service.cancelled.wait(10), 'cancel did not reach the service'
        child.stdin.close()
        child.wait(timeout=15)
        worker.join(timeout=5)
        log = child.stderr.read()
        no_token(log)
        assert child.returncode == 0, 'agent failed during shutdown'
        output.with_suffix('.log').write_bytes(log)
        output.with_suffix('.trace.json').write_text(json.dumps(trace, indent=2))
        requests = [item for item, _ in service.requests]
        no_token(json.dumps(requests).encode())
        output.with_suffix('.requests.json').write_text(json.dumps(requests, indent=2))
        required = {'provider.describe.request', 'provider.models.list.request',
                    'inference.create.request', 'inference.cancel.request'}
        assert required <= {item['type'] for item in requests}, 'missing request surface'
        expected = None if token is None else 'Bearer ' + token
        assert all(header == expected for _, header in service.requests), 'wrong transport authorization'
    finally:
        service.cancelled.set()
        if child.poll() is None:
            child.kill()
        child.wait(timeout=5)
        for pipe in (child.stdin, child.stdout, child.stderr):
            if pipe and not pipe.closed:
                pipe.close()
        worker.join(timeout=5)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=False)
    binary = str(args.binary.resolve())
    results = []

    def check(name, action):
        try:
            action()
            results.append((name, True))
            print('PASS', name, flush=True)
        except Exception as error:
            results.append((name, False))
            message = str(error).replace(TOKEN, '[redacted]')
            print('FAIL', name, type(error).__name__, message, flush=True)

    with tempfile.TemporaryDirectory(prefix='oap-service-token-') as temporary:
        home = Path(temporary)
        for index, token in enumerate(['', 'two words', 'tab\there', 'line\r\nbreak',
                                       'control\x01', 'delete\x7f', 'space\u00a0', 'control\u0085']):
            def invalid(token=token):
                result = startup(binary, home, 'http://127.0.0.1:1', token)
                assert result.returncode != 0 and b'InvalidProviderServiceToken' in result.stderr, 'missing typed token refusal'
                if token:
                    assert token.encode() not in result.stdout + result.stderr, 'invalid token echoed'
            check(f'invalid-token-{index}', invalid)

        def missing_url():
            result = startup(binary, home, None, TOKEN)
            no_token(result.stdout + result.stderr)
            assert result.returncode != 0 and b'ProviderServiceUrlRequired' in result.stderr, 'missing URL refusal'
        check('token-without-url', missing_url)
        for name, token in [('unset', None), ('set', TOKEN)]:
            def headers(name=name, token=token):
                with running(Service()) as service:
                    dialogue(binary, home, service, token, args.output_dir / name)
            check('headers-and-redaction-' + name, headers)

        def refusal():
            with running(Service('error')) as service:
                result = startup(binary, home, service.url, TOKEN)
                no_token(result.stdout + result.stderr)
                assert result.returncode != 0 and b'ProviderServiceHttpFailure' in result.stderr, 'missing typed HTTP refusal'
        check('HTTP-error-redaction', refusal)

        def redirect():
            with running(Service()) as destination, running(Service('redirect', destination.url)) as origin:
                result = startup(binary, home, origin.url, TOKEN)
                no_token(result.stdout + result.stderr)
                assert result.returncode != 0, 'redirect was accepted'
                assert not destination.requests, 'credential-bearing redirect reached another origin'
                assert origin.requests and origin.requests[0][1] == 'Bearer ' + TOKEN, 'origin missing authorization'
        check('cross-origin-redirect', redirect)
    passed = sum(ok for _, ok in results)
    (args.output_dir / 'result.json').write_text(json.dumps({'passed': passed, 'total': len(results), 'cases': results}, indent=2))
    print(f'{passed}/{len(results)} checks passed', flush=True)
    return 0 if passed == len(results) else 1


if __name__ == '__main__':
    raise SystemExit(main())
