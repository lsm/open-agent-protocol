# Decision 0008: Tool Sources

Status: proposed
Date: 2026-09-16
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `tool-sources` (claim term `+tool-sources`; plan sub-units T3a and T3b)
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T3. Tool sources and control-layer tools"

## Context

A v0.1 catalog is a flat list of tool definitions. It says what an endpoint can
call and nothing about where any of it comes from, so a consumer that wants to
show which MCP server a call went to has two options: parse the server out of a
name the harness happened to namespace, or not show it. Both are guesses about
a convention the protocol never stated, and neither survives a harness that
namespaces differently or not at all.

The endpoints already have the information. Claude Code's per-turn
`system/init` frame carries its tool list and its MCP server list side by side
(`research/claude-code-agent-sdk-2.1.263-mapping.md:264`), and ACP's
`session/new` takes the MCP server array as a required parameter that the
adapter has been filling from its own Go configuration ever since the live gate
found it sending `null`
(`research/acp-v1.7.0-mapping.md:553-557`). What was missing was any wire on
which to say it: no source shape, no attribution on a tool or on a call, and no
way for a control layer to attach a source the operator configured.

Two smaller faults came with that. The catalog exchange was gated on
`feature("tools")`, whose aliases resolve to `action.tools` and never to
`action.tools.list`, so an endpoint that advertised only the catalog key would
have had its own catalog diagnosed as an unavailable capability — while an
endpoint that advertised `action.tools` for lifecycle observation alone, as ACP
and pi do while stating outright that they expose no portable catalog, would
have passed a gate it never claimed. And `session.open.request` had no way to
carry anything: `ToolDefinition`'s `source` would have had nothing to resolve
against on a session that attached one.

This decision graduates the catalog with sources and attachment at session
open. It is the third unit under Decision 0003's gate.

## Decisions

### An adapter that does not attach says so

`OpenRequest.ToolSources` is a field every adapter written before this unit
ignores, which makes silence the default failure: such an adapter returns a
successful session having dropped the sources the caller asked for, and the
caller cannot tell that session from one that attached them. That is precisely
the outcome the fail-closed discipline exists to prevent, and it is not a
refusal anyone can act on.

`adapter.RefuseUnadvertisedToolSources` is the shared form, on the same
footing as `adapter.RefuseUnadvertisedControls`: every adapter runs it in
`Open`, before any native write and before a process starts, and the seven that
advertise no attachment refuse with `unsupported_feature`, `details.feature:
"action.tool_sources.attach"`, and `details.reason: "unadvertised"`. The two
that advertise it run the same call with the key, so one grep finds every
endpoint's admission and no endpoint is admitting by omission.

### A source is described, not managed

The unit adds one published shape, `ToolSourceDescriptor` — `{ id, kind,
display_name?, protocol?, endpoint? }` — and says where a tool comes from. It
adds no way to start a source, stop one, health-check one, or attach one after
the session is open. The harness runs the client; OAP describes the source,
attaches it at open, and observes its calls. That is the whole of the claim,
and it is the placement Decision 0003 chose first precisely because it is what
ACP, Claude Code, and Codex already do natively.

