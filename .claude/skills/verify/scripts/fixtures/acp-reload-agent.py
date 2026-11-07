import json
import os
from pathlib import Path
import sys
import time

out = Path(sys.argv[1])
data = json.loads((out / 'native.json').read_text())
(out / 'pid').write_text(str(os.getpid()))


def send(frame):
    frame = dict(jsonrpc='2.0', **frame)
    with (out / 'wire.jsonl').open('a') as wire:
        wire.write(json.dumps({'from': 'agent', 'frame': frame}) + '\n')
    print(json.dumps(frame), flush=True)


def read():
    line = sys.stdin.readline()
    if not line:
        return None
    frame = json.loads(line)
    with (out / 'wire.jsonl').open('a') as wire:
        wire.write(json.dumps({'from': 'host', 'frame': frame}) + '\n')
    return frame


while (frame := read()) is not None:
    method = frame.get('method')
    if method == 'initialize':
        result = {'protocolVersion': 1, 'agentCapabilities': data['capabilities']}
    elif method in ['session/load', 'session/resume']:
        (out / 'load-seen').write_text(method)
        for index in range(data.get('history', 320)):
            send({'method': 'session/update', 'params': {'sessionId': 'bound-native', 'update': {
                'sessionUpdate': 'agent_message_chunk', 'content': {'type': 'text', 'text': 'old history'}}}})
        if data.get('reverse', True):
            send({'id': 'reload-query', 'method': 'fixture/unsupported', 'params': {}})
            answer = read()
            assert answer == {'jsonrpc': '2.0', 'id': 'reload-query', 'error': {
                'code': -32601, 'message': 'method not supported during session reload'}}, answer
            (out / 'reverse-answered').touch()
        if data.get('quiet'):
            while True:
                time.sleep(1)
        if 'error' in data:
            send({'id': frame['id'], 'error': data['error']})
            continue
        result = data['result']
    elif method == 'session/new':
        raise AssertionError('reload unexpectedly created a new conversation')
    elif method == 'session/prompt':
        send({'method': 'session/update', 'params': {'sessionId': 'bound-native', 'update': {
            'sessionUpdate': 'agent_message_chunk', 'content': {'type': 'text', 'text': 'continued'}}}})
        result = {'stopReason': 'end_turn', 'usage': {'inputTokens': 8, 'outputTokens': 2, 'totalTokens': 10}}
    else:
        raise AssertionError(frame)
    send({'id': frame['id'], 'result': result})
