# oap-adapter-claude

The Rust port of `go/adapter/claude`: an OAP adapter over Claude Code's
stream-json control protocol on stdio, pinned by `harnesses/claude-code.json`
and judged by the corpus in `fixtures/adapters/claude-code-2.1.302`.

`native` holds the pinned frame types, decoded as Go's `encoding/json` decodes
them. So far `decode_observation` reads user and assistant frames; every other
frame type reads as `Unknown` until its own types land.

One `encoding/json` rule is not followed: Go falls back to a case-insensitive
key match, so `{"MESSAGE":{...}}` fills `Message`, while these types match keys
exactly, as the ACP crate does. Claude Code writes every key in its
exact case, and the pinned corpus spells them so, so nothing observes the
difference.
