# Go SDK runtime binary resolver journey

After changing the Go SDK binary resolver or its environment precedence, run:

```sh
python3 .claude/skills/verify/scripts/go-sdk-binary-resolver.py --output-dir /tmp/oap-go-resolver-<unique-name>
```

The script builds a standalone consumer of the public `go/sdk` package and
resolves a fake runtime through every step: local `zig-out` builds, `PATH`, and
a real checksum-verified download from a loopback HTTP server. `oxap` must win
over `oapx` in the local build, in `PATH`, and in the `OXAP_SDK_BINARY_*`
variables, while `OAP_SDK_BINARY_PATH`, `OAP_SDK_BINARY_URL` and
`OAP_SDK_BINARY_SHA256` keep resolving when the new name is absent. Inspect the
per-case stdout/stderr files and the requested URL log; a skipped case is not a
pass. No provider, credentials or network beyond loopback are used.
