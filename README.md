# Open Agent Protocol

Draft public-domain semantic protocol for agent software layers, agent loops,
model IO, tools, resources, and adapters for existing agent SDKs.

The first conformance target is an agent-control core covering the boundary
between a control layer and an agent loop: capabilities, session state, message
submission, run lifecycle, streamed content, cancellation, and state recovery
without binding to a specific transport.

OAP layers are logical software boundaries, not deployment sides. A
presentation layer, control layer, agent loop, model provider, tool executor,
resource provider, and binding can live in one process or across multiple
transports while preserving the same protocol semantics. Adapters for existing
SDKs or protocols are implementation shims, not a separate semantic layer.

[**drafts/composition.md**](drafts/composition.md) sets out how the layers
compose and what a client may choose about the layers below the one it talks
to. Two of those boundaries have profiles. `agent-control-core` is the control
layer talking to an agent loop — sessions, runs, tools — and is what this
repository executes today.
[Decision 0016](decisions/0016-model-provider-profile.md) and
[**drafts/model-provider-core.md**](drafts/model-provider-core.md) specify the
lower model-provider boundary. `oapx` now implements both profiles and uses
model-provider-core for its own agent inference; it can expose them together
on one stdio connection. Other agent implementations may still wrap vendor
SDKs internally.

Run the combined endpoint with:

```sh
oapx serve agent,provider --stdio
```

The four SDKs in `sdk/` use this OAP mode by default. The agent's selected
model can be changed mid-session with `session.model.switch`, and local
authentication flows use the optional `+auth` unit. Dynamic provider
attachment is specified but optional; remote provider transport is follow-up
work. Client-executed agent tools and agent sampling options are not yet
represented by this `oapx` OAP endpoint, so SDKs refuse them explicitly on
the default path; the old Makai wire is available only by explicit opt-in.

Implementing OAP natively? [**STABILITY.md**](STABILITY.md) describes the
v0.1 pre-release contract: what will freeze at the first tag, what may still
be added, how conformance is defined and answered, how long a deprecation runs,
and what you are owed before a breaking change lands. Implementers who want
notice of proposed breaking changes add themselves to
[IMPLEMENTERS.md](IMPLEMENTERS.md).

Current drafts:

- [Conformance Draft](drafts/conformance.md)
- [Presentation Control Profile](drafts/presentation-control-profile.md)
- [Agent Control Core](drafts/agent-control-core.md)
- [Agent Control Profile](drafts/agent-control-profile.md)
- [Model Provider Core](drafts/model-provider-core.md)
- [Provider Binding: Inference Envelopes Over stdio](drafts/provider-stdio.md)
- [Layered Agent Protocol](drafts/layered-agent-protocol.md)
- [Staged Units Graduation Plan](drafts/staged-units-graduation.md)

Protocol artifacts:

- [Illustrative protocol envelopes](examples/README.md)
- `fixtures/`: normative executable conformance traces
- `fixtures/packs/`: extension packs, loadable with `oap validate -pack`
- `schema/v0.1/`: JSON Schema bundle for agent control and model provider profiles

Executable core (Go 1.27 or later):

```sh
go run ./go/cmd/oap check
```

The command validates positive and negative fixtures and drives the deterministic
in-memory reference adapter. The reference adapter proves the public adapter
boundary and bounded process-memory recovery; it is not a production harness or
a durable persistence implementation.

### Validating a trace (`oap validate`)

```sh
go run ./go/cmd/oap validate [--format=human|json] [-mode strict|tolerant] [-pack <dir>]... <trace.json>...
```

`-mode strict` (the default) compiles the bundle exactly as published: an
unknown member, enum value, or envelope type fails. `-mode tolerant` compiles
it under the layered draft's extension rules, so a later revision's additive
field or an extension's envelope type is accepted on the common fields alone.

`-pack` loads an extension pack — a directory holding a `pack.json` descriptor
and the schemas it contributes — and is repeatable. With the pack loaded its
envelope types are validated against its own branches and its capability keys
are gated exactly as core keys are; without it the same envelopes take the
tolerant unknown path. Both flags are the caller's stated choice and neither is
inferred from the input: a live envelope saved to a file is indistinguishable
from a fixture. `fixtures/packs/storage` is a worked pack, and
[Decision 0004](decisions/0004-extension-packs.md) specifies the format.

### Run controls (`+run-controls`)

A submission may carry four per-run controls — `model_id`, `instructions`,
`tool_choice`, and `output_schema` — and each is gated on its own capability
key. The discipline is fail-closed: a control an endpoint has not
affirmatively advertised is refused *before* admission with
`unsupported_feature` naming the key, a `degraded` one needs the caller's
`allow_degraded_features` opt-in, and one whose value cannot be honoured is
refused as `unsatisfiable`. Nothing is accepted and ignored, and a refusal
always says what to change. A request failing more than one of these gets one
refusal, ranked capability → degradation → unsatisfiability → state.

