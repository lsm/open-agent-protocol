# oxap-adapter-claude

The Rust port of `go/adapter/claude`: an OAP adapter over Claude Code's
stream-json control protocol on stdio, pinned by `harnesses/claude-code.json`
and judged by the corpus in `fixtures/adapters/claude-code-2.1.322`.

`native` holds the pinned frame types, decoded as Go's `encoding/json` decodes
them. `decode_observation` reads every frame Go's `DecodeObservation` reads:
an unrecognised type reads as `Unknown`, and an unrecognised system subtype as
a `SystemNotice` that keeps its bytes. `decode_control_request` reads the
reverse requests Go's `DecodeControlRequest` reads, and `new_user_turn` writes
the user turn Go's `NewUserTurn` writes, byte for byte. The hook answers, the
initialize and interrupt requests, the permission answers and the MCP response
are written as Go's `json.Marshal` writes them, with a raw member compacted and
escaped as Go marshals a `json.RawMessage`. Two writes are not ported yet: the
`apply_flag_settings` request, whose settings Go writes from a free-form map,
and the `get_settings` request the reopen path sends. `decode_interrupt_result` reads Claude Code's answer to an
interrupt as Go reads it into `InterruptResult`.

Three `encoding/json` behaviours are not followed, and nothing observes any of
them. Go falls back to a case-insensitive key match, so `{"MESSAGE":{...}}`
fills `Message`, while these types match keys exactly, as the ACP crate does;
Claude Code writes every key in its exact case, and the pinned corpus spells
them so. A frame that is `null` or not an object is refused in serde's words
rather than Go's, which name the Go type; Go's reader refuses a line that is
not an object before it decodes a frame, so no such frame arrives. Go's `int`
members (`queued_turn_count`, `api_error_status`) read as `i64`, which is Go's
`int` on every platform the runtime ships for.

Go's `json.RawMessage` keeps a member's bytes as they arrived, while its
string decoding reads a lone surrogate escape such as `\ud800` as U+FFFD. Every
frame is decoded from text with that replacement applied, keys included, so a
key holding such an escape is skipped like any unknown member, as Go skips
it. Each raw member is then copied from the same span of the frame's own
bytes, so it keeps the escape; the replacement keeps every escape six bytes
long, so the spans line up. A raw member is UTF-8 text, so invalid UTF-8
inside one reads as U+FFFD where Go keeps the byte.
