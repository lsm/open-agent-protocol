# Decision 0011: Control-Layer-Provided Tools

Status: accepted 2026-09-17. Assessed against Decision 0003 earlier the same
day and **held**, correctly: steps 2 and 4 had landed — 58 `control-tools`
fixtures, the validator rules, this record — and steps 1 and 3 had not, so the
wire shape was prose and authored files agreeing with each other. That hold is
discharged by the work that carries this status. Step 1: the memory reference
adapter provisions at open and executes a control-owned call. Step 3: the Makai
adapter executes the unit through its production codec and reducer, with the
`tool-bridge-roundtrip` corpus case — the one this record names as required and
the corpus never contained — pinned to its ledger commit. Nothing pending
remains inside this record
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `control-tools` (claim term `+control-tools`; plan sub-unit T3c)
Amends: nothing. Extends
[Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0008](0008-tool-sources.md) without amending either, and discharges
the named deferral in Decision 0008's status line — "T3c control-layer tools
stays staged as a named deferral with its own future decision" — by being that
decision
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T3c. Control-layer-provided tools"

## Context

OAP has no caller-executed tool path. `protocol/envelope.go:50-58` carries six
observational `action.call.*` types and no resolve pair, so an endpoint can
report that a tool was called and can report how it went, and a control layer
can do neither. There is no envelope on which to say "I will run this", no
envelope on which to say "here is the result", and nowhere for an adapter to
put the result if it had one.

That is not a gap in a capability nobody reaches, but the way it is unreachable
is the point. This repository's Makai adapter declares an empty `tools` array
on every `agent_message`, so the harness is offered nothing to hand back and
never emits `tool_execute`; the refusal waiting behind it —
`adapter/makai/session.go` failing the run with the adapter-minted reason
`makai_tool_executor_unavailable` — is a path no run takes. Makai's own native
OAP mode declares empty in both payloads for the same reason.

Both integrations suppress the capability at the source rather than meeting it
in flight, and they suppress it *because* OAP has nowhere to put a
caller-executed tool. That is stronger evidence than a refusal in a live trace
would be: a refusal proves an endpoint hit a wall once, while two independent
integrations declining to offer the feature at all shows the wall is load-
bearing enough to design around. Anything a Makai consumer hosts itself is
unavailable over OAP today, and that is the last blocker on an external
maintainer's decision about implementing OAP natively.

**Correction, 2026-09-17.** This paragraph first said the adapter "reaches it
on every run" and cited the failure as observed behaviour. It reaches it on no
run, for the reason now stated: `"tools": []any{}` was marshalled onto every
message before this unit landed, so the frame that would trigger the refusal
never arrived. The gap the unit closes is real and the evidence for it was
wrong. `makai_tool_executor_unavailable` is also this repository's identifier
rather than Makai's, and appears nowhere in the Makai tree — a reader grepping
for it there finds nothing.

Decision 0008 graduated the other two thirds of the tool-sources work: a
catalog that says where its tools come from (T3a), and attachment of sources at
session open (T3b). Both describe execution the harness performs, and both are
accepted. What it did not graduate it deferred by name, to "its own future
decision"; this is that decision, and it graduates the third third: tools the
control layer supplies and executes itself.

The core draft already anticipates it in the terms this decision uses.
`execution_owner` is required on every catalog entry and every `action.call.*`
payload precisely so the mixed case is expressible, and the draft states the
rule this decision makes executable: "a call claiming an owner the catalog does
not give that tool is not a call the endpoint should honor"
(`drafts/agent-control-core.md`). What was missing was not the field but
everything the control layer needed to answer with.

## Decisions

### Tools are provided at session open, and the open is provisioned whole

`session.open.request` gains `tools: [ToolDefinition]`, the definitions the
control layer supplies for the session's lifetime, gated on the new capability
key `action.tools.provide`. An endpoint that cannot provision every definition
it is given refuses the open with `unsupported_feature`
(`details.feature: "action.tools.provide"`, `details.reason: "unsatisfiable"`)
rather than accepting a subset: a caller that provided five tools and got three
has a run whose behaviour depends on which two the endpoint silently dropped.

Session open is the whole provisioning surface this unit admits. Per-submit
provisioning — Makai supplies `tools` on every `agent_start` — is deferred, and
the Evidence section records the check that says it can be.

