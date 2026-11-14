# Rust conformance client correlation and wait budgets

Use shared toolchain setup from `SKILL.md`, then run

```sh
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-client-<unique-name>
cargo test --manifest-path rust/Cargo.toml -p oxap-conformance --test client -- --nocapture
cargo test --manifest-path rust/Cargo.toml -p oxap-conformance --lib client::tests -- --nocapture
cargo test --manifest-path rust/Cargo.toml -p oxap-conformance
```

The standalone public consumer must match a capabilities response from the
rebuilt Rust endpoint, retain its request/answer trace and observe exit 0 after
EOF. A synthetic peer emits events continuously while the consumer awaits a
missing response. Inspect the elapsed time and close reason in `consumer.txt`.
That wait must expire after its single 500 ms budget and within one second.

A second peer emits unrelated controls. Its control wait must spend one 500 ms
budget, leave the endpoint open, then receive the response to a subsequent
request. Require exit 0 and PASS. Consumer source and build/output logs are
retained. Temporary HOME/data/config and the process-group cleanup follow the
existing process journey.

The public client tests also exercise buffered replies, event waits receiving
unrelated replies, probe exclusion and a chatty endpoint ignoring EOF. EOF
draining has one total budget. Run the complete baseline too.

The synthetic client fixtures publish a READY file after interpreter startup,
imports and any injected startup delay. The host waits for that state before
returning the client to a timed test. This startup-only wait has a separate
10-second safety guard; it changes no Client deadline and adds no protocol frames.
The shared verification-lock wait occurs before the test and is outside both
phases. Rust test threads still run concurrently within that lock.

Require both chatty-EOF cases to report more than two real frames and a drain
between one and two seconds, with a typed one-second timeout. The slow case
injects two seconds before READY, and readiness_ms must include that delay while
drain_ms still uses the original one-second budget. This checks actual Python
process I/O, not a mock or a retry. Keep signed failing slow-start reproduction
`25994601f7` before the readiness fix. Run Rust all-features under the same mutex.

The EOF-expiry regressions first reap a real synthetic subprocess and confirm
its reader has delivered EOF. One deliberately pauses the caller inside the
wait loop past the unchanged two-second budget: the already-delivered EOF must
still succeed without recording a runner timeout. The other starts at an
expired response boundary and must return `EndpointGone`. Both failed before
the fix in signed regression commit `cbb6e10adf`; the native fixtures and reader
workers are closed before their result assertions. The original intermittent
failure log did not retain scheduler telemetry, so this deterministic boundary
case proves the false-timeout path rather than reconstructing that attempt's
precise scheduling interleaving. The existing public consumer must still match
real endpoint replies and exit 0 after EOF, while silent and chatty peers keep
their original timeout and cleanup behavior.

Also require the expired-budget drain to deliver a queued reply before a later
malformed line, leaving the wire error for the next consumer call. A real native
peer queues those outputs in order; regression commit `7efa863f8d` fails before
the return-order fix. This prevents bulk draining from discarding a reply that
the caller can already receive.
