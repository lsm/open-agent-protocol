# oap-adapter-deepseek

The Rust port of `go/adapter/deepseek`: an OAP adapter over the DeepSeek
harness SDK runtime's JSON-RPC on stdio, pinned by
`harnesses/deepseek-harness.json` and judged by the corpus in
`fixtures/adapters/deepseek-harness-dsh-v0.2.1-rc.2`.

So far it holds the native handshake, prompt and notification types and Go's
`DecodeStrict`: a repeated key is refused, an unknown member is refused, a
null reads as the zero value, a null array element reads as its zero value,
each invalid UTF-8 byte and each unpaired surrogate escape reads as U+FFFD,
and a non-object refused as a struct names the Go type. The handshake and
prompt results decode leniently, as Go's client does. Three of Go's
`encoding/json` rules differ:

- Go falls back to a case-insensitive key match, so `{"sessionid":"s"}` sets
  `SessionID`; these types match keys exactly. The pinned corpus spells every
  key in its exact case, so nothing observes the difference.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029, and serializing
  a type here does not; the rpc layer writes every frame through
  `oap_adapter::gojson::go_marshal`.
- A malformed document or a mistyped member is refused with the duplicate-key
  walker's `invalid JSON` or with serde_json's wording rather than Go's. A
  trailing value carries Go's `trailing JSON value`.