A provided tool joins the session's catalog next to the harness's own. It is
listed by `action.tools.list.response` with the opener as `execution_owner`,
and a submit's `tool_choice` governs it like any other catalog entry. Because
provisioning is for the session's lifetime and all-or-nothing, a provided tool
never drops out of a later catalog and never changes the members it was
supplied with — the same lifetime rule Decision 0008 gives an attached source,
enforced the same way (`catalog_mismatch`), and rerun against every
revision-changing descriptor so a refresh cannot shadow a provided tool with a
native one or stop declaring a source a provided tool references.

### Ownership is checked where it is stated, not where it fails

Each supplied definition's `execution_owner` must be the opening participant:
in a trace, the participant `protocol.initialize.request` declared. Envelopes
carry no sender field, so there is no per-envelope identity to compare against
and none is invented; the declared control participant is the comparison, and a
trace that never declared one stands the rule down rather than guessing. An
open supplying a tool owned by anyone else is refused before the session exists
(`unsupported_feature`, `details.reason: "unsatisfiable"`, `details.tool`),
because otherwise the endpoint has admitted a tool it will later have to
classify as harness-owned or route to a participant that never provided it.

The same refusal answers a supplied tool whose `source` names a source neither
the descriptor declares nor the same open attaches (`details.source`), for the
same reason: a dangling source cannot be attributed or routed, and the defect
is visible in the request.

Both are adapter rules that the validator enforces on the *admission* and on
the *refusal*, never on the request. The typed refusal is conforming and
produces no diagnostic; a refusal under another code, or without the detail
that names the offending entry, is `wrong_tool_owner` or
`unmatched_tool_source` on the `error.response`; an open the adapter admitted
despite either condition is the same diagnostic on the
`session.open.response`. A refusal that does not say which entry to change
tells the caller only that something was wrong.

`wrong_tool_owner` also fires on an `action.call.requested` whose
`execution_owner` differs from the owner the catalog in force records for that
tool, in either direction. A harness-owned tool routed to the control
participant leaves the run waiting for a resolution nobody owes; a provided
tool routed to the harness executes something the control layer was supposed
to. The pre-existing check that an owner does not change mid-lifecycle cannot
see either, because both are consistent from the first event onwards.

### A call to a provided tool is an interaction

`action.call.requested` for a control-owned tool carries `interaction_id`,
`requested_by` (the agent), `responded_by` and `execution_owner` (the control
participant). It is a full interaction under Decision 0001, so every rule
already written for permission gates and input prompts governs it: only the
declared responder resolves it, it resolves once, and a run cannot terminate
with it pending. Omitting `interaction_id` or `responded_by` is
`illegal_tool_transition` — an interaction the endpoint opens without saying
which one it is, or who may answer it, is one no control layer can resolve.

The call stays `requested` until execution is evidenced. `action.call.started`
is emitted when the control participant acknowledges, or — when it resolves
without having acknowledged — immediately before the terminal derived from that
resolution, because the result is itself the evidence. `started` is never
emitted on the endpoint's own initiative, so durations and cancellation keep
the meaning the core gives them, and the validator's transition table is
unchanged.

Run cancel closes pending control-owned calls with `action.call.cancelled`
before the run terminal, as the memory adapter already does for permission
gates; an unacknowledged call goes from `requested` to `cancelled`, which the
transition table already permits. Deadlines remain deferred (PF-2); a
harness-side timeout settles the call as `cancelled`, acknowledged or not,
with the harness's code as the reason.

**Correction, 2026-09-17.** This paragraph first said an acknowledged call
times out as `failed`, and that sentence was never satisfiable. The rule three
sections below requires `action.call.failed` to derive from an accepted
`error`-arm resolution; an acknowledgement is not one, and
`validation/controltools.go` enforces it, so a `failed` terminal on that path
is `illegal_tool_transition` on every trace that carries it. Nothing ever
emitted it, because nothing could emit it and pass. `cancelled` is what the
transition table admits from both `requested` and `started` with no resolution
behind it, and it is also the truer statement: the call was not resolved.

The correction changes no wire behaviour and invalidates no trace — it replaces
a sentence describing an unreachable state with one describing the reachable
one, and the executable rule was always the other section's.

### The resolution is a request/response pair with three arms

`action.call.resolve.request` and `action.call.resolve.response` are new
session- and run-scoped envelope types, mirroring the permission resolve pair.

