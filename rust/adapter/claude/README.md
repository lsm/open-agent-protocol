# oxap-adapter-claude

The Rust port of `go/adapter/claude`: an OAP adapter over Claude Code's
stream-json control protocol on stdio, pinned by `harnesses/claude-code.json`
and judged by the corpus in `fixtures/adapters/claude-code-2.1.322`.

`native` holds the pinned frame types, decoded as Go's `encoding/json` decodes
them. `decode_observation` reads every frame Go's `DecodeObservation` reads:
an unrecognised type reads as `Unknown`, and an unrecognised system subtype as
a `SystemNotice` that keeps its bytes. `decode_control_request` reads the
reverse requests Go's `DecodeControlRequest` reads.

Three `encoding/json` behaviours are not followed, and nothing observes any of
them. Go falls back to a case-insensitive key match, so `{"MESSAGE":{...}}`
fills `Message`, while these types match keys exactly, as the ACP crate does;
Claude Code writes every key in its exact case, and the pinned corpus spells
them so. A frame that is `null` or not an object is refused in serde's words
rather than Go's, which name the Go type; Go's reader refuses a line that is
not an object before it decodes a frame, so no such frame arrives. Go's `int`
members (`queued_turn_count`, `api_error_status`) read as `i64`, which is Go's
`int` on every platform the runtime ships for.
