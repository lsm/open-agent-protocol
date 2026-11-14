# Python SDK runtime binary resolver journey

After changing the Python SDK binary resolver or its environment precedence,
run from the repository root:

```sh
python3 .claude/skills/verify/scripts/python-sdk-binary-resolver.py --output-dir /tmp/oap-python-resolver-<unique-name>
```

The script runs a standalone consumer of the public `oxap_sdk.binary` entry
point against a fake runtime through every step: local `zig-out` builds, `PATH`,
and a real checksum-verified download from a loopback HTTP server. `oxap` must
win over `oapx` in each step, on `PATH`, and in the `OXAP_SDK_BINARY_*`
variables, while `OAP_SDK_BINARY_PATH`, `OAP_SDK_BINARY_URL` and
`OAP_SDK_BINARY_SHA256` keep resolving when the new name is absent. A URL's
checksum is never borrowed across the two pairs, and a URL passed in the
options takes either environment checksum. Inspect the per-case stdout/stderr
files and the requested URL log; a skipped case is not a pass. Pass
`--python` to select an interpreter; no provider, credentials or network beyond
loopback are used.