The request is
`{ interaction_id, session_id, run_id, tool_call_id, requested_by,
responded_by, started? | result? | error? }` with exactly one of the three
arms. `started` is an empty object: an acknowledgement, not a resolution. It
may appear at most once and only before the resolution.

The response is
`{ interaction_id, session_id, run_id, tool_call_id, accepted, reason?,
details? }`. `reason` is present exactly when `accepted` is false; `details` is
present exactly when `reason` is `already_resolved`, and carries
`settlement_id`, the envelope id of the settlement the refusal points at.

The detail member is declared rather than left to a free-form object. Over HTTP
the refusal arrives on the POST body and the settlement travels the event
stream, so a client can read the refusal first and, with nothing in it,
conclude a valid rejection was arbitrary. `settlement_id` lets it wait for, or
look up, the event that justifies the refusal — and a field the validator and
the client recovery path both depend on cannot be an assumption, so it is on
`protocol.ActionCallResolveResponse` and in the schema. The validator requires
the id to name a settlement the trace actually carries for that interaction, so
it cannot be invented.

### The refusal reasons are ranked, and the endpoint reports the highest

The five reasons are `unknown_interaction`, `wrong_responder`,
`already_resolved`, `repeated_acknowledgement`, `late_acknowledgement`, in that
order of precedence.

A request can satisfy several at once — a foreign responder sending a second
acknowledgement is both `wrong_responder` and `repeated_acknowledgement`, a
foreign result after settlement both `wrong_responder` and `already_resolved` —
and one response carries one reason. So the adapter reports, and the validator
requires, the highest the request satisfies.

The order asks what the sender most needs to know. Whether the interaction
exists comes first, because nothing else is answerable without it. Then whether
this sender may speak for it at all — a foreign responder's request is refused
for being foreign however the interaction stands, since the state of a call it
does not own is not its business, and telling it otherwise would leak that
state. Only then how far the call has progressed, most advanced first.

Each reason names a condition the validator can observe, which is what makes a
refusal checkable rather than a free-form excuse, and each is reachable as the
highest one, which is what stops a name in the enumeration from being
decoration:

- `unknown_interaction`: no such pending interaction on the run.
- `wrong_responder`: the sender is not the interaction's declared responder.
- `already_resolved`: the call is resolved — a terminal the trace carries,
  whether the endpoint derived it or a harness-side timeout produced it, or,
  for a `result` or `error` arm, a resolution the endpoint has already
  accepted.
- `repeated_acknowledgement`: the arm is `started` and one was already
  accepted.
- `late_acknowledgement`: the arm is `started`, arriving after the
  participant's own `result` or `error` was accepted but before the terminal
  derived from it was published.

The last two are both "this acknowledgement can no longer be accepted", and the
repeat is the more specific answer, so it outranks the other where both hold —
which is the sense in which the acknowledgement reasons are ordered, while
`already_resolved` outranks them both by being the more advanced state.

`settlement_id` belongs to `already_resolved` and to no other reason because
`already_resolved` is the only one with a settlement to point at, and *both*
halves of its condition supply one. Where a terminal exists, that is the
settlement. Where the call is resolved but its terminal has not been published
yet, the accepted `action.call.resolve.response` is: the acceptance is what
settled the call, the trace carries it, and it is correlated by `in_reply_to`
to a request the resolver itself sent, so a resolver reading the refusal can
place it.

That second half is not a convenience. Without it the window between an
accepted resolution and its terminal has *no conforming refusal at all*: the
reason is required, the reason requires an id, and the only envelope that could
have supplied one has not been published. That window is precisely the
result-and-retry interleaving `request_id` exists for — a resolution whose
response was lost and the retry that follows it — so it is the last window that
may be left without an answer. Fixture
`control-call-overlapping-resolutions` is the conforming refusal there, and
`control-call-ack-after-settlement` the one for the other half.

Two properties are asserted of the ladder as a whole, because neither is
self-evident and the enumeration has already failed both once. Every reason
must be reachable *as the highest*, or the vocabulary carries a name nothing
can produce. And every reason must have a conforming refusal in every window it
can fire, or an endpoint is required to report a reason it cannot legally
report. The windows are enumerated beside the ladder in
`validation/controltools.go`, and each has a fixture.

