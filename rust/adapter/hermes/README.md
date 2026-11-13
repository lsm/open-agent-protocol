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
unusable. A session-less event is ignored. `Session::submit` is
Go's `Submit`: it takes one user message of text, refuses any control Hermes
does not advertise, calls `prompt.submit`, and starts the run only once
Hermes has both answered `streaming` and opened the turn with
`message.start`, in either order; events seen before then are held and
replayed. A second open fails the run as `hermes_invalid_grammar`, foreign
activity as `hermes_external_activity`, and a transport that dies after the
turn opened as `hermes_process_exit` settled by inference. An approval,
clarify, sudo or secret request that reaches a running turn surfaces as
`user.input.requested` for the session's participant (`user` when the open
named none), followed by `run.status.updated` waiting for that input; it is
not answered until it is resolved. A `request.cancel` naming its request id
resolves it as cancelled and puts the run back to running. A gate that does
not decode fails the run as `invalid <method> gate`, and a clarify choice
that is empty as a question nobody can answer; Go's other answerability
checks cannot fail once the request decodes, so they are not ported.
`message.complete` settles the run: `complete`
emits `run.completed` with the turn's text and usage, any other status
`run.failed` coded `hermes_` plus the error surface's code or the status,
and a status-less settlement, which only a child turn's mirror sends, fails
the run as `hermes_invalid_settlement`. `message.delta` and
`reasoning.delta` become text and reasoning content deltas. `tool.start`
emits `action.call.requested`, carrying the arguments re-encoded as Go's map
encodes them, then `action.call.started`; `tool.complete` emits
`action.call.completed` with the result, or `null`. A reused tool id or a
completion for no open tool fails the run as `hermes_tool_lifecycle`, and
a settlement or failure first cancels every gate still open, then fails
every tool still open as `incomplete_tool`, each in the order it arrived.
`Session::resolve` is Go's `Resolve`: it refuses a gate it cannot find or
already settled as `InteractionNotFound`, and a foreign run, session,
responder or requester or an answer that does not fit the gate's questions
as `InvalidResolution`. It answers Hermes outside the reducer's lock; a
settlement arriving meanwhile waits until every answer in flight is
written, and a `request.cancel` arriving meanwhile resolves the gate as
cancelled and refuses the resolution. A failed write leaves the gate open.
Go also cancels the gate on a failed write while a settlement waits, counts
clarify answers before indexing them, copies the resolver as the responder,
and checks for answers in flight before flushing a waiting settlement; none
of these changes an outcome, since the settlement cancels the gate,
indexing refuses a missing or extra answer, a responder must already be the
participant, and the settlement checks again itself, so they are not
ported. `Session::cancel`, `update_settings` and `resume` are Go's
`Cancel`, `UpdateSettings` and `Resume`: a cancel interrupts Hermes for a
started run and reports it cancelling, and the interruption settles the run;
a settings change sets the reasoning level through `config.set` on an idle
session; a resume replays a live or ended run from the journal. Go also
refuses settings when the session is not idle, which only holds while a run
is open, so only the run is checked.
`Adapter::native_list` is Go's `NativeList` over a factory that can list:
it reads each stored session's id, its title or preview, and its start time
in seconds as milliseconds. Go also trims the start time and refuses an
empty, infinite or not-a-number one before its bound; the decoder never
keeps whitespace and the bound refuses the rest, so only the bound is
ported.
`client::ProcessFactory` lists as Go's process factory does: one gateway
kept for every listing, its messages drained, replaced once when a reused
one breaks, a busy one leaving the listing a gateway of its own, and
`Adapter::close` stopping it. A listing takes no deadline, so each call
gives up after Go's sixty-second listing limit, which no test waits out.
Go also checks that a kept gateway has exited before reusing it and closes
each gateway it drops; here a gateway reports its exit only after its
output ends, so a call to it fails and is replaced the same way, and a
gateway closes when it is dropped. Go's check that a gateway which answered
an error has exited races that exit, so no test pins it.
`client::ProcessFactory` is Go's process client factory: it
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

`tests/submit.rs` compares every admission and envelope byte for byte with
Go's `Submit` over the same fake gateway, using Go's test ids and clock. To
see it bite, start the run without `message.start` or without the reply, drop
the transcript cursor, the transport-death deferral or the abort on a refused
reply, join text parts other than by a newline, or let a second run in:
one test must fail or hang for each.

`tests/settle.rs` compares the envelopes for every settlement status and
both delta kinds byte for byte with Go's reducer over the same frames. To
see it bite, drop the empty-delta skip, the reasoning part, the child-mirror
check, the negative-count refusal, the error-surface code, the
error-before-text order, the `turn <status>` fallback, the total-token count
or the reasoning delta: one test must fail for each.

`tests/tools.rs` compares every tool lifecycle Go distinguishes byte for
byte with Go's reducer over the same frames. To see it bite, drop the
duplicate check, the argument re-encoding, the `null` default for absent
arguments or result, the reply links, the open-tool filter on completion or
settlement, the settlement of open tools on completion or failure, the tool
call id on the envelope, or the requesting endpoint: one test must fail for
each.

`tests/gates.rs` compares every gate Go surfaces, expires or cancels byte
for byte with Go's reducer over the same requests, and checks no gate is
answered. To see it bite, drop the empty-choice check, the description
fallback, a question kind, the request-id or resolved filter on expiry, the
running update, the cancellation on settlement, the turn id, the reply link,
the pending input id, the participant or its default, `allow_cancel`, the
waiting update, or the replay of a buffered gate: one test must fail for
each.

`tests/resolve.rs` compares every resolution outcome, the answer Hermes
receives and the envelopes byte for byte with Go's `Resolve` over the same
gates, including a parked settlement, a withdrawn gate, a failed write and
two nested resolutions. To see it bite, drop the resolved filter, any of
the five ownership checks, the answer validation, the multi-choice encoding,
the deferral or the settling flag, the withdrawal, its refusal or its
cancelled status, the flush after a failed write, the submitted answers, or
the running update: one test must fail for each.

`tests/controls.rs` compares cancel, settings and resume with Go's
`Cancel`, `UpdateSettings` and `Resume` over the same gateway: the
interrupt and its failure, the interrupted settlement, a cancel or resume
before the turn opens, a level change and its refusal, a live, ended and
caught-up replay, a cursor past the run, a journal gap, and a closed or
dead session. To see it bite, drop the started or run-id check on cancel,
the interrupt failure, the live-settings refusal, the closed or unusable
check, the session check, the active-run check, the empty-level guard, the
timestamp, the closed check or started filter on resume, the cursor
arithmetic or comparison, or the gap refusal: one test must fail for each.

`tests/native_list.rs` compares every page a factory's `session.list`
answers with Go's `NativeList`: the limit, rows that are not objects or
name no id, a title falling back to the preview, duplicate and
case-folded keys, a page that is not a list, a failed listing, a factory
that cannot list, and the start time read as seconds, saturated or
refused. To see it bite, drop the limit, the id check, the preview
fallback, the long-s or case fold, the last-key rule on the page or on a
row, the integer path or its saturation, the float bound or its
arithmetic: one test must fail for each.

`tests/kept_list.rs` drives scripted gateways through Go's listing rules:
one gateway serves every listing and drains what it sends, an error it
answers keeps it, a broken one is replaced once only when it was reused, a
busy one leaves the listing a gateway of its own, a gateway that cannot
start fails the listing, and closing the adapter stops the kept one. To see
it bite, drop the drain, the busy branch, the remote-error check, the
reuse check, the retry's keeping, the kept gateway, the adapter's close or
the limit: one test must fail for each.
