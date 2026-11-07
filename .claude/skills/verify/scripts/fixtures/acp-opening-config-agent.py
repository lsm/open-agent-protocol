import copy
import json
import os
from pathlib import Path
import sys
import time

out = Path(sys.argv[1])
data = json.loads((out / 'native.json').read_text())
options = data['options']
(out / 'pid').write_text(str(os.getpid()))
setting = 0


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
        result = {'protocolVersion': 1, 'agentCapabilities': {'loadSession': True}}
    elif method in ['session/new', 'session/load']:
        result = {'sessionId': 'native', 'configOptions': options}
    elif method == 'session/set_config_option':
        setting += 1
        (out / 'set-seen').write_text(str(setting))
        for index in range(data.get('notifications', 320)):
            send({'method': 'session/update', 'params': {'sessionId': 'native', 'update': {
                'sessionUpdate': 'session_info_update', 'title': str(index)}}})
        send({'id': 'opening-query-' + str(setting), 'method': 'fixture/unsupported', 'params': {}})
        answer = read()
        assert answer == {'jsonrpc': '2.0', 'id': 'opening-query-' + str(setting), 'error': {
            'code': -32601, 'message': 'method not supported during session reload'}}, answer
        (out / 'reverse-answered').touch()
        if data.get('quiet'):
            while True:
                time.sleep(1)
        if 'error' in data:
            send({'id': frame['id'], 'error': data['error']})
            continue
        for option in options:
            if option.get('id') == frame['params']['configId']:
                option['currentValue'] = frame['params']['value']
        result = {'configOptions': options}
        replies = data.get('replies', [])
        if setting <= len(replies) and replies[setting - 1] is not None:
            result = copy.deepcopy(replies[setting - 1])
            options = result.get('configOptions', []) if isinstance(result, dict) else []
    else:
        raise AssertionError(frame)
    send({'id': frame['id'], 'result': result})