An empty set means the request is valid, and a valid request must be accepted.
Without that rule an endpoint could refuse the one correct resolution under any
reason at all, emit nothing, and let a later cancellation settle the call and
the run so the trace passed. A refusal of a valid resolution is
`unmatched_interaction` on the response; a refusal naming a condition other
than the highest one present is diagnosed as the condition actually present —
`wrong_interaction_responder` for a foreign sender, `duplicate_interaction` for
a settled or already-acknowledged call, `unmatched_interaction` otherwise.
Accepting a request one of the five conditions forbids earns the same
diagnostic, so "one resolution", "at most one acknowledgement", and "no
acknowledgement once resolved" are enforced at the response rather than only at
the events that follow.

### A terminal must derive from an accepted resolution, and carry what it said

`action.call.started` for a control-owned call must be preceded by an
`action.call.resolve.response` with `accepted: true` for its interaction,
answering either arm. Acceptance is tracked per arm: `action.call.completed`
must derive from an accepted `result` and `action.call.failed` from an accepted
`error`, so a terminal following a rejected resolution is
`illegal_tool_transition` even when an earlier acknowledgement was accepted. A
rejected exchange evidences nothing.

The accepted payload is stored with the acceptance, and the terminal must carry
it: a completion's `result` must equal the accepted `result` and a failure's
`error` the accepted `error`, compared as canonical JSON. Otherwise the new
diagnostic `resolution_payload_mismatch`. The authorization and the payload are
separate facts, and without this an adapter that forwarded something other than
the participant's actual outcome to the harness would pass conformance with an
authorized terminal that says what nobody said.

A resolve-derived `action.call.started` or terminal carries `request_id`, the
envelope id of the `action.call.resolve.request` it came from. Over HTTP the
resolve response is a POST body and the event it releases travels the SSE
connection, so a client must hold the event until the call that authorized it
returns — and `tool_call_id` cannot say which call that was, because two
resolutions of one call can be outstanding at once, a result and its retry. A
client holding on the call alone would either release on the retry's refusal
before the original's acceptance arrived, or hold forever if the request it
happened to pair the event with never returned. The validator requires
`request_id` to name a resolve request the trace carries for that interaction,
so the field cannot be omitted or invented.

### The capability discloses its limits, or it promises nothing

A native name shape, a schema dialect, a cardinality ceiling — any of these can
make a well-formed `tools` array unprovisionable, and an adapter permitted to
say so about any array could advertise `action.tools.provide`, refuse every
array it is ever given, and pass conformance while honouring nothing.

So a constraint must be *advertised to be exercised*. The feature's
`FeatureSupport.limits` declares the ones the adapter actually has —
`max_tools`, a `name_pattern`, the accepted `schema_dialect` — and refusing a
protocol-valid array is conforming only where it violates a declared limit.
Refusing one that satisfies every advertised limit is
`undisclosed_provide_limit`, the shape T2 uses for an undisclosed queue bound
and Decision 0008 for an undisclosed attachment limit, and for the same reason:
the refusal is itself the evidence that a constraint exists which the caller
was never told about.

The qualifier is load-bearing. An array carrying a foreign `execution_owner`, a
duplicated tool name, or a `source` that resolves to nothing is refused under
the rules above, which name the defect and validate the refusal, and those
refusals stay conforming however generous the limits are. What this rule
reaches is the array with no defect any rule names. A disclosed dialect binds
only a definition that declares a different one: a schema naming no `$schema`
elects the endpoint's, so an endpoint cannot disclose a dialect and then refuse
every array that did not repeat it.

### Recovery reads two lists, because one cannot answer the question

`active_runs[]` entries' `pending_interactions` (T2, where it lists the run's
unresolved permission and user-input interactions) extends to control-owned
calls, so the list names the run's unresolved interactions of every kind.

That list is the state surface a resolver that lost its resolve response reads,
and for that it has to distinguish one case the bare list cannot. For a
`result` or `error` resolution, absence is the answer: an interaction absent
from the list was resolved, one still present was not, so a rejected resolution
is re-sent and an accepted one is not. For a `started` acknowledgement both
outcomes leave the interaction present — an accepted `started` does not settle
the call, and a rejected request changes nothing — so presence alone cannot say
whether the acknowledgement landed. A resolver that guessed would either resend
an acknowledgement, be refused `already_resolved`, and misread that as the call
having settled, or never send one the harness is still waiting for.

Each entry therefore also carries `acknowledged_interactions`, the subset of
its `pending_interactions` whose `started` acknowledgement the endpoint has
accepted. Recovery reads both: present and acknowledged means the
acknowledgement landed and only the result is owed; present and unacknowledged
means the acknowledgement is owed, or the original was refused, which the same
resend now settles; absent means resolved.

