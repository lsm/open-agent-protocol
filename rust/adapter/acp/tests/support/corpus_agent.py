import json, os, sys, time
from pathlib import Path

root = Path(sys.argv[1])
(root / 'pid').write_text(str(os.getpid()))
frames = [json.loads(line) for line in (root / 'native.jsonl').read_text().splitlines()]
sent = ['update', 'permission', 'complete', 'complete-await', 'cancelled', 'refusal', 'prompt-error']


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


def wait(name):
    deadline = time.monotonic() + 10
    while not (root / name).exists():
        assert time.monotonic() < deadline, name
        time.sleep(0.001)


while True:
    request = read()
    if request is None:
        break
    method = request.get('method')
    if method == 'initialize':
        send(frames[0]['raw'], request['id'])
    elif method == 'session/new':
        send(frames[1]['raw'], request['id'])
    elif method == 'session/prompt':
        for frame in frames[2:]:
            action, raw = frame.get('action', ''), frame['raw']
            if action == 'cancel':
                cancelled = read()
                assert cancelled.get('method') == 'session/cancel', cancelled
            elif action == 'process-exit':
                sys.exit(0)
            elif action in ('', 'observe'):
                pass
            elif action in sent:
                is_response = 'result' in raw or 'error' in raw
                send(raw, request['id'] if is_response else None)
                if action == 'permission':
                    read()
                if action == 'complete-await':
                    wait('drained')
            else:
                (root / 'unsupported').write_text(action)
                sys.exit(1)
        (root / 'replayed').write_text('')
