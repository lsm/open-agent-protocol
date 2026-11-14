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
(cd rust && cargo test -p oxap-adapter-claude --test native_turns)
(cd rust && cargo test -p oxap-adapter-claude --test native_raw_bytes)
(cd rust && cargo test -p oxap-adapter-claude --test native_host_writes)
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
A raw member keeps a lone surrogate escape (`\ud800`) as Go's
`json.RawMessage` does, while a string member reads it as U+FFFD. Write such a
case from a script, since an editor may turn the escape into the character, and
grep the file for the literal escape. A control request is decoded from the
normalised text, so a key holding such an escape is skipped as Go skips it,
and `source::raw` copies each raw member from the same span of the original
text; the replacement keeps every escape six bytes long, so the spans line
up. To see it bite, keep the normalised span in `source::raw`: the raw-member
test must fail. Decode the original text instead: the ignored-key test must
fail.

`tests/native_turns.rs` compares the user turn the host writes byte for byte
with Go's `NewUserTurn`, which marshals a map: members sorted, `<`, `>`, `&`
and the line separators escaped. It ports the uuid cases of Go's
`TestHostWriteShapes` and pins both length bounds, counted in bytes, so 65
two-byte characters are too long. To see it bite, write `client_composed:
false` on a turn the CLI may expand: the expand test must fail. Skip
`go_marshal`: the escaping test must fail. Count characters instead of
bytes: the uuid test must fail.

`tests/native_raw_bytes.rs` pins the same rule for observation frames: an
assistant block's `input` and `usage`, a user frame's tool result and block
content, a result's structured output, model usage and denials, and a stream
event keep a lone surrogate escape byte for byte, while the string members
beside them, a user frame's text and a stream delta read it as U+FFFD. It
also skips an ignored key holding the escape at every level, including in a
user frame's blocks and a stream event, which are read again from their raw
bytes. Nested members are parsed by `source::structure` and `source::slice`,
which borrow from the decoded text so each raw span stays in it. To see it
bite, parse a nested member from a copy: the raw-member assertions must fail.
Read a user frame's blocks without `source::decode_slice`: the ignored-key
test must fail.

`tests/native_host_writes.rs` pins the bytes of the hook answers, the
initialize and interrupt requests, the permission answers and the MCP response
Go's session writes to Claude Code, compared with what `json.Marshal` gives the Go types in
`go/adapter/claude/internal/native/types.go`: members in declaration order,
each `omitempty` member left out when empty, and a raw member compacted and
escaped as Go marshals a `json.RawMessage`, a lone surrogate escape kept. It
also reads an interrupt result as Go reads one, a null member or element as
its zero value and a non-object refused. To see it bite, drop the
`skip_serializing_if` on `interrupt`: the deny test must fail. Skip
`go_marshal`: the allow test must fail. Decode the result without
`source::decode_object`: the null result case must fail.
