# Remote provider service bearer authentication

Build the product and run the synthetic HTTP journey on the designated test host.

```sh
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe
python3 .claude/skills/verify/scripts/provider-service-token.py --binary zig/zig-out/bin/oapx --output-dir /tmp/oap-service-token-evidence
zig build --build-file zig/build.zig test-unit-core test-unit-tools test-unit-protocol test-unit-agent-bridge test-unit-makai-cli -Doptimize=ReleaseSafe --summary all
```

Require 23/23 journey checks and exit 0. The journey launches the served agent
with an isolated home and an empty credential environment, adding only the
synthetic service token for authenticated cases. A local synthetic service
observes discovery, models, SSE inference and cancellation requests. Each
request must have the configured bearer header when set and omit it when unset.

Startup cases cover a token without a service URL, an empty value, ASCII
whitespace and controls, Unicode, punctuation outside the b64token alphabet,
and misplaced or prefix-free padding. A synthetic provider asks the real Shell
tool to run `env`, and the tool result and its OAP trace must omit the token
while retaining an ordinary environment marker. Another Shell call reads the
agent process environment through `/proc` on Linux or `ps` on macOS, targeting
the served agent PID across the shell wrapper. That output and its trace must
also omit the token and retain the marker. Typed refusals
exclude configured values. HTTP error responses deliberately contain the
synthetic token to exercise diagnostic redaction. A redirect targets a second
origin whose GET and POST observations must stay empty.

The journey retains OAP traces, request bodies and diagnostic logs after
checking that they exclude the token. Authorization is checked in memory.
The unit groups also check header ownership and allocation-failure cleanup.
