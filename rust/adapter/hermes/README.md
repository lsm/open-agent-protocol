# oap-adapter-hermes

The Rust port of `go/adapter/hermes`: an OAP adapter over the Hermes TUI
gateway's JSON-RPC on stdio, pinned by `harnesses/hermes.json` and judged by
the corpus in `fixtures/adapters/hermes-v0.23.1`.

So far it holds the gateway's event envelope, the payloads of its run events,
and its server requests (approval, clarify, sudo, secret) with Go's
`DecodeServerRequest` and `RequestSessionID`, the params and results of the
five methods Go's adapter calls (session.create, session.resume, config.set,
prompt.submit and session.interrupt), and `rpc::DIALECT`, the
gateway's JSON-RPC over `oap_adapter::jsonrpc`: strict framing, params only as
an object or null, and an empty string id that parses but never marshals, as in
Go's `internal/rpc`. `adapter::Adapter::describe` is Go's `Probe`: the
descriptor `tests/go_capabilities.json` holds, the catalog's revision and
Go's journal and cancellation fields. `adapter::Adapter::open` is Go's
`Open`: it refuses tool sources, tools and a compaction policy, starts the
gateway through `adapter::Config`'s factory (a `client::ProcessFactory` over
its process settings when none is given), sets a reasoning level through
`config.set` and refuses one Hermes rejects as unsatisfiable, and reports an
idle `session::Session` bound to the stored session. A reopen is refused when
the binding names no stored session or the factory cannot reload one, and
otherwise reports the recovery and the model Hermes last ran under.
`client::ProcessFactory` reloads one with `session.resume`, as Go's
`resumeStored` does, refusing a session Hermes does not know, a reply naming
no session, a pending auto-continue and a session still running or busy. The
session so far answers `state` and `close`, and reduces the gateway's
inbound messages as Go's `dispatch` does while no run is reserved: an event
for another session, out of sequence, or run-scoped makes the session
unusable, as does a server request with a non-string id, for another
session, or with no run to own it; an unmapped request is answered
`-32601 method not found`, and a closed transport makes the session
unusable. A session-less event is ignored. Submitting arrives next. `client::ProcessFactory` is Go's process client factory: it
starts the gateway, calls `session.create` with the configured model and
reasoning effort, and refuses a reply naming no live or stored session as
`client::NATIVE_PROTOCOL`, closing the gateway either way. Its client relays
the gateway's inbound messages through a buffer of 256 as Go's
`relayInbound` does, acknowledging each response barrier at once so a call's
result never waits on the session, and reports the gateway's exit status
before the transport's error. A result Go cannot decode is refused with
Go's `decode session.create response: ` prefix and serde_json's detail.
`process::Process` starts the gateway as Go's `Start`
does: its client checks every notification with `decode_notification`,
refuses a response to no pending call, and takes only `gateway.ready` as the
first observation; `close` releases stdin, then interrupts and finally kills a
gateway that outlives the exit timeout. Go also runs `ValidateReady` on the
ready payload; `decode_notification` has already refused every payload it
would. `decode_notification` is Go's `DecodeNotification`: it
takes only the `event` method, then checks the envelope's sequencing, the
event type against Go's typed and observed vocabularies, and each typed
payload, with Go's text for every refusal of its own. A method result, an approval request and
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

`tests/client.rs` drives shell gateways through `session.create`, with each
reply's outcome printed from Go's process factory over the same scripts. To
see it bite, drop either id check, the model or the effort from the params, or
report the transport's error before the exit status: one test must fail for
each.

`tests/open.rs` opens sessions over scripted factories, with every state and
refusal printed from Go's `Open` over the same fakes. To see it bite, send
Hermes's reasoning level as OAP spells it, keep the client open after a
refused level, stop trimming the stored id, ignore the reloaded model, close
the client twice, or answer `state` once closed: one test must fail for each.

`tests/reopen.rs` drives shell gateways through `session.resume`, with each
refusal printed from Go's process factory over the same scripts. To see it
bite, drop the not-found code, the null auto-continue exemption, the status
or running check, the empty-id check, or the stored id on the client: one test
must fail for each.

`tests/reduce.rs` drives the reducer over a fake gateway, with each outcome
printed from Go's reducer over the same messages. To see it bite, drop the
session-less, foreign-session, sequence or run-scoped check, or the non-string
id check: one test must fail for each. The foreign-session check on a server
request is only observable once a run exists, so the submit slice pins it.
