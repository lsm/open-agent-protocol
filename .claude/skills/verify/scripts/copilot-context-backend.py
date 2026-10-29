#!/usr/bin/env python3
"""Exercise Copilot context limits through a loopback model server and the TUI."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location('driver', ROOT / 'scripts/tui-pty-driver.py')
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default=str(ROOT / 'zig/zig-out/bin/oapx'))
    parser.add_argument('--output-dir', required=True)
    parser.add_argument('--expect-alpha28-refusal', action='store_true')
    options = parser.parse_args()
    output = Path(options.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    metadata = {'data': []}
    for model, prompt, maximum in [('claude-sonnet-4.6', 200000, 1000000),
                                    ('gpt-5.4', 272000, 1050000),
                                    ('claude-haiku-4.5', 168000, 200000)]:
        metadata['data'].append({'id': model, 'model_picker_enabled': True,
            'supported_endpoints': ['/chat/completions'], 'capabilities': {
                'type': 'chat', 'limits': {'max_prompt_tokens': prompt,
                    'max_context_window_tokens': maximum, 'max_output_tokens': 128000}}})
    (output / 'mock-models.json').write_text(json.dumps(metadata, indent=2))
    requests = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            requests.append({'method': 'GET', 'path': self.path})
            body = json.dumps(metadata).encode()
            self.send_response(200 if self.path == '/models' else 404)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    session = None
    try:
        with tempfile.TemporaryDirectory(prefix='oap-copilot-backend-') as temporary:
            root = Path(temporary)
            home = root / 'home'
            data = home / '.oapx'
            cache = data / 'model_catalog'
            cache.mkdir(parents=True)
            binary = root / 'oapx'
            shutil.copy2(options.binary, binary)
            url = f'http://127.0.0.1:{server.server_port}'
            (data / 'auth.json').write_text(json.dumps({'github-copilot': {
                'refresh': 'synthetic-refresh', 'access': 'synthetic-access',
                'expires': 4102444800000, 'provider_data': json.dumps({'baseUrl': url})}}))
            config = data / 'config.json'
            config.write_text(json.dumps({'model': 'claude-sonnet-4.6', 'provider': 'github-copilot',
                'api': 'openai-completions', 'workspace': str(root)}))
            (cache / 'github-copilot.json').write_text(json.dumps(metadata))
            profile = root / 'isolation.sb'
            profile.write_text('(version 1) (allow default)\n(deny network*)\n'
                '(allow network-outbound (remote ip "localhost:*"))\n'
                '(deny mach-lookup (global-name-regex #".*[Ss]ecurity.*"))\n'
                f'(deny file-read* (subpath {json.dumps(str(Path.home()))}))\n')
            os.environ.clear()
            os.environ.update({'PATH': '/usr/bin:/bin', 'HOME': str(home), 'TMPDIR': str(root),
                'TERM': 'xterm-256color'})
            args = argparse.Namespace(binary='/usr/bin/sandbox-exec', width=150, height=40, fixture_text='')
            session = driver.PtySession(args, home=str(home), use_fixture=False, cwd=str(root),
                argv=['-f', str(profile), str(binary)])
            session.wait_for(driver.WELCOME_MARKER, 15, 'welcome', since=0)
            results = []

            def command(text, marker, persisted=None, check=True):
                since = len(session.plain)
                session.type_text(text)
                session.send(b'\r', text)
                session.wait_for(marker.encode(), 15, text, since=since)
                session.quiesce(0.2)
                actual = json.loads(config.read_text()).get('mode', {}).get('context_window')
                if check:
                    assert actual == persisted, (text, actual, persisted)
                results.append({'command': text, 'expected': marker, 'persisted': actual})
                (output / f'screen-{len(results):02d}.txt').write_bytes(session.screen_text())

            command('/model refresh', 'model catalog refreshed', check=False)
            assert any(r['path'] == '/models' for r in requests), requests
            for model, prompt, maximum in [('claude-sonnet-4.6', 200000, 1000000),
                                           ('gpt-5.4', 272000, 1050000),
                                           ('claude-haiku-4.5', 168000, 200000)]:
                command('/model ' + model, 'model switched to ' + model, check=False)
                command('/context default', f'context window: {prompt}')
                if options.expect_alpha28_refusal:
                    command(f'/context {maximum}', f'takes at most {prompt} context tokens')
                else:
                    command(f'/context {maximum}', f'context window: {maximum}', maximum)
                    command(f'/context {maximum + 1}', f'takes at most {maximum} context tokens', maximum)
                    command('/context default', f'context window: {prompt}')
            session.type_text('/quit')
            session.send(b'\r', 'quit')
            assert session.wait_exit(10) == 0
            (output / 'results.json').write_text(json.dumps(results, indent=2))
            print('PASS: loopback GET /models, real TUI context boundaries and persistence; exit 0; synthetic backend')
    finally:
        if session is not None:
            (output / 'transcript.bin').write_bytes(b''.join(c for _, c in session.chunks))
            (output / 'screen.txt').write_bytes(session.screen_text())
            session.close()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        (output / 'requests.json').write_text(json.dumps(requests, indent=2))


if __name__ == '__main__':
    main()
