# oap-adapter-claude

The Rust port of `go/adapter/claude`: an OAP adapter over Claude Code's
stream-json control protocol on stdio, pinned by `harnesses/claude-code.json`
and judged by the corpus in `fixtures/adapters/claude-code-2.1.302`.

`native` holds the pinned frame types, decoded as Go's `encoding/json` decodes
them. So far `decode_observation` reads user and assistant frames; every other
frame type reads as `Unknown` until its own types land.