Execution is claimed per control, only for the keys an endpoint advertises
above `unavailable`, so an endpoint that supports none of the four still
claims the unit by refusing all four correctly.
[Decision 0005](decisions/0005-run-controls.md) graduates the discipline and
`model_id`: Codex applies it natively per run
(`run.model_selection`, scope `run`), which means the model binds that run
alone and `current_model_id` — the model the next control-free submission
would use — does not move. The reference adapter executes all four.

### Queue delivery (`+queue`)

A session may hold one started run and an advertised number of queued
reservations. An explicit `delivery: "queue"` is admitted as a reservation —
`admission: "queued"`, nothing emitted for it yet — and promotes when every
earlier-admitted run of the session is terminal; on a busy session an `auto`
submission resolves to the same shape and says so with
`delivery_resolution: "session_busy"`. One run executes at a time, in admission
order, with one exception: the pre-start terminal of a reservation that never
started is published when it happens, because the slot it releases is capacity
the trace has to show at the moment it changes.

Advertising the queue is a claim that some submission will be queued, so the
descriptor discloses the bound that makes the claim checkable:
`capabilities.response.limits` carries `max_queued_runs_per_session` and,
optionally, `max_active_runs_per_session`. A queue advertised with no reachable
bound is a defect, not a permissive default. Beyond the bound the wire's answer
is `run_active`, and that refusal is validated in both directions: a reached
bound may not hide behind another code, and a bound the window shows was never
reached may not be reported at all.

`session.state` grows `active_runs` — every nonterminal run in admission order,
with the reservation's queue position — and `as_of`, the position the snapshot
was captured at, so a state read that raced a lifecycle event is reconciled
rather than diagnosed.
[Decision 0007](decisions/0007-queue-delivery.md) graduates the unit; OpenCode
advertises it `native` on `SessionInput.Admitted`, and the reference adapter
emulates one reservation.
### Tool sources (`+tool-sources`)

A tool catalog says where each of its tools comes from. `action.tools.list.response`
carries `sources` — `{ id, kind, display_name?, protocol?, endpoint? }` — and
each `ToolDefinition` carries the `source` id it belongs to, so a consumer
attributes a call to an MCP server without parsing a namespaced name, and
`action.call.*` carries exactly what the catalog in force attributes the tool
to — the session's own catalog where it has served one, the descriptor's
otherwise — and nothing where neither does, because a cross-reference a reader
cannot follow is not one. An endpoint whose sources are known before any
session declares them in its descriptor and may name them from the start; one
that learns a source from a session names it once it has served the catalog
that publishes it, and before that the catalog keeps the attribution exactly. A source `id` is unique across a
session's catalog and a tool `name` is unique whatever its source. The catalog
is gated on `action.tools.list` and nothing else: `action.tools` means
lifecycle observation, and several adapters observe tool calls while publishing
no portable catalog at all.

A session may attach sources for its lifetime:
`session.open.request.tool_sources` carries `ToolSourceAttachment` values —
the descriptor's members plus, for a `process` source, `command`, `args`, and
`environment`. The attachment shape is deliberately not the catalog shape,
because an attachment's environment can hold a credential: a published source
carries none of those three members, and an endpoint that reflected one back
into its catalog is a schema rejection rather than a convention. The endpoint
discloses what it accepts in `action.tool_sources.attach` — `modes` (always
`session_open`, plus `remote` where offered) and `limits` (`max_sources`, the
`transports` it takes) — so a refusal is checkable in both directions. The
modes are a set, following `run.tool_selection`, so disclosing `remote` never
erases the session-open one.

**The daemon does not take a command from the wire.** "Loopback, single-user"
describes the transport, not the origin of a request on it: a page in the
user's browser can reach `127.0.0.1` with a valid envelope. On
`POST /adapters/{name}/sessions` a `process` attachment therefore names an
operator-configured source by `id` only — from the registry document's
`tool_sources` map — and the daemon fills the rest from its own entry: the
`command`, `args`, and `environment` it runs the source with, and the `kind`,
`display_name`, `protocol`, and `endpoint` it publishes for it. A wire-supplied
command, argument list, or literal `NAME=value` environment value is refused
before the open is forwarded, and so is a `kind`, `display_name`, `protocol`, or
`endpoint` differing from the operator's — a caller that could label the
operator's own MCP server in the catalog a user reads would be spoofing it, one
that could set its `kind` would choose how it is reached, and overwriting either
silently would leave the request and its response disagreeing about one source.
The bare-`NAME` allowlist form is the only `environment` a wire caller may
write, and it is additive only for names the operator did not configure: a
caller naming one the registry entry already carries is dropped, so the
operator's value is the one the source runs with and one variable never reaches
a child twice. A registry entry naming one variable twice is refused at
registration for the same reason. An attaching open may pin the descriptor it elected against, and the pin is
honoured as the profile states it: a `capability_revision` that is not the
endpoint's current one is refused `stale_capabilities` with `expected_revision`
and `current_revision`, while an open that carries none is evaluated against
current capabilities and answered with the revision it was admitted under. Both
clients pin — one extra request, on attaching opens only — because the validator
is stricter than the wire here and requires any optional-feature envelope to
cite the active descriptor. An open that attaches nothing is not probed and
keeps its own revision. A configured `id` resolves to the operator's source whatever the caller
claims about it, so an open that names an id alone gets a fuller descriptor back
than it sent, and never a different one.
Beside that, every route refuses a request bearing an `Origin` header, and the
routes that read a request body also require `Content-Type: application/json`.
The origin refusal wraps the whole mux rather than living in the routes that
parse an envelope, so it covers the ones that read no body — `close` — and the
ones not yet written.

