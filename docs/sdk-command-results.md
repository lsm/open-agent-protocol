# Command exit evidence in SDK streams

The Go, Rust and TypeScript SDKs retain an action terminal's original result
JSON on their tool execution end event: Go `ToolExecutionEnd.Result`, Rust
`AgentEvent::ToolExecutionEnd.result`, TypeScript `tool_execution_end.result`.
The daemon TypeScript client (`clients/ts`) exposes the same value unchanged
on `ActionCallCompletedPayload.result`.

For `codex.command`, the result carries `command`, `cwd`, `output` and native
`exitCode`. Numeric zero is successful process-exit evidence; nonzero retains
the actual code. Native omission or null is explicit null after adapter
normalization, meaning unknown. No layer invents zero. Additional native JSON
survives under `nativeResult`.

A completed action alone is not successful-command evidence. SDKs project a
`codex.command` terminal as non-error only when it is `action.call.completed`
and its result has numeric `exitCode: 0`. Nonzero and unknown both project as
error; their original result distinguishes a known failure from unknown exit
status. An explicit native failure remains error even if a zero is present.
Other tools retain their existing action-lifecycle error projection.

These are SDK event projections over the existing free-JSON OAP result; the
wire schema and validator rules are unchanged. Rust has no native Codex
adapter in this repository; native normalization is supplied by Go or Zig.
The SDK parity journey replays issue #1386's native fixture through both real
CLIs and all three SDKs, checks `clients/ts` preservation, and validates both
wire traces in Go, Rust and Zig.