The validator holds the new member to the same standard as the list it
subsets. Every id in it must be one the entry lists as pending and must have an
accepted acknowledgement the trace carries, and an acknowledged call missing
from it is `session_state_mismatch`. Acknowledgement is ordered by the trace
rather than by the run's sequence domain, because a resolve response consumes
no sequence and so has no position in it; what `as_of_sequence` anchors is the
pending set, and the acknowledged subset is read at the snapshot itself.

### A recovered call is judged on what the trace can still see

A reattach names a control-owned call as pending and says nothing else about
it. Which resolution authorized it, what that resolution stated, which request
it named, whether it was acknowledged before the cursor — all of it is behind
the disconnect, and no envelope for any of it will arrive. Every check above
that reads one of those facts therefore stands down, exactly as the permission
and user-input rules stand down for a recovered gate.

What does not stand down is what this trace can still see. That the call
terminated here, and therefore left the pending set here, are facts of this
trace whatever is unknown about its history — the same two facts Decision 0001's
recovery rule already keeps for a recovered permission gate. Dropping them
would convict the one endpoint that answered the reattach honestly: its run
would terminate with an interaction the validator still believes pending, and
its truthful post-resolution snapshot would be judged as omitting one.

An acknowledgement accepted *after* the reattach is the same kind of fact, and
is kept on the same terms. It is what `acknowledged_interactions` projects, so
an entry beside it must report it; one accepted before the cursor stays
unknown and stays unrequired, and the two are distinguished by whether this
trace saw the acceptance rather than by whether the interaction is recovered.

Which of these an event is allowed to settle is read from the event, not from
the interaction: the `execution_owner` on the call says whether the control
layer owns it. A recovered interaction cannot say — its kind is among the
things behind the cursor — and a recovered harness-owned call is settled by its
own lifecycle and its gate by the permission resolution, neither of which is
this unit's to settle.

## Evidence

Makai's `tool_execute`/`tool_result` bridge is exactly this boundary. The
adapter's native codec already decodes both frames and every `agent_message`
already carries a `tools` list; what it lacked was anywhere to route the
request, so it failed the run instead.

It no longer does. The adapter provisions at open, writes the provided
definitions onto every `agent_message`, turns a `tool_execute` naming one of
them into a control-owned interaction, and writes the participant's answer
back as the `tool_result` the harness is waiting for before publishing the
derived terminal. `tool-bridge-roundtrip` — the case the ledger has named as
required since the first pin and the corpus never contained — now exists,
pinned to the same commit as every other Makai case, and it asserts both
halves: the ranked response the resolver reads and the native frame the
harness receives. Pinning only the OAP side would pass an adapter that emitted
a conforming terminal and told makai nothing.

`makai_tool_executor_unavailable` — this repository's identifier for the
refusal, not one Makai mints — survives, narrowed to what it now names: a
`tool_execute` for a tool the session never provided. That frame still has no
owner to route to, and this unit gave the adapter a place for the frames it
can route rather than for every frame.

It is also reachable for the first time. The adapter now writes the provided
catalog onto every `agent_message` instead of an empty array, so the harness
can ask, which means the refusal and the round trip are both paths a run can
actually take.

The reference execution is the memory adapter, which provisions under three
disclosed limits — a ceiling, a name pattern and a schema dialect — and calls
a provided tool in place of its own scripted one when a session supplies a
catalog. A session that provides nothing runs exactly the script it always
did, which is what makes the unit inert where it is not elected.

The per-submit deferral was checked against the Makai maintainers' own model
rather than assumed. Their finding:

> Every makai consumer writes `tools` into both `config_json` and
> `message_json` from a single source and always emits the key. Their
> resolution rule is `message.tools orelse config.tools`, so the message always
> shadows the config, and the session-scoped field has never been read by any
> consumer.

That is what makes session-open provisioning sufficient for Makai: the
per-message field is always present and always identical to the session-scoped
one, so a session-scoped provisioning that fills both says exactly what every
existing consumer already says.

Its limit belongs here too, or a future reader mistakes an incidental property
of their clients for a protocol conclusion:

> Session-open tools are sufficient for makai while makai owns every consumer.
> Their host already accepts repeated `agent_message`; only their SDKs decline
> to use it. A persistent-session API with steering reopens per-submit.

