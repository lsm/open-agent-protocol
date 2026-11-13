# TypeScript SDK runtime binary resolver journey

After changing the TypeScript SDK binary resolver, its environment precedence or
its bundled-package lookup, run:

```sh
npm ci --ignore-scripts --include=dev
python3 .claude/skills/verify/scripts/ts-sdk-binary-resolver.py --output-dir /tmp/oap-ts-resolver-<unique-name>
```

The script builds the SDK and a standalone consumer of the public entry point,
then resolves a fake runtime through every step: the bundled platform package,
local `zig-out` builds, `PATH`, and a real checksum-verified download from a
loopback HTTP server. `oxap` must win over `oapx` in each step and in the
`OXAP_SDK_BINARY_*` variables, while `OAP_SDK_BINARY_PATH`,
`OAP_SDK_BINARY_URL` and `OAP_SDK_BINARY_SHA256` keep resolving when the new
name is absent. Inspect the per-case stdout/stderr files and the requested URL
log; a skipped case is not a pass. No provider, credentials or network beyond
loopback are used.
