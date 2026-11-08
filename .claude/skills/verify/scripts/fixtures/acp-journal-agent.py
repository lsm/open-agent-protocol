import copy, json, os, sys, time
from pathlib import Path
root = Path(sys.argv[1])
(root / 'pid').write_text(str(os.getpid()))
with (root / 'pids').open('a') as pids:
    pids.write(str(os.getpid()) + '\n')
frames = [json.loads(line) for line in (root / 'native.jsonl').read_text().splitlines()]
options = json.loads((root / 'options.json').read_text()) if (root / 'options.json').exists() else {}
turn = 0
for line in sys.stdin:
    request = json.loads(line)
    with (root / 'host.jsonl').open('a') as wire:
        wire.write(line)
    method = request.get('method')
    if method == 'initialize':
        selected = frames[:1]
    elif method in ['session/new', 'session/load']:
        selected = frames[1:2]
    elif method == 'session/prompt':
        turn += 1
        selected = [frames[2]] * options.get('chunks', 1) + frames[-1:]
    else:
        continue
    for index, frame in enumerate(selected):
        raw = copy.deepcopy(frame['raw'])
        if method == 'initialize' and options.get('reload'):
            raw['result']['agentCapabilities']['loadSession'] = True
        if 'result' in raw or 'error' in raw:
            raw['id'] = request['id']
        with (root / 'agent.jsonl').open('a') as wire:
            wire.write(json.dumps(raw) + '\n')
        print(json.dumps(raw), flush=True)
        if method == 'session/prompt' and index == 0 and options.get('hold'):
            while not (root / ('release-' + str(turn))).exists():
                time.sleep(.001)
