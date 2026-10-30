# oap-adapter

The adapter/session contracts and an in-memory reference adapter for OAP.

## Collect a run's terminal result

`run_to_terminal(&dyn Session, MessageSubmitRequest).await` submits one request
and returns `Result<RunOutcome, RunError>`. On completion, the outcome contains
the run id, authoritative final message, its assembled text, optional usage and
the number of observed tool starts. Repeated sequence observations do not count
again. `message_text(&Message)` also works independently on text or content-part
messages, appending each part's text in order.

```rust
use oap_adapter::{run_to_terminal, RunErrorKind, Session};
use oap_protocol::MessageSubmitRequest;

async fn reply(session: &dyn Session, request: MessageSubmitRequest) -> String {
    match run_to_terminal(session, request).await {
        Ok(outcome) => outcome.text,
        Err(error) => match error.kind {
            RunErrorKind::Failed(failure) => format!("{}: {}", failure.code, failure.message),
            _ => error.to_string(),
        },
    }
}
```

`RunError` retains the partial `outcome`, including the admitted run id so the
host can reconcile it. `RunErrorKind` distinguishes failed, cancelled,
ended-without-terminal, unanswered gate, adapter error, payload decode error and
invalid message content. Failed runs carry their complete protocol error;
failed and cancelled outcomes preserve any supplied usage. Missing usage is
`None`; an empty measurement is `Some(Usage::default())`; a measured zero is
`Some(0)` in the corresponding field. Counts are never summed or inferred.

This is the terminal/text slice of Go's `adapter.RunToTerminal`, not the whole
helper. It has no gate policy, overflow replay, observer, stall/compaction timer
or automatic run cancellation when its future is dropped. It returns
`GateUnanswered` on permission, input or provided-call gates and preserves stream
errors, including overflow, for the host to handle. Harness-owned calls continue
normally. All gate payloads are decoded before reporting `GateUnanswered`;
missing or mistyped payloads instead produce `Decode`. An open quiet stream
waits for the host's executor/deadline.

The consumer owns and drops its stream on return or future cancellation. It
spawns no tasks or processes and does not close the caller's session. Dropping
its future releases the stream; it does not prove that the admitted run ended.
The host must cancel or reconcile any unfinished run and close its session when
finished. The `prompt` example demonstrates this explicitly with memory.

Run `cargo run -p oap-adapter --example prompt -- --memory` from `rust/` for a
credential-free memory example. It reaches a gate, obtains the run id, cancels
the run and closes the session. The verifier compares public Rust and Go
consumers; its journey is `.claude/skills/verify/journeys/rust-adapter-prompt.md`.

## Go's encoding/json rules

`gojson` reproduces the `encoding/json` behaviour the harness corpora pin: the
duplicate-key walk and its 10000-deep limit, `json.Marshal`'s compaction and
HTML escaping, `strconv.Quote`'s printability table, and the replacement of a
lone surrogate escape with U+FFFD. One Go behaviour is deliberately not reproduced:
Go's decoder replaces invalid UTF-8 inside a string with U+FFFD and accepts the
document, while `reject_duplicate_keys` refuses it as a syntax error. Every
caller reads frames through a reader that has already refused invalid UTF-8, as
Go's frame readers do, so the walk never sees such bytes on a harness's wire.

One limit differs on purpose. `remarshal_any` nests as deep as Go (10000
levels, on a large-stack thread past 128), but typed decoding through serde,
including the `zero`/`structure` helpers and every adapter's strict decoder,
keeps serde_json's 128-level limit, where Go's typed decode goes deeper. No
pinned corpus nests a native frame that deep.

## Child processes

`wait_error` writes the text Go's `exec.ExitError` carries: `exit status N`,
or `signal: <name>` with Go's per-platform signal names, followed by
` (core dumped)` when the status reports a core dump, as Go's
`os.ProcessState.String` appends it (`os/exec_posix.go`).

`process::Child` follows Go's launchers in every observable way but two. When
a harness leaves a grandchild holding its stderr, Go closes the pipe on the
drain-timeout and kill paths, which ends the copy. Safe Rust cannot close a
pipe under a blocked read, so the capture thread, its descriptor and its
buffer of at most `stderr_limit` bytes live until that grandchild exits. The
child's exit and every launcher's close are not delayed by it.

Go interrupts a child in process with `os.Interrupt`. Safe Rust has no signal
call, so `interrupt` runs `/bin/kill -INT` (or `/usr/bin/kill`) while it holds
the child, and only if the child has not been reaped, so a recycled pid is
never signalled. On a host with neither binary it kills the child instead,
which is the step Go would reach after its grace period anyway.

Stdin has the same limit. `release_stdin` never waits: when a write is
blocked on a full pipe it marks the pipe released and returns, and the writer
drops the descriptor as soon as its write returns, which killing the child
causes. Go closes the descriptor under the blocked write at once; Rust's
stays open until that write ends, so a grandchild holding the read end keeps
the writer blocked until it exits.

Stdout follows Go's order: a child's exit is recorded only after its stdout
reader ends, so every frame is read first. When a grandchild holds stdout
open, Go's kill closes the pipe and the read ends. Rust's `kill` cannot close
it, so it releases the watch instead: the child is reaped and `exited` fires,
while the blocked reader thread lives until the grandchild exits.
