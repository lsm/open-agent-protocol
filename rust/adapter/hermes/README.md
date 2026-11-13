# oap-adapter-hermes

The Rust port of `go/adapter/hermes`: an OAP adapter over the Hermes TUI
gateway's JSON-RPC on stdio, pinned by `harnesses/hermes.json` and judged by
the corpus in `fixtures/adapters/hermes-v0.23.1`.

So far it holds the gateway's event envelope, the payloads of its run events,
and its server requests (approval, clarify, sudo, secret) with Go's
`DecodeServerRequest` and `RequestSessionID`, and `rpc::DIALECT`, the
gateway's JSON-RPC over `oap_adapter::jsonrpc`: strict framing, params only as
an object or null, and an empty string id that parses but never marshals, as in
Go's `internal/rpc`. `adapter::Adapter::describe` is Go's `Probe`: the
descriptor `tests/go_capabilities.json` holds, the catalog's revision and
Go's journal and cancellation fields. `adapter::Config` takes the gateway's
process settings; Go's client and process factories arrive with the session.
`process::Process` starts the gateway as Go's `Start`
does: its client checks every notification with `decode_notification`,
refuses a response to no pending call, and takes only `gateway.ready` as the
first observation; `close` releases stdin, then interrupts and finally kills a
gateway that outlives the exit timeout. Go also runs `ValidateReady` on the
ready payload; `decode_notification` has already refused every payload it
would. `decode_notification` is Go's `DecodeNotification`: it
takes only the `event` method, then checks the envelope's sequencing, the
event type against Go's typed and observed vocabularies, and each typed
payload, with Go's text for every refusal of its own. An approval request and
`session.info` decode leniently, as Go's `json.Unmarshal` does; everything else
decodes as Go's `DecodeStrict` does: unknown members refused, a null
member read as its zero value, a trailing value refused, only JSON's four
whitespace characters skipped, and an unpaired surrogate escape or invalid
UTF-8 inside a string read as U+FFFD. Three of Go's `encoding/json` rules differ:

- Go keeps the last of a repeated member; serde's derived structs refuse the
  frame instead.
- Go falls back to a case-insensitive key match, so `{"Session_ID":"s"}` sets
  `SessionID`; these types match keys exactly.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029, and serializing
  an `Event` here does not. The rpc layer writes every frame through
  `oap_adapter::gojson::go_marshal`.

The gateway writes each member once, in its exact case, and the pinned corpus
spells them so, so nothing on the wire observes the first two.

`decode_strict` refuses every frame Go's `DecodeStrict` refuses. Its error text
is Go's for the refusals the adapter reports: a non-object struct, which names
`native.<Type>` or Go's `usageAlias` and `surfaceAlias`, a non-object tool
`args`, and an unknown member, which reads `json: unknown field "x"`. That text
matters: Hermes reports a refused notification as the transport's error, and a
run it closes carries it. Other refusals carry serde_json's wording. Examples
are a mistyped member, which Go names by struct field, a trailing value, and a
byte that cannot begin a JSON value.
