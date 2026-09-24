#!/usr/bin/env python3
# Local fake model provider for scripts/tui-pty-driver.py.
#
# Serves static streamed replies for the three wire formats the TUI's real
# provider path speaks, so the PTY harness can exercise the bridge, provider
# registry, HTTP client and SSE parsing without OAPX_TUI_FIXTURE and without
# network access or credentials:
#
#   POST .../messages          anthropic-messages SSE
#   POST .../chat/completions  openai-completions SSE ending in [DONE]
#   POST .../responses         openai-responses SSE
#   GET  .../models            a one-model list
#
# Every streamed reply is DELTAS text deltas "fp0 ", "fp1 ", ... so the full
# reply is known in advance. With --require-key, requests lacking
# "Bearer <key>" or "x-api-key: <key>" get 401. With --cert/--key the server
# speaks TLS. The chosen port is printed on stdout as the first line; each
# request is appended to --log as one JSON line (path and whether auth matched,
# never the header value).
#
#   python3 scripts/tui-fake-provider.py serve --deltas 5 [--require-key K] [--cert C --key K]
#   python3 scripts/tui-fake-provider.py gen-certs DIR
#
# gen-certs writes ca.pem (a throwaway CA), leaf.pem and leaf.key (a
# certificate for localhost / 127.0.0.1 / ::1 signed by it) using openssl.

import argparse
import http.server
import json
import os
import socket
import socketserver
import ssl
import subprocess
import sys


def reply_words(count):
    return [f"fp{i} " for i in range(count)]


def sse(event, data):
    head = f"event: {event}\n" if event else ""
    body = data if isinstance(data, str) else json.dumps(data)
    return (head + "data: " + body + "\n\n").encode()


