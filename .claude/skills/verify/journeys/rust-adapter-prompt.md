# Rust adapter terminal/text consumer

Run from the repository root with only synthetic sessions/envelopes and the
in-memory adapter:

```sh
python3 .claude/skills/verify/scripts/rust-adapter-prompt.py --output-dir /tmp/oap-adapter-prompt-<unique>
```

The `oap-adapter` public exports `run_to_terminal` and `message_text` are the
first slice of Go's `adapter.RunToTerminal`/`MessageText`. The `prompt` example
uses those exports with a synthetic `Session`, and the Go testdata program uses
the actual Go helper on the same inputs. The script retains input JSON and both
consumers' outputs. Completed, failed, cancelled, ended-without-terminal,
submission/stream failure and unanswered gates must have distinct typed results;
failures retain the partial outcome and admitted run id. Duplicate sequence
observations must not double-count tool calls. The authoritative completed
message supplies text, not a concatenation of streaming deltas. Text strings
and content parts concatenate in order without losing Unicode bytes.

Usage absence, an empty usage object, measured zero, all six counters and exact
u64 maximum remain distinct. No total is inferred. One intentional difference
is observed separately: Rust retains `run.cancelled` usage, whereas the current
Go helper omits it. This follows the terminal payload, not a new wire rule.
Malformed message content is an explicit content error; malformed terminal
payloads are decode errors, never successful terminal results.

A synthetic stream held open after a terminal must be dropped immediately.
Dropping a pending Rust consumer future must release its owned stream. The
memory journey reaches an unanswered permission gate, returns the run id, then
explicitly cancels that run and closes the session. No subprocess, reader task
or resolver is spawned by the Rust helper. The verification script bounds and
reaps its own compiler/consumer subprocesses; it never calls native harnesses,
providers or credential APIs.

This slice has no gate-policy callbacks, overflow replay, observer callback,
stall/compaction timers or automatic run cancellation when the future is
dropped. Permission/input/provided-call gates return `GateUnanswered`;
harness-owned calls pass through. Stream errors, including overflow, retain the
adapter error instead of claiming replay support. A quiet open stream waits for
the caller's executor/deadline. Dropping the future releases the stream but is
not protocol cancellation: the host retains session ownership and must cancel
or reconcile the admitted run and eventually close its session. These are
explicit follow-up slices, not whole-helper parity.

With no concurrent Rust builds, mutation-check the outcome assertions:

```sh
python3 .claude/skills/verify/scripts/rust-adapter-prompt.py --mutation-check --output-dir /tmp/oap-adapter-prompt-mutations-<unique>
```

The script temporarily changes duplicate suppression, usage propagation,
failed/no-terminal judgments and terminal-path stream cleanup. Every mutation
must cause a runtime test assertion failure; compilation failure is not a kill.
It restores the original source in `finally` and reruns the complete focused
suite. Also run the repository baseline, full Rust all-features tests, fmt,
clippy with warnings denied, and nocomment before opening/merging. The baseline
includes credential-isolated PTY verification; inspect its rendered reply and
quit result. Live providers and real IME preedit remain unverified.