`ToolDefinition` gains `source`, the id — never an inline copy of the
descriptor. A copy would let two entries disagree about one source while each
validated, and it would put the endpoint of an MCP server on the wire once per
tool. `action.call.*` gains the same optional member, so a consumer attributes
a call without parsing a name, and a harness that namespaces its MCP tools
(Claude's `mcp__<server>__<tool>`) exposes the namespaced string as `name`
while `source` carries the attribution.

### The catalog is gated on its own key

`action.tools.list.request` and `.response` are gated on `action.tools.list`
and on nothing else. `action.tools` is deliberately not an alias: that family
key means lifecycle observation, and aliasing it would let a served catalog
pass a gate the endpoint never claimed while an endpoint that advertised only
the catalog key failed one it did. Adapters migrate to `action.tools.list` on
listing evidence of their own; Claude does so here and the other seven keep
`action.tools` alone, which now means exactly what it says.

The gate is judged on the correlated response, never on the request, for the
reason Decision 0005 gives: the wire makes a typed refusal the required
behaviour, and diagnosing the request would fail the conduct it mandates. An
endpoint that advertises no catalog may still be asked for one, and answering
`unsupported_feature` with `details.feature` naming `action.tools.list` and
`details.reason: "unadvertised"` is the correct behaviour. The validator
retains the expectation and settles it: a catalog served anyway is
`unavailable_capability` on the response, and a refusal under another code, or
under the right code without the feature or the reason, is the same defect as
no refusal at all.

The other direction is judged too. A catalog request an endpoint advertises,
carrying no defect any rule names, refused anyway, is `unhonoured_capability`.
Without it an endpoint could advertise `action.tools.list`, refuse every
request, and pass.

### A list request is a request of its own

`action.tools.list.request` gains `session_id` and `allow_degraded_features`.

The scope member is what ties a catalog to the attachment it reflects: a
session opened with `tool_sources` answers session-scoped lists, and the
response repeats the session on its envelope and in its payload. An unscoped
answer to a scoped request is `scope_mismatch` rather than an endpoint-level
catalog, so an adapter cannot evade the lifetime-catalog check below by
dropping the scope.

The opt-in is there because `action.tools.list` can be advertised `degraded`
like any key, and Claude's is. Without a field to consent on, such an endpoint
would have to refuse every list with `capability_degraded` or serve degraded
behaviour without consent, which Decision 0005's rule forbids. The rule is that
decision's unchanged, applied to a new carrier.

### A catalog resolves, or it is not a catalog

Three properties make a catalog usable, and each is checked where it can be
checked:

- a source `id` is unique across a session's catalog, so a tool's `source` and
  a call's `source` resolve to one descriptor and one endpoint even when one
  owner serves several sources (`duplicate_tool_source`);
- a tool `name` is unique whatever its source, so a policy entry (Decision
  0005) and a call resolve to one entry and one `execution_owner`
  (`duplicate_tool_name`, the diagnostic the run-controls unit introduced for
  the catalog a `tool_choice` is judged against, applied here to every session
  catalog);
- a tool's `source` names a source the same response declares, and a call's
  `source` names the source the session's catalog records for that tool — or a
  declared source when the catalog does not list the tool at all
  (`unmatched_tool_source`).

The catalog judges a call where its attribution is established, on
`action.call.requested`, and every later event of that call is held to the
source it was requested under rather than re-judged against the catalog.
`name` is optional on the progress and terminal payloads, so a call catalogued
under one source could otherwise omit its name and name another, and the
catalog lookup would miss and accept the second merely because it is declared
somewhere. The call's own track carries the requested source for exactly the
reason it already carries the requested `execution_owner`: neither may be
reassigned mid-lifecycle. The attribution is held more strictly than the owner,
because `source` is optional where `execution_owner` is required — an absent
source on a later event carries no attribution and says nothing, while one
introduced where the request named none has moved the call to an endpoint the
request never named.

All three are also judged on every accepted `capabilities.response`, over the
descriptor's effective catalog and its declared sources normalized across
layers, so a descriptor that is ambiguous or dangling on its own is diagnosed
before any list, open, or call. A session that never lists or selects tools
cannot otherwise reach a call whose source lookup is ambiguous, and a call that
agrees with a dangling descriptor entry is not excused by that agreement.

The descriptor is where the ambiguity is published, so it is where the
ambiguity is diagnosed: the run-controls unit's policy-time
`duplicate_tool_name` check now defers to it rather than blaming each
submission for the descriptor's one defect. The check itself is retained for a
catalog the descriptor did not publish.

### The attachment shape is not the catalog shape

`session.open.request` gains `tool_sources: [ToolSourceAttachment]`, a shape of
its own carrying the descriptor's members and, for a `process` source, the
attachment-only `command`, `args`, and `environment` (the registry's allowlist
form: a bare `NAME` forwards the endpoint's own value, `NAME=value` passes
literally).

The separation is the point. `environment` can hold a literal credential, and
`ToolSourceDescriptor` is what `action.tools.list.response` and `session.state`
publish back to clients. One schema serving both would make those members legal
in a catalog, so an implementation that reflected the open-time value straight
into its own catalog would leak the secret and still validate. The descriptor
keeps `additionalProperties: false` without them, which makes the leak a schema
rejection rather than a convention, and the validator adds
`attachment_field_in_catalog` for a published source carrying one anyway — the
tolerant bundle and a hand-rolled serializer are caught too.

`session.state` and `SessionOpenResponse` gain `sources`, in the descriptor
shape. In Go that is two structs and not one: the schema aliases the two
payload shapes but `protocol.SessionOpenResponse` is separate from
`protocol.SessionState` and the daemon builds it on its own path, so adding the
member to one alone would leave an open carrying `tool_sources` unable to
report the sources its own response is required to carry.

### Attachment is bounded by what the endpoint disclosed

Every rule above judges a refusal that names a defect the validator can see: a
dangling source, a duplicate id, an undisclosed mode. None of them reaches a
refusal of an attachment with no defect at all, so an endpoint could advertise
`action.tool_sources.attach`, refuse every well-formed array as
`unsatisfiable`, and satisfy every one of them.

`action.tool_sources.attach` therefore discloses its constraints in
`FeatureSupport.limits` — `max_sources` and the `transports` it accepts — and
refusing an array that violates none of them is `undisclosed_attach_limit`. The
key plus its limits tells a caller exactly which arrays are honoured, and the
key alone tells it that an unlimited one is.

`max_sources` is a positive ceiling, and the schema refuses zero. Zero is not
"attach nothing": an empty `tool_sources` array does not elect the capability
at all, so the only requests a zero ceiling could describe are the ones that do
elect it, every one of which would then sit outside the limit. Refusing all of
them would be conforming, and the endpoint would advertise the key while
honouring nothing — the exact outcome the limit mechanism exists to prevent. A
non-positive value therefore discloses no ceiling at all, in the schema and in
`FeatureSupport.MaxSources`, and such an endpoint is held to accepting every
well-formed array. `fixtures/schema-invalid/tool-source-attach-limit-zero.json`
pins the schema's half.

### Attachment discloses a set of modes, not one

`action.tool_sources.attach` discloses where it attaches in
`FeatureSupport.modes`, the plural, and not in the scalar `mode`.

The scalar was the first shape, and it cannot carry what this key has to say.
Every attachment-capable endpoint attaches at session open; an endpoint that
also accepts a `remote` source — one reaching an endpoint the operator never
configured — has a second thing to disclose, and with one slot saying the
second erases the first. A descriptor claiming `mode: "remote"` would read as
an endpoint that cannot attach at open, which is not what it meant and which
nothing would catch. The existing contract already names the distinction the
right way round: `mode` is "the single application mode a key has one of", and
`modes` is "the set a key can enforce more than one of". `run.tool_selection`
already discloses its enforced `tool_choice` modes as a set for exactly this
reason, so following it costs no new vocabulary, where a separate boolean
`remote` flag would have invented a third shape for one key.

`session_open` is therefore required wherever the key is advertised above
`unavailable`: an attach capability disclosing no session-open mode offers
nothing an open can elect. An open that attaches anyway is diagnosed on the
capability rung — `unavailable_capability`, refusal `unsupported_feature` with
`details.reason: "unadvertised"` — rather than through a diagnostic of its own,
because from the caller's side there is no difference between a key that is
missing and one that is unusable where the caller stands. A `remote`
attachment to an endpoint whose set omits `remote` stays on the unsatisfiable
rung with `details.source`, because there the capability is usable and one
member of the request is not.

The two gates are ordered, not concurrent. Where the descriptor omits
`action.tool_sources.attach` or advertises it `unavailable`, the capability
rung owns the response and its `unadvertised` refusal is the conforming one:
one `error.response` cannot carry both `unadvertised` and `unsatisfiable`, and
a caller told the capability is missing has no use for a detail about one of
its modes.

The ordering binds a binding's own constraints too. The daemon's credential
rule below is an unsatisfiability of the daemon's own, and applying it before
the endpoint's disclosure answers a question the caller never asked: told to
name an operator-configured source, a caller would keep reissuing opens
against an endpoint that attaches nothing, never learning the capability is
missing. So `POST /adapters/{name}/sessions` probes the adapter and settles the
capability and degradation rungs from its descriptor before it applies any rule
of its own. That is a reordering, not a second gate — the adapter's own
`RefuseUnadvertisedToolSources` still answers inside `Open`, which is what an
in-process embedding of `serve.Hub` sees, and the two cannot drift because the
route composes the same error types and renders them through the same
`serve.ControlRefusal` mapping it uses to relay the adapter's. A
probe that fails leaves the pre-check out rather than inventing a verdict: the
adapter's gate still runs, and nothing here is a security boundary. The open
route is the only path into an open — `servestdio` has no open op, and the
in-process embedding calls `Hub.Open` directly — so one placement covers the
wire.

### Attachment is for the session's lifetime

The open response's `sources`, every later snapshot's, and the `sources` of a
session-scoped catalog are the union of the open's attachments and the
descriptor's declared sources, compared by `id` and by each descriptor's
published members. A snapshot that omits an attached source, adds one never
attached or declared, or describes one differently is `session_state_mismatch`;
a catalog that omits an attached source or lists it with another `kind`,
`protocol`, `endpoint`, or `display_name` is `catalog_mismatch`. Native entries
may differ between lists — a harness refreshes its own catalog — but the
open-time entries never drop out and never change, because attachment is not
revocable in this unit and runtime attach and detach stay deferred.

A capability refresh does not release them either. The descriptor's declared
sources are invalidated with the revision, as the tool catalog already is, but
a session's attachments are session-lifetime facts the next catalog must still
carry — and a refreshed descriptor that declares an id an open session already
attached is `duplicate_tool_source` on the descriptor envelope, since a
post-refresh list is not mandatory and the ambiguity would otherwise go
unnoticed until a call resolved to the wrong endpoint.

### The daemon does not take a command from the wire

"Loopback, single-user" describes the transport, not the origin of a request on
it. `serve/servehttp` admits any request whose `Host` names an allowlisted
hostname and checked nothing else, so a page in the user's browser could issue
a simple cross-origin `text/plain` POST to `127.0.0.1` carrying a valid JSON
envelope, and the hidden response is no obstacle because the damage is the
request. With `command` and `args` accepted from the wire, that request would
execute a process as the daemon's user.

So the daemon's client-facing binding does not accept them, whatever the wire
shape allows. On `POST /adapters/{name}/sessions` a `process` attachment names
an operator-configured source by `id` only — from the registry document's new
`tool_sources` map — and the daemon fills `command`, `args`, and `environment`
from its own entry before forwarding the open. An attachment carrying a
`command`, an argument list, or a literal `NAME=value` on that route is refused
before the open is forwarded, with `unsupported_feature`, `details.feature:
"action.tool_sources.attach"`, `details.reason: "unsatisfiable"`, and
`details.source` naming it. The bare-`NAME` allowlist form is the only
`environment` a wire caller may write: the literal form is not the caller's own
secret when the caller may be a webpage, and `LD_PRELOAD` into an allowlisted
executable is the same process execution by another member.

This is a binding rule rather than a protocol rule — the validator cannot tell
a daemon from an embedding — so it is pinned by `servehttp` tests rather than by
corpus fixtures, and `command`, `args`, and a literal `environment` stay legal
exactly where the sender is the daemon itself or the in-process embedding of
`serve.Hub`, which has no network boundary to cross.

Alongside it, and not instead of it, `readRequest` gains the origin boundary a
loopback daemon should have had: it requires `Content-Type: application/json`
and rejects any request bearing an `Origin` header, which turns the browser's
simple request into a preflight the daemon never answers. That hardening is
defence in depth; the allowlist is what the unit graduates on, because an
executable the operator never configured is not something the daemon should run
under any boundary check.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `tool-sources`): 47 traces covering the
catalog gate in every direction, the scope a session-scoped catalog must answer
in, the three resolvability rules on both a list and a descriptor, the
attachment gate and its typed refusals, the remote mode ordered behind the
capability rung, the attachment limits in both directions, the union a session
snapshot publishes, the lifetime catalog an attachment binds, the
attachment-only member a published source may never carry, a call's
attribution and its reassignment mid-lifecycle, a refresh that collides with
an attachment, an attach capability disclosing no session-open mode in both
directions, and a schema-invalid `max_sources: 0`. Three boundary tests carry
what no trace can: every registered adapter refusing an attachment it never
advertised before a process starts (`serve`), the open route relaying both
typed refusals with the details that name what to change, and that same route
settling the capability and degradation rungs ahead of its own credential rule
(`serve/servehttp`).