Every served catalog carries the `capability_revision` it was served under, and
`capability_revision` is schema-required on `action.tools.list.response` as it
is on `models.response`: the catalog belongs to one descriptor snapshot, so a
caller caches it against that revision and discards it when
`capabilities.updated` reports another. It matters more here than on the models
route, because a session's catalog is a function of the descriptor *and* of the
sources that session attached under it. The hub checks what an adapter hands
back before it reaches a codec — the payload's scope must be the scope the
request named, in both directions, and the revision must be present — because a
codec labels the envelope with the session it addressed, so an unchecked payload
scope would survive into a response the clients reject. A mis-scoped catalog is
refused rather than relabelled; a nil tool list is repaired, because an absent
list and an empty one say the same thing and only one is legal on the wire.

[Decision 0008](decisions/0008-tool-sources.md) graduates the unit: Claude Code
serves the catalog at `degraded` from its per-turn `system/init` frame, and ACP
attaches at `native` onto `session/new`'s `mcpServers`. Control-layer-provided
tools are the separate `+control-tools` unit and are not graduated by it.
### Models catalog (`+models`)

A session publishes the models it can run, and is bound by the listing in both
directions: an id the catalog omits is refused `model_not_found` naming it, and
an id it lists is never reported missing. A catalog that accepts an unlisted
model, answers `model_not_found` for a listed one, or refuses an unlisted one
under a code the caller cannot act on is diagnosed the same way —
`model_not_in_catalog` — because each leaves a picker built on the listing
unable to trust it. A listed selection may still be refused for reasons that
are not about the model at all — a busy session, another control — and those
refusals are left alone.

The catalog belongs to the capability revision it was served under: a refresh
discards it, and within one revision it may not move without a
`capabilities.updated`. `GET /sessions/{id}/models` serves it over HTTP, the
`models` op over stdio, `Session.Models` and `session.models()` from the two
clients; the degraded opt-in travels with the query on every one of them.
[Decision 0006](decisions/0006-models-catalog.md) graduates the unit. OpenCode
advertises `models.list` `degraded` and serves the models a session is observed
to run — its server's own catalog routes have no pinned response shape at the
adapter's pin — which makes it the first native adapter to exercise the
degraded opt-in end to end. The reference adapter serves the fixed catalog its
model gate already enforces.

### Local daemon (`oap serve`)

`oap serve` exposes the adapter registry over HTTP + Server-Sent Events so any
client — not only Go hosts — can drive any OAP adapter. The daemon is a thin
HTTP+SSE codec (`serve/servehttp`) over the embeddable `serve` package; its
wire behavior is the contract the `client` package proves:

```sh
oap serve [--config examples/oap-serve.json] [--addr 127.0.0.1:6270]
```

Without `--config` the daemon serves the built-in memory reference adapter
only. The registry document maps names to in-repo adapter configurations
(`examples/oap-serve.json` shows every entry type); constructor requirements
(absolute working directories, provider settings) surface at startup. The
`environment` list of an entry is an explicit allowlist: a bare `NAME`
forwards the value the daemon itself carries (unset names are omitted) and
`NAME=value` passes through literally — ambient credentials are never
inherited by a child process unless their variable was listed.

The document's `tool_sources` map is the same allowlist idea for the MCP
sources a client may attach at session open: each entry names a `kind`, the
descriptor members the catalog publishes, and the `command`, `args`, and
`environment` the daemon supplies on the client's behalf. Its `environment`
takes the same form with one rule of its own: a bare `NAME` the daemon does not
carry fails at startup, naming the source and the variable, rather than being
omitted the way an adapter's is. An adapter's list forwards whatever of a
harness's variables the daemon happens to have; a tool source's names the
credentials of one executable the daemon itself launches, and dropping one
starts that MCP server without its token to fail later as though the server
were broken. Write `NAME=` if a name is meant to be optional. (The example
config lists `MCP_TOKEN` for its filesystem source, so export it or drop the
entry before starting with that document.) A `process` entry must carry a
`command`: it is the one kind the daemon supplies an executable for, so an
entry without one could never resolve, and the failure is reported when the
entry is registered — naming the source — rather than as a generic
`open_failed` on the first open that attaches it. Other kinds need none,
because nothing spawns them. A `kind` outside the protocol's five is refused
too, because no adapter can accept it and no client can name it. Both rules
belong to `Registry.RegisterToolSource`, which the config loader goes through,
so an embedding host registering entries programmatically meets the same checks
rather than weaker ones. A client attaches one by `id` and
nothing else — see "Tool sources" above for why the wire form is refused on
that route.

