## Served oapx session restart journey

Build ReleaseSafe with the shared setup in `SKILL.md`, then run:

```sh
python3 .claude/skills/verify/scripts/oapx-session-restart.py --output-dir /tmp/oap-session-restart-<unique-name>
```

First it checks that `--no-save-oapx-sessions` omits reopen support and writes
no transcript after a completed synthetic turn. Then it starts the real `oapx serve --stdio` with an oapx adapter and default
session saving, submits a synthetic prompt to a loopback HTTP provider, and
observes both prompt and reply through `work.read`. It exits the hub, starts
a fresh process with the same temporary HOME, reads the stored turns before
opening, reopens the session, and submits a second prompt. The provider's
captured request must contain the original prompt, original reply, and new
prompt, proving that the restored loop uses its history.

All provider replies are synthetic. The provider binds an OS-assigned free
loopback port. The child environment is cleared, HOME/data and working directory
are temporary, and on macOS the sandbox denies reads under the real HOME,
Security Mach services and outbound network except loopback. Do not remove the
sandbox if it fails. Children are stopped and temporary state removed on exit.
Require exit 0 and `PASS`; inspect `opt-out.jsonl`, `before.jsonl`, `after.jsonl`, `restored.json`
and `provider-requests.json`. No credential file may be written. Live providers
and recovery of an interrupted active run are not exercised by this journey.
