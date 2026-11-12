import json, sys, time
from pathlib import Path

root = Path(sys.argv[1])
name, version, mode = sys.argv[2], sys.argv[3], sys.argv[4]
frames = [json.loads(line) for line in (root / 'native.jsonl').read_text().splitlines()]
host = {'oap-control', 'overlap-submit', 'wait-submit'}
cursor = 0


def read():
    line = sys.stdin.readline()
    if not line:
        return None
    with (root / 'host.jsonl').open('a') as wire:
        wire.write(line)
    return json.loads(line)


def send(raw, reply_to=None):
    raw = dict(raw)
    if reply_to is not None:
        raw['id'] = reply_to
    print(json.dumps(raw), flush=True)


def pause(index):
    (root / f'at-{index}').write_text('')
    deadline = time.monotonic() + 10
    while not (root / f'go-{index}').exists():
        assert time.monotonic() < deadline, index
        time.sleep(0.001)


def play(prompt):
    global cursor
    while cursor < len(frames):
        index, frame = cursor, frames[cursor]
        action, raw = frame['action'], frame['raw']
        if action == 'submit':
            return
        cursor += 1
        if action in ('observe', 'observe-invalid'):
            send(raw)
        elif action in ('reply', 'reply-error'):
            send(raw, prompt)
        elif action in host:
            pause(index)
        elif action == 'process-exit':
            sys.exit(0)
        else:
            (root / 'unsupported').write_text(action)
            sys.exit(1)


def automatic():
    raws = [frame['raw'] for frame in frames if frame['action'] == 'auto']
    notes = [raw for raw in raws if 'method' in raw and 'id' not in raw]
    receipts = [raw['result']['messageId'] for raw in raws if 'result' in raw]
    return notes, receipts[0] if receipts else ''


def framing(index):
    literal = frames[index]['raw']
    if frames[index]['action'] == 'decode-error-unterminated':
        sys.stdout.write(literal)
        sys.stdout.flush()
        sys.exit(0)
    sys.stdout.write(literal + '\n')
    sys.stdout.flush()


notes, receipt = automatic()
served = False
while True:
    request = read()
    if request is None:
        break
    method = request.get('method')
    if method == 'initialize' and mode == 'pre-observe':
        send(notes[0])
        time.sleep(60)
    elif method == 'initialize':
        info = {'serverInfo': {'name': name, 'version': version}}
        send({'jsonrpc': '2.0', 'id': request['id'], 'result': info})
        if mode.startswith('framing:'):
            framing(int(mode.split(':')[1]))
        elif mode == '':
            play(None)
    elif method == 'session/prompt' and mode == 'lifecycle':
        if not served and receipt:
            served = True
            for note in notes:
                send(note)
            send({'jsonrpc': '2.0', 'id': request['id'], 'result': {'messageId': receipt}})
        else:
            error = {'code': -32603, 'message': 'fixture prompt rejection'}
            send({'jsonrpc': '2.0', 'id': request['id'], 'error': error})
    elif method == 'session/prompt':
        assert frames[cursor]['action'] == 'submit', (cursor, request)
        cursor += 1
        play(request['id'])
    elif method == 'shutdown':
        send({'jsonrpc': '2.0', 'id': request['id'], 'result': {}})
        break
