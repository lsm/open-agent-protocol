import json
import os
from pathlib import Path
import sys
import time

root = Path(sys.argv[1])
scenario = json.loads((root / 'native.json').read_text())
(root / 'pid').write_text(str(os.getpid()))
with (root / 'pids').open('a') as output:
    output.write(str(os.getpid()) + '\n')


def record(direction, frame):
    with (root / 'wire.jsonl').open('a') as output:
        output.write(json.dumps({'from': direction, 'frame': frame}) + '\n')


def send(frame):
    record('agent', frame)
    print(json.dumps(frame), flush=True)


for line in sys.stdin:
    request = json.loads(line)
    record('host', request)
    method = request.get('method')
    if method == 'initialize':
        result = dict(protocolVersion=1, agentCapabilities=scenario.get('caps', dict(loadSession=True)))
    elif method in ['session/new', 'session/load', 'session/resume']:
        (root / 'opening').write_text(method)
        if scenario.get('quiet'):
            while True:
                time.sleep(1)
        if method != 'session/new':
            for _ in range(320):
                send(dict(jsonrpc='2.0', method='session/update', params=dict(sessionId='native', update=dict(sessionUpdate='session_info_update', title='ignored'))))
            send(dict(jsonrpc='2.0', id='reload-query', method='unknown-reload-query', params={}))
            answer = json.loads(sys.stdin.readline())
            record('host', answer)
            assert answer['id'] == 'reload-query' and answer['error']['code'] == -32601, answer
        result = dict(sessionId='native', configOptions=[])
    else:
        continue
    send(dict(jsonrpc='2.0', id=request['id'], result=result))
