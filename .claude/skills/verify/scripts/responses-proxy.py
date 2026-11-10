#!/usr/bin/env python3
import argparse
import importlib.util
import json
from pathlib import Path
import ssl
import subprocess
import tempfile
import threading


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


transport = load('connect_fixture', 'http-connect.py')
service_fixture = load('service_fixture', 'provider-service-token.py')


class StreamHandler(transport.OriginHandler):
    def do_POST(self):
        origin = self.server
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert self.path == '/responses' and body['model'] == 'fixture-model' and body['stream'] is True
        origin.requests.append(dict(self.headers))
        if origin.retry and len(origin.requests) == 1:
            self.send_response(503)
            self.send_header('Retry-After', '0')
            self.send_header('Content-Length', '0')
            self.send_header('Connection', 'close')
            self.end_headers()
            return
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Connection', 'close')
        self.end_headers()
        self.event({'type': 'response.output_text.delta', 'delta': 'sensitive-response-fixture', 'output_index': 0})
        if not origin.stream_gate or origin.delta_seen.wait(10):
            origin.streamed += 1
            self.event({'type': 'response.completed', 'response': {'status': 'completed', 'usage': {'input_tokens': 3, 'output_tokens': 1}}})
        self.close_connection = True

    def event(self, value):
        self.wfile.write(('data: ' + json.dumps(value) + '\n\n').encode())
        self.wfile.flush()


def model_case(binary, home, pair, output, proxy_name, audit_id, retry=False, count=1, denied=False, invalid=False, stream_gate=True, bypass=False, extra_env=None):
    origin = transport.Origin(*pair)
    origin.RequestHandlerClass = StreamHandler
    origin.retry, origin.streamed = retry, 0
    origin.stream_gate = stream_gate
    origin.delta_seen = threading.Event()
    with transport.running(origin), transport.running(transport.Proxy(origin, denied)) as proxy:
        env = {'HOME': str(home), 'PATH': '/usr/bin:/bin', 'NODE_ENV': 'test',
               'TEST_URL': f'https://localhost:{origin.server_port}', 'TEST_CA_FILE': str(pair[0]),
               'TEST_STREAMS': str(count)}
        if proxy_name:
            scheme = 'socks5' if invalid else 'http'
            env[proxy_name] = f'{scheme}://synthetic:proxy@127.0.0.1:{proxy.server_port}'
        env.update(extra_env or {})
        if bypass:
            env['no_proxy'] = 'localhost'
        if audit_id is not None:
            env['OAP_ATTEMPT_AUDIT_ID'] = audit_id
        child = subprocess.Popen([str(binary)], env=env, cwd=home, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        lines = []

        def read():
            for line in child.stdout:
                lines.append(line.decode().strip())
                if line.strip() == b'DELTA':
                    origin.delta_seen.set()

        reader = threading.Thread(target=read, daemon=True)
        reader.start()
        try:
            child.wait(timeout=30)
            reader.join(5)
            stderr = child.stderr.read()
            output.with_suffix('.log').write_bytes(stderr)
            output.with_suffix('.stdout').write_text('\n'.join(lines) + '\n')
            records = [json.loads(line) for line in stderr.splitlines()]
            observations = {'inferences': lines.count('INFERENCE'), 'provider_attempts': len(origin.requests), 'records': records, 'connects': len(proxy.connects)}
            output.with_suffix('.observed.json').write_text(json.dumps(observations, indent=2))
            failed = denied or (invalid and not bypass)
            expected_requests = 0 if failed else count + int(retry)
            assert child.returncode == (1 if failed else 0), 'unexpected inference outcome'
            assert len(origin.requests) == expected_requests, 'wrong provider HTTP attempt count'
            assert lines.count('INFERENCE') == count, 'wrong inference count'
            if bypass:
                assert proxy.connects == [] and proxy.other_requests == 0, 'excluded destination contacted proxy'
            elif proxy_name:
                assert len(proxy.connects) == (0 if invalid else (4 if denied else expected_requests)), 'proxy was bypassed or CONNECT count was wrong'
                assert all(value == transport.AUTH for value in proxy.connects), 'CONNECT authentication missing'
            if failed:
                assert origin.connections == 0, 'proxy failure fell back directly'
            else:
                assert origin.streamed == count and lines.count('DELTA') >= count, 'SSE was buffered instead of streamed'
            for headers in origin.requests:
                assert all(name.lower() != 'proxy-authorization' for name in headers), 'proxy credential reached the origin'
            enabled = audit_id is not None and 0 < len(audit_id) <= 64 and all(c.isascii() and (c.isalnum() or c in '._-') for c in audit_id)
            assert len(records) == (expected_requests if enabled else 0), 'wrong audit record count'
            for record in records:
                assert set(record) == {'audit_id', 'attempt', 'outcome'}, 'audit included extra data'
                assert record['audit_id'] == audit_id, 'audit ID changed'
                assert record['outcome'] in ('success', 'http_error', 'transport_error', 'stream_error'), 'unbounded outcome'
            assert sorted(record['attempt'] for record in records) == list(range(1, len(records) + 1)), 'attempt numbers are not unique and process-wide'
            if retry and enabled:
                assert [record['outcome'] for record in records] == ['http_error', 'success'], 'retry outcomes were not recorded'
            for forbidden in (b'sensitive-prompt-fixture', b'sensitive-response-fixture', transport.AUTH.encode()):
                assert forbidden not in stderr, 'content or credentials reached audit output'
            return {'inferences': lines.count('INFERENCE'), 'provider_attempts': len(origin.requests), 'records': records, 'connects': len(proxy.connects)}
        finally:
            origin.delta_seen.set()
            if child.poll() is None:
                child.kill()
            child.wait(timeout=5)
            for pipe in (child.stdout, child.stderr):
                pipe.close()
            reader.join(5)


class AmbientProxy(transport.Proxy):
    def __init__(self, origin, deny=False):
        super().__init__(origin, deny)
        self.RequestHandlerClass = AmbientHandler
        self.forwarded = []


class AmbientHandler(transport.ProxyHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers['Content-Length']))
        self.server.forwarded.append((dict(self.headers), body.decode()))
        self.send_error(403)


