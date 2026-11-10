#!/usr/bin/env python3
import argparse
import base64
import contextlib
import http.server
import json
from pathlib import Path
import select
import socket
import ssl
import subprocess
import tempfile
import threading


AUTH = 'Basic ' + base64.b64encode(b'synthetic:proxy').decode()


class Origin(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, certificate, key, plaintext=False, dual=False):
        super().__init__(('127.0.0.1', 0), OriginHandler)
        self.dual = dual
        self.wrong_connects = 0
        self.context = None
        if not plaintext:
            self.context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            self.context.load_cert_chain(certificate, key)
        self.connections = 0
        self.requests = []

    def get_request(self):
        connection, address = super().get_request()
        self.connections += 1
        connection.settimeout(10)
        if self.context and not (self.dual and connection.recv(1, socket.MSG_PEEK) != b'\x16'):
            try:
                connection = self.context.wrap_socket(connection, server_side=True)
            except Exception:
                connection.close()
                raise
        return connection, address

    def handle_error(self, *_):
        pass


class OriginHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.server.requests.append(dict(self.headers))
        self.send_response(200)
        self.send_header('Content-Length', '14')
        self.end_headers()
        self.wfile.write(b'through tunnel')


    def do_CONNECT(self):
        self.server.wrong_connects += 1
        self.send_error(405)


class Proxy(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, origin, deny=False):
        super().__init__(('127.0.0.1', 0), ProxyHandler)
        self.origin = origin
        self.deny = deny
        self.connects = []
        self.other_requests = 0
        self.tls = []


class ProxyHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_CONNECT(self):
        proxy = self.server
        authorization = self.headers.get('Proxy-Authorization')
        proxy.connects.append(authorization)
        if authorization != AUTH or proxy.deny:
            self.send_error(407 if authorization != AUTH else 403)
            return
        if self.path != f'localhost:{proxy.origin.server_port}':
            self.send_error(403)
            return
        with socket.create_connection(('127.0.0.1', proxy.origin.server_port), timeout=10) as upstream:
            self.send_response(200, 'Connection established')
            self.end_headers()
            self.wfile.flush()
            streams = [self.connection, upstream]
            first = True
            prefix = b''
            while True:
                ready, _, _ = select.select(streams, [], [], 10)
                if not ready:
                    return
                for source in ready:
                    try:
                        data = source.recv(65536)
                        if not data:
                            return
                        if source is self.connection and first:
                            prefix += data
                            if len(prefix) >= 3:
                                proxy.tls.append(prefix.startswith(b'\x16\x03'))
                                first = False
                        destination = upstream if source is self.connection else self.connection
                        destination.sendall(data)
                    except OSError:
                        return

    def do_GET(self):
        self.server.other_requests += 1
        self.send_error(403)


@contextlib.contextmanager
def running(server):
    worker = threading.Thread(target=server.serve_forever, daemon=True)
    worker.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        worker.join()


def certificate(home, name, hostname):
    cert, key = home / (name + '.pem'), home / (name + '.key')
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(key), '-out', str(cert), '-days', '2',
                    '-subj', '/CN=' + hostname, '-addext', 'subjectAltName=DNS:' + hostname],
                   env={'PATH': '/usr/bin:/bin', 'HOME': str(home), 'NODE_ENV': 'test'},
                   check=True, capture_output=True, timeout=30)
    return cert, key


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    binary = args.binary.resolve()
    args.output_dir.mkdir(parents=True, exist_ok=False)
    results = []
    with tempfile.TemporaryDirectory(prefix='oap-connect-') as temporary:
        home = Path(temporary)
        trusted = certificate(home, 'trusted', 'localhost')
        untrusted = certificate(home, 'untrusted', 'localhost')
        mismatch = certificate(home, 'mismatch', 'wrong.invalid')
        cases = [('direct', trusted, True, False, False, False),
                 ('authenticated', trusted, True, True, False, False),
                 ('pooled-origin', trusted, True, True, False, False),
                 ('bad-auth', trusted, False, True, False, False),
                 ('denied', trusted, False, True, True, False),
                 ('untrusted', untrusted, False, True, False, False),
                 ('hostname', mismatch, False, True, False, False),
                 ('plaintext', trusted, False, True, False, True)]
        for name, pair, success, proxied, deny, plaintext in cases:
            try:
                with running(Origin(*pair, plaintext=plaintext, dual=name == 'pooled-origin')) as origin, running(Proxy(origin, deny)) as proxy:
                    env = {'HOME': str(home), 'PATH': '/usr/bin:/bin', 'NODE_ENV': 'test',
                           'TEST_URL': f'https://localhost:{origin.server_port}/responses',
                           'TEST_CA_FILE': str(pair[0] if name == 'hostname' else trusted[0])}
                    if name == 'pooled-origin':
                        env['TEST_WARM_URL'] = f'http://localhost:{origin.server_port}/warm'
                    if proxied:
                        password = 'incorrect' if name == 'bad-auth' else 'proxy'
                        env['https_proxy'] = f'http://synthetic:{password}@127.0.0.1:{proxy.server_port}'
                    child = subprocess.run([str(binary)], env=env, cwd=home, capture_output=True, timeout=20)
                    (args.output_dir / (name + '.log')).write_bytes(child.stdout + child.stderr)
                    assert child.returncode == (0 if success else 1), 'unexpected client outcome or crash'
                    if proxied:
                        assert len(proxy.connects) == 1, 'CONNECT count or direct fallback'
                        expected = 'Basic ' + base64.b64encode(f'synthetic:{password}'.encode()).decode()
                        assert proxy.connects == [expected], 'CONNECT omitted or changed proxy authentication'
                        assert proxy.other_requests == 0, 'proxy denial fell back to a forwarded request'
                    expected_origins = 0 if name in ('bad-auth', 'denied') else (2 if name == 'pooled-origin' else 1)
                    assert origin.connections == expected_origins, 'unexpected origin connection or direct fallback'
                    assert origin.wrong_connects == 0, 'CONNECT reached the origin instead of the proxy'
                    assert len(origin.requests) == (2 if name == 'pooled-origin' else (1 if success else 0)), 'origin accepted an invalid tunnel'
                    for headers in origin.requests:
                        assert all(key.lower() != 'proxy-authorization' for key in headers), 'proxy credential reached origin'
                        assert AUTH.split(' ', 1)[1] not in json.dumps(headers), 'encoded proxy credential reached origin'
                    if proxied and name not in ('bad-auth', 'denied'):
                        assert proxy.tls == [True], 'tunnel did not start TLS'
                    result = {'case': name, 'passed': True, 'client_exit': child.returncode,
                              'connects': len(proxy.connects), 'origin_connections': origin.connections,
                              'origin_requests': len(origin.requests), 'tls': proxy.tls}
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
