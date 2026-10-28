# oap-adapter-hermes

The Rust port of `go/adapter/hermes`: an OAP adapter over the Hermes TUI
gateway's JSON-RPC on stdio, pinned by `harnesses/hermes.json` and judged by
the corpus in `fixtures/adapters/hermes-v0.22.2`.

So far it holds the gateway's event envelope and the payloads of its run
events, decoded as Go's `DecodeStrict` does: unknown members refused, a null
member read as its zero value, a trailing value refused.

One divergence: Go's decoder keeps the last of a repeated member, and serde's
derived structs refuse the frame instead. The gateway writes each member once.
