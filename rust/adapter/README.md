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
normally. An open quiet stream waits for the host's executor/deadline.

The consumer owns and drops its stream on return or future cancellation. It
spawns no tasks or processes and does not close the caller's session. Dropping
its future releases the stream; it does not prove that the admitted run ended.
The host must cancel or reconcile any unfinished run and close its session when
finished. The `prompt` example demonstrates this explicitly with memory.

Run `cargo run -p oap-adapter --example prompt -- --memory` from `rust/` for a
credential-free memory example. It reaches a gate, obtains the run id, cancels
the run and closes the session. The verifier compares public Rust and Go
consumers; its journey is `.claude/skills/verify/journeys/rust-adapter-prompt.md`.