So the deferral is a statement about the current consumer population and not
about the protocol. The moment a Makai session can be steered by a caller that
does not own the SDK, `message.tools` stops being a copy of `config.tools` and
per-submit provisioning has to be designed rather than deferred. This decision
does not pre-empt that design; it records why it is not needed yet and what
would make it needed.

The other two candidate endpoints follow the same boundary. Claude's
`sdkMcpServers` with `mcp_message` reverse control, and ACP's reverse
filesystem and terminal calls, are both a harness asking the client to execute
something and both need the resolve pair this decision adds. Two pinned ledgers
already say their adapters will refuse the key under the ordinary capability
gate: DeepSeek has no reverse interaction channel on the selected wire
(`research/deepseek-harness-47f9438-mapping.md:327`) and pi owns tool execution
with its interaction extensions disabled
(`research/pi-v0.85.1-mapping.md:250-253`).

The corpus is the rest of the evidence, and it is written as an enumeration of
failure modes rather than a happy path with decoration. The ladder is itself
such an enumeration — a tool request answered late, answered twice, answered
after settlement, answered by a participant that does not own it, and answered
against an interaction that does not exist — and each has a fixture, in both
directions where the wire admits two: the endpoint that got it wrong, and the
endpoint that got it right and must not be diagnosed for it.

## Consequences

An endpoint can now be asked to run a tool it does not own, and a control layer
can answer. The Makai adapter's `makai_tool_executor_unavailable` is an adapter
decision rather than a protocol limit: the frames it refused have a place to
go, and the adapter graduated the unit by adding the `tool-bridge-roundtrip`
corpus case and advertising `action.tools.provide` at capability revision
`makai-agent-67ad514-oap-v2`.

The `+tools` unit's meaning is untouched. `action.tools.execute` keeps the
meaning that unit gives it — normalized harness-side execution — so an old
client reading a new descriptor and a new client reading an old one both
interpret that key exactly as they do today. Only `action.tools.provide` gates
control-owned execution, and an endpoint that does not advertise it behaves as
it always has.

Three diagnostics are added: `wrong_tool_owner`, `undisclosed_provide_limit`,
and `resolution_payload_mismatch`. The first two are ownership and disclosure
faults visible in a single envelope; the third is the only one that compares
two envelopes' payloads, and it exists because the alternative is a conformant
trace in which the endpoint told the harness something the control layer never
said.

Two envelope types are added, and the wire's type set grows for the first time
since the models catalog. Every existing trace stays valid: the new members on
`session.open.request`, `active_runs[]`, and the three resolve-derived
`action.call.*` events are optional, and an endpoint that emits none of them is
judged exactly as before.

The interaction vocabulary now has three kinds rather than two. That is a
widening of Decision 0001's contract's surface, not of its rules: a call
interaction is resolved once, by its declared responder, and never outlives its
run, and the checks that enforce those are the same ones permission gates and
input prompts already pass through.

## What this unit does not admit

Per-submit tool provisioning. A submit cannot add, remove, or replace the
session's provided tools; the Evidence section records both why that is
sufficient today and what would make it insufficient.

Runtime provisioning changes of any other kind: no attach, no detach, no
replacement of a definition mid-session. Provisioning is fixed at open for the
session's lifetime, which is what lets the lifetime catalog rule be stated at
all.

Deadlines. A control layer that never answers blocks the call until the run is
cancelled or the harness times out, and neither the wire nor the validator says
how long an endpoint should wait. PF-2 keeps this deferred.

An MCP client inside the daemon. The plan's `serve/mcpconnect` is designed
against this unit and is not part of it; the hub adds no wire vocabulary and
hosting an MCP client elsewhere does not make an adapter without a reverse
interaction channel a candidate.

The daemon and client surfaces the plan states for this unit: the
pre-validation `execution_owner` projection on
`POST /adapters/{name}/sessions`, `session.open.response.participant_id`, the
`adapter.ErrResolutionRefused` sentinel, the publication gate across `Resolve`
and `Cancel`, and `Session.ResolveToolCall` in the two clients. They are the
binding of this wire rather than the wire, and they are deliberately a separate
change; nothing in this decision is contingent on them, and nothing in them may
contradict it.

A second wire identity for the hub. Ownership stays an internal provisioning
boundary: whatever provisioned a tool, the adapter sees the declared control
participant as its `execution_owner`, which is what makes the ownership rule
above checkable with the identities the wire already carries.
