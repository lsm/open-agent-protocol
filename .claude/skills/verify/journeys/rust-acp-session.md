# Rust ACP session/new, idle state and model catalog

After the baseline builds the ReleaseSafe binary, run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-session.py --output-dir /tmp/oap-acp-session-<unique>
python3 .claude/skills/verify/scripts/rust-acp-foundation.py --output-dir /tmp/oap-acp-foundation-<unique>
(cd rust && cargo test -p oap-adapter-acp)
```

The standalone public consumer opens a credential-free synthetic ACP agent,
observes the initialize/session-new wire with its working directory and configured
MCP servers, then reads OAP idle state and an opted-in model catalog. Native
model options flatten in order, including groups. UI placeholders stay absent
from the current model. The response carries no inferred provider, pricing or
context window. The catalog in this slice is the opening response's snapshot;
live native configuration updates belong to the later config/event slice.

The consumer closes and repeats close, observes state refused after closing,
and checks every native PID is gone. Missing/invalid session ids and a null
response fail and reap the process. A child that ignores EOF produces the bounded
shutdown error, remains closed for later calls and is reaped. The focused Rust
tests also observe degraded opt-in refusal, mismatched session scope and two
concurrent close futures. Opening and closing use the owned cancellation bridge.

The verifier constructs a complete OAP initialize/capabilities/open/models
exchange from the actual returned descriptor, state and catalog. All three
validators must accept it. Native wire, public consumer JSON and validator
outputs remain in the requested evidence directory. This is session-opening
proof. Prompt streaming, gates, reopen and full corpus parity
remain later slices. The staged descriptor revision is `-rust-switch` until the
completed adapter can use the catalog's full revision.

Keep the complete baseline and Rust all-features/format/clippy/comment checks.
Inspect the baseline's isolated PTY fixture reply and quit result. Use synthetic
agents only, with an empty child environment and no live harness or providers.
