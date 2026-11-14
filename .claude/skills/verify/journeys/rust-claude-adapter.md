# Rust Claude Code adapter

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oxap-adapter-claude --test native_messages)
(cd rust && cargo test -p oxap-adapter-claude --test native_results)
(cd rust && cargo test -p oxap-adapter-claude --test native_lifecycle)
(cd rust && cargo test -p oxap-adapter-claude --test native_system)
(cd rust && cargo test -p oxap-adapter-claude --test native_tasks)
(cd rust && cargo test -p oxap-adapter-claude --test native_control)
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

`tests/native_lifecycle.rs` ports Go's `TestCommandLifecycleStates` and pins
the refusal Go prints for a `tool_progress` or `conversation_reset` frame
missing a required member, a null member read as its zero value, and a
`keep_alive` read as Go reads it: without its body and with no subtype. To see
it bite, drop the `keep_alive` arm: the keep-alive test must fail. Drop the
`tool_name` check: the tool-progress test must fail. Drop the `uuid` check on
a reset: the reset test must fail.

`tests/native_system.rs` ports the init, session state and notice cases of
Go's `TestSystemVocabulary` and pins what Go settles without a test: an empty
`tools` list is accepted where a null or absent one is refused, a null member
or list element is its zero value, a `status` frame is compacting only when
its status says so, and a notice carries the frame's own subtype and bytes.
To see it bite, drop the `tools` check on init: the init refusal test must
fail. Leave a notice's bytes empty: the notice test must fail. Read any status
as compacting: the status test must fail.

`tests/native_tasks.rs` ports the task cases of Go's `TestSystemVocabulary`
and `TestTaskNotificationRequiresOutputFilePresentButAcceptsItEmpty`. It pins
each refusal Go prints, including a non-terminal notification status quoted
as Go's `%q` quotes it, and a null `usage`, `patch` or `output_file` read as
Go reads it: a present null `output_file` counts as present. To see it bite,
drop the `output_file` presence check: the output-file test must fail. Read
no patch as terminal: the task-updated test must fail. Print the status
unquoted: the notification test must fail.

`tests/native_control.rs` ports Go's `TestCanUseToolRequestDecode` for the
reverse requests Claude Code sends the host: `can_use_tool`, `hook_callback`
and `mcp_message`. It pins what Go settles without a test: a present null
`input` counts as present, a null `request` or hook `input` reads as its zero
value, an MCP message keeps its bytes as written, whitespace included, and any
other subtype is unknown without its body being read. To see it bite, drop
the `input` presence check: the can-use-tool refusal test must fail. Drop the
`callback_id` check: the hook test must fail. Decode an unknown subtype's
body: the unknown test must fail.
