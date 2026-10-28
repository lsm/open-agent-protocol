#!/usr/bin/env python3
"""Token-free Copilot-shaped stdio peer for the public registry journey."""
import json
import os
import sys


def send(value):
    body = json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode()
    sys.stdout.buffer.write(f"Content-Length: {len(body)}\r\n\r\n".encode() + body)
    sys.stdout.buffer.flush()


def read():
    headers = {}
    while True:
        line = sys.stdin.buffer.readline()
        if not line:
            return None
        if line == b"\r\n":
            break
        key, value = line.decode().strip().split(":", 1)
        headers[key.lower()] = value.strip()
    length = int(headers["content-length"])
    body = sys.stdin.buffer.read(length)
    assert len(body) == length
    return json.loads(body)


def main():
    assert {"--headless", "--stdio", "--no-auto-update"} <= set(sys.argv[1:])
    log = open(os.environ["OAP_SYNTHETIC_LOG"], "a", buffering=1)
    log.write(json.dumps({"pid": os.getpid(), "started": True}) + "\n")
    session = "native-synthetic"
    def event(kind, data):
        send({"jsonrpc": "2.0", "method": "session.event", "params": {
            "sessionId": session, "event": {"type": kind, "data": data}}})
    count = 0
    while (message := read()) is not None:
        log.write(json.dumps(message, ensure_ascii=False) + "\n")
        method = message["method"]
        params = message.get("params", {})
        result = {}
        if method == "ping":
            result = {"protocolVersion": 3}
        elif method == "status.get":
            result = {"version": os.environ["OAP_SYNTHETIC_VERSION"], "protocolVersion": 3}
        elif method == "session.create":
            assert params["requestPermission"] is False and params["requestUserInput"] is False
            assert params["streaming"] is True and params["workingDirectory"] == os.environ["HOME"]
            result = {"sessionId": session}
        elif method == "session.send":
            count += 1
            result = {"messageId": f"m{count}"}
            event("user.message", {"messageId": result["messageId"], "content": params["prompt"]})
        send({"jsonrpc": "2.0", "id": message["id"], "result": result})
        if method != "session.send" or params["prompt"] == "hold-first":
            continue
        prompt = params["prompt"]
        if prompt == "tool":
            event("tool.execution_start", {"toolCallId": "t1", "toolName": "read_file", "arguments": {"path": "synthetic.txt"}})
            event("tool.execution_partial_result", {"toolCallId": "t1", "partialOutput": "synthetic output"})
            event("tool.execution_complete", {"toolCallId": "t1", "success": True, "result": {"content": "synthetic output"}})
        event("assistant.message_delta", {"deltaContent": prompt})
        event("assistant.message", {"content": prompt})
        event("assistant.usage", {"inputTokens": 9007199254740993, "outputTokens": 0, "cost": 0})
        event("session.idle", {})


if __name__ == "__main__":
    main()