The daemon binds `127.0.0.1` by default and has no authentication: v0 is a
single-user local service, and pointing it at an external interface is
explicitly unsupported. On a loopback bind the daemon serves only requests
whose `Host` header names a loopback host, which closes the browser-borne
cross-origin and DNS-rebinding vectors against an unauthenticated local
service; binding a non-loopback `--addr` deliberately opts out of the
single-user trust model. Restarts kill every session — run child processes
are per-session and no adapter here survives a daemon restart — and no session
state persists across restarts. Session entries accumulate for the daemon's
lifetime (closed sessions stay listed with their final state); there is no
eviction in v0.

OAP operations exchange verbatim schema/v0.1 envelopes (rejected input gets a
correlated `error.response`; `GET /capabilities` responses cite a
daemon-minted correlation id a client can pair with its own request envelope):

| Endpoint | Operation |
| --- | --- |
| `POST /adapters/{name}/sessions` | `session.open.request` → `session.open.response` |
| `GET /adapters/{name}/capabilities` | `capabilities.response` (probed descriptor) |
| `POST /sessions/{id}/submit` | `session.message.submit.request` → admission response |
| `GET /sessions/{id}/events` | SSE stream of run-event envelopes |
| `POST /sessions/{id}/resolve` | `action.permission.resolve.request` or `user.input.resolve.request` → response |
| `POST /sessions/{id}/cancel` | `run.cancel.request` → `run.cancel.response` |
| `GET /sessions/{id}/state` | `session.state.response` |
| `GET /sessions/{id}/models` | `models.response` (repeatable `?allow_degraded=<key>`) |
| `POST /sessions/{id}/close` | Close (v0.1 defines no close envelope; returns 204) |
| `GET /adapters`, `GET /sessions` | daemon-management listings, plain JSON |

The daemon acts as participant `user`: interactive gates opened over a
session resolve with `responded_by: "user"`.

`GET /sessions/{id}/events` streams envelopes with `data:` carrying the
envelope JSON and `id:` carrying the envelope sequence, so an SSE
Last-Event-ID reconnect (or an explicit `?after=` cursor) maps directly onto
`Resume.AfterSequence` for the session's current run: the daemon drives the
adapter `Resume` and streams the replayed suffix, then live events, and ends
the stream at the run's terminal event. Two terminal signals are transport
framing, not envelopes: `event: oap-overflow` reports that the connection's
bounded buffer fell behind (`last_sequence` names the last sequence this
connection delivered; reconnect with a cursor after it), and
`event: oap-replay-gap` reports `adapter.ReplayGap` — the requested cursor is
no longer retained (`oldest_available`/`latest_available` bound what is;
reconnect with a cursor at or after `oldest_available - 1`). A stream also
ends when the client closes the connection; a stream that is open when the
session closes receives the events already in flight and then ends, and a
connection made to an already-closed session is refused with
`409 session_closed` rather than parking. One non-terminal signal opens the
stream rather than ending it: `event: oap-subscribed` is written first when the
subscription joins a session whose run has already emitted, and `joined_after`
names the last sequence it missed, so a host that subscribed separately can see
it did not start at the beginning and resubscribe with a cursor. The signal
reports a position inside the run the subscription is attached to, and nothing
about earlier runs: a subscription that begins at the start of its run gets no
such line, whether that is a compound open's, whose subscription begins before
the session has a run, or one that arrives between runs, after a second run is
admitted and before it emits. In that last case the subscriber did miss the
run before, and is not told so here — those events are another run's sequence
space, and a signal naming a run the stream is not carrying would be worse than
silence. Sequence numbers are per-run: a
connection that happens to span an immediate resubmit (a second run admitted
inside the settle window of the first) continues into the new run, and
clients keying on the envelope `run_id` see each run's own sequence space.
Because of that, a cursor without a run is ambiguous, and `run_id` says which
one it was cut from: `?after=7&run_id=run-1` replays run-1 whether or not it is
still the current run, and `?after=7` alone resolves onto whichever run is
current, which is right until a second one is admitted. A `run_id` the session
never had is `404 run_not_found`; one sent without a cursor is
`400 invalid_cursor`, because a live subscription is always the current run.

On SIGINT/SIGTERM the daemon stops accepting, terminates in-flight streams,
and closes every session inside a bounded window — active runs that refuse
Close are cancelled first — so child agent processes are settled rather than
orphaned.

### Subprocess embedding (`oap serve --stdio`)

A host that would rather spawn a child process than manage a port gets the
same surface over newline-delimited JSON on the process's own pipes:

```sh
oap serve --stdio [--config examples/oap-serve.json]
```

Spawning the process is the authorization, so there is no port, no TLS and no
`Host` allowlist. The registry's `environment` allowlist still governs adapter
credentials exactly as it does over HTTP, and a wire-supplied `command`,
`args` or literal `NAME=value` in a `tool_sources` attachment is refused here
too — a stdio peer is a separate process on the far side of a pipe, not an
in-process embedding of `serve.Hub`, so it gets the wire rule. `--stdio` and
`--addr` name two transports and are mutually exclusive.

