# Rust DeepSeek adapter configuration

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-deepseek --test adapter)
(cd rust && cargo test -p oap-adapter-deepseek --test session)
(cd rust && cargo test -p oap-adapter-deepseek --test admission)
(cd rust && cargo test -p oap-adapter-deepseek --test submit)
(cd rust && cargo test -p oap-adapter-deepseek --all-features)
```

`tests/go_capabilities.json` is Go's `Probe` capabilities for the catalog
revision, marshalled by Go's `encoding/json`. Regenerate it whenever Go's
DeepSeek descriptor changes by printing `json.MarshalIndent(descriptor.Capabilities, "", "  ")`
from a throwaway test in `go/adapter/deepseek`, and require the Rust test to
compare equal to the new file. The other tests pin Go's construction
refusals, the initialize params and reasoning a launch carries, and each
compaction patch, including the token-count refusal.

`tests/session.rs` opens sessions against a shell runtime that answers
`initialize` and `shutdown` and records each request. It pins Go's open
refusals, the reasoning and compaction patch a session starts with, the
patch file's removal after start, the idle state, minted ids, the refused
resolve, cancel and unknown-run resume, and a single shutdown on close. To see
the patch check bite, keep the temporary patch file after start: the open
test must fail. The runtime can also print a notification, a reverse request
or exit after the handshake, and each must leave `state` reporting the session
closed. The in-crate tests in `src/session.rs` feed the reader directly: to see
them bite, drop the exemption for this session's own `session.status`.

`tests/admission.rs` feeds native events to the admission check in the order
Go's reducer buffers them, checking after each one, and once more with a
receipt that arrives after its turn ended. To see it bite, stop a `turn/end`
from marking its turn closed: the three tests whose turn ends without the
proof must fail.

`tests/submit.rs` drives a shell runtime that answers `session/prompt` with a
scripted reply, notifications included. Each submit runs under a five-second
bound, so a submit that never returns fails the test instead of hanging it. To
see it bite, let a notification retire the session while a prompt is pending:
the pending-notification test must fail. Let a native `seq` fall back without
failing the run: the backwards-sequence test must fail. Make `Drop` call
`close` instead of shutting down: the drop test must fail, because the runtime
outlives a session dropped with its run still running. Keep the run after a
terminal `run.failed`: the three after-admission tests must fail; settle a
runtime exit without `inferred`: the exit test must fail. Settle without
waiting for the runtime's idle status: the idle-wait test must fail; let an
assistant event through when its step is not the open one: the grammar test
must fail. Count a buffered idle status from before the turn ended: the
buffered-idle test must fail. Skip failing open tools before a run fails: the
open-tool and lifecycle tests must fail. Settle while a child still runs: the
two still-running subagent tests must fail.
