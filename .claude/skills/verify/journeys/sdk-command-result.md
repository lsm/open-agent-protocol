## SDK command result parity journey

The script uses `python3.11` if available for the Python SDK; otherwise it uses
its own interpreter. Pass `--python-sdk /path/to/python3.11-or-newer` to override.

After changing tool-event SDK projection, install the root and client dependencies
with `npm ci --ignore-scripts` and `npm --prefix clients/ts ci --ignore-scripts`,
rebuild `oapx`, then run:

```sh
python3 .claude/skills/verify/scripts/sdk-command-result.py --output-dir /tmp/oap-sdk-command-<unique-name>
```

It includes the native Codex command journey above and feeds that same fixture
through the public Go, Rust, Python and TypeScript SDK streams against both real CLIs.
All eight consumer runs must retain the original result JSON and distinguish 0,
nonzero and null exit evidence. Only numeric zero may project a `codex.command`
as non-error; unknown must remain null in the result and project as error.
Explicit native failures still take priority over zero. A non-Codex `Bash`
completed-action fixture without `exitCode` must retain its existing non-error
outcome in all four public SDK streams. Top-level `result: null` must remain null.
The real `clients/ts` parser must preserve both traces unchanged. All three validators must accept
both wire traces with exit 0. The baseline and backend parity CI lane run this
journey; logs retain every SDK outcome and validator result. This proves the
fixture boundary, not a live Codex process or provider call.