Native evidence: Claude Code graduates the catalog at `degraded` on the
per-turn `system/init` frame, now pinned in
[the ledger](../research/claude-code-agent-sdk-2.1.263-mapping.md) and executed
by the corpus case `tools-catalog-sources` through the production reducer. ACP
graduates attachment at `native` with `modes: ["session_open"]` and
`limits.transports: ["process"]` on `session/new`'s `mcpServers`, executed by
the corpus case `open-with-tool-sources`. Its admission is decided before the
child is started, because it depends only on the request and the adapter's own
configuration: a refused open should not pay a process spawn and an initialize
round trip, nor leave a started child for the refusal path to clean up. And
because ACP names each MCP server by the attachment's id, without requiring
those names to be unique, an id colliding with another attachment or with a
server the operator configured is refused `unsatisfiable` with
`details.source`: two entries under one id would leave both the catalog's
attribution and the native routing ambiguous.

Reference execution: `adapter/memory.go` declares two sources, attributes its
scripted tool to one of them, serves the session's catalog, accepts attachments
within its disclosed limits, refuses the ones outside them by naming the
offending source, and publishes the union through state.

Surfaces: `serve.Session.Tools`, `GET /sessions/{id}/tools` with a repeatable
`?allow_degraded=<key>`, the stdio `tools` op taking `allow_degraded_features`
directly, `POST /adapters/{name}/sessions` forwarding `tool_sources` and
`allow_degraded_features`, and both clients' `Tools`/`tools` and open options.
The stdio op is held to the HTTP route's body by `parity_test.go`.

