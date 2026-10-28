# oap-adapter-acp

The Rust port of `go/adapter/acp`: an OAP adapter over an Agent Client Protocol
v1 agent on stdio, pinned by `harnesses/acp.json` and judged by the corpus in
`fixtures/adapters/acp-v1.12.0`.

`native` holds the pinned upstream wire types and nothing else. They decode the
way Go's `encoding/json` does, because the corpus records what the Go adapter
accepted: a `null` member is its zero value, a struct member refuses an array,
and a raw member keeps an explicit `null`. `native::decode` applies the same
rules to a whole message.
