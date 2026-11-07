import json
import os
from pathlib import Path
import select
import sys

out = Path(sys.argv[1])
(out / 'pid').write_text(str(os.getpid()))
options = json.loads((out / 'opened.json').read_text())
pending = None
buffer = b''


def send(frame):
    frame = dict(jsonrpc='2.0', **frame)
    with (out / 'wire.jsonl').open('a') as wire:
        wire.write(json.dumps({'from': 'agent', 'frame': frame}) + '\n')
    print(json.dumps(frame), flush=True)


def marker(token):
    send({'id': 'observed-' + token, 'method': 'fixture/observed', 'params': {}})


while True:
    command = out / 'command.json'
    if command.exists():
        data = json.loads(command.read_text())
        command.unlink()
        token = str(data['token'])
        if data.get('finish'):
            send({'id': pending, 'result': {'stopReason': 'end_turn'}})
            pending = None
        else:
            update = data['update']
            if isinstance(update.get('configOptions'), list):
                options = update['configOptions']
            send({'method': 'session/update', 'params': {
                'sessionId': data.get('sessionId', 'native'), 'update': update}})
        marker(token)
    if b'\n' not in buffer:
        if not select.select([sys.stdin], [], [], .005)[0]:
            continue
        chunk = os.read(sys.stdin.fileno(), 65536)
        if not chunk:
            break
        buffer += chunk
        continue
    line, buffer = buffer.split(b'\n', 1)
    frame = json.loads(line)
    with (out / 'wire.jsonl').open('a') as wire:
        wire.write(json.dumps({'from': 'host', 'frame': frame}) + '\n')
    if str(frame.get('id', '')).startswith('observed-'):
        assert frame['error']['code'] == -32601, frame
        (out / str(frame['id'])).touch()
        continue
    method = frame.get('method')
    if method == 'initialize':
        result = {'protocolVersion': 1, 'agentCapabilities': {}}
    elif method == 'session/new':
        result = {'sessionId': 'native', 'configOptions': options}
    elif method == 'session/prompt':
        pending = frame['id']
        send({'method': 'session/update', 'params': {'sessionId': 'native', 'update': {
            'sessionUpdate': 'agent_message_chunk', 'content': {'type': 'text', 'text': 'before'}}}})
        marker('prompt')
        continue
    elif method == 'session/set_config_option':
        for option in options:
            if option['id'] == frame['params']['configId']:
                option['currentValue'] = frame['params']['value']
        result = {'configOptions': options}
    else:
        raise AssertionError(frame)
    send({'id': frame['id'], 'result': result})