## Implementation details this decision chose

The plan left these open; each is recorded here rather than left to be
rediscovered from the code.

- **Claude attributes a tool to an MCP server by joining two members of one
  frame, not by reading a convention out of a name.** The ledger pins that
  `system/init` carries a tool list and an MCP server list; it does not pin
  the `mcp__<server>__<tool>` naming the plan names. So the reducer attributes
  a tool to a server only when its `mcp__` prefix matches a server the *same
  frame listed*, and attributes everything else — including a built-in tool
  that merely looks namespaced — to the adapter's own native source. The
  frame reports no endpoint for a server, so the descriptor carries none; an
  invented one would put a value on the wire the harness never said.
- **Overlapping server names resolve to the longest match.** One frame may
  list both `foo` and `foo__bar`, and `mcp__foo__bar__tool` then carries both
  prefixes: the separator is the same `__` a server name may itself contain,
  so the split is genuinely ambiguous and the wire offers nothing to settle
  it. The longest match is the reading under which every listed server keeps
  its own tools — `foo` winning would strand `foo__bar` entirely — and, being
  a total order over distinct names, it answers the same way on every run.
  Scanning the server set as a Go map would have let identical native
  evidence produce two different catalogs, which is the failure this rule
  exists to remove rather than merely to document.
- **Claude's source ids are namespaced `mcp:<name>`**, so a server called
  `claude-code-native` could never collide with the adapter's own native
  source id.
