## Codex command result consumer journey

After changing Codex command normalization, rebuild `oapx`, then run:

```sh
python3 .claude/skills/verify/scripts/codex-command-result.py --output-dir /tmp/oap-codex-command-<unique-name>
```

This launches each real CLI against a credential-free native app-server fixture
using the issue #1386 command input. `codex.command` completed results must retain
numeric `exitCode` values 0, 1 and −7; omitted/null status must remain explicit
null, never zero. The result must retain `command`, `cwd`, `output` and an
additional native result under `nativeResult`. Explicit native failed/error
items must still emit typed `action.call.failed`. All three validators must
accept both retained traces with exit 0. The script saves trace JSON, stderr
and each validator's output. This proves the normalization boundary; the native
fixture reports outcomes rather than executing a real Codex command or reading
credentials. The baseline and backend parity CI lane include this journey.