**Stdout carries protocol lines and nothing else.** The banner, the adapter
list and every diagnostic go to stderr, so a host may parse stdout strictly.

Host → daemon lines are one JSON object each: `{"id":N,"op":"...", ...}`,
where `id` correlates the answer. It is a **signed 64-bit integer** — the host
picks the value, and negatives are fine, but a fractional number or one
outside the int64 range is a framing defect, not a bad request. So is an
unknown field, a repeated or case-aliased key, a missing `id`, a line over the
frame limit, or trailing data after the object. The daemon fails closed on all
of them; a JavaScript host minting ids above 2^53 should keep them inside
int64 and send them as integers.

| op | params | answers with |
| --- | --- | --- |
| `adapters` | — | the registry listing |
| `capabilities` | `adapter` | `capabilities.response` |
| `open` | `adapter`, `request` | `session.open.response` |
| `sessions` | — | the session listing |
| `state` | `session_id` | `session.state.response` |
| `models` | `session_id`, `allow_degraded_features` | `models.response` |
| `tools` | `session_id`, `allow_degraded_features` | `action.tools.list.response` |
| `submit` | `session_id`, `request` | `session.message.submit.response` |
| `resolve` | `session_id`, `request` | the matching resolve response |
| `cancel` | `session_id`, `request` | `run.cancel.response` |
| `close` | `session_id` | `null` |
| `events` | `session_id`, `after`, `run_id` | `null`, then the stream (below) |

`request` carries a verbatim schema/v0.1 request envelope — the same document
the corresponding HTTP route takes as its body, validated against the same
bundled schema. An op that takes a parameter it does not define is refused
`invalid_request`: the shape is checked on presence, so a supplied-but-null
parameter still counts as supplied.

Daemon → host lines are either responses, `{"id":N,"ok":true,"result":...}`
or `{"id":N,"ok":false,"error":{"code":...,"message":...}}`, carrying the
codes the HTTP routes answer with, or subscription lines, which carry
`"event"` instead of `"ok"`. Exactly one writer goroutine emits them, so every
line is atomic and no response is ever broken by interleaving. Four refusals
are this framing's own and have no HTTP counterpart: the line-shape pair
`invalid_request` and `unknown_op`; `response_too_large` for a result the
frame limit cannot carry; and `busy`.

`busy` is the one a host has to handle rather than fix. Ops are admitted
against a bound on how many run at once and how many request bytes they hold,
and a host that pipelines past it gets a correlated refusal instead of a
stalled pipe — the daemon answers rather than waiting, because waiting is what
would stop it reading the host's end at all. The message ends with `send this
request again`, and that is the whole recovery: the request was never served,
so resending it is safe. A host that pipelines deeply should be ready to
resend.

`events` acknowledges with `null` — the acknowledgement the SSE route gives by
starting a bodyless response — and the subscription's envelopes follow as
their own lines, tagged with the `events` request's `id` so overlapping
subscriptions on one session stay attributable. The acknowledgement is always
written before the first envelope.

| line | means |
| --- | --- |
| `"event":"oap-subscribed"` | the subscription joined its run already in progress; `joined_after` is the last sequence of that run it missed. Advisory, so a form too large to frame is retried without `session_id`, `run_id` and `message` and then dropped, never ending the subscription |
| `"event":"envelope"` | one event, with `sequence` repeated outside the envelope so a host can resume without decoding it |
| `"event":"oap-overflow"` | the consumer fell behind; `last_sequence` is where a cursor resumes |
| `"event":"oap-replay-gap"` | the `after` cursor is no longer retained; `oldest_available`/`latest_available` bound what is |
| `"event":"oap-session-closed"` | the session closed under the subscription |
| `"event":"oap-frame-limit"` | an envelope this framing cannot carry; `sequence` is where a fresh cursor resumes past it |
| `"event":"oap-stream-failed"` | the run's event stream failed; `run_id` and `sequence` are the last position delivered |

Every ending a host could not otherwise observe gets one of these, because
this framing has no end to observe: the SSE response body stops and the client
sees a closed connection, while the pipe here stays open and carries every
other subscription. Two endings need no line. A healthy stream ends at the
run's terminal envelope — `run.completed`, `run.failed` or `run.cancelled` —
which is delivered as an ordinary envelope line and is the marker, exactly as
the SSE response simply ends after it; and the daemon's own shutdown ends the
session the host is watching, which the host observes directly.

Ops are pipelined and each runs on its own worker, so responses to overlapping
requests may arrive in any order and the `id`, not the position, is what
correlates them. A host that needs one op to precede another waits for its
response, exactly as it would over HTTP: a `submit` sent before its `open` is
answered is refused `unknown_session` rather than silently queued.
`drafts/compound-open.md` proposes removing that constraint for the common
case.

`examples/oap-stdio-session.ndjson` is the host's half of a full session —
listings, open, subscription, an interactive run with both gates answered,
cursor replay, close. It is a script rather than something to `cat` into the
process, for the reason just given: each line follows the previous answer, and
the two `resolve` lines follow the gates they answer.
`TestStdioExampleSessionRuns` drives it against the built binary, so it cannot
drift from the surface it documents.

Shutdown is stdin EOF or SIGINT/SIGTERM: the daemon stops admitting, settles
the work it already admitted inside a bounded window, closes every session so
child agent processes are not orphaned, and exits zero. The session sweep runs
on every exit, including the failures below, so a child agent process is never
orphaned by one.

A non-zero exit means the session did not end cleanly, and stderr carries one
bounded diagnostic saying which:

| exit reason | means |
| --- | --- |
| a malformed line | the host's framing defect, naming the line number |
| the output could not be written | the host closed its end of stdout, or the write failed |
| requests were dropped unserved | the host ended the session while work it had sent was still unadmitted or unanswered — those requests were never served and may be sent again |
| shutdown stalled | a stage outlived its bounded window and was abandoned rather than waited on |

A host that classifies every non-zero exit as bad input will misdiagnose its
own closed pipe, or an adapter that outlived the shutdown window, as a
protocol error.

### Embedding the registry (`serve`)

The `serve` package is the transport-neutral core the daemon is built on: a
Go host that wants the full registry semantics in process — several adapters,
many concurrent sessions, per-session subscriptions with cursor replay —
embeds it directly with no HTTP hop. `serve/servehttp` (the daemon) is one
codec over this same hub.

```go
registry, _ := serve.DefaultRegistry()           // or serve.LoadRegistry(path, os.LookupEnv)
hub := serve.New(registry, serve.Options{})