def remote_case(binary, home, output, security, pair, denied=False):
    original = service_fixture.environment
    origin = service_fixture.Service()
    if security == 'tls':
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(*pair)
        origin.socket = context.wrap_socket(origin.socket, server_side=True)
    with transport.running(origin), transport.running(AmbientProxy(origin, denied)) as proxy:
        def environment(*args, **kwargs):
            env = original(*args, **kwargs)
            env['HTTP_PROXY'] = f'http://synthetic:proxy@127.0.0.1:{proxy.server_port}'
            env['HTTPS_PROXY'] = env['HTTP_PROXY']
            env['OAP_ATTEMPT_AUDIT_ID'] = 'remote-fixture'
            env['OAPX_PROVIDER_SERVICE_SECURITY'] = security
            if security == 'tls':
                env['OAPX_PROVIDER_SERVICE_URL'] = f'https://localhost:{origin.server_port}'
            return env
        service_fixture.environment = environment
        try:
            if security == 'tls':
                result = service_fixture.startup(str(binary), home, origin.url, service_fixture.TOKEN)
                assert result.returncode != 0 and not origin.requests, 'untrusted TLS origin or denied proxy accepted'
                assert proxy.connects == [transport.AUTH], 'TLS service did not authenticate CONNECT'
                assert proxy.tls == ([] if denied else [True]), 'TLS service omitted target TLS'
                assert proxy.forwarded == [], 'TLS service sent plaintext to proxy'
                assert b'"audit_id"' not in result.stderr, 'remote transport counted as a model call'
                service_fixture.no_token(result.stderr)
            else:
                service_fixture.dialogue(str(binary), home, origin, service_fixture.TOKEN, output)
                assert len(origin.requests) >= 4, 'remote request surfaces did not run'
                assert proxy.connects == [] and proxy.forwarded == [], 'local provider reached ambient proxy'
                assert b'"audit_id"' not in output.with_suffix('.log').read_bytes(), 'remote transport counted as a model call'
            return {'remote_requests': len(origin.requests), 'connects': len(proxy.connects), 'forwarded': len(proxy.forwarded)}
        finally:
            service_fixture.environment = original


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--oapx', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=False)
    results = []
    with tempfile.TemporaryDirectory(prefix='oap-responses-proxy-') as temporary:
        home = Path(temporary)
        pair = transport.certificate(home, 'origin', 'localhost')
        cases = [('direct-opt-out', None, None, {}),
                 ('direct-retry-audit', None, 'fixture-run', {'retry': True, 'stream_gate': False}),
                 ('proxy-lower', 'https_proxy', None, {}),
                 ('proxy-upper', 'HTTPS_PROXY', None, {}),
                 ('proxy-all', 'ALL_PROXY', None, {}),
                 ('proxy-denied', 'https_proxy', 'fixture-run', {'denied': True}),
                 ('retry-audit', 'https_proxy', 'fixture-run', {'retry': True}),
                 ('retry-opt-out', 'https_proxy', None, {'retry': True}),
                 ('concurrent-audit', 'https_proxy', 'fixture-run', {'count': 2}),
                 ('audit-id-bound', 'https_proxy', 'a' * 64, {}),
                 ('audit-id-long', 'https_proxy', 'a' * 65, {}),
                 ('audit-id-framing', 'https_proxy', 'bad"\nlabel', {}),
                 ('invalid-proxy', 'https_proxy', 'fixture-run', {'invalid': True}),
                 ('bypass-invalid-proxy', 'https_proxy', 'fixture-run', {'invalid': True, 'bypass': True}),
                 ('unused-invalid-http-proxy', 'https_proxy', 'fixture-run', {'extra_env': {'HTTP_PROXY': 'socks5://127.0.0.1:1'}}),
                 ('unused-malformed-http-proxy', 'https_proxy', 'fixture-run', {'extra_env': {'HTTP_PROXY': 'http://['}})]
        for name, proxy_name, audit_id, options in cases:
            try:
                details = model_case(args.binary.resolve(), home, pair, args.output_dir / name, proxy_name, audit_id, **options)
                result = {'case': name, 'passed': True, **details}
            except Exception as error:
                result = {'case': name, 'passed': False, 'error': type(error).__name__ + ' ' + str(error)}
            results.append(result)
            print('PASS' if result['passed'] else 'FAIL', name, result.get('error', ''), flush=True)
        for security, denied in [('loopback', False), ('loopback', True), ('mesh_proxy', False), ('mesh_proxy', True), ('tls', False), ('tls', True)]:
            name = 'remote-' + security + ('-denied' if denied else '-surfaces')
            try:
                details = remote_case(args.oapx.resolve(), home, args.output_dir / name, security, pair, denied)
                result = {'case': name, 'passed': True, **details}
            except Exception as error:
                result = {'case': name, 'passed': False, 'error': type(error).__name__ + ' ' + str(error)}
            results.append(result)
            print('PASS' if result['passed'] else 'FAIL', name, result.get('error', ''), flush=True)
    passed = sum(result['passed'] for result in results)
    (args.output_dir / 'checks.json').write_text(json.dumps(results, indent=2))
    print(f'{passed}/{len(results)} checks passed', flush=True)
    return 0 if passed == len(results) else 1


if __name__ == '__main__':
    raise SystemExit(main())