def anthropic_stream(model, words):
    out = [sse("message_start", {"type": "message_start", "message": {"id": "msg_fake", "type": "message", "role": "assistant", "model": model, "content": [], "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 0}}})]
    out.append(sse("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}))
    out += [sse("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": w}}) for w in words]
    out.append(sse("content_block_stop", {"type": "content_block_stop", "index": 0}))
    out.append(sse("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": len(words)}}))
    out.append(sse("message_stop", {"type": "message_stop"}))
    return out


def completions_stream(model, words):
    def chunk(delta, finish):
        return sse("", {"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1, "model": model, "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]})
    out = [chunk({"role": "assistant", "content": ""}, None)]
    out += [chunk({"content": w}, None) for w in words]
    out += [chunk({}, "stop"), sse("", "[DONE]")]
    return out


def responses_stream(model, words):
    seq = iter(range(1 << 20))
    text = "".join(words)
    item = {"id": "msg_fake", "type": "message", "role": "assistant", "status": "in_progress", "content": []}
    done_item = dict(item, status="completed", content=[{"type": "output_text", "text": text, "annotations": []}])
    base = {"output_index": 0, "item_id": "msg_fake", "content_index": 0}
    out = [sse("response.created", {"type": "response.created", "sequence_number": next(seq), "response": {"id": "resp_fake", "object": "response", "status": "in_progress", "model": model, "output": []}})]
    out.append(sse("response.output_item.added", {"type": "response.output_item.added", "sequence_number": next(seq), "output_index": 0, "item": item}))
    out.append(sse("response.content_part.added", dict(base, type="response.content_part.added", sequence_number=next(seq), part={"type": "output_text", "text": "", "annotations": []})))
    out += [sse("response.output_text.delta", dict(base, type="response.output_text.delta", sequence_number=next(seq), delta=w)) for w in words]
    out.append(sse("response.output_text.done", dict(base, type="response.output_text.done", sequence_number=next(seq), text=text)))
    out.append(sse("response.output_item.done", {"type": "response.output_item.done", "sequence_number": next(seq), "output_index": 0, "item": done_item}))
    out.append(sse("response.completed", {"type": "response.completed", "sequence_number": next(seq), "response": {"id": "resp_fake", "object": "response", "status": "completed", "model": model, "output": [done_item], "usage": {"input_tokens": 1, "output_tokens": len(words), "total_tokens": len(words) + 1}}}))
    return out


STREAMS = (
    ("/messages", anthropic_stream),
    ("/chat/completions", completions_stream),
    ("/responses", responses_stream),
)


def make_handler(opts):
    words = reply_words(opts.deltas)

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass

        def record(self, method, path, authorized):
            if opts.log:
                with open(opts.log, "a") as handle:
                    handle.write(json.dumps({"method": method, "path": path, "authorized": authorized}) + "\n")

        def authorized(self):
            if opts.require_key is None:
                return True
            bearer = self.headers.get("authorization", "")
            return bearer == f"Bearer {opts.require_key}" or self.headers.get("x-api-key", "") == opts.require_key

        def send_body(self, status, content_type, body):
            self.send_response(status)
            self.send_header("content-type", content_type)
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            path = self.path.split("?")[0]
            ok = self.authorized()
            self.record("GET", path, ok)
            if not ok:
                self.send_body(401, "application/json", b'{"error":{"message":"missing key"}}')
                return
            if not path.endswith("/models"):
                self.send_body(404, "application/json", b"{}")
                return
            body = json.dumps({"object": "list", "data": [{"id": opts.model, "object": "model"}]}).encode()
            self.send_body(200, "application/json", body)

        def do_POST(self):
            path = self.path.split("?")[0]
            raw = self.rfile.read(int(self.headers.get("content-length", "0") or "0"))
            ok = self.authorized()
            self.record("POST", path, ok)
            if not ok:
                self.send_body(401, "application/json", b'{"error":{"message":"missing key"}}')
                return
            try:
                request = json.loads(raw or b"{}")
            except ValueError:
                request = {}
            model = request.get("model", opts.model)
            for suffix, stream in STREAMS:
                if path.endswith(suffix):
                    events = stream(model, words)
                    break
            else:
                self.send_body(404, "application/json", b"{}")
                return
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("cache-control", "no-cache")
            self.send_header("connection", "close")
            self.end_headers()
            for event in events:
                self.wfile.write(event)
                self.wfile.flush()
            self.close_connection = True

    return Handler


class DualStackServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    address_family = socket.AF_INET6

    def server_bind(self):
        self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        super().server_bind()


class Ipv4Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True


def serve(opts):
    handler = make_handler(opts)
    try:
        server = DualStackServer(("::", opts.port), handler)
    except OSError:
        server = Ipv4Server(("127.0.0.1", opts.port), handler)
    if opts.cert:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(opts.cert, opts.key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    print(server.server_address[1], flush=True)
    server.serve_forever()


def gen_certs(directory):
    os.makedirs(directory, exist_ok=True)
    path = lambda name: os.path.join(directory, name)
    ext = path("leaf.ext")
    with open(ext, "w") as handle:
        handle.write("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1\n")
    commands = [
        ["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", path("ca.key")],
        ["openssl", "req", "-x509", "-new", "-key", path("ca.key"), "-sha256", "-days", "2", "-subj", "/CN=oap tui pty test CA", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-out", path("ca.pem")],
        ["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", path("leaf.key")],
        ["openssl", "req", "-new", "-key", path("leaf.key"), "-subj", "/CN=localhost", "-out", path("leaf.csr")],
        ["openssl", "x509", "-req", "-in", path("leaf.csr"), "-CA", path("ca.pem"), "-CAkey", path("ca.key"), "-CAcreateserial", "-sha256", "-days", "2", "-extfile", ext, "-out", path("leaf.pem")],
    ]
    for command in commands:
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def main():
    parser = argparse.ArgumentParser(description="Fake streaming model provider for the TUI PTY harness.")
    sub = parser.add_subparsers(dest="command", required=True)
    serve_parser = sub.add_parser("serve")
    serve_parser.add_argument("--port", type=int, default=0)
    serve_parser.add_argument("--deltas", type=int, default=5)
    serve_parser.add_argument("--model", default="pty-fake-model")
    serve_parser.add_argument("--require-key")
    serve_parser.add_argument("--cert")
    serve_parser.add_argument("--key")
    serve_parser.add_argument("--log")
    certs_parser = sub.add_parser("gen-certs")
    certs_parser.add_argument("directory")
    opts = parser.parse_args()
    if opts.command == "gen-certs":
        gen_certs(opts.directory)
        return 0
    if bool(opts.cert) != bool(opts.key):
        parser.error("--cert and --key go together")
    serve(opts)
    return 0


if __name__ == "__main__":
    sys.exit(main())
