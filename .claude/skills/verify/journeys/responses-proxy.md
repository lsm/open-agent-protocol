# Responses proxy routing and attempt audit

Run on the designated test host after the authenticated CONNECT transport lands.

```sh
zig build --build-file zig/build.zig install build-responses-proxy-probe -Doptimize=ReleaseSafe
python3 .claude/skills/verify/scripts/responses-proxy.py --binary zig/zig-out/bin/responses-proxy-probe --oapx zig/zig-out/bin/oapx --output-dir /tmp/oap-responses-proxy-evidence
```

Require 22/22 checks and exit 0. The native probe calls the real Codex Responses
streaming implementation using an explicitly anonymous synthetic model and a
fixture trust bundle. A TLS origin releases its terminal SSE event only after
the probe observes a delta. Authenticated CONNECT, uppercase and lowercase proxy
variables, ALL_PROXY, denial and invalid proxy configuration are observed.

One inference receiving 503 then success must produce two model POSTs and two
JSONL audit records. Opt-out produces zero records. Concurrent inferences must
use unique process-wide attempt numbers. Records contain only audit_id, attempt
and a fixed outcome, with bounded IDs and no content or credentials.

The served agent exercises discovery, models, inference SSE and cancellation
with a synthetic bearer in both loopback and mesh-proxy modes. Ambient proxies
must receive zero requests. TLS-mode service requests authenticate CONNECT,
reject an untrusted target certificate and fail closed on proxy denial.
Remote-provider calls produce no Responses attempt records. A destination
exclusion and a valid HTTPS proxy still work when an unused proxy setting is
unusable. All children use isolated test environments without provider keys.