session, _, _ := hub.Open(ctx, "memory", adapter.OpenRequest{SessionID: "demo"})
sub, _ := hub.Subscribe(ctx, session.ID())        // subscribe before submitting
session.Submit(ctx, protocol.MessageSubmitRequest{
	SessionID: session.ID(), Delivery: protocol.DeliveryAuto,
	Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}},
})
for {
	envelope, err := sub.Next()
	if err == io.EOF {
		break // the run reached its terminal event
	}
	if err != nil {
		break // fell behind: resume per the error's cursor
	}
	// resolve gates and collect content as events arrive
}
```

Subscriptions are the daemon's SSE connections without the socket: any number
per session, bounded per-subscription buffers, and the same typed terminal
signals — `*serve.OverflowError` carries the resume cursor (re-subscribe with
`serve.After(err.RunID, err.LastSequence)`), and an expired cursor fails `Subscribe` with
`*adapter.ReplayGap` naming what is still retained. `hub.Sessions(ctx)` lists
every session with its adapter and lifecycle status (closed entries stay
listed with their final state), and `hub.CloseSessions(ctx)` settles every
session for shutdown — in-process hosts own their lifetime, since the
restart-kills-sessions property of the daemon is a process boundary, not a
library one. The hub adds no protocol semantics of its own and never
validates envelopes; hosts forwarding untrusted input validate at their own
boundary, exactly as `servehttp` does against the bundled schema.

Pick the tier that fits: `adapter.Session` directly for one embedded session
(a single run stream with a single consumer, replay through `Resume`);
`serve` for multi-adapter, multi-session hosts in process; `oap serve` plus
`client` for out-of-process or non-Go consumers over HTTP + SSE.

### Go client (`client`)

The `client` package is the far-side conformance proof for that wire: a public
Go client that drives `oap serve` over HTTP + SSE, and the template later
clients (TypeScript) copy. It speaks verbatim schema/v0.1 envelopes, consumes
the event stream through a real `text/event-stream` parser, and resolves
interactive gates as participant `user` by default:

```go
c := client.New("127.0.0.1:6270")

