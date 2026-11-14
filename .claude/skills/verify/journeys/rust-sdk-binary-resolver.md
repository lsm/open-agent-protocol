# Rust SDK runtime binary resolver journey

After changing the Rust SDK binary resolver or its environment precedence, run
from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-sdk-binary-resolver.py --output-dir /tmp/oap-rust-resolver-<unique-name>
```

The script builds a standalone consumer crate with a path dependency on
`rust/sdk` (with the `download` feature) and resolves a fake runtime through
every step: local `zig-out` builds, `PATH`, and a real checksum-verified
download from a loopback HTTP server. `oxap` must win over `oapx` in each step,
on `PATH`, and in the `OXAP_SDK_BINARY_*` variables, while
`OAP_SDK_BINARY_PATH`, `OAP_SDK_BINARY_URL` and `OAP_SDK_BINARY_SHA256` keep
resolving when the new name is absent. A URL's checksum is never borrowed
across the two pairs, and a URL passed in the options takes either environment
checksum. Inspect the per-case stdout/stderr files and the requested URL log; a
skipped case is not a pass. The first build compiles the workspace, so allow a
few minutes; no provider, credentials or network beyond loopback are used.