- **Claude refuses a catalog request that does not opt in.** The key is
  advertised `degraded`, and serving one anyway would give the caller
  degraded behaviour it never asked for.
- **Claude serves an empty catalog before the first turn rather than
  refusing.** The CLI publishes no `system/init` frame until it has been
  given input, so a session's catalog is genuinely unknown at open. Refusing
  there was wrong in a way this unit's own validator catches: the descriptor
  advertises `action.tools.list` affirmatively, so a refusal of a request
  within every constraint the endpoint disclosed is `unhonoured_capability`
  — an endpoint advertising a capability and honouring nothing. The session
  therefore answers with the native source declared and an empty tool list,
  and `degraded` is the disclosure that makes that readable: the catalog is
  only as current as the last turn, and there has not been one. An empty
  answer a caller can reason about beats a refusal that leaves it nothing.
- **The union check runs only for a session whose open attached sources.**
  Without an attachment a snapshot can hide nothing the descriptor does not
  already publish, and requiring every endpoint that declares a source to
  repeat it in every snapshot would make a T3b rule bind endpoints that never
  attached anything.
- **ACP drops an unresolvable bare `NAME` rather than refusing the open.**
  "A bare `NAME` resolves only if the adapter's registry entry allowlists it"
  is a resolution rule, and refusing for it would be a refusal no disclosed
  limit covers — which the bounded-refusal rule above would then diagnose. A
  `process` attachment with no command at all is refused as an invalid
  argument rather than as a capability refusal, because over the daemon the
  command comes from the operator's registry and is never empty, so only an
  embedder can produce one.