session, _ := c.Open(ctx, "memory", "demo")
stream := session.Events(ctx)          // subscribe before submitting
session.Submit(ctx, protocol.MessageSubmitRequest{
	Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}},
	Delivery: protocol.DeliveryAuto,
})
for {
	envelope, err := stream.Next()
	if err == io.EOF {
		break // the run reached its terminal event
	}
	// resolve action.permission.requested / user.input.requested as they
	// arrive, or collect content.delta / run.completed envelopes
}
```

`Session.Events` resumes invisibly: when the SSE connection drops, the client
reconnects with the last observed sequence as the `Last-Event-ID` / `?after=`
cursor, the daemon replays the suffix, and the stream continues with no
duplicates — a repeated or skipped sequence within a run is surfaced as an
error, never silently accepted. `WithStrictResume` turns resume off and reports
the drop as a `DisconnectError` carrying the cursor. The daemon's terminal
signal events
surface as typed errors: `OverflowError` (reconnect with
`Session.EventsAfter(RunID, LastSequence)`) and `ReplayGapError` (the requested cursor
expired; `OldestAvailable`/`LatestAvailable` bound what is retained). Daemon
refusals arrive as `ServerError` with the correlated `error.response` code.

`WithHTTPClient` injects the underlying transport, `WithParticipant` overrides
the responder identity, and `WithEnvelopeValidation` turns on dev-mode
validation of every inbound envelope against the bundled schema (off by
default). The client logs nothing. See `example_test.go` for the canonical
lifecycle and the package documentation for the full surface.

### TypeScript client (`clients/ts`)

The TypeScript client mirrors the Go one — same wire surface, same typed
errors, same invisible resume — for non-Go hosts, with a zero-dependency
runtime on platform fetch and streams (Node 18+ baseline):

```ts
const client = dial('127.0.0.1:6270');
const session = await client.open('memory', { sessionId: 'demo' });
const events = session.events();          // subscribe before submitting
await events.ready;
await session.submit({ messages: [{ role: 'user', content: 'hi' }], delivery: 'auto' });
for await (const envelope of events) {
  // resolve gates, collect deltas, end at run.completed
}
```

See `clients/ts/README.md` for the full surface. npm publishing is
intentionally out of scope; the package is consumed from a checkout.

Research:

- [Harness interoperability study](research/harness-interoperability.md)
- [P0 protocol gaps from harness interoperability](research/p0-protocol-gaps.md)
- [Pinned Codex app-server mapping](research/codex-app-server-8d7cc24-mapping.md)
- [Pinned ACP v1 and Devin Desktop mapping](research/acp-v1.7.0-mapping.md)
- [Pinned Pi coding-agent mapping](research/pi-v0.85.1-mapping.md)
- [Pinned DeepSeek Harness mapping](research/deepseek-harness-47f9438-mapping.md)
- [Pinned Hermes agent mapping](research/hermes-v2026.8.31-mapping.md)
- [Pinned Claude Code CLI and Agent SDK mapping](research/claude-code-agent-sdk-2.1.263-mapping.md)
- [Z.ai China Coding Plan evidence matrix](research/zai-china-coding-plan-evidence.md)
- [Protocol feedback from eight adapter tranches](research/protocol-feedback-2026-09.md)

### Implementing OAP natively

Every adapter in this repository translates a harness *into* OAP. An endpoint
is the other direction: one agent loop that speaks OAP itself.

`drafts/endpoint-stdio.md` is the binding — OAP envelopes, one per line, over
stdin and stdout. It is deliberately narrower than `oap serve --stdio`, which
exposes a hub; an implementer should not have to build a registry, twelve ops
and multiplexed subscriptions to be conformant.

```sh
oap endpoint --adapter memory     # the reference endpoint: one adapter, raw envelopes
oap conformance                   # drive that reference endpoint and judge it
oap conformance --command "some-agent --oap" # drive somebody else's
```

The binding carries cursor replay as a transport control frame — the same
place the HTTP binding puts it, where `?after=` and `Last-Event-ID` are not
envelopes either.

`oap conformance` spawns the command as a process, drives a scripted session
over the binding, assembles every envelope it sent and received into a trace,
and hands that trace to the same validator `oap validate` uses. The runner
drives; the validator judges. Driving a process rather than linking a library
is what keeps the runner usable against an endpoint written in any language.

Decisions:

- [0001 — agent-control v0.1 executable core](decisions/0001-agent-control-v0.1-executable-core.md) (accepted)
- [0002 — admission before started](decisions/0002-admission-before-start.md) (accepted)
- [0003 — graduating staged control units](decisions/0003-staged-unit-graduation.md) (accepted)
- [0004 — extension packs](decisions/0004-extension-packs.md) (accepted)
- [0005 — run controls](decisions/0005-run-controls.md) (accepted)
- [0006 — models catalog](decisions/0006-models-catalog.md) (accepted)
- [0007 — queue delivery](decisions/0007-queue-delivery.md) (accepted)
- [0008 — tool sources](decisions/0008-tool-sources.md) (accepted)
- [0009 — compound open](decisions/0009-compound-open.md) (accepted)
- [0010 — terminal provenance](decisions/0010-terminal-provenance.md) (accepted)
- [0011 — control-layer-provided tools](decisions/0011-control-layer-provided-tools.md) (proposed — awaiting adapter evidence)

Decision 0003 defines what `accepted` means and what moves a record from
proposed to accepted.

Provider compatibility is tested independently from harness conformance. Inspect
the credential-free China Coding Plan presets with:

```sh
go run ./go/cmd/oap providers zai-cn
```

Ordinary tests use fake credentials and loopback provider servers. Credentialed
provider evidence is separately and explicitly gated as documented in the matrix;
credential presence alone never enables network traffic.

Pi real-process checks are also explicitly opt-in and skipped by ordinary CI.
Provide an absolute Pi v0.85.1 executable in `OAP_PI_BIN`, then set
`OAP_PI_SMOKE=1` for the credential-free readiness check or
`OAP_PI_INTEGRATION=1` for the loopback-provider path. The executable's reported
semver is runtime-version evidence only; it does not prove the source commit.
Set `OAP_PI_SHA256` to the expected 64-character artifact digest when exact
artifact provenance is required. The gate never downloads an executable and
passes no ambient credentials to it.

DeepSeek Harness real-process checks follow the same opt-in gate. Provide an
absolute runtime built from the pinned source commit in
`OAP_DEEPSEEK_HARNESS_BIN` — the build emits
`deepseek-harness-sdk-runtime-linux-x64` from the pinned release — then set
`OAP_DEEPSEEK_HARNESS_SMOKE=1` for the credential-free initialize/shutdown
check or `OAP_DEEPSEEK_HARNESS_INTEGRATION=1` for the loopback-provider path.
The gates never download a runtime and pass no ambient credentials. The
runtime boots the shipped `sdk` profile (`--profile sdk`) against an isolated
`DSH_HOME`; the loopback gate redirects the stock deepseek provider with
`DEEPSEEK_BASE_URL`. The wire `serverInfo` version and any release text are
runtime-version evidence only; the pinned source commit and tree in the
mapping ledger remain the provenance. Set `OAP_DEEPSEEK_HARNESS_SHA256` to the
expected 64-character artifact digest when exact artifact provenance is
required.

Hermes agent real-process checks follow the same opt-in gate. Provide an
absolute python interpreter in `OAP_HERMES_BIN` (able to import the pinned
checkout's dependencies) and the pinned hermes-agent v2026.8.31 checkout in
`OAP_HERMES_ROOT` (the gateway's cwd), then set `OAP_HERMES_SMOKE=1` for the
credential-free ready/session.create/EOF-teardown check or
`OAP_HERMES_INTEGRATION=1` for the loopback-provider path (streaming OpenAI
chat completions against an in-process mock, test-owned key only). The gates
never download anything and pass no ambient credentials; teardown evidence is
stdin EOF, matching the pinned gateway, which has no shutdown RPC. Set
`OAP_HERMES_SHA256` to the expected 64-character interpreter digest when exact
artifact provenance is required.

Claude Code real-process checks follow the same opt-in gate. Provide an
absolute path to the pinned claude 2.1.263 binary in `OAP_CLAUDE_BIN`, then
set `OAP_CLAUDE_SMOKE=1` for the credential-free spawn/initialize/EOF-teardown
check or `OAP_CLAUDE_INTEGRATION=1` for the loopback-provider path (streaming
Anthropic Messages against an in-process mock, test-owned key only;
structural request assertions only, per the mapping pin). The gates never
download anything and pass no ambient credentials; readiness is the
initialize control exchange, and teardown evidence is stdin EOF. Set
`OAP_CLAUDE_SHA256` to the expected 64-character binary digest when exact
artifact provenance is required.

ACP real-process checks follow the same opt-in gate, driven against an
independent open-source ACP agent rather than a Devin product. Provide an
absolute `docker-agent` binary built from the pinned docker/cagent release
(Apache-2.0, tag `v1.138.0`) in `OAP_ACP_BIN`, then set `OAP_ACP_SMOKE=1` for
the credential-free `initialize`/`session/new`/teardown check or
`OAP_ACP_INTEGRATION=1` for one prompt through the production adapter against
an in-process loopback chat-completions mock. The generated agent file points
`base_url` at the loopback endpoint; the checked-in real-provider examples are
never reused. Building cagent needs Go 1.27. Set `OAP_ACP_SHA256` to the
expected 64-character artifact digest when exact artifact provenance is
required. Devin CLI also speaks ACP (`devin acp`) but is proprietary and
prebuilt-only, so it cannot be pinned as evidence.

### Real-process gate coverage

CI runs `gofmt`, `go vet`, the full and race suites, and `oap check` on every
push and pull request. The real-process gates above are **not** run in CI:
they need pinned third-party binaries, are skip-by-default, and require an
explicit opt-in variable plus an absolute binary path (and optionally a
64-hex digest). Credential presence alone never enables them, and they never
download anything.

| Adapter | Gate variables | CI |
| --- | --- | --- |
| Codex app-server | `OAP_CODEX_INTEGRATION`, `_BIN`, `_COMMIT`, `_SHA256` | skipped |
| OpenCode | `OAP_OPENCODE_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| pi | `OAP_PI_SMOKE` / `OAP_PI_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| DeepSeek Harness | `OAP_DEEPSEEK_HARNESS_SMOKE` / `_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| Hermes | `OAP_HERMES_SMOKE` / `OAP_HERMES_INTEGRATION`, `_BIN`, `_ROOT`, `_SHA256` | skipped |
| Claude Code | `OAP_CLAUDE_SMOKE` / `OAP_CLAUDE_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| ACP (docker/cagent) | `OAP_ACP_SMOKE` / `OAP_ACP_INTEGRATION`, `_BIN`, `_SHA256` | skipped |

Each adapter additionally has a hermetic corpus that runs in CI: it decodes
sanitized native frames through the production codec and drives the
production reducer, so codec and reducer regressions are caught without any
external process.

The repository is dedicated under CC0-1.0 so any presentation layer, control
layer, agent loop, model provider, tool executor, resource provider, tool
source, or SDK adapter can implement the protocol without project-specific
licensing friction.
