# Rust Claude Code adapter

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oxap-adapter-claude --test native_messages)
(cd rust && cargo test -p oxap-adapter-claude --test native_results)
(cd rust && cargo test -p oxap-adapter-claude --all-features)
```

`tests/native_results.rs` ports Go's `TestResultArbitrationInputs` and
`TestStreamEventDeltas` from `go/adapter/claude/internal/native`, and adds the
cases Go's decoder settles without a Go test: a null member read as its zero
value, an `aborted_tools` terminal reason, `max_turns` as a terminal reason,
and each stream event that is not a text or thinking delta, including a delta
whose text is a number and an event that is `null` or a string. Regenerate an
expectation by printing `DecodeObservation`, `Cancelled`, `MaxTurns` and
`StreamDelta` from a throwaway test in that package. To see it bite, drop
`aborted_tools` from `cancelled`: the settle test must fail. Drop the
`session_id` check on a result: the refusal test must fail. Read a thinking
delta's `text`: the delta test must fail.
