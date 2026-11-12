# Rust DeepSeek adapter configuration

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-deepseek --test adapter)
(cd rust && cargo test -p oap-adapter-deepseek --test session)
(cd rust && cargo test -p oap-adapter-deepseek --test admission)
(cd rust && cargo test -p oap-adapter-deepseek --test submit)
(cd rust && cargo test -p oap-adapter-deepseek --test submit_drop)
(cd rust && cargo test -p oap-adapter-deepseek --test submit_unsent)
(cd rust && cargo test -p oap-adapter-deepseek --test corpus)
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
two still-running subagent tests must fail. Drop the guard's `close` when a
dropped `submit` still awaits its receipt: the before-receipt drop test must
fail. Close on every drop, receipt or not: the after-receipt drop test must
fail. Drop the handshake's cancel arm: the dropped-open test in
`tests/adapter.rs` must fail, because the runtime outlives it. `tests/submit_drop.rs`
drops a submit while its admission is pending and counts the process's
threads, so it runs alone in its binary. Forward the admission to the caller
through a thread blocked on the run's channel: it must fail, because that
thread outlives the dropped submit. `tests/submit_unsent.rs` holds a first prompt
in flight, drops a second submit before it can send, then lets the first be
refused; it also counts threads, so it runs alone. Ignore a cancel that lands
before the send: it must fail, because the dropped submit sends its prompt.

`tests/corpus.rs` replays every case of the pinned corpus through the adapter
against `tests/support/corpus_runtime.py`, a runtime that plays the case's
recorded frames and pauses at each harness-side frame until the test has acted
on it. Traces are compared after Go's normalization. Two differences are the
harness's, not the adapter's: Go records an empty trace as `null`, and Go's
in-process transport reports the case's own exit reason where a real process
reports `EOF`. To see it bite, spell a turn-end failure code with a hyphen:
`turn-end-reasons` must fail. Change the provider the adapter is configured
with: `initialize-lifecycle` must fail on the host wire. Strip the whitespace
from a framing violation before the runtime writes it: `framing-strictness`
must fail. Every submission is either admitted, and its run validated even
when it emits nothing, or refused; the admitted count must equal the case's
`run.started` events. Let a run that ends before admission answer its submit
with a run id: the replay must fail.