- **Both native revisions are bumped** (`claude-code-2.1.263-oap-v2`,
  `acp-v1.7.0-schema-v1.21.0-oap-v2`) and the reference adapter's with them
  (`reference-memory-v3`), because a revision identifies exactly one
  descriptor and each of the three changed.
- **`examples/tool-source.json` needed no change**: it was already a
  session-scoped request and response pair on the flat envelope, each tool
  carrying its source id and its `execution_owner`. Before this unit its
  `session_id`, `sources`, `source`, and `features` members made it
  schema-invalid; the schema now admits every one of them. It stays
  illustrative, as everything in `examples/` is — a two-envelope excerpt
  carries no capabilities exchange, so the semantic phase still has no
  descriptor to gate the catalog against.

## Consequences

- The catalog exchange's gate moves from `action.tools` to
  `action.tools.list`. No existing adapter advertised the latter, and none
  served a catalog, so no existing fixture changes meaning; Claude is the
  first to advertise it.
- `duplicate_tool_name` is diagnosed on the descriptor that publishes the
  ambiguous catalog rather than on every submission judged against it. The
  run-controls fixture that asserted it keeps its code and its count; only the
  envelope it blames moves.
- The corpus-completeness check now binds two more keys. `action.tools.list`
  carries `tools-list-unadvertised-served` (gate) and
  `tools-list-refused-advertised` (honour); `action.tool_sources.attach`
  carries `open-attach-unadvertised` (gate) and
  `tool-source-attach-refused-within-limits` (honour). Neither honour aspect
  is deferred.
- The daemon gains an origin boundary that every existing client already
  satisfies: both in-repo clients send `Content-Type: application/json` on
  every POST and no `Origin` header.
- The registry document gains a `tool_sources` map, resolved at load like the
  adapter `environment` allowlists, so a bare `NAME` the operator never
  exported fails at hub start rather than at open.
- The Go client's GET-style session methods now bind their response to the
  session that asked. Those routes send no request envelope, so the
  request-based scope check never ran and a misrouted answer was returned as
  this session's — which this unit makes materially worse, because a catalog
  and a session snapshot now carry the sources a session attached. The rule is
  applied to `Session.State` as well as `Session.Tools`: both have the shape,
  and a client-side guarantee that holds on one route and not its neighbour is
  one a caller cannot reason about. `Session.State` therefore now rejects a
  response it previously accepted. The TypeScript client already made both
  checks; it gains the tests that pin them, so the two clients reject the same
  set.

## What this unit does not admit

- **Control-layer-provided tools.** `session.open.request.tools`,
  `action.call.resolve.*`, the `action.tools.provide` capability key, and the
  diagnostics `wrong_tool_owner`, `undisclosed_provide_limit`, and
  `resolution_payload_mismatch` are the separate `control-tools` unit and are
  not graduated here. Nothing in this unit registers them, and the plan's
  per-sub-unit exit criteria are what make a partial T3 legitimate.
- **Runtime attach and detach.** Attachment is for the session's lifetime.
  Claude's `mcp_set_servers` is the only evidence of a runtime surface on any
  pinned ledger, and one harness is not two.
- **Per-run tool-source attachment.** Session open is what ACP and Claude Code
  support.
- **A catalog that is not a session's or an endpoint's.** There is no
  cross-session catalog and no catalog cache.
- **Reconciling a policy or a sourced call retained across a capability
  refresh.** The plan's `unjudgedTools` bookkeeping — a `tool_choice` or a
  sourced call emitted between `capabilities.updated` and the next list, held
  and reconciled by that list — is not implemented. The session catalog is
  discarded with its revision and a sourced call in the gap falls back to the
  declared sources rather than being retained. That is strictly weaker than
  the plan, never wrong in the other direction, and it is a validator rule
  with no wire consequence, so it can be added by amendment without touching
  the schema.
- **A version bump.** Every addition is additive under the layered draft's
  compatibility rules: new optional fields, one new capability key, no new
  envelope type, and no narrowing of an existing field. `version` stays `0.1`
  and the profile identifier is unchanged.
