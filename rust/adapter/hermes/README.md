# oap-adapter-hermes

The Rust port of `go/adapter/hermes`: an OAP adapter over the Hermes TUI
gateway's JSON-RPC on stdio, pinned by `harnesses/hermes.json` and judged by
the corpus in `fixtures/adapters/hermes-v0.22.2`.

So far it holds the gateway's event envelope and the payloads of its run
events, decoded as Go's `DecodeStrict` does: unknown members refused, a null
member read as its zero value, a trailing value refused, and an unpaired
surrogate escape read as U+FFFD. Three of Go's `encoding/json` rules differ:

- Go keeps the last of a repeated member; serde's derived structs refuse the
  frame instead.
- Go falls back to a case-insensitive key match, so `{"Session_ID":"s"}` sets
  `SessionID`; these types match keys exactly.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029, and serializing
  an `Event` here does not. The rpc layer, which lands separately, writes every
  frame through `oap_adapter::gojson::go_marshal`.

The gateway writes each member once, in its exact case, and the pinned corpus
spells them so, so nothing on the wire observes the first two.
