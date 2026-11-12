# Rust ACP public text submission

Follow shared setup and verification mutex instructions in SKILL.md, and run
from the repository root.

```sh
python3 .claude/skills/verify/scripts/rust-acp-submit.py --output-dir /tmp/oap-submit-<unique>
(cd rust && cargo test -p oap-adapter-acp --test submit)
```

The standalone `submit_owner` consumer uses normal `Adapter.open` and
`Session.submit` with submit/streaming degradation opt-ins, which the descriptor no longer requires but still accepts.
Credential-free synthetic native processes run over stdio at queue capacity
one. The script retains actual admission responses, session states, envelopes,
native requests, native PIDs and OS thread inventories.

A controlled PATH first selects the verifier's Python interpreter for the Rust
integration fixture. The native agent records its actual interpreter, which
must match that selection while its environment stays empty. Keep this
portability regression committed before the fixture lookup fix.

Eight cases cover complete Unicode text, bounded subscriber overflow, dropping
an active session, opt-in abort close, cancelling close, cancelling blocked
write admission, opt-in closing during blocked admission and immediate default
close refusal while the writer is blocked. Opt-in close also awaits the
interrupted submission and requires its typed session-closed result. Quiet agents ignore EOF.
The consumer stays alive after cleanup so the verifier can require native PID
absence and exact return to the initial OS thread count. Explicit shutdown of
an EOF-ignoring child reports its timeout after killing, reaping and joining.
Abandonment closes the retained prefix. The complete text trace uses actual
Rust admission and events, with a synthetic host request, and must pass Go,
Rust and Zig validators.

Focused tests additionally cover two successive turns, generated identities,
contiguous sequences, unsupported controls, required degradation opt-ins,
default active-close refusal and one-active-run admission. The original normal
submit failure must remain committed before the implementation.

Run the existing ACP foundation, opening, model, bridge and standalone text
journeys, full shared baseline and Rust all-features suite under the same mutex.
Inspect the retained PTY fixture reply and `/quit` result. Reported usage and
native message identities are covered by `rust-acp-results.md`. Tool lifecycle is covered by `rust-acp-tools.md`. Normal permission integration is covered by `rust-acp-session-permissions.md`.
Native cancellation is covered by rust-acp-cancel.md. Live config projection is covered by `rust-acp-live-config.md`. Shared process-helper
grandchild-held-pipe limitations remain applicable.

The integration fixture gives cooperative Python agents one second to finish
interpreter shutdown after EOF. Its controlled delayed-exit case waits 200 ms and
must close successfully, then prove the child reaped. The EOF-ignoring quiet,
blocked and blocked-abort cases retain their 100 ms termination budget. Keep the
signed failing delayed-exit regression before the fixture-budget fix. This
fixture budget covers cooperative process finalization during parallel tests.
