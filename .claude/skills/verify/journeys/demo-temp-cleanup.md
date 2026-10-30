# Demo temporary-directory ownership

Run from the repository root after the shared dependency setup in SKILL.md.
This observes the actual CLI under a fresh temp root and inspects what remains
after it exits. It removes only its own root, including on failure.

```sh
npm run build:sdk
python3 - <<'PY'
import os, subprocess, tempfile
from pathlib import Path
with tempfile.TemporaryDirectory(prefix='oap-demo-observe-') as root:
    env = dict(os.environ, TMPDIR=root, TMP=root, TEMP=root, NODE_DISABLE_COMPILE_CACHE='1')
    env.pop('NODE_TEST_CONTEXT', None)
    env.pop('OAP_SDK_BINARY_PATH', None)

    result = subprocess.run(['node', '--test', 'dist/test/demo_server.test.js'], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
    print(result.stdout)
    remaining = sorted(p.name for p in Path(root).iterdir())
    print(f'CLI exit={result.returncode}; remaining directories={remaining}')
    if result.returncode != 0 or remaining:
        raise SystemExit('FAIL: CLI exit or directory ownership mismatch')
PY
node --test scripts/demo-server-cleanup.test.mjs
```

Expect the CLI exit shown by the observer and an empty directory listing.
The separate demo cleanup proof also requires the injected startup-failure
diagnostic. Inspect the printed child output;
any unexpected exit, timeout, or remaining directory fails verification.
No credentials or network provider calls are needed.
