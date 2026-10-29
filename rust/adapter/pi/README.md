# oap-adapter-pi

The Rust port of `go/adapter/pi`: an OAP adapter over Pi's RPC mode on stdio,
pinned by `harnesses/pi.json` and judged by the corpus in
`fixtures/adapters/pi-v1.3.0`.

So far it holds the native commands, responses, session state, events and
extension UI requests and responses.
`decode_strict` follows Go's `DecodeStrict`: a repeated key is refused, an
unknown member is refused, a null member reads as its zero value, and an
unpaired surrogate escape reads as U+FFFD. Two of Go's `encoding/json` rules
differ:

- Go falls back to a case-insensitive key match, so `{"sessionid":"s"}` sets
  `SessionID`; these types match keys exactly. The pinned corpus spells every
  key in its exact case, so nothing observes the difference.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029, and serializing
  a command here does not. The rpc client, which lands separately, writes every
  frame through `oap_adapter::gojson::go_marshal`.
