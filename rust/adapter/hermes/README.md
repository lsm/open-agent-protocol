# oap-adapter-hermes

The Rust port of `go/adapter/hermes`: an OAP adapter over the Hermes TUI
gateway's JSON-RPC on stdio, pinned by `harnesses/hermes.json` and judged by
the corpus in `fixtures/adapters/hermes-v0.22.2`.

So far it holds the gateway's event envelope and the payloads of its run
events, decoded as Go's `DecodeStrict` does: unknown members refused, a null
member read as its zero value, a trailing value refused, only JSON's four
whitespace characters skipped, and an unpaired surrogate escape or invalid
UTF-8 inside a string read as U+FFFD. Three of Go's `encoding/json` rules differ:

- Go keeps the last of a repeated member; serde's derived structs refuse the
  frame instead.
- Go falls back to a case-insensitive key match, so `{"Session_ID":"s"}` sets
  `SessionID`; these types match keys exactly.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029, and serializing
  an `Event` here does not. The rpc layer, which lands separately, writes every
  frame through `oap_adapter::gojson::go_marshal`.

The gateway writes each member once, in its exact case, and the pinned corpus
spells them so, so nothing on the wire observes the first two.

`decode_strict` refuses every frame Go's `DecodeStrict` refuses. Its error text
is Go's for the refusals the adapter reports: a non-object struct, which names
`native.<Type>` or Go's `usageAlias` and `surfaceAlias`, and a non-object tool
`args`. Other refusals carry serde_json's wording. Examples are a mistyped
member inside `usage` or `error_surface`, and a byte that cannot begin a JSON
value.
