import json, sys
from pathlib import Path

root, cwd = Path(sys.argv[1]), sys.argv[2]
frames = [json.loads(line) for line in (root / 'native.jsonl').read_text().splitlines()]
for frame in frames:
    if frame['direction'] == 'client_request':
        line = sys.stdin.readline()
        actual = json.loads(line) if line else None
        params = (actual or {}).get('params') or {}
        recorded = frame['raw'].get('params') or {}
        if 'cwd' in recorded and params.get('cwd') == cwd:
            params['cwd'] = recorded['cwd']
        if actual != frame['raw']:
            (root / 'mismatch').write_text(line or 'EOF')
            sys.exit(1)
    else:
        print(json.dumps(frame['raw']), flush=True)
(root / 'replayed').write_text('')
for line in sys.stdin:
    (root / 'mismatch').write_text(line)
    sys.exit(1)
