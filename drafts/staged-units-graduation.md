# Staged Units Graduation Plan

Status: proposed design (companion to
[Decision 0003](../decisions/0003-staged-unit-graduation.md))
Date: 2026-09-13
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Prompt: [issue #13](https://github.com/lsm/open-agent-protocol/issues/13)

This document is the per-unit design behind Decision 0003. For each staged
control unit it fixes the wire delta, the validator rules, the reference
adapter behavior, the native evidence and the adapter that graduates first,
the cut through `serve`, the daemon, the stdio frontend, and both clients,
the fixtures, and the exit criteria its own decision will cite. It does not
graduate anything: each unit's decision does that when the gate is met.

The units keep issue #13's labels. The plan's order is Decision 0003's:
T0 extension packs, T1 run controls, T5a models catalog, T2 queue
delivery, T3 tool sources and control-layer tools, T4 steer, T5b auth
state. T0 comes first because the later units give capability keys
executable meaning and T0 is what makes a key's namespace and its gate
well defined; it is also the one unit not gated on ledger evidence,
for the reason its own section gives.

## Where v0.1 leaves the staged surface

The wire already carries more than the executable subset accepts:

- `session.message.submit.request` carries `model_id`, `instructions`,
  `tool_choice`, `output_schema`, `allow_degraded_features`, and `metadata`
  ([`schema/v0.1/session.schema.json`](../schema/v0.1/session.schema.json),
  [`protocol/control.go`](../protocol/control.go)). Every executable adapter
  rejects `instructions`, `tool_choice`, and `output_schema` before admission.
  Codex applies `model_id` to `turn/start` and Makai applies it as
  `agent_message.model_ref`, both natively per run and both without
  advertising a model-selection capability; every other adapter rejects it.
- The delivery enums carry `queue`, `steer`, and `btw`; the admission enum
  carries `steered` and `side_started`. Decision 0002 made the queued shape
  canonical for `auto`; explicit `queue` and `steer` requests are rejected by
  every adapter, and the validator's `sessionTrack` holds one active run.
- `ToolDefinition` has `execution_owner` and the `action.call.*` payloads
  carry `interaction_id`, `requested_by`, and `responded_by`, so tool
  ownership is already expressible; no envelope resolves a control-owned call.
- The layered draft names `models.request`/`models.response` and the
  `auth.*` family; none is in the envelope `oneOf`, so the schema rejects them.
- `adapter.Descriptor.MaxActiveRunsPerSession` exists in Go but has no wire
  projection.

## The gate, made concrete

Decision 0003's four steps translate into these exit criteria for every unit:

1. `adapter/memory.go` executes the unit and `adapter/adaptertest` asserts
   it; `oap check` drives the reference path.
2. `validation/state.go` enforces the unit's invariants with new diagnostic
   codes registered in `validation/diagnostic.go` and
   `validation/manifest.go`; `fixtures/manifest.json` lists the unit's
   positive and negative fixtures under a new unit name; every existing
   fixture validates unchanged. A unit's inventory is every fixture named
   anywhere in that unit's section, not only those in its **Fixtures**
   heading: a rule that introduces a fixture names it where the rule's
   expectation is defined, which keeps the two from drifting apart, and
   the **Fixtures** heading collects the rest. Decisions citing a unit's
   fixtures mean the whole inventory. A fixture belongs to the unit that
   introduces the diagnostic it asserts, so one expecting a later unit's
   code lands with that unit however early the rule motivating it appears. A fixture's `valid` comes from its own
   annotation in the unit's fixture list, never from the `Positive:` or
   `Negative:` heading it happens to sit under: an entry naming a
   diagnostic code is `valid: false` and must produce exactly that code,
   and one marked *(positive)* or *a correct rejection* is `valid: true`
   with no codes. The lists are grouped by the behaviour each trace
   exercises, so a unit's refusal cases read together — the admitted
   negative, the conforming refusal, and the wrongly coded refusal are
   one story, and splitting them across two headings hides which is
   which. This matters because half of this plan's rules make a typed
   refusal the required behaviour: those fixtures sit beside the
   admission they refuse and are positives, and a generator that read the
   heading instead of the annotation would demand a diagnostic from
   correct fail-closed behaviour.
3. One native adapter executes the unit through its production codec and
   reducer with a corpus case pinned to its ledger commit, its descriptor
   advertises the unit at the evidenced level, and its live gate (where one
   exists) exercises it.
4. `serve.Hub`, `serve/servehttp`, the stdio frontend, `client`, and
   `clients/ts` expose the unit in the same change as the hub; the Go and TS
   e2e tests drive it end to end.
5. The unit's decision is accepted; the core draft, the conformance draft, and
   the README move the unit from staged to executable.

## Order and evidence

| Order | Unit | Native evidence in the pinned ledgers | Graduating adapter | Follow-on adapters |
| --- | --- | --- | --- | --- |
| 0 | T0 extension packs | none, and none required: this unit describes the protocol's own extension seam rather than harness behaviour (see its scope) | — | all, passively |
| 1 | T1 run controls | Codex `turn/start.model` and Makai `agent_message.model_ref` (native per-run); Claude `set_model` control request and `--model` at spawn; pi model selection commands; ACP `session/set_config_option` model; Hermes `session.create.model`; OpenCode `CreateSession.model`; Makai `agent_start.system_prompt` (session-level) | Codex (`model_id`, native) | Makai (native), Claude (`set_model`, emulated), pi, ACP |
| 2 | T5a models catalog | OpenCode `model.list`/`provider.list`; pi `get_available_models`; Makai `models_request`; Claude `system/init.models` and `list_models`; Hermes `model.options` | OpenCode (native) | pi, Makai, Claude (degraded) |
| 3 | T2 queue delivery | OpenCode `SessionInput.Admitted{delivery:"queue", promotedSeq}`; pi `follow_up` with `queue_update`; Hermes busy `queued` status under `busy_input_mode=queue`; Claude `queued_turn_count`/`still_queued` | OpenCode (native) | pi, Hermes |
| 4 | T3 tool sources | T3a catalog with sources: Claude `system/init` (tools, MCP servers), Codex `mcpToolCall.server`; T3b attach at open: ACP `session/new.mcpServers`, Claude MCP config at spawn and `mcp_set_servers`; T3c control-layer tools: Makai `tool_execute`/`tool_result` bridge, Claude `sdkMcpServers` with `mcp_message`, ACP reverse fs/terminal calls | T3a Claude, T3b ACP, T3c Makai | Codex, Claude |
| 5 | T4 steer | pi `steer` with `queue_update` and injection at a turn boundary; OpenCode `delivery:"steer"` while busy; Hermes `session.steer` and busy `steered` | pi | OpenCode, Hermes; Codex only after a new ledger pin covers `turn/steer` (the pinned Codex ledger defers it; the expected-turn-id detail comes from unpinned research) |
| 6 | T5b auth state | Claude `auth_status` frames; OpenCode `provider.list` | staged | — |

Ledgers: [Codex](../research/codex-app-server-8d7cc24-mapping.md) ·
[Claude Code](../research/claude-code-agent-sdk-2.1.263-mapping.md) ·
[OpenCode](../research/opencode-v1.18.29-mapping.md) ·
[pi](../research/pi-v0.85.1-mapping.md) ·
[Hermes](../research/hermes-v2026.8.31-mapping.md) ·
[ACP](../research/acp-v1.7.0-mapping.md) ·
[Makai](../research/makai-agent-67ad514-mapping.md) ·
[DeepSeek](../research/deepseek-harness-47f9438-mapping.md). DeepSeek
contributes no native evidence to any unit at its pin and advertises each
`unavailable`.

## T0. Extension packs

Unit name: `extensions`. Planned decision: 0004.

### Scope

Three things the protocol needs before it mints its next capability key:
a namespace rule that separates spec vocabulary from everyone else's, a
pack format that lets a third party ship capability keys *with* their
schemas so their envelopes are validated rather than merely tolerated,
and a conformance claim for a pack that leaves the core claim untouched.

This unit is the one exception to the evidence rule in "Order and
evidence", and the exception is deliberate rather than overlooked: the
other six units describe harness behaviour, so a pinned ledger is what
establishes that the behaviour is real. This one describes the
protocol's own extension seam, where the evidence is the seam's current
state rather than any harness's. That state is the argument for doing it
first. `layer.features` is already an open map
(`capabilities.schema.json#/$defs/layer`,
`additionalProperties: { "$ref": "#/$defs/featureSupport" }`), so a
vendor can already advertise `com.example.storage.objects` as `native`
and the fail-closed gate already treats it exactly like a core key —
advertisement needs no change at all. What it cannot do is say what the
key *means*. `manifest.schema.json` pins `schemas` at `minItems: 7`,
`maxItems: 7` over an `enum` of the seven core files with
`additionalProperties: false`, and `validation.CompileSchemas`
(`validation/schema.go:15`) takes no parameters and reads only the
embedded `schema/v0.1` directory. An extension bundle is therefore not
unspecified, it is structurally forbidden: there is no way to add a
schema to a bundle and no way to compile one that includes it.

So a third-party surface today is *tolerated*, not *supported*. Its
envelopes reach the unknown-type path the tolerance step opens, which
accepts them because nobody can say what they should look like. Nothing
distinguishes a well-formed vendor call from a malformed one, which is
the difference between an extension and an unvalidated hole.

It goes before T1 for two reasons. The tolerance step already lands
before T1 and already changes what the validator compiles; packs change
the same seam, and doing them together avoids designing that seam twice.
And every later unit mints capability keys — four in T1 alone, ten
across the phase. Minting the spec's own keys without a stated namespace
rule makes the
unprefixed form the precedent a third party will copy, and the rule is
much cheaper to state before that than to retrofit after.

### Wire

No envelope change for advertisement, per the open map above. What is
added is a rule about names and a way to carry schemas.

**Namespace.** The rule is stated the way round that leaves the existing
vocabulary alone: *the unprefixed namespace is the spec's, in its
entirety.* Capability keys, envelope `type` values, `error.response`
codes, and validator diagnostics that carry no prefix are spec-owned,
whatever their shape — `content.delta`, `user.input.requested` and
`error.response` sit under no common root, and `unsupported_feature` and
`scope_mismatch` have no dots at all, so any rule built on a list of
permitted root segments would classify the protocol's own vocabulary as
invalid extensions. An extension name is one that carries a reverse-DNS
prefix, as the layered draft already requires of extension fields
(`drafts/layered-agent-protocol.md:488`): `com.example.storage.objects`,
never `storage.objects`.

Stated this way the rule needs no list to maintain, and enforcement has
one home rather than two. The validator never has to classify a name it
meets on the wire: a name matching a loaded pack's prefix is validated
under that pack, a known core name under core, and anything else takes
the tolerant unknown path exactly as before. What is checked, and
checked once, is a *pack at load*: every name it declares must begin
with its own `id` followed by a dot. A pack cannot declare an unprefixed
name, so it cannot mint into the spec's namespace, and the core
vocabulary grows only by spec change — which was the property the root
list was reaching for, obtained without enumerating anything.

The own-prefix rule alone does not make two packs non-overlapping, and
composition needs that separately: `com.example` and
`com.example.storage` both satisfy it while both legally claiming
`com.example.storage.read`, and even where their declarations differ,
"the loaded pack whose prefix matches" stops naming one pack. The loaded
set is therefore required to be *prefix-free* — no pack `id` may equal
or be a dot-prefix of another — and that is checked across the set at load, not
per pack, since neither pack is at fault alone. Prefix-freedom makes
prefix matching a function rather than a search: at most one loaded id
can prefix any name, so ownership is decided without a precedence rule,
and collision handling is a load refusal naming both ids rather than a
runtime tie-break. Two vendors then compose by construction, and a vendor
subdividing its own namespace ships one pack that declares both families
rather than two packs that nest.

**Manifest.** `manifest.schema.json` gains an optional `extensions`
array; `schemas` keeps its 7-of-7 bound, which from here describes the
core bundle rather than the whole compiled set. Each entry is a pack
descriptor: `id` (reverse-DNS, the pack's namespace), `version`, and
`schemas` (the files the pack contributes).

**Pack.** A pack is that descriptor plus its schema files and,
optionally, its own fixture manifest in the existing fixture format. The
descriptor declares what the pack defines — `capability_keys`,
`envelope_types`, `error_codes`, and the `payload_members` it adds to
core payloads — rather than leaving it to be inferred
from the schemas, so containment can be checked before anything is
compiled.

Those lists are independent, and independence is not enough: a pack
declaring the key `com.example.foo` and the type
`com.example.foo.request` has said nothing tying one to the other, so
nothing would require the key to be advertised before the type is used.
The fail-closed gate this plan is built on would then stop at the edge
of core — the strongest promise the protocol makes, silently not
extended to the surfaces T0 exists to support. Neither existing
mechanism can supply the link: a schema cannot see the capability
descriptor that preceded it in the trace, and the stateful validator
reaches its feature keys by hard-coded name at each known-type case
(`validation/state.go:256-300`, `612-639`, `943-961`), which by
construction knows nothing a pack declares.

So the descriptor carries the mapping explicitly: `gates`, from each
declared envelope type — and each control member a pack adds to an
existing payload, for which see `payload_members` below — to the
capability key that must be advertised for it,
which containment already requires to be one of the pack's own. Every
declared `request` and `event` must appear in `gates` or be declared
ungated, stated rather than omitted, so a missing entry is a load
refusal and not a silent hole; a `response` is deliberately excluded,
because it derives its gate from the request it answers through
`replies_to` (the role rule below) and an entry of its own would be a
second, possibly disagreeing, source for the same gate — so a `response`
that appears in `gates` is itself a load refusal (`pack_response_gated`),
and an ordinary request/response pack lists the request and nothing
else. The stateful validator consumes it at the same dispatch
point the core keys use: a packed type resolves its feature key through
the loaded pack's `gates` instead of a hard-coded string, and the
existing gate then applies unchanged — unadvertised means the typed
refusal is owed, an admission is `unavailable_capability`, and the
refusal is itself validated. An extension capability is fail-closed in
the same machinery as a core one, which is the claim T0 makes, and this
is what makes it implementable generically rather than per pack.

A key per type is still not enough to *run* the gate, because the gate
is judged on the correlated response and never on the request, and the
validator cannot tell a packed request from a packed event: both omit
`in_reply_to`, and the format has no naming convention to lean on. For
an unadvertised key the two need opposite treatment — a request is
permissible and is retained until its correlated response says whether
the endpoint refused it, whereas an event or a success response is
already the endpoint acting on a capability it does not have, and is
`unavailable_capability` the moment it appears. Without the distinction
the validator would either convict every packed request on sight, which
diagnoses the fail-closed behaviour the wire requires, or retain every
packed event forever, which diagnoses nothing.

So each declared envelope type carries a `role` — `request`, `response`,
or `event` — and a `response` names the request type it answers in
`replies_to`. The stateful validator then treats packed types exactly as
it treats core ones. A `request` is retained with its envelope id and
its gating key, and the gate settles on the correlated response: a
success response of the declared `replies_to` type admitted while the
key is unadvertised is `unavailable_capability` on that response, and a
correlated `error.response` — the core type, so a packed request is
refused with the vocabulary every other refusal uses — must carry
`unsupported_feature` with `details.feature` naming the key and
`details.reason: "unadvertised"`, anything else being
`unavailable_capability` on the error response, as the ladder rules for
every capability-rung refusal. A `response` must carry `in_reply_to`,
which is what correlation is; it takes its gate from the request it
answers and needs no `gates` entry of its own, and a `response` naming a
`replies_to` that is not a declared `request` of the same pack is a load
refusal (`pack_reply_target_unknown`). An `event` has no correlation and
takes its own `gates` entry, judged on arrival. A declared type with no
`role` is a load refusal (`pack_role_undeclared`), on the same rule as an
ungated type: stated, not inferred. `payload_members` need none of this
— a member added to `session.message.submit.request` is judged where the
submit is, on its own response.

A control member added to an existing core payload needs more than a
gate, because nothing in the format so far can carry its *shape*. New
envelope types arrive as whole branches, but a member on
`session.message.submit.request` has no branch of its own: the strict
core payload is `additionalProperties: false` and rejects it, the
tolerant compile ignores it, and loading the pack would validate it in
neither. A packed control would then be gated but unchecked — worse than
an unknown one, because the gate implies it was understood. So the
descriptor declares `payload_members`, each entry naming a core payload
type, the member it adds — prefixed, like every other declared name, so
containment and prefix-freedom already keep two packs apart — and the
subschema that member's value must satisfy.

Composition is additive and stated as such. The compile adds each
declared member to that payload's `properties` and nothing else: it does
not lift `additionalProperties`, so an *undeclared* member is still
rejected in strict mode exactly as today; it does not touch `required`,
so a pack cannot make a core member optional or a new one mandatory; and
it cannot restate a member the core payload already defines, which is a
load refusal rather than an override, since a pack that could narrow
`model_id` would change core validity through the door T0 closes
everywhere else. The result is that core payloads accept precisely their
own members plus the declared ones, in both modes, and the member is
validated when its pack is loaded and tolerated as unknown when it is
not — the same tolerance-becomes-conformance the envelope types get.

Where a member's gate settles follows from the role of the payload it is
added to, and that is defined for every permitted target rather than for
submit requests alone — otherwise a pack could legally add a member to
`run.completed`, have its schema compiled, and never meet the gate,
which is unadvertised extension behaviour with T0's fail-closed
guarantee stamped on it. A core type's role is already known, so the
rule needs no new declaration: a member added to a core *request* is
retained with that request and its `gates` key settles on the request's
correlated response, exactly as a control on
`session.message.submit.request` settles on the submit response; a
member added to a core *response* or *event* is the endpoint acting on
the capability, and is judged on arrival — present while the key is
unadvertised, `unavailable_capability` on that envelope. A
`payload_members` entry naming a type that is not a core envelope type is
a load refusal (`pack_member_target_unknown`), since a target with no
known role has no gate point. Fixtures `ext-member-on-event-unadvertised`
(`unavailable_capability` on a `run.completed` carrying a packed member
while its key is unadvertised) and `ext-pack-member-target-unknown`
(load refusal).
Fixtures `ext-member-validated` (a packed member with a body its
subschema rejects: tolerated without the pack, diagnosed with it),
`ext-member-undeclared-still-rejected` (positive; a different unknown
member still refused by the strict core payload with the pack loaded),
and `ext-pack-restates-core-member` (load refusal).

### Semantics

- **Loading a pack turns tolerance into conformance for its vocabulary.**
  Without the pack, its envelope types take the tolerant unknown-type
  path and are accepted on the common envelope fields alone. With the
  pack, they are validated against the pack's own branches, strictly, the
  same way a core type is. This is the whole point of the unit: an
  extension becomes a thing an implementation can be wrong about.
- **Containment.** A pack may define names only under its own `id`
  prefix. A pack declaring a key outside it — one belonging to another
  pack, or an unprefixed name, which is the spec's namespace entire —
  fails to load — it is not a
  validation diagnostic but a refusal to compile, because a pack that
  could redefine core or shadow a peer would make every other guarantee
  here conditional on which packs happened to be loaded. This is what
  makes packs composable: two vendors' packs can be loaded together and
  cannot collide, because their names cannot overlap by construction.
- **Core validity never depends on a pack.** Core conformance is computed
  against the core bundle alone, so loading a pack can never make a core
  envelope valid that would otherwise be invalid, nor the reverse. A pack
  adds vocabulary; it does not amend the protocol.
- **The gate is unchanged.** An extension capability is fail-closed like
  any other: unadvertised means refused, and the refusal is the same
  typed `unsupported_feature` naming the key in `details.feature`. An
  agent handed a vendor pack therefore uses that vendor's surface through
  exactly the machinery it already uses for core capabilities, which is
  the property this unit exists to deliver.

### Validator

- `validation.CompileSchemas` keeps its signature and its meaning. A
  variant takes options — the tolerance mode this phase already
  introduces, and zero or more packs — so the two seam changes land as
  one API rather than two. Packs are registered as additional resources
  under their own base URI and contribute branches to the envelope
  `oneOf`; the tolerant fallback branch stays, and now catches only types
  no loaded pack claims.
- Pack compilation resolves references through the same refusing loader
  T1 compiles `output_schema` with, restricted to the resources actually
  registered: the core bundle and the loaded packs, each under its own
  base URI. The engine's default loader reads local files (the reason T1
  refuses an external `$ref` in a control), and a third-party pack is a
  document a user loads from someone else, so compiling it with the
  default loader would let a `$ref` reach paths outside the pack on the
  loading machine. A `$ref` a pack schema cannot satisfy from the
  registered set — a file path, an unregistered URI, another pack's base
  URI it did not declare a dependency on — is a load refusal
  (`pack_external_ref`) rather than a compile error surfaced later, and
  the validator detects it by compiling with the refusing loader, never
  by resolving it, exactly as T1 does. Fixture `ext-pack-external-ref`
  (load refusal; a pack schema carrying a `$ref` to a path outside its
  own resources).
- A contributed branch must pin `type` to a `const` naming exactly one of
  the pack's declared `envelope_types`, and that is verified at load. The
  check is not bookkeeping: `oneOf` requires exactly one match, so a pack
  that declared `com.example.foo` while supplying a branch broad enough
  to also match `run.started` would make every `run.started` match two
  branches and turn a core envelope invalid *because a pack was loaded* —
  precisely the invariant the previous rule states. Declared names alone
  cannot prevent it, because the guarantee has to hold over what the
  schemas actually match, not over what the descriptor says they match.
  With the discriminator pinned and verified, branch selection is a
  dispatch on `type`: a core type never reaches a pack branch, and the
  invariant holds by construction rather than by a pack's good behaviour.
- Containment is checked at load, before compilation, against the
  declared names in each descriptor.
- `oap validate` gains a repeatable `-pack <path>`. Packs are opt-in on
  the command line for the same reason tolerance is: which vocabulary is
  in force must be the caller's stated choice, not inferred from the
  input.
- `validation/manifest.go` needs two changes, and they are different in
  kind. `knownUnits` gains `extensions`, because pack loading — the
  namespace check, containment, composition, gate resolution — is core
  behaviour implemented by this unit, and its fixtures are core fixtures
  like any other unit's. That is a static addition to the hard-coded map,
  as every graduated unit makes.
- A pack's *own* fixtures are the separate mechanism, and a static unit
  cannot express them: `LoadManifest` today rejects any unit outside that
  map (`validation/manifest.go:70`, `:97-100`), so a pack's fixtures
  cannot be loaded at all. The unit term is therefore derived from the
  packs actually loaded rather than added to the hard-coded list: an
  `ext:<pack id>/<version>` term is accepted exactly when that pack is
  loaded and its version matches. Nothing is widened for a run that
  loads no packs, which is every core run, so the list keeps its present
  meaning and a stale or misspelled pack claim still fails closed.
- Diagnostic codes are *not* admitted the same way, and the reason is a
  boundary the plan draws everywhere else: an `error.response` code is
  wire vocabulary the endpoint emits, and a validator diagnostic is what
  the validator says about a trace, and the two do not correspond
  (T1's empty `model_id` is refused `model_not_found` and diagnosed
  `unsatisfiable_control`). A pack's `error_codes` are the first kind. A
  pack ships schemas and gate metadata, not code, so there is no
  mechanism by which it could *produce* a diagnostic of its own, and
  admitting one into a manifest would load an expectation nothing can
  ever emit. `diagnosticCodes()` (`:89-93`) is therefore untouched: a
  pack's fixtures assert only what the validator can actually say about
  packed content — a schema-phase failure on the pack's own branch or
  member, the gate diagnostics `unavailable_capability` and
  `unhonoured_capability` resolved through `gates`, and the load
  refusals. That is the honest extent of the seam T0 opens: schema and
  gate, not stateful semantics. Pack-supplied validation rules, and with
  them pack-defined diagnostics, are deliberately later and listed as
  such below.
- Load refusals need a fixture kind of their own, because the existing
  manifest cannot express them. `FixtureEntry` admits `positive`,
  `schema-invalid`, and `semantic-invalid`, and requires a diagnostic
  code and a decode/schema/semantic phase for every invalid entry
  (`validation/manifest.go:78-87`) — but a pack that declares an
  unprefixed name fails before any trace is read, in a phase that has no
  name and with no diagnostic, since the validator never ran. Half of
  T0's negative fixtures are of that shape, and without a way to state
  their expectation the unit's own fixture gate could not pass. So the
  manifest gains kind `load-invalid` with phase `load`: the entry's
  `path` names a pack directory rather than a trace, and its `codes` are
  drawn from a small load-error vocabulary that is deliberately *not*
  added to `diagnosticCodes()`, because a load error is what the loader
  says about a pack and a diagnostic is what the validator says about a
  trace — `pack_unprefixed_name`, `pack_foreign_prefix`,
  `pack_id_collision`, `pack_branch_undeclared_type`,
  `pack_branch_unpinned`, `pack_ungated_type`,
  `pack_restates_core_member`, `pack_member_target_unknown`,
  `pack_role_undeclared`, `pack_response_gated`,
  `pack_reply_target_unknown`, `pack_external_ref`,
  `pack_fixture_claims_core_unit`, and
  `ext_claim_without_pack`, one per load refusal this section names.
  The runner asserts that loading the named pack fails with exactly
  those codes, and a `load-invalid` entry that loads cleanly, or fails
  with different codes, fails the fixture. Every other T0 fixture is an
  ordinary trace fixture validated with the pack loaded.
- The two directions are kept apart deliberately: a pack's fixture may
  not claim a core unit, and a core fixture may not claim an `ext:` term.
  Without that a pack could contribute evidence toward `+queue`, which is
  the one thing "a pack can never widen a core claim" has to mean.
  Fixtures `ext-pack-fixture-claims-core-unit` and
  `ext-claim-without-pack` (both load refusals).

### Conformance

The executable claim is unchanged for core and gains an independent term
per pack: `open-agent-protocol.agent-control-core/0.1-executable` plus
graduated units, plus `+ext:<pack id>/<pack version>` for each pack an
endpoint implements. A pack ships its fixtures in the existing manifest
format and they run under the same runner, so "conformant to Cloudflare's
extension" is a claim with the same executable meaning as "conformant to
`+queue`" — stated by the vendor, checked by the same tool, and carrying
no authority over the core claim beside it.

### Fixtures

Every entry described as a load refusal below is a `load-invalid`
fixture asserting the corresponding `pack_*` load-error code (validator
section); the rest are trace fixtures run with the pack loaded.

Negative: `ext-pack-claims-unprefixed-name` (a pack declaring a name with
no reverse-DNS prefix, which is the spec's namespace; load refusal, not a
diagnostic), `ext-pack-claims-foreign-prefix` (a pack declaring a name
under another pack's id; load refusal), `ext-packs-nested-ids` (two
individually valid packs whose ids are `com.example` and
`com.example.storage`, loaded together; load refusal naming both, since
neither is at fault alone) and `ext-packs-duplicate-ids` (the same
refusal for two packs sharing one id, the degenerate prefix case), `ext-pack-branch-undeclared-type`
(a contributed branch whose `type` `const` is not among the pack's
declared `envelope_types`; load refusal), `ext-pack-ungated-type` (a declared `request` or `event` absent from
`gates` and not declared ungated; load refusal),
`ext-pack-response-gated` (a `response` given a `gates` entry of its
own instead of deriving it through `replies_to`; load refusal), `ext-pack-role-undeclared` (a
declared type with no `role`; load refusal),
`ext-pack-reply-target-unknown` (a `response` whose `replies_to` names
no declared `request` of the pack; load refusal), `ext-packed-type-unadvertised`
(a packed operation admitted while its gating key is unadvertised;
`unavailable_capability`), `ext-pack-branch-unpinned`
(a contributed branch that does not pin `type` to a `const` at all, the
shape that would otherwise match core envelopes and invalidate them by
double match; load refusal),
`ext-packed-type-malformed` (an envelope of a loaded pack's type with a
body its schema rejects — accepted without the pack, rejected with it,
which is the pair that shows tolerance becoming conformance).

Positive: `ext-unpacked-type-tolerated` (the same envelope with no pack
loaded, accepted on the common fields in tolerant mode),
`ext-advertised-key-gated` (an extension key advertised and used, and the
same key unadvertised drawing `unsupported_feature` with
`details.feature` naming it — the gate resolved through the pack's
`gates` rather than a hard-coded name, and the refusal reached by
retaining the packed `request` and judging its correlated
`error.response`, as the `role` makes possible),
`ext-packed-event-unadvertised` (`unavailable_capability` on arrival; a
packed `event` emitted while its key is unadvertised — the case that
must *not* be retained), `ext-packed-request-wrong-refusal`
(`unavailable_capability` on the error response; the packed request
refused under `internal_error` instead of the typed refusal), `ext-core-claim-unchanged` (the core
fixture manifest passing identically with and without a pack loaded —
the regression guard for the invariant that a pack cannot change core
validity, run against a pack whose declared type is deliberately close
to a core one).

### Corpus completeness

Every unenforced promise this plan has had to close so far had the same
shape: a capability key whose advertisement implied behaviour that no
fixture exercised, so an endpoint could advertise it, refuse everything
it was given, and pass. `action.tools.provide`, `run.tool_selection`,
`action.tool_sources.attach`, and the queue's second bound were all found
by reading, and reading is not repeatable. The check is, so it becomes
part of the corpus rather than a review habit.

A capability key has two aspects a corpus must falsify, and every key a
unit introduces or gives executable meaning needs a negative fixture for
each:

- `gate` — the key unadvertised, its operation admitted anyway, and the
  admission diagnosed. Every unit already carries these.
- `honour` — the key advertised, a request that satisfies every
  constraint the endpoint disclosed and carries no defect any rule names,
  refused, and the refusal diagnosed. This is the class that was missing,
  and the class an endpoint can otherwise pass conformance without ever
  honouring.

Fixture manifest entries declare what they cover, so the check can find
them: `covers`, a list of `{ "capability", "aspect" }`. `LoadManifest`
gains, beside `knownUnits`, the capability keys each unit owns, and fails
the manifest when any key of a claimed unit lacks a negative fixture for
either aspect. The check runs where the corpus is loaded, so CI fails on
the first key added without its pair — a T6 key, or a vendor's — rather
than on the next audit. A key whose `honour` aspect cannot be falsified
until a later unit says so in the unit's key list, `honour_deferred_to:
<unit>`, a stated deferral rather than a silent gap; `run.model_selection`
is the one instance today, deferred to `models`, because a false refusal
of a model id is only decidable against a catalog
(`models-listed-selection-false-miss`). Packs are held to the same rule
over their own `capability_keys` and their own fixture manifest, and a
pack whose corpus lacks the pair for any key it declares fails to load.
A vendor therefore cannot claim `+ext:` conformance for a key that
nothing could show it dishonouring, which is the same protection core
gets, extended to the surfaces T0 exists for.

The `honour` fixture's expected diagnostic is the unit's specific one
where a rule already names the failure — `unsatisfiable_control` for a
control, `queue_limit_exceeded` for a bound that never bound,
`undisclosed_provide_limit` and `undisclosed_attach_limit` for their
arrays — and otherwise the generic `unhonoured_capability`, new here: an
operation within every disclosed constraint refused on an endpoint that
advertises the governing key, where no unit-specific rule names a
defect. The generic form is what a pack's fixtures assert — a pack has
no diagnostics of its own, for the reason the validator section gives —
and it is what two core keys turned out to need.

Run against the plan as it stands, the check finds five gaps, which is
the argument for it. Each is closed in its unit's fixture list, and
recorded here so the first run of the check has nothing left to find:

- `run.instructions`: no `honour` fixture at all. The note under
  "Cross-cutting vocabulary" stands — whether admitted instructions were
  *applied* is unobservable — but their *acceptance* is not, and
  `instructions` has no unsatisfiability condition, so any refusal on an
  advertised endpoint is a false one. T1 gains
  `controls-instructions-refused` (`unsatisfiable_control` on the
  `error.response`), which is the executable content of "this endpoint
  accepts instructions".
- `run.structured_output`: the only false-refusal fixture was the
  reference adapter's disclosed-result case. T1 gains the general form,
  `controls-structured-satisfiable-refused` (`unsatisfiable_control` on
  the `error.response`; a compilable, object-rooted, self-contained
  schema refused as unsatisfiable), and the settlement rule now states
  that direction in words rather than leaving it to follow from "the
  validator and the reference adapter reject the same policies".
- `models.list`: no rule diagnosed refusing a `models.request` on an
  endpoint advertising the catalog. T5a gains
  `models-list-refused-advertised` (`unhonoured_capability`).
- `action.tools.list`: the same, for a list request. T3a gains
  `tools-list-refused-advertised` (`unhonoured_capability`).
- `session.message.delivery.queue`: the stale-limit rule had a positive
  fixture and no negative. T2 gains `queue-limit-unreached-refused`
  (`queue_limit_exceeded` on the `error.response`; the bound unreached
  throughout the window, refused `run_active`).

### Exit criteria

The namespace rule is stated and checked; a pack can be declared, loaded,
and compiled alongside core; a loaded pack's envelopes are validated
strictly while the same envelopes are tolerated without it; containment
refusals are covered; the core fixture corpus passes identically with and
without packs loaded; the corpus-completeness check runs in CI over the
core corpus and over every loaded pack's corpus, and passes with the five
fixtures above in place; and `oap validate -pack` is documented beside
`-mode`.

## T1. Run controls

Unit name: `run-controls`. Planned decision: 0005.

### Scope

Graduate the fail-closed control discipline for all four per-submit
controls, and graduate `model_id` as the first control with native evidence.
`instructions`, `tool_choice`, and `output_schema` get executable shapes and
reference-adapter behavior in the same slice; native adapters advertise them
`unavailable` until a ledger pins per-run evidence (today the only per-run
native surfaces are the Codex and Makai model parameters). Nothing here
changes run lifecycle.

That is one decision covering four controls whose evidence arrives at
different times, and the claim has to be shaped so it never says more
than the evidence does — the same problem T3 solves with sub-units, and
solved here the same way, using the four capability keys the controls
already have. `+run-controls` claims two things. The first is the
*discipline*, common to all four: presence-carrying types, the
fail-closed gate, the refusal ladder, and the `unsatisfiable_control` /
`unapplied_control` machinery. Every endpoint implements it whether or
not it supports a single control, because refusing an unadvertised
control correctly *is* the discipline, and it has native evidence today:
Codex and Makai refuse three of the four. The second is *execution*,
claimed per control and only for the controls the endpoint advertises
above `unavailable`: an endpoint claiming `+run-controls` passes the
discipline fixtures for all four and the executable fixtures for each
control it advertises, and nothing for the ones it does not. Gate item 3
therefore binds per advertised control, not per unit. Decision 0005
graduates the discipline and `model_id` execution on the Codex and Makai
evidence; `instructions`, `tool_choice`, and `output_schema` keep their
frozen shapes and reference-adapter execution, and each graduates as
executable when a native adapter advertises its key against a pinned
ledger, recorded as an amendment to 0005 rather than a fresh design. An
endpoint advertising only `run.model_selection` is fully conformant to
`+run-controls`; one advertising `run.instructions` before that control
has graduated is making a claim the corpus checks but the spec has not
yet endorsed, and the core draft's per-control status is what says
which.

### Wire

No new envelope types. Changes to
[`session.schema.json`](../schema/v0.1/session.schema.json) and
[`capabilities.schema.json`](../schema/v0.1/capabilities.schema.json):

- `tool_choice` keeps its permissive schema (`true`), so every envelope
  that validates today still validates. Its executable shape is a typed
  policy over the advertised catalog, enforced by the validator's
  `run-controls` rules and by adapters rather than by the schema: `{
  "mode": "auto" | "none" | "required" | "named", "name"?: string,
  "allowed"?: [string], "disallowed"?: [string] }`, with `name` present
  when and only when `mode` is `named`, and `allowed`/`disallowed` mutually
  exclusive. Precedence is fixed so no two implementations can read one
  policy differently: `allowed` or `disallowed` filters the catalog first,
  then `mode` applies to the filtered set; every entry of `allowed` and
  `disallowed` must name a tool in the advertised catalog (an unknown entry
  is unsatisfiable, not ignored, so a misspelled `disallowed` entry fails
  closed instead of silently blocking nothing), `named` must name a tool
  in the filtered set, and `required` needs a non-empty filtered set;
  otherwise the policy is unsatisfiable and is rejected before admission
  (`unsupported_feature`, `details.reason: "unsatisfiable"`), never
  resolved by choosing one member over another. The catalog a policy is
  judged against is the session's full catalog, including tools the control
  layer provides at open (T3c), so a policy governs those tools the same
  way. Plain names suffice only because the catalog a policy is judged
  against never carries two tools with the same `name`: the tool
  definition schema does not require it, so T1 itself introduces the rule
  and its diagnostic (`duplicate_tool_name`, on any catalog a
  `tool_choice` is evaluated against), and T3a later extends the same rule
  to every session catalog whatever its sources. In
  Go,
  `MessageSubmitRequest.ToolChoice` stays `json.RawMessage`;
  `protocol.ToolChoice` (today `Mode` and `Name` only) gains `Allowed
  []string` and `Disallowed []string`, and a strict
  `MessageSubmitRequest.ToolChoicePolicy()` accessor decodes the typed
  shape, rejecting unknown members. `ModelID` and `Instructions` become
  `*string`. They are plain `string` with `omitempty` today, and the
  schema permits an empty one, so a decoder cannot tell an absent control
  from `{"model_id": ""}` — and since adapters test these fields with
  `!= ""`, such a request slips past the fail-closed gate entirely, with
  an empty `model_id` read as "no selection" rather than as a control the
  endpoint must refuse. Presence is what the gate is about, so the type
  has to carry it; the alternative, defining empty as absence, would make
  the wire's meaning depend on a Go convention and still leave
  `{"model_id": ""}` silently admitted on an endpoint that advertises
  nothing. A present-but-empty control is a control, judged through the
  gate like any other and, once past it, a model id like any other — one
  that no catalog can list, so it is a catalog miss and takes the miss's
  refusal, `model_not_found` with `details.model_id: ""`, not a separate
  unsatisfiability. The classification matters because the reference
  adapter answers `model_not_found` for every id outside its fixed
  catalog and would have disagreed with a rule calling this one
  `unsatisfiable_control`; an empty string is necessarily outside every
  catalog, so the existing rule already covers it and a second one would
  only contradict the first. It is judged on the correlated response
  under T5a's miss rule, with the pre-catalog retention and the
  precedence ladder applying unchanged — except that the refusal itself
  cannot wait for T5a. `models.list` graduates there and stays optional,
  while Codex and Makai advertise `run.model_selection` from T1, so
  between the two units an empty id would pass the capability gate, find
  no catalog check to classify it, and reach a native codec that reads it
  as the default model: exactly the silent selection this whole pointer
  conversion exists to eliminate, reintroduced for the duration of the
  staging. Emptiness needs no catalog to decide it, because no catalog
  can list an empty id, so the refusal is owed at T1 whether or not one
  was ever served: an admitted empty `model_id` is `model_not_found` with
  `details.model_id: ""` from T1 on. T5a adds only what genuinely depends
  on a catalog — the revision-scoped bookkeeping that classifies
  *non-empty* ids a catalog does not list. T1 therefore carries the
  condition itself, in both directions, rather than only the admission.
  `unsatisfiable_control` is the diagnostic — a present control the
  endpoint cannot satisfy — and its T1 definition below names the empty
  `model_id` as a third condition beside the `tool_choice` and
  `output_schema` ones, since a diagnostic that covered only those two
  could not be asserted here.

  What the wire owes differs from what a fixture asserts, and the empty
  id is the one condition in this unit where the two diverge. Every other
  `unsatisfiable_control` condition is refused with `unsupported_feature`
  and `details.reason: "unsatisfiable"`; this one is refused with
  `model_not_found` and `details.model_id: ""`, because an empty string
  is a catalog miss and the reference adapter already answers
  `model_not_found` for every id outside its catalog. That carve-out is
  stated in the settlement rule below rather than left to be inferred
  from two rules that would otherwise contradict. So the validator
  retains the empty-id condition from the `session.message.submit.request`
  and settles it on the correlated response: an admission is
  `unsatisfiable_control` on the `session.message.submit.response`, and
  an `error.response` must carry `model_not_found` with
  `details.model_id: ""`. A refusal under any other code — `internal_error`
  included, and `unsupported_feature`/`unsatisfiable` included, since the
  carve-out runs the other way too — or without that detail is
  `unsatisfiable_control` on the `error.response`. Without that half an
  adapter could advertise `run.model_selection`, answer `internal_error`
  to an empty id, and pass T1 while avoiding the response the wire
  requires.

  T1's fixtures are therefore `controls-empty-model-id-unadvertised`
  (the capability gate's own refusal),
  `controls-empty-model-id-rejected` (positive; the typed
  `model_not_found` refusal with `details.model_id: ""`),
  `controls-empty-model-id-admitted` (`unsatisfiable_control` on the
  admission) and `controls-empty-model-id-wrong-refusal`
  (`unsatisfiable_control` on the error response; the right rejection
  under `internal_error`). T5a carries `models-select-unlisted` for the
  non-empty miss and the bookkeeping behind it. A
  unit's fixtures must produce exactly the codes it introduces, so a
  fixture cannot be listed before the diagnostic it asserts.
- `output_schema` stays a JSON Schema object, and it must describe a JSON
  object: its root `type` is `"object"` (a `type` list may name only
  `"object"`). `run.completed.result` is `type: "object"` in
  `run.schema.json` and `map[string]any` in Go for every adapter, so a
  root-array or scalar schema could never be met by a conforming success;
  such a schema is unsatisfiable and every adapter rejects it before
  admission (`unsupported_feature`, `details.feature:
  "run.structured_output"`, `details.reason: "unsatisfiable"`). This
  matches the native structured-output surfaces, which accept object roots
  only. It is also self-contained: every `$ref` resolves within the
  document (a fragment or a `$id`-anchored subschema), and a reference to
  anything outside it (an absolute URI, a file, an HTTP resource) is
  unsatisfiable and rejected before admission. Adapters and the validator
  compile it with a loader that refuses every reference outside the
  resources they added themselves (`jsonschema.Compiler.UseLoader` with a
  loader that returns an error; the engine's default loader reads local
  files), and the draft 2020-12 metaschema is embedded in the engine, so
  an untrusted schema can never make an adapter or a validator read a
  local path or fetch a URL. The structured result travels in `run.completed.result` (already on
  the wire) and must validate against the admitted schema. In Go,
  `protocol.RunCompletedPayload.Result` is today `map[string]any` with
  `omitempty`, which drops a valid empty object `{}` from the wire and
  would make a conforming run fail the presence check; T1 changes the
  field to `json.RawMessage` (still `omitempty`, which for a raw message
  omits only `nil`), so presence is preserved exactly as emitted and no
  adapter re-encodes the harness's result. The memory adapter's
  `{"ok": true}` and a schema admitting `{}` both round-trip, and the
  T1 fixture `controls-structured-empty-object` (a schema with no required
  members and an empty-object result, valid) pins the behavior.
- Capability keys, all optional, gated per submit:

  | Key | Governs | Levels in v0.1 adapters after T1 |
  | --- | --- | --- |
  | `run.model_selection` | `model_id` | Codex and Makai `native` (`per_run`); memory `emulated` (fixed catalog); others `unavailable` until evidence |
  | `run.instructions` | `instructions` | memory `emulated`; others `unavailable` |
  | `run.tool_selection` | `tool_choice` | memory `emulated`; others `unavailable` |
  | `run.structured_output` | `output_schema` | memory `emulated`; others `unavailable` |

  `run.model_selection` is new; the other three are already named in the
  core draft. `FeatureSupport.mode` discloses how an emulated control is
  applied: `per_run` (native per-run parameter), `session_mutation` (a
  serialized native config change applied immediately before the run it
  was requested for starts, which changes the session default), or
  `restart` (not offered in this phase).
- Typed error codes on `error.response`: `unsupported_feature` with
  `details.feature` naming the key and `details.reason` distinguishing the
  two conditions it covers: `unadvertised` (control present, capability
  `unavailable` or absent) and `unsatisfiable` (capability advertised, but
  this request's value of the control cannot be honored, such as a tool
  name outside the catalog or an output schema a fixed-output endpoint
  cannot meet); `capability_degraded` with `details.feature` (capability
  `degraded`, key absent from `allow_degraded_features`);
  `model_not_found` with `details.model_id` (`run.model_selection` advertised,
  id not in the effective catalog). All three already appear in the layered
  draft's vocabulary except `model_not_found`.

### Semantics

- A submit carrying a control the endpoint has not affirmatively advertised
  is rejected before admission, before any native write. No submission or run
  identity is allocated.
- `emulated` controls need no opt-in. `degraded` controls need the key in
  `allow_degraded_features`; otherwise `capability_degraded`. Which
  failure a request that trips more than one must report is not decided
  by the order of the controls in the payload but by the single refusal
  precedence the validator applies (see **Refusal precedence** below):
  the rungs first, so an unadvertised control outranks a degraded one
  whichever field carries it — a caller told to stop sending
  `instructions` learns something permanent, where an opt-in it could
  have supplied is a request it can simply reissue — and, among failures
  on the same rung, the lower capability key lexicographically. One order
  governs the whole plan, so an adapter and the validator cannot pick
  different conforming refusals for the same request. Fixture
  `controls-degraded-model-unadvertised-instructions` (positive; a
  `degraded` `model_id` without the opt-in alongside an unadvertised
  `instructions`, refused with `unsupported_feature` naming
  `run.instructions`).
- An admitted `model_id` is authoritative for the run: the submit response
  repeats it in `model_id`, `run.started` repeats it, and `run.completed`
  may not name another model. That last clause needs wire to stand on:
  `run.schema.json`'s `completed` payload carries `final_response`,
  `stop_reason`, `result`, `usage`, and `duration_ms` and no model, so T1
  adds an optional `model_id` to it. The addition earns its place beyond
  making the rule enforceable — it lets a consumer check which model
  produced the final response without correlating back to the admission,
  which matters most where the answer is surprising. It is additive in
  the sense that no existing member changes and no reader needs the new
  one, but the payload is *not* open: `completed` carries
  `additionalProperties: false`, so a strict reader on the unmodified
  bundle rejects the member rather than ignoring it. That is what the
  tolerance step before T1 is for, and why it lands first — under the
  tolerant variant an unknown payload member is ignored as the wire rule
  requires, which is the compatibility this addition relies on. Fixture
  validation stays strict against the bundle at its own revision, so the
  member is added to `run.schema.json` in the same change. The
  validator
  diagnoses `unapplied_control` when a present `model_id` on
  `run.completed` differs from the admitted one, and says nothing when it
  is absent. Absent an admitted `model_id`, the response reports the
  effective model when known, as today.
- `current_model_id` in session state is the model the next `auto`
  submission without `model_id` would use. A `per_run` application leaves it
  unchanged; a `session_mutation` application changes it, and the adapter
  reflects the change rather than restoring the previous default.
- A `session_mutation` application runs immediately before the run it was
  requested for starts, never while another run is started, because the
  started run's admitted model is authoritative until its terminal. For a
  submit admitted `start` that is before admission, as today. For a submit
  admitted `queued` (once T2 is executable) the adapter validates the id
  against the catalog at admission but applies the mutation at promotion:
  `current_model_id` keeps reporting the started run's model until then, a
  queued run cancelled or dropped before start never applies its mutation,
  and a mutation that fails at promotion settles the run pre-start as
  `run.failed` with the typed error admission would have produced. An
  adapter that cannot defer the mutation rejects the combination
  (`run_active`) rather than mutating under a started run.
- `output_schema` binds the run's final response: `run.completed.result` is
  present and conforms, or the run fails with `structured_output_failed`.
  A harness that retries structured output natively (Claude Code's
  `error_max_structured_output_retries`) maps exhaustion to that failure.

### Validator

- `session.message.submit.request` with any control is judged through the
  existing `feature()` gate with the control's key, but on the correlated
  response rather than on the request, because the semantics above make
  refusal the required behavior and a conforming refusal must validate:
  the request's unadvertised or `unavailable` controls are remembered, an
  admission correlated to it (`session.message.submit.response`) is
  `unavailable_capability` on the response, and a correlated
  `error.response` must carry `unsupported_feature` with `details.feature`
  naming the control's key and `details.reason: "unadvertised"`, the
  reason the wire contract above assigns to this condition
  (`unavailable_capability` on the error response otherwise: the refusal
  happened, but under a code, feature, or reason that does not tell the
  caller what to stop sending — `unsatisfiable`, which says the request
  was understood and could not be honoured, is exactly the wrong story
  for a capability the endpoint never offered, and an absent reason
  leaves the caller guessing whether retrying differently would help).
  The same response-time gate serves the explicit queue and steer
  deliveries of T2 and T4, so it holds them to the same reason. The
  stale-revision and missing-descriptor branches of `feature()` stay on
  the request, as for every optional envelope. Fixtures:
  `controls-unadvertised-model` (`unavailable_capability` on the
  admission), the correct rejection `controls-unadvertised-model-rejected`
  (`error.response` with `unsupported_feature`, `details.feature:
  "run.model_selection"`, `details.reason: "unadvertised"`, then no
  admission), which is also the shape of the Codex `instructions`
  rejection T1 keeps, and `controls-unadvertised-wrong-reason` (the right
  code and feature under `details.reason: "unsatisfiable"`).
- **Refusal precedence.** A request can fail several of this plan's
  fail-closed tests at once, and a single `error.response` carries one
  code and one reason, so the retained expectations are ranked rather
  than conjoined. The validator evaluates them in this order and, once
  one owns the response, discharges every lower expectation on that
  request without diagnosis:
  1. **Capability** — a control, delivery, or open-time attachment whose
     key the descriptor omits or advertises `unavailable`:
     `unsupported_feature` with `details.feature` and `details.reason:
     "unadvertised"`.
  2. **Degradation** — an advertised-`degraded` key without the opt-in:
     `capability_degraded` with `details.feature`.
  3. **Unsatisfiability** — the request is understood and offered, but
     its content cannot be honoured (a contradictory `tool_choice`, an
     external `output_schema`, a colliding or dangling tool source, a
     `remote` source under an attach capability that discloses no such
     mode, a `model_id` outside the served catalog): the typed
     `unsupported_feature`/`unsatisfiable` or `model_not_found` each rule
     names.
  4. **State** — the request is well formed and supported but the
     session cannot take it now (`run_active`, `queue_limit_exceeded`,
     `invalid_steer_target`).
  Peers within a rung are ordered too, or the ladder would leave a tie
  unbreakable: a `session.open.request` carrying both `tool_sources` and
  `tools` on a descriptor advertising neither fails the capability rung
  twice, under `action.tool_sources.attach` and `action.tools.provide`,
  and the two demand different `details.feature`. Within a rung the
  expectation whose capability key sorts first lexicographically wins and
  the rest are discharged. The rule is arbitrary only in the sense that
  some rule was needed; it is total, stable, needs no amendment when a
  later unit adds a key, and on the one pair this plan actually produces
  it agrees with the dependency order, since `action.tool_sources.attach`
  precedes `action.tools.provide` and a source must exist before a
  provided tool can cite it. Where a rung's peers are not capability
  failures the same ordering applies to the diagnostic names. Fixture
  `open-unadvertised-sources-and-tools` (positive; both supplied, neither
  advertised, refused under `action.tool_sources.attach`).
  One rule can fail twice over — a `tool_choice.allowed` naming two
  unknown tools, an open carrying two independent source collisions —
  and those expectations share a capability key and a diagnostic name,
  so the ordering is carried down to the offending value itself: among
  peers of the same rule, the winner is the one whose offending member
  has the lowest JSON Pointer, compared segment by segment — object
  member names lexicographically, array indices numerically — and its
  peers are discharged. Pointer order, not serialized document order:
  JSON object member order carries no meaning, and a client, a proxy, or
  the Go decoder this plan proposes may reorder or drop it, so two
  encodings of the same request would otherwise owe different refusals.
  Within an array the pointer's numeric segment is the caller's own
  order, so the first entry it wrote is still the first it is told about.
  The detail the refusal carries (`details.tool`, `details.source`) is
  that member's, so the validator and the adapter name the same entry and
  a conforming refusal cannot be rejected for having picked the other
  one. Fixtures
  `controls-tool-choice-two-unknown-entries` (positive; `allowed` naming
  two tools outside the catalog, refused with `details.tool` naming the
  first) and `tool-source-two-collisions` (positive; two duplicated ids,
  refused with `details.source` naming the first).
  The ladder runs from the most permanent failure to the most transient,
  which is the order in which the caller can act: what it must stop
  sending outranks what it must send differently, which outranks what it
  may simply retry. Every precedence decision elsewhere in this plan —
  the ungated steer, the ungated explicit `queue`, the remote-mode check
  behind the attach gate — is this ladder applied, not a separate rule,
  and each unit's fixtures include one trace where two rungs would
  otherwise claim the same response.
- New diagnostic `degraded_without_optin`: `feature()` today accepts every
  level but `unavailable`, so a request carrying a control, an
  explicit non-`auto` delivery, or a `models.request` (T5a) or
  `action.tools.list.request` (T3a), both of which carry the same field,
  whose key the descriptor advertises
  `degraded` and whose `allow_degraded_features` omits that key is
  remembered, and an admission correlated to it (a
  `session.message.submit.response`, or the `models.response`) is
  diagnosed on the response, while
  a correlated `error.response` must carry `capability_degraded` with
  `details.feature` naming that key (otherwise `degraded_without_optin`
  on the error response, so a refusal under `internal_error` or naming
  the wrong feature does not pass as the typed refusal); for an `auto`
  request the key is the delivery the admission resolved to (`queued`,
  `steered`), judged on the response. The core's mandatory `auto`
  delivery itself is exempt: Claude, Hermes, and DeepSeek advertise
  `session.message.delivery.auto` at `degraded` today with disclosed
  reasons, every existing corpus admits `auto` without
  `allow_degraded_features` (no fixture carries the field), and a caller
  refused `auto` would have no way to submit at all. Its `degraded` level
  is therefore disclosure the caller reads from the descriptor, not a
  consent gate, and the opt-in rule applies to what a caller elects:
  controls, explicit deliveries, and the delivery an `auto` resolves to
  beyond `start`. The positive fixture `controls-auto-degraded-admitted`
  records that this is legal, so the exemption is stated rather than
  accidental. The rejection path
  is the correct one and stays a positive fixture; this rule catches the
  adapter that admits and executes degraded behavior without consent.
- New diagnostic `unapplied_control`: the submit response's `model_id` (when
  the request carried one) differs from the request; `run.started.model_id`
  differs from the admitted model (today's `illegal_run_transition` case
  moves to this code); `run.completed` under an admitted `output_schema`
  lacks `result`, or carries a `result` that does not validate against the
  admitted schema (the validator compiles the schema with the same
  `jsonschema` engine it already uses for the bundle); an
  `action.call.requested` in a run whose admitted `tool_choice` excludes
  that tool (`mode: "none"`, filtered out by `allowed` or `disallowed`, or
  `named` naming another tool), whichever participant owns the call; and,
  at `run.completed`, a run admitted with `mode: "required"` that emitted
  no `action.call.requested`, or with `mode: "named"` that emitted none
  naming that tool (a run that fails or is cancelled first is not judged,
  since the requirement binds a completed response). An adapter that
  cannot make its harness honor `required` or `named` discloses that and
  rejects such a policy as unsatisfiable; it never completes the run as
  if the requirement were met. "Discloses" is machine-readable, not
  editorial: `run.tool_selection`'s `FeatureSupport` carries `modes`,
  the `tool_choice` modes the endpoint can actually enforce, and a
  refusal is conforming only for a mode outside it. Without that, the
  key would promise nothing — an adapter could advertise
  `run.tool_selection`, refuse every `required` and `named` policy as
  `unsatisfiable`, and pass, which is the same empty advertisement
  `max_queued_runs_per_session` was disclosed to prevent on the queue.
  A descriptor advertising the key with no `modes`, or with an empty
  one, is `undisclosed_selection_modes` on the `capabilities.response`.

  The catalog binds in both directions here as it does for models. A
  policy whose named and filtered tools are all carried by the active
  catalog, whose mode is one the endpoint disclosed, and whose filtered
  set is non-empty is satisfiable, and refusing it is
  `unsatisfiable_control` on the `error.response` — the mirror of the
  `models-listed-selection-false-miss` rule, and the more damaging
  direction for the same reason: the caller discards a policy that was
  valid, and re-listing only confirms the tools it was just refused for.
  Where a retained choice is reconciled at the first list (the gap rule
  below), the false refusal is diagnosed there instead, on the same
  terms. Fixtures `controls-tool-choice-listed-false-refusal`
  (`unsatisfiable_control`; a policy naming a listed tool under a
  disclosed mode, refused) and `controls-tool-choice-undisclosed-mode`
  (positive; the same refusal for a mode the endpoint never disclosed). For a run
  admitted under `per_run` (the mode `runState` retains from admission,
  below, together with `defaultModel`, the session default
  `sessionTrack.currentModel` held at that admission), a `session.state`
  snapshot whose `current_model_id`
  differs from that retained default is also `unapplied_control`,
  whether it moved to the run's admitted `model_id` or to any other
  value: the per-run rule leaves the session default untouched, so the
  comparison is against what the default was, not merely against the
  model the run selected; the Codex and Makai overwrite T1 fixes is the
  first case, so the fix is verifiable. The window does not close at the
  run's terminal. A `per_run` application leaves the session default
  untouched for good, not merely for the run's lifetime, and the moment
  an adapter is most likely to write it back is when it emits the
  terminal — so limiting the comparison to the nonterminal window would
  accept a post-`run.completed` snapshot reporting the run's `model_id`
  and let the next control-free submit use the wrong model, which is the
  whole failure the diagnostic exists to catch. The retained default
  therefore survives the terminal as the session's expected default, and
  every later snapshot is judged against it until something the
  validator credits moves it: an admitted `session_mutation`
  application, which sets a new expected default, and nothing else. A
  `capabilities.updated` or a refreshed `capabilities.response` is not
  such an event — it invalidates the descriptor and the catalogs served
  under the old revision, which is why the models and tools bookkeeping
  is discarded at a refresh, but it does not run a session's model back
  to some earlier value, and treating it as a reset would hand every
  adapter an escape hatch: emit an unrelated refresh after a `per_run`
  submit and the next snapshot could report the run's model unchallenged.
  The retained default therefore survives capability changes along with
  terminals. Fixture `controls-per-run-overwrites-default-after-refresh`
  (`unapplied_control`; a `per_run` submit, an unrelated
  `capabilities.updated`, then a snapshot reporting the run's model), and
  fixture
  `controls-per-run-overwrites-default-at-terminal` (`unapplied_control`;
  a `per_run` submit, its `run.completed`, then a snapshot reporting the
  run's model as `current_model_id`). While the run is still queued
  (T2), its retained default follows the applications the validator
  itself credits: an earlier-admitted `session_mutation` run promoted
  before it applies its mutation at that promotion and moves the session
  default (`premature_session_mutation` requires snapshots in that run's
  started window to report its model), so the queued `per_run` run's
  `defaultModel` is rebased to the applied model at that point and a
  snapshot reporting it is conforming; once the `per_run` run has
  started no mutation can apply (a `session_mutation` never runs while
  another run is started), so from its `run.started` the retained
  default is fixed. T2 fixture `queue-per-run-behind-mutation` covers
  the coexistence: a started run, a queued `session_mutation` run, a
  queued `per_run` run admitted after it, then the mutation run's
  promotion and a snapshot reporting its model, no diagnostic.
- New diagnostic `unsatisfiable_control`: a `tool_choice` that is not the
  typed policy, carries both `allowed` and `disallowed`, names a tool in
  its own `disallowed` list or outside its own `allowed` list, or, when the
  trace carries a catalog (capabilities or `action.tools.list.response`,
  plus any `tools` provided at open), lists or names a tool outside that
  catalog or is `required` or `named` against an empty filtered set. The
  same condition covers an `output_schema` that does not compile, and a
  `model_id` that is present and empty — the condition T1 owns outright,
  since no catalog can list an empty id and none is needed to decide it.
  That third condition is the one whose conforming refusal is
  `model_not_found` with `details.model_id: ""` rather than
  `unsupported_feature`/`unsatisfiable`, as set out above and in the
  settlement rule below. A
  non-object root and an external reference are the two named cases, but
  they are not the only ways compilation fails: `{"type": "object",
  "required": "x"}` is wire-valid, object-rooted, and self-contained, yet
  no schema compiler will take it. The rule is therefore the general one
  — any metaschema or compilation failure makes the control
  unsatisfiable — with the two named cases as instances rather than an
  enumeration, and the validator detects an external reference by
  compiling with the refusing loader, never by resolving it. Stating it
  generally is what keeps the two sides aligned: the reference adapter
  compiles every requested schema before admission, so anything it
  refuses must be something the validator also calls unsatisfiable, or an
  adapter could answer `internal_error` for a schema the reference
  rejects and escape the typed refusal. The validator and the reference
  adapter therefore reject the same
  policies: a policy the unit's rules accept is admitted by the
  reference, and one the reference refuses is diagnosed. Fixture
  `controls-structured-uncompilable-schema` (`unsatisfiable_control` on
  the admission; `required` given a string, which the metaschema
  rejects).
  Like every other gate in this plan the condition is judged on the
  correlated response, never on the request, because the wire requires
  the endpoint to refuse an unsatisfiable control and diagnosing the
  request would fail the behaviour it mandates. The validator retains the
  condition from the `session.message.submit.request` (the offending
  control, and the tool or field that makes it unsatisfiable) and settles
  it at the correlated response: an admission is `unsatisfiable_control`
  on the `session.message.submit.response`, and an `error.response` must
  carry `unsupported_feature` with `details.feature` naming the control's
  key, `details.reason: "unsatisfiable"`, and the detail the condition
  identifies (`details.tool` for a `tool_choice` naming or filtering to
  an unavailable tool, `details.field` for an `output_schema`), with a
  refusal under any other code, feature, reason, or without that detail
  diagnosed as `unsatisfiable_control` on the `error.response`. The
  direction runs both ways, and the second is stated rather than implied:
  a control the validator finds satisfiable — a `tool_choice` whose tools
  the catalog carries under a disclosed mode, an `output_schema` that
  compiles — refused under `unsupported_feature`/`unsatisfiable` is
  `unsatisfiable_control` on the `error.response` too, because the
  adapter has claimed a condition the validator can see does not hold.
  Without that, "the validator and the reference adapter reject the same
  policies" would bind the reference alone. The empty `model_id`
  condition settles on the same terms under a different code:
  its conforming refusal is `model_not_found` with `details.model_id: ""`,
  and anything else — including `unsupported_feature` with
  `details.reason: "unsatisfiable"`, which is right for every other
  condition here and wrong for this one — is `unsatisfiable_control` on
  the `error.response`. The check
  runs only when the submit carries a control, so envelopes that do not
  use the unit are untouched. The contradictory-policy and
  external-reference cases therefore appear twice: as positives where the
  typed refusal arrives (`controls-tool-choice-contradictory-rejected`,
  `controls-output-schema-external-rejected`), and as negatives where the
  submit is admitted (`controls-tool-choice-contradictory`,
  `controls-structured-external-ref`) or refused under the wrong shape
  (`controls-unsatisfiable-wrong-refusal`).
- New diagnostic `duplicate_tool_name`: the catalog a `tool_choice` is
  judged against (the descriptor's effective catalog or the latest
  `action.tools.list.response` for the session) lists two tools with one
  `name`, whatever their owners, so no `named` or list entry can be
  ambiguous. The descriptor's effective catalog is the union of its
  top-level `tools` and every `layers.*.tools`, since a valid descriptor
  may publish its catalog under a layer alone; the validator normalizes
  both into one list (with their `sources`) wherever this plan says "the
  descriptor's `tools`", including the open-time collision and refresh
  checks under T3. Introduced here so the `run-controls` unit is sound before
  T3; T3a applies the same diagnostic to every session catalog.
- `runState` gains `controls` (the admitted request's control set) so the
  checks above are keyed off the request, not the response, and
  `capabilityRevision` with the `run.model_selection` mode and the levels
  of the admitted controls as the descriptor active at admission declared
  them. Every per-run judgement above and under T2 (the `per_run`
  snapshot rule, `premature_session_mutation`, the degraded opt-in) reads
  the run's retained mode and levels, never the current descriptor's: the
  core draft fixes the effective capability revision for an admitted run,
  so a `capabilities.updated` that changes the mode while a controlled run
  is nonterminal, started or still queued, changes how later admissions
  are judged and never how that run is. A queued run's `session_mutation`
  therefore applies at promotion under the mode it was admitted with
  (T2 fixture `queue-mode-refreshed-while-queued`).

### Reference adapter

`adapter/memory.go` advertises `run.model_selection` (`emulated`, catalog of
two fixed ids), `run.instructions`, `run.tool_selection`, and
`run.structured_output` (`emulated`). It echoes the admitted model on the
response and `run.started`; prepends `instructions` to the scripted text so
the effect is observable; and applies every `tool_choice` member: `mode:
"none"` skips the scripted tool, a `disallowed` list naming `scripted_tool`
skips it, an `allowed` list that omits `scripted_tool` skips it, `mode:
"required"` or `mode: "named"` with `name: "scripted_tool"` calls it, and a
`named` choice or list entry naming a tool outside its catalog fails before
admission with `unsupported_feature` (`details.reason: "unsatisfiable"`).
Its structured
result is the fixed object `{"ok": true}`: at admission it compiles the
requested `output_schema` with the refusing loader and rejects, before
any identity is allocated, a schema carrying an external reference and
any schema that object does not satisfy (`unsupported_feature`,
`details.feature: "run.structured_output"`, `details.reason:
"unsatisfiable"`); under an accepted schema `run.completed` carries that
object as `result`, so the reference never emits a nonconforming success.
A fixed result is an endpoint-specific constraint, and leaving it in
`FeatureSupport.reason` — prose, for a human — would let any adapter
advertise `run.structured_output` and refuse every usable schema while
the validator, unable to know the constraint, accepted each refusal as
typed and conforming. So T1 makes it machine-readable:
`FeatureSupport` (`protocol/control.go:39`) gains an optional
`constraints` object whose member for this key is `fixed_result`, the
exact object every `run.completed` under an accepted schema will carry.
Disclosing it turns the refusal into something checkable in both
directions. Where an endpoint declares `fixed_result`, a refusal of a
schema that object *does* satisfy is `unsatisfiable_control` on the
`error.response` — the endpoint said the result would do and then
refused it — and an admission of a schema it does not satisfy is
`unsatisfiable_control` on the admission, since the run could only
complete nonconforming. Where no `fixed_result` is declared the endpoint
is claiming no such constraint, and a refusal citing one is
`unsatisfiable_control` all the same: an adapter cannot both withhold
the constraint and rely on it. The promise also binds at the other end,
or it is only half a promise: where `fixed_result` is declared, every
`run.completed` under an admitted `output_schema` must carry exactly
that object as `result` (`unapplied_control` otherwise). Checking the
result against the schema alone would not do it — a permissive schema
admits many objects, so an endpoint could promise `{"ok": true}`, accept
the schema, emit `{}`, and satisfy every rule while breaking the
machine-readable commitment a consumer planned against. Fixtures
`controls-structured-fixed-result-refuses-satisfiable`
(`unsatisfiable_control`; a schema the disclosed result satisfies,
refused), `controls-structured-undisclosed-constraint`
(`unsatisfiable_control`; a refusal for an unmet result on a descriptor
declaring no `fixed_result`) and
`controls-structured-fixed-result-not-emitted` (`unapplied_control`; a
declared `{"ok": true}`, a permissive admitted schema, and `{}` at
`run.completed`). Unknown model ids fail with `model_not_found`.

### Native evidence

Codex graduates `model_id` at `native` (`per_run`): its adapter already passes
`request.ModelID` to `turn/start`; T1 adds the capability key, a corpus case
`model-per-turn` (the `turn/start` frame carrying the requested model and
`turn/started` attributed to it), the rejection case for an unadvertised
`instructions`, and a fix for the same `current_model_id` overwrite Makai
has: the Codex session today assigns the per-turn model to
`state.CurrentModelID`, which the `per_run` rule above forbids, so the
session default stays the configured thread model and the per-run model
lives on the admission response and `run.started` only. Makai lands in the
same slice as the second native source: its adapter already sends
`model_ref` on every `agent_message` and echoes it, so T1 adds the key and
a `model-per-message` corpus case, and stops the adapter overwriting
`current_model_id` with a per-run value in the same way. Follow-on
evidence for the other adapters is gated on new ledger entries: Claude
`set_model` (`session_mutation`), pi `set_model` (`session_mutation`), ACP
`session/set_config_option` (`session_mutation`, degraded because
attribution is unsafe under concurrent changes). A `session_mutation`
adapter that also advertises `session.message.delivery.queue` (pi is the
first candidate) must show the deferred application in its corpus: a
queued submit naming another model while a run is started, with the
native mutation frame appearing only after the started run's terminal.

### Surfaces

- `adapter`: new `*adapter.UnsupportedControlError{Feature}` and
  `*adapter.DegradedControlError{Feature}` wrapping `ErrUnsupportedInput`, so
  codecs can emit the typed codes; `ErrModelNotFound`.
- `serve/servehttp` and the stdio frontend: `writeSubmitError` maps the new
  errors to `unsupported_feature`, `capability_degraded`, and
  `model_not_found` with `details`; the current `invalid_submission` mapping
  stays for malformed input.
- `client` and `clients/ts`: no new operations; `ServerError` exposes
  `details`; e2e tests drive a `model_id` submit against the memory adapter
  and assert the echoed model on `run.started`.

### Fixtures

Positive: `controls-model-admitted`, `controls-instructions-emulated`,
`controls-structured-output`, `controls-structured-empty-object` (a
schema with no required members and an empty-object `result`, valid),
`controls-auto-degraded-admitted` (an `auto` submit admitted without
`allow_degraded_features` on a descriptor whose `auto` delivery is
`degraded`, legal under the exemption above),
`controls-unadvertised-model-rejected` (`error.response` with
`unsupported_feature`, `details.feature: "run.model_selection"`,
`details.reason: "unadvertised"`, then no admission; validated as a
correct rejection). Negative:
`controls-unadvertised-model`
(`unavailable_capability` on the admission), `controls-model-mismatch`
(`unapplied_control`),
`controls-structured-missing-result` (`unapplied_control`),
`controls-structured-nonconforming-result` (`unapplied_control`; `result`
present but invalid against the admitted schema),
`controls-tool-choice-contradictory` (`unsatisfiable_control` on the
admission; `required` with the only tool disallowed, and `named` outside
its own allowlist), `controls-tool-choice-unknown-entry`
(`unsatisfiable_control` on the admission; an `allowed` list naming a
tool outside the catalog the trace carries),
`controls-tool-choice-contradictory-rejected` (positive, `valid: true`,
no diagnostic; the same policy refused with `unsupported_feature`,
`details.feature: "run.tool_selection"`, `details.reason:
"unsatisfiable"`, `details.tool`),
`controls-unsatisfiable-wrong-refusal` (`unsatisfiable_control` on the
`error.response`; the same policy refused with `internal_error`),
`controls-instructions-refused` (`unsatisfiable_control` on the
`error.response`; `instructions` refused on an endpoint advertising
`run.instructions` — the control has no unsatisfiability condition, so
every refusal is false; the `honour` fixture T0's corpus-completeness
check requires), `controls-structured-satisfiable-refused`
(`unsatisfiable_control` on the `error.response`; a compilable,
object-rooted, self-contained `output_schema` refused as unsatisfiable;
the `honour` fixture for `run.structured_output`),
`controls-tool-choice-ignored` (`unapplied_control`; `action.call.requested`
under `mode: "none"`),
`controls-structured-non-object-schema` (`unsatisfiable_control` on the
admission; a root-array `output_schema`), `controls-structured-external-ref`
(`unsatisfiable_control` on the admission; an `output_schema` with an
absolute `$ref`, which the validator must diagnose without any resource
access), `controls-output-schema-external-rejected` (positive, `valid: true`, no
diagnostic; the same schema refused with `unsupported_feature`,
`details.feature: "run.structured_output"`, `details.reason:
"unsatisfiable"`, `details.field`),
`controls-required-without-call` (`unapplied_control`; `mode: "required"`,
`run.completed` with no call), `controls-named-without-call`
(`unapplied_control`; `named` naming `scripted_tool`, `run.completed` with
no call),
`controls-tool-choice-ambiguous-name` (`duplicate_tool_name`; two native
tools sharing a name in the descriptor, then a `named` policy),
`controls-degraded-without-optin` (positive, `valid: true`, no
diagnostic; `error.response` with `capability_degraded` then no
admission),
`controls-degraded-admitted-without-optin` (`degraded_without_optin`; the
same request admitted), `controls-degraded-wrong-refusal`
(`degraded_without_optin` on the error response; the same request
refused with `internal_error`), `controls-per-run-overwrites-default`
(`unapplied_control`; a `per_run` descriptor, a submit with `model_id`,
then a snapshot reporting it as `current_model_id`),
`controls-per-run-changes-default` (`unapplied_control`; the same, but
the snapshot reports a third model that is neither the retained default
nor the admitted one).

### Exit criteria

The five gate items for the discipline and for `model_id` execution, with
gate item 3 met per advertised control as the scope states; the Codex and
Makai descriptors advertise `run.model_selection`; the core draft's
"Message Submit And Run Admission" section marks `model_id` executable
and the other three "shape frozen, evidence pending", each moving to
executable by an amendment to 0005 when its native evidence lands. The
unit does not wait for all four, and does not claim all four.

## T5a. Models catalog

Unit name: `models`. Planned decision: 0006.

### Scope

Graduate `models.request`/`models.response` as a session-scoped control-plane
query. Model resolution, aliases, pricing, and cache refresh stay out.

### Wire

New envelope types `models.request` and `models.response` added to the
envelope `oneOf`, with their payload definitions in
[`capabilities.schema.json`](../schema/v0.1/capabilities.schema.json)
beside the other control-plane payloads (initialize and capabilities), so
the bundle's file inventory and the manifest schema are untouched.

```json
{ "type": "models.request", "session_id": "s1",
  "payload": { "session_id": "s1" } }
{ "type": "models.response", "in_reply_to": "…", "session_id": "s1",
  "capability_revision": "…",
  "payload": {
    "session_id": "s1",
    "current_model_id": "provider/model-a",
    "models": [
      { "id": "provider/model-a", "display_name": "Model A",
        "provider_id": "provider", "context_window": 200000,
        "features": { "model.reasoning.output": { "level": "native" } } }
    ]
  } }
```

`ModelDescriptor` fields: `id` (required, `minLength: 1`, unique within a
response, the value `model_id` accepts — non-emptiness is a schema
constraint rather than a validator rule because T1 refuses an empty
`model_id` unconditionally, so a catalog that listed one would offer a
picker a value the endpoint is required to reject, and the two rules
would contradict each other on the wire; `current_model_id` carries the
same constraint for the same reason),
`display_name`, `provider_id`, `context_window`, `features` (the layered
draft's `model.*` keys as `FeatureSupport`), `default` (boolean; at most one
per response). `current_model_id` repeats session state. Both envelopes
are session-scoped in the envelope `oneOf`: `models.request` extends the
`session` base that requires the top-level `session_id`, as
`session.state.request` does, and the payload's `session_id` must agree
with it. The payload also carries an optional `allow_degraded_features`,
the same carrier `session.message.submit.request` has
(`protocol/control.go:156`), because the opt-in is per request and the
catalog query is a request of its own: Claude exposes `models.list` at
`degraded`, and without a field to consent on, a caller could never
construct the consenting query — the endpoint would have to refuse every
catalog request with `capability_degraded` or serve degraded behaviour
without consent, and T1 forbids both. The rule is T1's, unchanged: a
`models.request` on a descriptor advertising `models.list` as `degraded`
whose `allow_degraded_features` omits the key is refused
`capability_degraded` with `details.feature: "models.list"`, and a
`models.response` correlated to it is `degraded_without_optin`. Capability
key: `models.list` (already named). The catalog is part of the capability
snapshot: a catalog change is a `capabilities.updated` invalidation on
endpoints that advertise `capabilities.updates`, and a static endpoint may
serve one catalog for its lifetime.

### Semantics

- `models.request` is session-scoped because every native source is a
  process or session surface (pi, Claude, Makai, ACP) or trivially scoped to
  one (OpenCode). The response is the effective catalog for that session.
- A `model_id` accepted under `run.model_selection` must be a listed `id`
  when `models.list` is advertised; otherwise `model_not_found`.
- Degraded catalogs disclose why: Claude's is the recurring `system/init`
  model list (`degraded`, refreshed per turn), Makai's is `unavailable` until
  its failure behavior is exercised.

### Validator

- `models.request` and `models.response` join the request/response
  correlation and scope checks: each envelope's top-level `session_id` must
  agree with its payload (`scope_mismatch`), and the response must repeat
  the request's session.
- `models.response` invokes the existing `feature()` gate with
  `models.list` (`unavailable_capability` when the descriptor omits the
  key or advertises it `unavailable`), so a catalog served without being
  advertised fails the gate's rule that nothing is applied without being
  advertised; the request is remembered rather than gated, as T1 and T2
  do for controls and deliveries, and a correlated `error.response` must
  carry `unsupported_feature` with `details.feature: "models.list"` and
  `details.reason: "unadvertised"`, the reason the ladder assigns to
  every capability-rung refusal (`unavailable_capability` on the error
  response under any other code, feature, or reason), so an endpoint that
  correctly refuses the query is conforming and one whose refusal points
  the caller at a value to change is not. Fixture
  `models-unadvertised-wrong-reason`.
- `current_model_id` on `models.response` must equal an effective session
  model the validator tracks at some point between the `models.request`
  and its correlated response, not the value at response arrival alone.
  A catalog overlapping a `session_mutation` is captured at an instant
  the trace cannot name: the adapter may read the old model and have the
  mutation's run event reach the trace first, and the reverse ordering
  can show the new value before the validator has observed the mutation.
  Either would be reported as a mismatch against a single endpoint value,
  though both are valid stale or early reads, so the rule takes the set
  of values the model held across the window — the same treatment the
  snapshot rules take — and diagnoses only a value that was never the
  session's model within it. The window is bounded by the trace, so it
  cannot see a mutation that happened inside the adapter before capture
  whose run event drains after the response — the validator would observe
  only the old model throughout and diagnose an accurate catalog. The
  response therefore carries its own position: `models.response` gains
  `as_of_model_event`, `{ "run_id", "sequence" }`, naming the last
  model-affecting event it reflects, absent when it reflects none. It is
  compound, and the same shape as `session.state.as_of.model_run_sequence`
  beside it, because sequences restart per run: a session with two
  model-mutating runs can hold several events at one sequence value, and
  a bare number could not say which an ahead-of-trace catalog meant. Where it is present
  the value is judged at that point rather than across the window, and an
  entry naming a position the trace has not reached is held and
  reconciled when it arrives, as a snapshot's `as_of_sequence` is; where
  it is absent the window rule stands, since an endpoint that reports no
  position is claiming no knowledge the trace lacks. Fixtures
  `models-current-model-mutation-in-flight` (positive; a catalog
  capturing the pre-mutation model whose response follows the mutation's
  application) and `models-current-model-ahead-of-trace` (positive; a
  catalog reporting the post-mutation model with an `as_of_model_event`
  the mutation's event only reaches afterwards). The model the validator tracks is
  (`sessionTrack.currentModel`: the latest
  `session.open.response` or `session.state` value, advanced by a
  `session_mutation` application at the run it applies to; the open
  response is decoded as `SessionState`, which is what
  `session.schema.json` aliases `openResponse` to, because
  `protocol.SessionOpenResponse` today carries no `current_model_id`,
  and T1 adds that member to the Go type so the daemon and adapters
  populate on the wire what the schema already promises). A value the
  session never held in the window is `session_state_mismatch`; a picker
  is never shown a current model the session does not report. A nonempty `current_model_id` must also name
  one of the response's own model ids (`model_not_in_catalog`, with
  `details.field: "current_model_id"`): a picker shown a current model
  the catalog does not describe could not resolve it, and re-selecting
  the same id would be refused by the catalog rule, so the adapter that
  serves such a catalog is diagnosed rather than the caller.
- The catalog binds in both directions. A refusal is checked against it
  as well as an admission: when the active `native` or `emulated` catalog
  *does* list the requested id and the correlated response is an
  `error.response` carrying `model_not_found`, that is
  `model_not_in_catalog` on the `error.response`, naming the id and the
  catalog that listed it. A catalog is a promise that its ids are
  selectable, and a false miss is the more damaging failure of the two:
  the client discards a selection that was valid, and re-listing only
  confirms the id it was just told does not exist. The rule runs
  alongside the miss rule and under the same precedence — where a higher
  rung owns the response it is discharged, as a catalog miss is. Fixture
  `models-listed-selection-false-miss`.
- New diagnostic `model_not_in_catalog`: an admitted `model_id` after a
  `models.response` in the same trace names an id the response did not
  list, or a `models.response` whose own `current_model_id` is not among
  its ids (above). The catalog is stored with the `capability_revision` it was served
  under: a `capabilities.updated` or a new `capabilities.response` that
  changes the active revision discards it, so no admission is judged
  against a stale catalog (a newly added model is not falsely diagnosed,
  a removed one is not silently accepted) until a `models.response` under
  the new revision replaces it; the `feature()` gate already rejects a
  `models.response` citing a stale revision. An admission with `model_id`
  under a revision whose catalog the trace has not yet served is not
  skipped but retained (`sessionTrack.unjudgedModels`: the id and the
  admitting response), and the first `models.response` under that
  revision reconciles them: every retained id the catalog does not list
  is `model_not_in_catalog` on that response, with `details` naming the
  admission, so an adapter cannot accept an unlisted model in the gap
  between a refresh and its catalog, or before its first catalog, and
  have the gap hide it. Reconciliation runs at `native` and `emulated`
  `models.list` only; at `degraded` the catalog refreshes per turn and an
  earlier admission may have matched a list that was never served.
- A mismatch is judged on the correlated response, not on the admission
  alone, so a refusal cannot escape it. The rule is scoped to a submit
  whose descriptor advertises `run.model_selection` as available, per
  the wire rule above: on an endpoint that serves a catalog without
  advertising selection (OpenCode, exactly as the T1 table orders it —
  `models.list` native, selection unavailable) the mandatory refusal is
  T1's `unsupported_feature` with `details.reason: "unadvertised"`, and
  demanding `model_not_found` there would leave no conforming response,
  so the T1 control gate wins and this rule stands down. Where selection
  is advertised and a catalog has been served under the active revision
  at `native` or `emulated`, a submit whose `model_id` names an id that
  catalog does not list is retained from the request
  (`sessionTrack.pendingModelMiss`: the requested id and the request's
  envelope id) and re-evaluated at the correlated response. An admission
  is `model_not_in_catalog` as above; an `error.response` must carry
  `model_not_found` with the requested id in `details.model_id`, and a
  refusal under any other code — `internal_error` most of all — or
  without that detail is `model_not_in_catalog` on the `error.response`,
  with `details` naming the requested id and the refusing code, so the
  adapter that swallows a catalog miss behind an untyped or
  undiagnosable failure is diagnosed exactly as the one that accepts it.
  A refused selection made before the first catalog under the active
  revision is retained too, alongside the admitted ones, in
  `sessionTrack.unjudgedModels` (which records the requested id, the
  correlating response, and whether it was admitted or refused) — unless
  a higher rung already owned that response. A submit carrying both an
  unadvertised control and a selectable-but-unknown model is answered by
  the capability rung, and that answer was conforming when it was given;
  a catalog arriving later cannot retroactively make it owe
  `model_not_found`, because precedence is a property of the request, not
  of what the trace learns afterwards. Such a selection is discharged at
  the response rather than retained, and `unjudgedTools` discharges an
  in-gap `tool_choice` on the same terms. Retention runs the other way
  too: a lower rung cannot settle while a higher one is still unjudged.
  An adapter that knows an id is absent must answer rung 3 even before
  the trace has a catalog to prove it — a busy session with queueing
  unavailable and an unknown-but-selectable `model_id` is owed
  `model_not_found`, not `run_active` — and the state rung would
  otherwise diagnose that refusal as `illegal_run_transition` at the
  response, a verdict the later catalog could not retract. So while a
  submit carries a model or tool condition the trace cannot yet judge
  (no catalog under the active revision), every lower-rung expectation
  on that submit is held rather than diagnosed, and the first catalog
  under the revision settles both: if it omits the id or tool, rung 3
  owned the response and the held expectation is discharged; if it lists
  them, there was no higher-rung failure and the lower rung is judged
  then, against the response it was correlated to. The same holds for
  `unjudgedTools`.
  Holding is not a reprieve. The catalog is an optional query no client
  need ever make, so a trace can end with the expectation unsettled, and
  what is held is only the *choice* between two rungs, never whether the
  response was conforming at all. Both branches name a typed code, so a
  response that satisfies neither is wrong under either outcome and is
  diagnosed at once, without waiting: an `internal_error` to a busy
  submit naming an unknown model cannot become right if the catalog
  lists the id and cannot become right if it omits it, and is diagnosed
  on the `error.response` immediately, under whichever code the held
  lower-rung expectation carries — `illegal_run_transition` for the
  busy-session refusal in this example, where queueing is unavailable,
  and `queue_limit_exceeded` only where the lower rung is an advertised
  queue bound. Only a
  response consistent with one branch is held, and the trace's end
  settles what remains: at the final envelope the validator sweeps the
  retained expectations, and a held response whose code belongs to the
  lower rung is judged as if the catalog had listed the id (there is no
  evidence of a miss, and diagnosing one on silence would convict an
  adapter for a query nobody made), while a held response carrying the
  higher rung's typed code stands. Fixtures
  `queue-busy-unknown-model-impossible-refusal`
  (`illegal_run_transition`; `internal_error` to the busy in-gap submit
  on an endpoint without queueing, diagnosed at the response, no catalog
  in the trace) and `queue-busy-unknown-model-no-catalog` (positive; the same
  race refused `model_not_found`, the trace ending before any catalog).
  Fixtures `queue-busy-unknown-model-refused` (positive;
  a busy `auto` selecting an unlisted id before the catalog, refused
  `model_not_found`, the catalog omitting it afterwards) and
  `queue-busy-listed-model-wrong-refusal` (the same race where the
  catalog does list the id, so the held `run_active` expectation is
  judged and an `internal_error` refusal is diagnosed).
  For a retained selection, when
  the first `models.response` under that revision arrives and omits the
  id, a retained admission is `model_not_in_catalog` as before and a
  retained refusal is held to exactly the code-and-detail test the
  immediately judged path applies: `model_not_found` with the requested
  id in `details.model_id`, and any other code, or `model_not_found`
  without that detail or naming a different id, is `model_not_in_catalog`
  on that `error.response`. Deferring the judgement must not weaken it —
  a refusal the caller cannot act on is no more conforming for having
  preceded the catalog — so refusing an unlisted model with
  `internal_error`, or with an undiagnosable `model_not_found`, in the
  gap before the catalog is no safer than admitting it. Fixtures
  `models-unlisted-selection-refused` (the typed refusal, a positive),
  `models-unlisted-selection-wrong-refusal`,
  `models-unlisted-selection-missing-detail` (`model_not_found` without
  `details.model_id`), `models-unadvertised-selection-unlisted`
  (positive; the T1 `unadvertised` refusal against an unlisted id on an
  endpoint that does not advertise selection) and
  `models-refused-before-catalog` (the refusal precedes the first
  catalog and is reconciled when it arrives) and
  `models-refused-before-catalog-missing-detail` (a `model_not_found`
  without `details.model_id` precedes the catalog that omits the id).
- New diagnostic `unannounced_catalog_change`: a second `models.response`
  under the same `capability_revision` whose catalog differs from the
  stored one in any descriptor, compared whole (`id`, `display_name`,
  `provider_id`, `context_window`, `features`, `default`), not only in
  its set of ids, at `native` or `emulated` `models.list`; the wire rule
  makes a catalog change a capability invalidation, so availability may
  not change without `capabilities.updated`. At `degraded` the
  descriptor's `reason` discloses per-turn refresh (Claude's `system/init`
  list) and the check is skipped, which is what degraded means here.
- New diagnostic `ambiguous_default_model`: a `models.response` with more
  than one descriptor carrying `default: true`, so the at-most-one rule
  above is enforced rather than stated.
- New diagnostic `duplicate_model_id`: a `models.response` with two
  descriptors sharing `id`, so an accepted `model_id` denotes exactly one
  descriptor's metadata.
- The `features` map on a descriptor is collected into the trace's catalog
  for the `model_not_in_catalog` check only; it does not gate run events.

### Reference adapter

`adapter/memory.go` lists its two fixed ids with `default` on the first;
`Models` is deterministic and revision-stable.

### Native evidence

OpenCode graduates first: `model.list` and `provider.list` are pinned native
catalog routes (not yet in the adapter's HTTP client, which today calls only
session, prompt, and event routes); the corpus case `models-list` decodes the
pinned response shape through the production HTTP client and projects
`ModelDescriptor` records with `provider_id` from the provider list.
pi follows with `get_available_models`; Claude at `degraded` from the
`system/init` frame; Makai stays `unavailable` at its pin.

### Surfaces

The request carries the degraded opt-in (wire, above), so every surface
has to be able to say it, or a `degraded` catalog — Claude's — is one no
client can lawfully read: the hub would owe `capability_degraded` to
every call, and the documented `models-list-degraded-optin` request would
be unconstructible. The parameter therefore threads through all five
layers as one value, `allow_degraded_features`, with the same meaning at
each.

- `adapter`: optional interface `adapter.ModelLister` on `Session`
  (`Models(ctx, protocol.ModelsRequest) (protocol.ModelsResponse, error)`),
  discovered by type assertion so existing `Session` implementations
  compile unchanged; the hub returns `unsupported_feature` for sessions
  whose adapter lacks it. `protocol.ModelsRequest` is the payload struct,
  `AllowDegradedFeatures []string` included, so the adapter sees exactly
  what the wire said and applies T1's opt-in rule itself.
- `serve`: `Session.Models(ctx, protocol.ModelsRequest)`; the hub
  forwards the request unchanged.
- `serve/servehttp`: `GET /sessions/{id}/models` returning `models.response`
  with a daemon-minted correlation id, mirroring `GET
  /adapters/{name}/capabilities`. The opt-in travels as a repeatable
  `?allow_degraded=<key>` query parameter — a GET carries no body, and a
  header would hide a wire-visible field from logs and curl — which the
  daemon maps onto the payload's `allow_degraded_features` before
  minting the `models.request`. The stdio op `models` takes an
  `allow_degraded_features` field directly.
- `client`: `Session.Models(ctx, ...ModelsOption)` with
  `AllowDegraded(keys ...string)`; `clients/ts`:
  `session.models({ allowDegradedFeatures? })`. Both send the query
  parameter (or the stdio field) only when the option is given, so an
  unmodified call is byte-identical to today's.

### Fixtures

T1's empty-control rule keeps both its fixtures, because emptiness is
decided without a catalog; what lands here is only the catalogued miss
for non-empty ids, `models-select-unlisted` below.

Positive: `models-list-then-select` (catalog, then a submit selecting a
listed id), `models-list-refused-advertised` (`unhonoured_capability`
on the `error.response`; a `models.request` refused on an endpoint
advertising `models.list` as available, or `degraded` with the opt-in —
the `honour` fixture T0's corpus-completeness check requires),
`models-list-degraded-optin` (positive; a `models.request` carrying
`allow_degraded_features: ["models.list"]` served by a `degraded`
catalog), `models-list-degraded-without-optin` (`degraded_without_optin`
on the `models.response`; the same query without the field, served
anyway), `models-list-degraded-refused` (positive; the same query
refused `capability_degraded` with `details.feature: "models.list"`),
`models-unadvertised-rejected` (`error.response` with
`unsupported_feature`, `details.feature: "models.list"`, against an
unadvertised `models.list`; the validator section defines this correlated
refusal as the conforming answer, so it carries `valid: true` and no
diagnostic — a fail-closed refusal is behaviour to require, never to
diagnose). Negative: `models-select-unlisted` (`model_not_in_catalog`),
`models-two-defaults` (`ambiguous_default_model`),
`models-request-scope-mismatch` (`scope_mismatch`; envelope and payload
`session_id` differ), `models-response-scope-mismatch` (`scope_mismatch`),
`models-unadvertised` (`unavailable_capability` on the response; a
catalog served unadvertised), `models-duplicate-id`
(`duplicate_model_id`), `models-empty-id` (schema; a descriptor with
`id: ""`, which T1's unconditional refusal would otherwise contradict), `models-current-mismatch`
(`session_state_mismatch`; state reports one model, the catalog another),
`models-current-not-listed` (`model_not_in_catalog`; state and the
response agree on one model, the catalog lists only another),
`models-select-removed-after-refresh` (`model_not_in_catalog`; a model
listed under revision 1, dropped by the revision 2 catalog, then
selected), `models-catalog-mutates-within-revision`
(`unannounced_catalog_change`; two responses under one revision with
different ids), `models-catalog-metadata-mutates-within-revision`
(`unannounced_catalog_change`; the same ids, one descriptor's `default`
and `context_window` changed). Positive as well: `models-refresh-replaces-catalog` (a model
added by the revision 2 catalog is selected after `capabilities.updated`,
and one selected between the refresh and the new catalog is retained and
found listed when that catalog arrives). Negative as well:
`models-select-in-gap-unlisted` (`model_not_in_catalog` on the revision 2
`models.response`; a model accepted between `capabilities.updated` and
the new catalog, which then omits it).

### Exit criteria

The five gate items; OpenCode advertises `models.list` `native`; the
conformance draft's `+models` unit text cites the fixtures.

## T2. Queue delivery

Unit name: `queue`. Planned decision: 0007.

### Scope

Graduate explicit `delivery: "queue"` requests and the `auto` to `queue`
resolution on a busy session, which means admitting a second nonterminal run
per session: one started run plus bounded queued reservations. Decision 0002
already fixed the queued admission shape and pre-start settlement; T2 adds
overlap, ordering, and state.

### Wire

No new envelope types. Additive fields:

- `session.state` gains `active_runs`: an ordered list of every nonterminal
  run, `[{ "run_id", "status", "relationship": "primary", "queue_position"?,
  "as_of_sequence"?, "admitted_submit_requests"? }]`, in admission order,
  with
  `as_of_sequence` naming the last sequence of that run the entry
  reflects and `admitted_submit_requests` the submit requests on it the
  entry reflects as admitted (the
  capture positions the validator judges the entry at, below), with `queue_position` on queued entries
  (1-based). `active_run_id` keeps naming the started run, or is absent when
  only queued runs remain (session status `queued`). Each entry also carries
  `pending_interactions: [interaction_id]` (additive), the run's
  unresolved permission and user-input interactions, so the single-run
  recovery path this unit requires can read the id it needs from a
  T2-only endpoint; T3c extends the list to control-owned calls and T4
  adds `pending_steers`, with settled steers on the session-level
  `settled_steers` surface T4 specifies.
- `capabilities.response` gains optional `limits`: `{
  "max_active_runs_per_session": int, "max_queued_runs_per_session": int }`,
  where `max_active_runs_per_session` bounds the nonterminal set, which is
  what `active_runs` lists (started run plus queued reservations; it is
  the wire projection of `adapter.Descriptor.MaxActiveRunsPerSession`,
  whose v0.1 value of 1 therefore means no queue at all), and
  `max_queued_runs_per_session` bounds the queued subset. Both are
  admission bounds: no admission may raise the set above the bound
  advertised at that admission, and a refresh that lowers a bound below
  a session's current set grandfathers that set and admits nothing more
  until it falls below the new bound (validator, below). A descriptor that
  advertises `session.message.delivery.queue` as available must disclose
  `max_queued_runs_per_session`, because otherwise the capability
  promises nothing: with no bound present the refusal validation never
  engages, and an adapter could advertise queueing, omit `limits`, and
  refuse every queued submission with `run_active` while remaining
  conforming. Advertising a queue is a claim that some submission will be
  queued, and the bound is what makes the claim checkable — one is
  enough, and `1` is an honest answer — but it must be at least one. A
  disclosed `max_queued_runs_per_session` of `0`, or any nonpositive
  value, is the same empty promise with a number attached: every queued
  reservation exceeds it, so the adapter refuses them all and still
  claims support, which is exactly what the disclosure was added to
  prevent. The wire constrains both limits to `minimum: 1`, and a
  descriptor advertising the capability with no
  `max_queued_runs_per_session`, or with a nonpositive one, is
  `undisclosed_queue_limit` on the `capabilities.response` (fixtures
  `queue-advertised-without-limit` and `queue-advertised-zero-limit`). An
  endpoint that genuinely cannot queue advertises the capability
  `unavailable` rather than available with a bound of zero. `degraded`
  is held to the same disclosure: the caller opts in and is entitled to
  the same promise, and without a bound the limit validation has nothing
  to test, so an adapter could take the opt-in and refuse every explicit
  queue submission with `run_active` — the empty promise this rule
  exists to prevent, reached by another door. Only an `unavailable` or
  absent capability is exempt, because neither claims anything.
  The two bounds have to agree, or the disclosure can be satisfied by
  numbers that still forbid what the capability claims. Queued
  reservations are listed in `active_runs` and counted by
  `max_active_runs_per_session`, so an endpoint advertising the queue
  with `max_active_runs_per_session: 1` beside
  `max_queued_runs_per_session: 1` has disclosed a positive queue bound
  the active bound can never let it reach: one started run fills the
  active set, and every queued reservation beside it exceeds that set, so
  the adapter refuses every busy submission with `run_active` and passes
  both disclosure rules. That is the same empty promise those rules
  exist to prevent, reached through the second number rather than the
  first. Where the capability is available or `degraded`, the active
  bound must therefore leave room for the queued subset beside a started
  run — `max_active_runs_per_session` at least
  `max_queued_runs_per_session + 1`, so at least 2 for any queue at
  all — and a descriptor whose bounds do not satisfy that is
  `undisclosed_queue_limit` on the `capabilities.response`, the same
  diagnostic for the same failure (fixture
  `queue-advertised-active-bound-one`). `max_active_runs_per_session`
  stays optional throughout, and its absence still means one started run,
  unenforced — but absent is not the same as disclosed-and-incompatible:
  an endpoint that never states an active bound has promised nothing
  about it and is judged on the queue bound alone, while one that states
  a bound is held to the arithmetic it implies.
- Typed error `run_active` (the daemon's existing code, adopted as the
  protocol code; the research draft's `session_busy` name is superseded): a
  submission that cannot be admitted because the session is busy and no
  advertised busy outcome applies, or the queue bound is reached. It is
  returned before any identity is allocated.

### Semantics

- Explicit `queue` on an idle session is admitted `queued` and promotes
  immediately; "run after current work reaches a safe boundary" is trivially
  satisfied. Explicit `queue` never resolves to `start`.
- `auto` on a busy session resolves to `queue` only when
  `session.message.delivery.queue` is advertised; otherwise `run_active`.
  `delivery_resolution` reports `session_busy` for that resolution.
- A delivery capability advertised `degraded` needs its key in
  `allow_degraded_features`, for an explicit `queue` and for an `auto`
  that would resolve to it alike; otherwise `capability_degraded` before
  admission, exactly as T1 rules for controls. T4 applies the same to
  `session.message.delivery.steer`.
- Explicit `queue` on a descriptor that omits
  `session.message.delivery.queue` or advertises it `unavailable` is
  refused before admission with `unsupported_feature` (`details.feature`
  naming the delivery key, `details.reason: "unadvertised"`), never
  admitted or silently started; T4 applies the same to `steer`.
- Promotion is in admission order: a queued run may emit `run.started` only
  when every earlier-admitted run in the session is terminal. Only one run
  per session is started at a time; Decision 0001's cancellation scope
  therefore still targets one execution.
- A queued run cancels pre-start under Decision 0002; a queued run whose
  harness drops it before start settles `run.failed` with a typed
  `queue_dropped` error.
- Every queued reservation is listed in `active_runs` until its terminal;
  reconnect state preserves the order.
- A queued submit's `session_mutation` control (T1) is applied at
  promotion, not at admission, so the started run's model stays
  authoritative; the T1 semantics fix the failure and cancel paths.
- Delivery order on a session stream is one run domain at a time, in
  admission order: a later-admitted run's envelopes are delivered only
  after every earlier-admitted run's terminal. The rule is about
  execution, so it has exactly one exception, and the exception is the
  reason it can be exact elsewhere: the pre-start terminal of a run that
  never started is delivered when it happens. Such a run has no execution
  to interleave, and its release of a queue slot is capacity a subscriber
  and the validator both need to see at the moment it occurs rather than
  whenever the earlier run happens to end — holding it is what previously
  left a legitimate reuse of a freed slot indistinguishable from an
  over-admission.
  The cursor model follows rather than resists that. What the exception
  interleaves is bounded: a queued run that settles pre-start emits one
  sequenced envelope in its own domain and, being terminal, never another,
  so a session-following subscriber sees at most one out-of-domain
  envelope per released reservation. That is the same shape as an
  interleaved session-scoped envelope, and it takes the same cursor
  member: the third component of the resume cursor below is the id of the
  last delivered envelope that lies outside the followed run's domain,
  whether it is session-scoped or a settled reservation's terminal. A
  single `(run, sequence)` pair therefore still carries the followed
  run's position, with the interleave member carrying everything beside
  it. T2
  itself interleaves no session-scoped envelope on a run stream (queue
  state travels in `session.state` and its `active_runs`), but the
  cursor member that T3c's and
  T4's hub-minted snapshots need lands in T2's client slice below and is
  what a pre-start terminal uses too, so
  those units find it in place.

### Validator

- `sessionTrack.active` becomes an ordered set of nonterminal runs with
  admission indices. A second admission on a session with a nonterminal run
  is legal only when the new admission is `queued` and the trace's descriptor
  advertises `session.message.delivery.queue`; otherwise the existing
  `illegal_run_transition` ("session already has a nonterminal run").
  The refusal is validated too: an `auto` submit on a session with a
  nonterminal run whose descriptor does not advertise
  `session.message.delivery.queue`, or advertises it `unavailable`, is
  remembered from the request and judged across the request/response
  window, as the queue bound beside it is: `Submit` decides at an instant
  the trace cannot name, so a session busy when the adapter looked and
  idle when the response lands admits either answer — an admission is
  conforming because the run had gone, and `run_active` is conforming
  because it had not when the decision was made. Only a session idle
  throughout the window makes `run_active` wrong. Where the session was
  busy at the decision and the adapter refuses, its `error.response` must
  be the wire's
  `run_active`; a refusal under any other code is `illegal_run_transition`
  on the error response (`/payload/error/code`), so the ordinary busy-session
  refusal cannot hide behind `internal_error`. This is rung 4 of the
  refusal ladder, so it yields to any T1 expectation the same submit
  carries: an `auto` on a busy session whose controls are unadvertised,
  degraded without the opt-in, or unsatisfiable is owed
  `unsupported_feature`, `capability_degraded`, or the typed
  `unsatisfiable` refusal respectively, and the retained `run_active`
  expectation is discharged. Fixture `queue-busy-auto-unadvertised-control`
  (positive; a busy `auto` carrying an unadvertised control, refused by
  the capability rung). Fixtures:
  `queue-busy-auto-rejected` (`error.response` with `run_active`;
  validated as a correct rejection) and `queue-busy-auto-wrong-refusal`
  (`illegal_run_transition` on the error response; the same request
  refused with `internal_error`).
- The capability gate does not depend on session state. The validator
  today invokes `feature()` with `delivery.<mode>` on the request itself
  for every explicit non-`auto` delivery; T2 moves that judgement to the
  correlated response on the same terms as T1's control gate, so that an
  adapter's required refusal of an unadvertised `queue` validates: the
  request is remembered, a `queued` admission response (from an explicit
  or an `auto` request) on a descriptor that omits
  `session.message.delivery.queue` or advertises it `unavailable` is
  `unavailable_capability` on the response whatever the session held, and
  a correlated `error.response` must carry `unsupported_feature` naming
  the delivery key in `details.feature` with `details.reason:
  "unadvertised"`, anything else being `unavailable_capability` on the
  error response (fixture `queue-idle-unadvertised-wrong-reason`). No
  fixture in the current manifest expects
  `unavailable_capability` for a delivery, so the move changes no
  existing fixture's meaning; T4 applies the same shape to `steer`.
  T1's `degraded_without_optin` covers the delivery keys too: a `queued`
  admission on a descriptor advertising the queue capability `degraded`
  without the caller's opt-in is diagnosed on the response.
- The existing `submitResponse` combination table (admission,
  `effective_delivery`, `status`, checked as `illegal_run_transition`) is
  extended so the stated resolutions are enforced, not described: a
  request with explicit `delivery: "queue"` may only be admitted `queued`
  (never `started`, even on an idle session, since the queued run promotes
  immediately instead), and an `auto` request admitted `queued` must carry
  `effective_delivery: "queue"` and `delivery_resolution: "session_busy"`.
  The response already has to repeat the requested delivery
  (`scope_mismatch`), so a resolution cannot be hidden by rewriting it.
- New diagnostic `queue_order_violation`: any sequenced stream event of a
  later-admitted run appearing while an earlier-admitted run in the
  session is nonterminal. Two things are exempt. Requests and responses
  addressed to the queued run (`run.cancel.request` and `.response` are
  run-scoped envelopes, and cancelling a queued run before promotion
  necessarily happens while the earlier run is nonterminal): the rule
  governs the adapter's timeline, not the control layer's commands. And
  the pre-start terminal of a run that never started — a `run.cancelled`
  or `run.failed` for a queued reservation that settles before promotion
  — which is published at once rather than held until the earlier run's
  terminal. Holding it was the stricter reading and it bought nothing:
  the rule exists to keep one run's *execution* from interleaving with
  another's, and a run that never started has no execution to interleave.
  Holding it cost a great deal, because the release of a queued
  reservation is capacity, and withholding the only evidence of it left
  the trace unable to tell a legitimate reuse of a freed slot from an
  over-admission, with every repair for that — a deferral, a declared
  release, a timestamp comparison — either too weak to catch the real
  breach or too broad to spare the conforming adapter. Publishing the
  terminal when it happens makes the release observable in the trace at
  the moment it occurs, so ordering is provable by construction and the
  bound is checked against what actually happened. Everything else the
  adapter emits in a later-admitted run's domain still violates the rule.
- New diagnostic `queue_limit_exceeded`: when the descriptor carries
  `limits`, a `queued` admission that would put the session's nonterminal
  set above `max_active_runs_per_session` or its queued subset above
  `max_queued_runs_per_session`, both checked at the reservation, since
  promotion adds no run; `sessionTrack` keeps both bounds from the
  descriptor and replaces them from every accepted refresh. Absent limits
  enforce nothing here, since absence advertises no bound. The bounds are
  admission bounds, and a refresh that lowers one below a session's
  current set grandfathers that set: the refreshed descriptor is not
  diagnosed for a set it inherited (an adapter cannot shrink the set
  except by cancelling work, which a descriptor change must never do),
  and every admission after the refresh is judged against the new bound,
  so a session over a lowered bound admits nothing until its set falls
  below it. Fixtures: `queue-limit-lowered-by-refresh` (positive; two
  nonterminal runs under `max_active_runs_per_session: 2`, a refresh to
  `1`, both settle, then one new admission) and
  `queue-limit-lowered-then-admitted` (`queue_limit_exceeded`; the same
  refresh, then a `queued` admission while both runs are still
  nonterminal). The refusal is validated as well as the admission: a
  submit that would exceed a bound is remembered from the request
  together with the bounds that applied to it — but only when the
  descriptor advertises `session.message.delivery.queue` as available,
  because a bound belongs to a queue the endpoint offers. An explicit
  `queue` to an endpoint that does not advertise it is already owed the
  gate's `unsupported_feature` naming
  `session.message.delivery.queue` with `details.reason: "unadvertised"`,
  and a single `error.response` cannot also be `run_active`; as with the
  ungated steer in T4, the capability gate wins, this rule stands down,
  and the retained limit expectation is discharged without diagnosis. So
  the rule covers an explicit `queue` on an advertising endpoint and an
  `auto` on a busy session whose descriptor advertises the queue
  capability, and for those the counts are re-evaluated against the
  retained bounds at the correlated response rather than settled at the request, as the
  busy-`auto` rule above already does — capacity moves while `Submit` is
  in flight, and a stale expectation cuts both ways: a queued run that
  terminated before the response leaves the submit admissible, so an
  arbitrary refusal must no longer pass merely for saying `run_active`,
  while a concurrent admission that fills the queue after the request
  was received creates a limit hit the request-time snapshot never saw
  and an `internal_error` refusal must still be caught. Where the bound
  is reached at the response, the `error.response` must be the wire's
  typed `run_active`; a refusal under any other code is
  `queue_limit_exceeded` on the error response (`/payload/error/code`), so
  an
  adapter cannot hide a reached bound behind `internal_error`. The
  inverse is checked too, or the re-evaluation would only ever tighten:
  where the bound was unreached *throughout* the request/response window
  — not at either edge nor anywhere between, so no atomic decision inside
  it could have seen a full queue — the submit was admissible, and an
  `error.response` carrying `run_active` reports a bound that never
  bound — `run_active` alone,
  because the wire vocabulary for a reached bound is `run_active` and
  `queue_limit_exceeded` is a validator diagnostic, never a code an
  adapter sends; a refusal that did send it would already be diagnosed by
  the reached-bound rule above for using the wrong code. That is
  `queue_limit_exceeded` on the error response, naming the bound and the
  counts, so a caller is not told to wait for capacity it never lacked —
  but only once every other state-rung condition that independently owes
  `run_active` has been excluded. The
  code is not the queue's alone: T1 lets a busy adapter that cannot defer
  a queued `session_mutation` refuse with it, and if the queued
  reservation terminates in flight while the started run remains, that
  adapter still owes `run_active` for the mutation constraint even though
  the bound has cleared. Diagnosing it would convict a conforming
  refusal, so the stale-limit check runs only when the response's code
  is explained by no surviving condition. The window is the only instant
  rule in play, for this check and the reached-bound check above alike,
  and it is stated once rather than as a second test at either end, as
  the snapshot rules are. `Submit` decides atomically at some instant
  the
  trace cannot name, and both edges of that ignorance produce false
  verdicts: a queue full at the decision whose queued run terminates
  before the response makes a correct `run_active` look stale, and a
  queue with room at the request that fills before the response makes a
  correct refusal look unfounded. So the bound counts as reached if it
  was reached at any point between the request and its correlated
  response, and the stale-limit check fires only when the bound was
  unreached throughout. Neither race then convicts a conforming adapter,
  and no boundary field is needed, because the window is bracketed by
  the two envelopes the trace already has. Fixture
  `queue-limit-cleared-mid-window` (positive; the bound reached at the
  request, cleared before the response, refused `run_active`) and its
  negative `queue-limit-unreached-refused` (`queue_limit_exceeded` on the
  `error.response`; the bound unreached throughout the window, refused
  `run_active` with no other state-rung condition surviving — the
  `honour` fixture T0's corpus-completeness check requires).
  The ordering rule creates the same blind spot on the admission side. A
  queued run that settles before promotion has released its reservation
  and the adapter may admit another in its place, but `queue_order_violation`
  withholds its pre-start terminal until the earlier-admitted run
  terminates, so the trace still counts it nonterminal and would report
  `queue_limit_exceeded` against a conforming adapter. The validator
  cannot see the settlement, and waiting for an optional state read is
  not a rule. The ordering rule is what made the settlement invisible, so
  the fix is there rather than here: a queued run's pre-start terminal is
  published when it happens, as the exemption above sets out. Capacity is
  then observable in the trace at the moment it changes, and this rule
  needs no deferral, no declared release, and no timestamp: an admission
  is judged against the reservations the trace shows outstanding, and one
  that reuses a slot the trace has already seen freed is simply within
  the bound. Fixtures `queue-reuses-slot-of-settled-reservation`
  (positive; a queued run cancelled pre-start under
  `max_queued_runs_per_session: 1`, its terminal published at once, then
  a new queued admission) and `queue-over-limit-after-settlement`
  (`queue_limit_exceeded`; two new admissions where one slot was
  freed).
  In-flight reservations are
  such a condition. Concurrent submits contend for the last slot, and the
  one that takes it may still be awaiting its own response when a later
  request is correctly refused `run_active`: the trace has the winner's
  request but not yet its admission, so counting only admitted runs sees
  a free slot that is not free. The validator therefore counts every
  unanswered `session.message.submit.request` on the session that could
  still be admitted into the set alongside the admitted runs, and the
  stale-limit check fires only when the bound is unreached even on that
  reckoning. The count is deliberately pessimistic — an outstanding
  request that is ultimately refused will have inflated it — because the
  cost of the two errors is not symmetric: a missed diagnosis leaves one
  stale refusal unflagged, while a false one convicts an adapter that
  did exactly the right thing under contention it could see and the
  validator could not. Fixture `queue-limit-concurrent-reservation`
  (positive; two submits contend for the last slot, the loser refused
  `run_active` before the winner's admission reaches the trace). Fixture
  `queue-limit-cleared-mutation-still-busy` (positive; the bound clears
  in flight, a started run remains, and the `session_mutation` submit is
  still refused `run_active`). Other codes on that response stay
  outside this rule: the adapter may have refused for a reason the queue
  knows nothing about. Fixture `queue-limit-cleared-stale-refusal` (the
  bound reached at the request, the queued run terminated before the
  response, still refused `run_active`). Fixtures:
  `queue-over-limit-rejected` (`error.response` with `run_active`;
  validated as a correct rejection), `queue-over-limit-wrong-refusal`
  (`queue_limit_exceeded` on the error response; the same request
  refused with `internal_error`) and `queue-ungated-over-limit`
  (positive; an explicit `queue` to a busy non-advertising endpoint at
  its active-run bound, refused with the gate's `unsupported_feature`,
  where capability and limit would otherwise both claim the response).
- New diagnostic `premature_session_mutation`: when the started run was
  admitted under a descriptor disclosing `run.model_selection` with mode
  `session_mutation` (the mode `runState` retains from admission, T1), a
  `session.state` snapshot taken while that run is started reports a
  `current_model_id` other than its admitted model — judged at the
  position the snapshot states, not at its arrival. A queued
  `session_mutation` promoting concurrently with a state read straddles
  this both ways: the snapshot can capture the old model while
  `run.started` reaches the trace first, or the new one while that event
  drains afterwards, and either accurate reading would be diagnosed
  against the trace as it stands. So `session.state.as_of` gains
  `model_run_sequence`, `{ "run_id", "sequence" }`, naming the last
  model-affecting event the snapshot reflects, the same marker
  `models.response` carries for the same reason. Present, the model is
  judged at that point, and a position the trace has not reached is held
  and reconciled when it arrives; absent, the snapshot claims no
  knowledge the trace lacks and is judged as it stands. A capture before
  the session's first model-affecting event has no such event to name and
  must still be representable, or the case the marker exists for is the
  one case it cannot express: a session opening on model A, captured just
  before the first `session_mutation` promotion whose `run.started`
  reaches the trace first, would fall to the absent branch and be
  diagnosed against model B. The marker therefore has a genesis form,
  `{ "run_id": null, "sequence": 0 }`, naming the position before any
  model-affecting event — the session's opening model, judged against no
  run at all. It is a stated position like any other, so omission keeps
  its own meaning and stays distinct from it. Fixtures
  `queue-state-model-at-promotion` (positive; a snapshot capturing the
  pre-promotion model whose `run.started` precedes the state response)
  and `queue-state-model-at-genesis` (positive; the opening model
  captured before the first promotion, marked at genesis).
- `session.state` snapshots: `active_runs` is required whenever the
  validator tracks a queued reservation, more than one nonterminal run,
  or, on an endpoint advertising any unit that introduces `active_runs`
  entries (T2 queue, T3c provide, T4 steer), a nonterminal run with a
  pending steer or an unresolved interaction of any kind (permission,
  user input, control-owned call), since the single-run recovery path
  reads the submission or interaction id from the entry's
  `pending_steers` or `pending_interactions` and an omitted field would
  lose it (an absent field would read as an empty queue to a reconnecting
  client); each present entry's `pending_interactions` must equal the
  validator's set of unresolved interactions for that run as of the
  position the entry was captured at, which the entry states: `active_runs[]`
  gains `as_of_sequence`, the last sequence of that run the snapshot
  reflects. Bracketing by the request and response envelopes is not
  enough, as it is for the session-level settled-steer history — that one
  is hub-tracked, so the hub's own two positions do bound it — because a
  run's state moves on the adapter's timeline, not the trace's. An
  interaction can resolve internally before the adapter captures state
  while its lifecycle event is drained only after the state response, so
  the validator would see it unresolved across the whole window and
  diagnose an accurate omission. State reads are not serialized with
  lifecycle publication and should not be; the position makes the read
  self-describing instead. The entry is then judged exactly: an
  interaction unresolved at `as_of_sequence` must be listed, one resolved
  by then must not be, and `session_state_mismatch` otherwise, with no
  window and no guessing. `as_of_sequence` may name a position the trace has not reached: an
  interaction can resolve inside the adapter before capture while the
  event carrying that sequence is drained after the state response, and
  an accurate snapshot must be able to point at it. Such an entry is not
  rejected but held, and reconciled when the named sequence arrives; if
  the run terminates or the trace ends first, the entry is judged at the
  last sequence the run reached, and a position that run never reaches is
  `session_state_mismatch` then — a snapshot may describe a position the
  trace has not yet seen, but not one that never exists.
  `pending_steers` needs one thing more, because a steer joins the
  pending set at the `session.message.submit.response`, which is
  unsequenced and advances no run cursor: two snapshots taken either side
  of that response can carry the same `as_of_sequence` and differ
  legitimately in whether the steer is listed, so the run position alone
  cannot judge them. A scalar boundary will not do either: steers are
  admitted under the per-run gate in the order they acquire it, which
  need not be the order their request envelopes reached the trace, so a
  snapshot can legitimately hold a later-sent steer and not an earlier
  one — and no single "up to here" id describes that. The entry therefore
  carries `admitted_submit_requests`, the set of submit request envelope
  ids on that run the snapshot reflects as admitted (absent when it
  reflects none), and `pending_steers` is judged against it: a steer
  whose request is in the set must be listed, one whose request is not
  must be absent, and `session_state_mismatch` otherwise. A set costs
  nothing an ordering would save — it is bounded by the steers
  outstanding on one run — and it says exactly what the adapter knows,
  which an order it does not control cannot.
  The request, not the response, because the marker has to name something
  the party producing the snapshot can know. Session state comes from the
  adapter, while the response envelope is minted by the binding after
  `serve.Session.Submit` returns — and an in-process `serve.Hub` caller
  may mint no response envelope at all — so a snapshot captured after
  admission and before minting could neither name the response nor be
  judged without it. The request id is already in the adapter's hands:
  it arrives as `adapter.SubmitRequest.EnvelopeID`, which T2 adds for the
  membership and steer capture markers above and T4 reuses for the
  settlement correlation. It is also
  strictly better ordered: a request the adapter has seen is by
  construction already in the trace, so unlike `as_of_sequence` this
  marker can never run ahead of it and needs no deferred reconciliation.
  A marker naming a request the trace does not carry for that run is
  `session_state_mismatch` at once.
  The set disambiguates the overlap and nothing more: an admission whose
  response precedes the `session.state.request` is settled fact, so its
  request must appear in the set and the snapshot must account for its
  steer, and only
  admissions whose responses fall inside the request/response window may
  be left out. Accounting for it is not the same as listing it as
  pending: a steer that settled at or before `as_of_sequence` belongs in
  the session's `settled_steers` and must be *absent* from
  `pending_steers`, so the anchor establishes that the snapshot knows of
  the admission, and the capture position then decides which of the two
  surfaces carries it. A steer in the set that appears in neither, or in
  both, is `session_state_mismatch`.
  The anchor reaches only as far as the state does. `settled_steers` is
  bounded, so a long-running session evicts its oldest settlements and
  reports `complete: false`; an anchor that still demanded those steers'
  request ids would be unsatisfiable, since including an id obliges the
  snapshot to carry the steer on one of the two surfaces and neither
  holds it any more. So once `settled_steers.complete` is false, a steer
  whose settlement has been evicted may be absent from
  `admitted_submit_requests` as well, and the anchor binds only
  admissions still represented in the retained state. What it still
  forbids is the case it was added for: dropping a steer that is pending,
  or whose settlement is retained, by saying nothing about it. Without that anchor the set would be permissive in the
  wrong direction — omitting an id would license omitting the steer, so
  an adapter could drop a steer admitted long before the read simply by
  saying nothing about it. Every id in the set must name a submit request
  the trace carries for
  that run (`session_state_mismatch` otherwise), and a snapshot that
  lists a steer while naming no
  `admitted_submit_requests` at all is judged against the trace as it
  stands,
  since it claims no knowledge the trace lacks. Fixture
  `steer-state-omits-settled-admission` (`session_state_mismatch`; a
  steer admitted before the state request, absent from both the set and
  `pending_steers`). Fixtures
  `steer-state-capture-straddles-admission`
  (positive; two snapshots at one `as_of_sequence` either side of a steer
  admission, each accurate at its own set) and
  `steer-state-out-of-order-admission` (positive; two overlapping steers
  admitted in the reverse of their request order, a snapshot listing only
  the later-sent one).
  `pending_interactions` needs no equivalent: an interaction joins and
  leaves the set on sequenced run events, which `as_of_sequence` already
  orders.
  The marker is optional on the wire only for entries that do not carry
  either collection; an entry carrying `pending_interactions` or
  `pending_steers` must carry `as_of_sequence`
  (`session_state_mismatch` otherwise), because those are precisely the
  entries whose reconciliation depends on it, and a serializer that
  omitted it would put the validator back to guessing whether a stale
  snapshot is accurate — the race the marker exists to settle. Requiring
  it conditionally rather than outright keeps the field off entries that
  have nothing to reconcile, and an endpoint unwilling to report a
  capture position can only conform by capturing the pending sets at the
  run's current cursor and saying so, which is the serialized behaviour
  the marker was meant to make unnecessary but not to forbid. Fixture
  `queue-state-pending-without-as-of` (`session_state_mismatch`).
  Fixtures `queue-state-interaction-resolved-in-flight`
  (positive; a snapshot whose `as_of_sequence` names a resolution
  drained only after the state response, reconciled when it arrives) and
  `queue-state-as-of-never-reached` (`session_state_mismatch`; a position
  the run never reaches before its terminal). This check lands here with the field so that a
  T2-only endpoint with a run blocked on a permission or user-input
  interaction cannot emit an entry without the id (T2 fixture
  `queue-state-omits-pending-interaction`); the interaction condition is
  scoped to those endpoints because
  a v0.1-only endpoint has no `active_runs` at all, so no existing fixture
  changes meaning. When present it must list the tracked nonterminal runs
  in admission
  order with consistent `queue_position`; `active_run_id` must be the
  started run; otherwise `session_state_mismatch`. Membership needs a
  boundary of its own, and it has to be session-level: the per-entry
  markers above describe an entry, and the hard case is a run with no
  entry at all. A snapshot can legitimately drop a queued run it settled
  before capture whose terminal is drained only after the state response,
  and can legitimately omit one admitted after capture whose admission
  reaches the trace first — in both the trace at response arrival
  disagrees with an accurate read. So `session.state` gains `as_of`,
  `{ "admitted_submit_requests"?: [envelope id], "settled"?: [{ "run_id",
  "sequence" }], "model_run_sequence"?: { "run_id", "sequence" } }`: the submit requests on the session the snapshot
  reflects as admitted, and the runs it has already removed with the
  sequence of each one's terminal. A set rather than a "last request"
  for the same reason the per-run steer boundary is one: two overlapping
  `Submit` calls need not be admitted in the order their requests reached
  the trace, so a snapshot can hold B and not A, and no scalar describes
  that — naming B would make A look required, naming A would either
  exclude B or license arbitrary omissions after it. Both members are
  facts the adapter holds at capture — the
  request ids come in with the operations, and the terminals are ones it
  emitted itself — which is what the response-envelope marker of an
  earlier round was not.
  Membership is then judged against `as_of` rather than the trace's
  current state, with the same anchor the per-run set takes: a run
  admitted before the `session.state.request` — its response already in
  the trace — must be listed whatever the set says, and only admissions
  whose responses fall inside the request/response window may be omitted
  on the strength of being absent from
  `as_of.admitted_submit_requests`. A run named in `as_of.settled`
  may be absent, and every other tracked nonterminal run must be listed.
  Every id in the set must name a submit request the trace carries for
  that session (`session_state_mismatch` otherwise). Fixture
  `queue-state-omits-established-run` (`session_state_mismatch`; a run
  admitted well before the read, omitted with its request left out of the
  set).
  A `settled` entry may name a terminal the trace has not reached, and is
  held and reconciled when it arrives; one whose named terminal never
  arrives, or arrives at another sequence, is `session_state_mismatch` at
  the run's terminal or the end of the trace. That alone would not be
  enough, since a sequence is predictable: a snapshot could drop a live
  run, name the sequence its terminal will eventually carry, and be
  vindicated whenever the run happens to end there, having reported false
  membership to a client in the meantime. Two things close it. The
  validator requires the claimed terminal to be the next envelope
  published for that run after the snapshot — any other envelope from
  that run in between is `session_state_mismatch` — so a claim about a
  run that is still doing anything at all is caught. And the hub, which
  forwards the snapshot and is the party that reads the run's stream,
  corroborates each `as_of.settled` claim before forwarding — by draining
  that run to the claimed sequence, not by checking whether it happens to
  have read it already. The adapter can settle a run and capture state
  before the hub's drainer has caught up, which is ordinary scheduling,
  so refusing an unread claim would make conformance depend on drainer
  timing and defeat the ahead-of-trace reconciliation the marker exists
  for. The hub therefore reads on, bounded by the state request's own
  context, and serves the snapshot once the claimed terminal is in hand;
  a claim the drain cannot reach — the stream ends, or the context does,
  without it — is refused, and the run stays listed. The adapter asserts,
  the hub corroborates, and the
  validator checks the outcome, which is the same division that lets the
  hub rather than the adapter classify a cross-session steer target.
  Fixture `queue-state-settled-claim-then-activity`
  (`session_state_mismatch`; a claim for a run that emits content before
  the terminal it predicted). A started run may be claimed
  as readily as a queued one: a run that terminates inside the adapter
  before a concurrent capture, whose terminal the hub's drainer publishes
  only after the state response, is the same ordinary race, and the claim
  is judged by whether the named terminal arrives at the named sequence,
  never by what the run did beforehand. Where `as_of` is absent the snapshot claims no
  knowledge the trace lacks and is judged against the trace as it stands,
  which is the rule that applied before. Ordering throughout
  is read from the trace and from stated positions, never from
  `updated_at_ms` and `timestamp_ms`, which are optional and can tie
  inside one millisecond — two events stamped the same would have let a
  premature drop pass. No run is settled-pending-delivery any more, and a
  started run's terminal was never held in any case. Fixtures
  `queue-state-omits-settled-before-terminal` (positive; a snapshot
  dropping a queued run it settled at capture, naming it in
  `as_of.settled`, with the terminal published after the state response)
  and `queue-state-false-settled-claim` (`session_state_mismatch`; an
  `as_of.settled` entry for a run that afterwards promotes and starts).

### Reference adapter

`adapter/memory.go` advertises `session.message.delivery.queue` `emulated`
with `limits` of `max_active_runs_per_session: 2` and
`max_queued_runs_per_session: 1`: a submit while the scripted
run is active reserves a second run (`queued`), lists both in `active_runs`,
and promotes it when the first settles; cancelling the queued run settles it
pre-start.

### Native evidence

OpenCode graduates first: `SessionInput.Admitted` with `delivery: "queue"`
and `promotedSeq` is the strongest native queue shape in the ledgers, and the
three Decision 0002 cases already pin the reservation. New corpus cases:
`queue-explicit-busy` (queue admitted while a run is active, promoted after
the first run's derived settlement), `queue-cancelled-before-promotion`.
Follow-on: pi `follow_up` (with `queue_update` as the queued-state
observation), Hermes `queued` under `busy_input_mode=queue`.

### Surfaces

- `serve`: subscriptions deliver one run domain at a time in admission
  order. The hub already drains every run's adapter stream independently
  and numbers runs by admission serial; T2 adds holding a later-admitted
  run's envelopes until the earlier run's terminal has been delivered,
  with the one exception the delivery rule names: the pre-start terminal
  of a run that never started is released as soon as it is read, while
  the earlier run is still nonterminal. Holding it would hide the
  released reservation and make a valid replacement admission look
  over-limit, which is the failure the exception exists to prevent; and
  because that run never started, the envelope is the only one its domain
  will ever produce. The exception is scoped to subscriptions that follow
  the session (`?follow=session`, below), and a subscription that does
  not follow never receives it. A legacy client is not merely uninformed
  by a second domain, it is broken by one: both clients read a sequence-1
  envelope naming another run as a live run switch, mark that run
  terminal, and then fail the next envelope from the still-running
  earlier run, which is long past sequence 1. And the failure the
  exception exists to prevent cannot reach that client — it cannot queue,
  so it has no reservation to be misled about; the accounting it would
  distort belongs to the T2-aware caller, which is following, and which
  can read `active_runs` besides. Non-following subscriptions therefore
  keep single-domain delivery exactly: the run they are bound to and
  nothing else. A subscription's replay cursor already
  carries `(RunID, AfterSequence)`: resume replays that run's retained
  suffix and, when the subscription follows the session (below), continues
  into later-admitted runs in order, and the hub stops assuming the newest
  admission is the run a bare sequence refers to. A cursor's run and
  sequence therefore stay correct: the run it names is the only run whose
  *execution* is in flight on the stream, and a settled reservation's
  terminal is delivered beside it without advancing it, the same way a
  session-scoped envelope is. The cursor's third
  member carries both, and the hub retains a released terminal for replay
  under it exactly as it retains an interleaved session-scoped envelope;
  the member is specified under T4's client section and landed by T2's
  client slice
  (below), since T2 is the first slice to touch cursor handling. T2's own
  tests cover the interleaving end to end, because T2 is where it first
  occurs: a queued reservation cancelled pre-start under a started run,
  its terminal delivered live, and a resume across the drop that replays
  it exactly once — and the same scenario read by a subscription that
  does not follow, which must see A's envelopes alone and B's terminal
  never, plus a bare-cursor reconnect on a non-following subscription
  whose sequence also falls inside a retained earlier run, which must
  resume on the started run with no `oap-replay-gap`, and the mixed
  case — that reconnect arriving after a queued B was promoted — which
  must reach the client as a run mismatch on the first envelope rather
  than as B's events read against A's cursor, together with its
  common variant — the same drop at a position B has not yet reached,
  which must resolve onto A and resume invisibly.
- `serve/servehttp`: the SSE `id:` field stays the bare sequence, because
  both v0.1 clients parse it as an unsigned integer (`strconv.ParseUint` in
  Go, `/^\d+$/` in TypeScript) and a qualified id would break them on the
  first event. Run identity for a cursor travels in an additive `?run=`
  query parameter beside `?after=`. A cursor without `run` cannot always
  be resolved, and the plan says so rather than picking a rule that
  trades one client's correctness for another's. Sequences restart per
  run, so a bare number can name a position in more than one retained
  domain: resolving onto the started run breaks a client resuming the
  earlier run — a v0.1 client that queued B while A ran and dropped
  before A's terminal reconnects with `?after=<A sequence>` and has it
  read against B — while resolving onto the earliest retained domain
  breaks the opposite case, a client reading B whose cursor also falls
  inside retained A's range. No function of the number alone is right for
  both. The rule therefore splits on what the subscription asked for,
  because the two kinds of subscriber cannot hold the same kind of
  cursor. A subscription that does not follow the session has only ever
  delivered one run domain and ends at that run's terminal, so a bare
  cursor on it can only have come from the run that was started when it
  was reading: the daemon keeps the v0.1 binding and resolves it against
  the started run. That matters because emitting a gap here would break a
  case that needs no queue at all to reach: finish A, submit B, read one
  event, drop, reconnect with `?after=n` that retained A also spans. Both
  current clients surface `oap-replay-gap` as a terminal
  `ReplayGapError`, so under any other rule upgrading the daemon would
  break the client behaviour this plan promises to leave alone.

  The started run is not always the run the cursor came from, and the
  reconnect is a fresh GET carrying no subscription history to say
  otherwise. In a mixed session a T2-aware caller can queue B while the
  legacy client reads A, and a drop after the hub processed A's terminal
  and promoted B leaves a cursor from A arriving at a stream whose
  started run is B. That case is indistinguishable at the daemon from the
  ordinary sequential one — finish A, start B, read one event of B,
  drop — since both are a small bare number that two retained domains
  span, so no daemon-side test separates them and a rule that refused
  both would break the ordinary case, which is the common one.

  One test does separate a large part of it, and it is not a guess: a run
  that has not itself reached sequence `n` cannot be where a cursor at
  `n` came from, and resuming there would ask for a position that run has
  not produced. So resolution is restricted before it is preferred — a
  bare cursor resolves only onto a retained domain that has reached the
  sequence, and among those the started run wins. A promoted B is usually
  at the very beginning of its own numbering while the legacy client had
  read some way into A, so most mixed drops resolve onto A and recover
  invisibly after all. What survives is the narrow overlap where B has
  already passed the client's position in A, and there the daemon has
  nothing left to distinguish with.

  For that remainder the v0.1 client itself separates them,
  and this is the reason the binding is safe rather than merely
  convenient. A resuming client carries the run it was reading, and the
  first envelope of a cursor-bearing stream is checked against it:
  `client/events.go:396-399` raises `ResumeMismatchError` when the run
  differs, before the sequence is used for anything
  (`clients/ts/src/events.ts` performs the same check). In the ordinary
  case the client holds B and receives B and resumes invisibly; in the
  mixed case it holds A, receives B, and fails loudly and permanently
  without consuming a single misattributed envelope. The wrong run is
  therefore caught where the knowledge to catch it lives, and the daemon
  keeps the v0.1 resolution rather than pre-empting a client check that
  already works.

  This is the plan's stated residual, not an oversight: in that narrow
  overlap a v0.1 subscriber's resume ends in `ResumeMismatchError` rather
  than invisibly, and the plan takes that over a silent splice of B's
  envelopes onto A's cursor, which is the only alternative left once the
  number is all the daemon has. The failure is diagnosable and the data
  is not lost: the error names both the expected and the observed run
  (`ExpectedRunID`, `ObservedRunID`), A's domain stays retained, and
  `?run=A&after=n` recovers its tail exactly — which is what `?run=`
  exists for and what a T2-aware client, which never reaches this branch,
  sends from the start. The cost is bounded to sessions that mix a
  queue-aware caller with a v0.1 subscriber, during the window between a
  terminal being processed and being read, at a position the promoted run
  has already passed.

  A following subscription is the one that can carry a cursor from a domain
  other than the started run, and only there is a bare number genuinely
  ambiguous. So on a following subscription the daemon resolves a bare
  cursor when exactly one retained domain reaches that sequence, which
  still covers every single-run session, and when more than one does it
  does not guess: it emits `oap-replay-gap` naming the candidates in
  `ambiguous_run_ids` and resumes from the started run's current
  position. Such a client is T2-aware by construction and sends `?run=`,
  so the gap is the rare repair path, not the ordinary one: it responds
  by sending `?run=`, and until it does it learns it lost continuity
  instead of silently receiving another run's stream, which is the
  outcome worth protecting there, since a signalled gap is recoverable
  and a wrong run is not. `?run=` remains the way to be unambiguous, and
  both clients send it from T2 on. The payload is stated
  once, here: `oap-overflow` and `oap-replay-gap` both carry `run_id`, and `run_id` always names the
  single run whose stream the client is now reading — the run the daemon
  resumed from, never the ambiguity — so a client can resume the right
  run (overflow already carries it). Ambiguity does not replace or
  pluralise `run_id`; it rides an additional optional
  `ambiguous_run_ids`, present only in the bare-cursor case above and
  listing every retained domain that reached that sequence. A gap has a
  `scope` besides — `run` for a cursor the run journal no longer spans,
  `interleaved` for an `after_interleaved` the hub journal has evicted
  (T3c) — since the two lose different things and a client recovers from
  them differently. The two
  members answer different questions and are deliberately not required to
  intersect: the resumed run is the started one, which on a gap is
  commonly a run that never reached the ambiguous sequence at all — A and
  B both retained past 10 while started C sits at 2. Carrying only the
  candidates would leave the client resuming into a domain it was told
  was ambiguous, and a retry of `?run=` with the original cursor would
  ask for a sequence that run has not produced, so `oap-replay-gap` also
  carries `last_sequence`, the position in `run_id` the stream resumes
  from. `run_id` with `last_sequence` is what to send back; `ambiguous_run_ids` is
  what the cursor could have meant. The stdio frontend's `events` op gains the same optional
  `run` parameter. `?run=` is still needed even with ordered
  delivery: a drop between one run's terminal and the next run's first
  envelope leaves the client holding the finished run's cursor while the
  next run is already the started one. Stream end changes with it: today
  the daemon ends a stream at a run terminal unless a resubmit already
  landed, and both clients read a terminal as the stream's clean end. Under
  T2 a following subscription (`?follow=session`, below) continues past a
  terminal into any later-admitted run's domain, and when the daemon ends
  a stream on purpose (a terminal with no later-admitted run, a terminal
  on a subscription that does not follow, or the session closed) it
  writes an explicit
  `event: oap-stream-end` signal (`run_id`, `last_sequence`) before
  closing, and whenever a stream leaves a run's domain for a later one,
  live or resumed, it first writes `event: oap-run-boundary` (`run_id`,
  `last_sequence`) for the run it is leaving, so a run's terminality is
  stated by the server rather than remembered by the client; the stdio
  frontend writes the same named lines beside its `oap-session-closed`.
  Both clients already skip named events they do not define, so both
  signals are additive. Continuation across run domains is opt-in, not
  assumed: a v0.1 client ends its stream at the terminal of the run it is
  reading (`client/events.go:143-146` returns `io.EOF` after a terminal
  and `clients/ts/src/events.ts:162-165` does the same), so a stream that
  continued into a queued run B would never be read by it and B's events
  would silently vanish from that subscription. A T2-aware client asks
  for continuation with an additive `?follow=session` parameter on the
  subscribe request (a `follow` flag on the stdio subscribe op); without
  it the daemon keeps the v0.1 behavior exactly, ending the stream at the
  terminal of the run the subscription is bound to, preceded by the end
  signal the legacy client skips. A legacy client therefore sees
  precisely what it sees today and never a partial view of a run it did
  not ask for, and one that submits again opens a new stream as it does
  now.
- `client` and `clients/ts`: both already track the run of the last
  observed envelope (`EventStream.runID`, `EventStream.runId`) and expose
  `EventsAfter(RunID, LastSequence)` / `eventsAfter`; the changes are to
  subscribe with `?follow=session`, send that run as `?run=` on
  reconnect, adopt the wire's own scoping and the `after_interleaved` cursor
  member specified under T4's client section (T2 lands them, as the first
  slice to touch cursor handling; the daemon accepts `?after_interleaved=`
  from T2 on and honours it for what T2 can actually interleave — the
  released pre-start terminal of a settled reservation, which it holds in
  the run buffers it already keeps — while ignoring it for session-scoped
  envelopes until T3c brings the journal those need. The ignore is
  scoped, not blanket: T2's own resume test requires the released
  terminal not to be replayed twice, and since that envelope advances no
  run cursor the member is the only thing that can suppress it), and
  stop
  treating a terminal envelope as the end of a following stream. A terminal ends the run; the stream
  ends on `oap-stream-end` (or a closed session). A connection that drops
  after a terminal without that signal is a drop like any other: the
  client resumes with the finished run's cursor and the daemon either
  continues into the later-admitted run or answers with the end signal.
  Against an older daemon, which closes at the terminal with no signal, the
  client bounds that post-terminal resume to one attempt and then reads
  `session.state`: a present `active_runs` that is empty is the clean
  end and a nonempty one keeps resuming, while an absent `active_runs`
  is the older representation rather than an answer, so the client falls
  back to `active_run_id`, resuming into the run it names when that run
  is not the finished one and treating an absent or empty
  `active_run_id` as the clean end, since a legacy daemon that admitted
  run B after A's terminal reports B only there. The client tests cover
  a legacy snapshot carrying `active_run_id` and no `active_runs`. The resume checks change in exactly one
  case to make this work: today a cursor-bearing connection whose first
  envelope names another run fails with `ResumeMismatchError`, and one
  whose first envelope is not `lastSeq+1` fails with `SequenceGapError`.
  Under T2 the first resumed envelope may instead be sequence 1 of a
  different run when the cursor's run is known terminal, and the client
  then adopts that run as the new domain and resets its sequence
  expectation to it. "Known terminal" has two sources: the in-memory
  `terminal` flag both clients already track, and an `oap-run-boundary`
  event naming the cursor's run at exactly the cursor's sequence. The
  second is what makes the public manual-resume path work: a fresh
  `EventStream` built by `EventsAfter(runA, n)` after a process restart
  holds only the `(run, sequence)` pair, and the daemon's answer to
  `?run=A&after=n` when A is terminal at n is the boundary for A, then B
  from sequence 1, so the client learns terminality from the server
  before the switch. A different run at any other sequence, or a
  different run with neither piece of evidence, still fails as today, so
  the daemon's answer is the end signal, the boundary followed by run B
  from sequence 1, or A's own continuation and nothing else. Both
  clients' e2e tests drop the connection between a run's terminal and the
  queued run's first envelope on the same stream object, and separately
  resume from the terminal cursor with a fresh `EventsAfter`, asserting
  the continuation in both. Because
  delivery is one run domain at a time, no per-run cursor table is needed:
  a run switch always follows a terminal, and on a live connection both
  clients already accept it.
- Daemon-management listing (`GET /sessions`) reports `active_runs`.

### Fixtures

Positive: `queue-explicit-idle-promoted`, `queue-busy-then-promoted`,
`queue-busy-cancelled-prestart` (its pre-start terminal published at
once, while the first run is still nonterminal),
`queue-state-active-runs`,
`queue-state-reflects-early-settlement` (a snapshot omitting the
cancelled queued run, after its pre-start terminal),
`queue-model-mutation-at-promotion` (a `session_mutation` descriptor, a
queued submit naming another model, `current_model_id` unchanged until the
first run's terminal). Negative:
`queue-promoted-out-of-order` (`queue_order_violation`),
`queue-prestart-terminal-interleaved` (`queue_order_violation`; a queued
run's `run.started` before the earlier run's terminal — its pre-start
`run.cancelled` is exempt and covered by the positive above),
`queue-state-drops-reserved-run` (`session_state_mismatch`; a snapshot
omitting a queued run whose pre-start terminal has not been published),
`queue-idle-unadvertised` (`unavailable_capability` on the admission;
explicit `queue` on an idle session with the capability unadvertised,
admitted `queued`), the correct rejection
`queue-idle-unadvertised-rejected` (positive, `valid: true`, no
diagnostic; `error.response` with `unsupported_feature`,
`details.feature: "session.message.delivery.queue"`, then no
admission),
`queue-degraded-admitted-without-optin` (`degraded_without_optin`; queue
advertised `degraded`, no opt-in, admitted `queued`), and the correct
rejection `queue-degraded-without-optin` (positive, `valid: true`, no
diagnostic; `error.response` with `capability_degraded`, then no
admission),
`queue-degraded-wrong-refusal` (`degraded_without_optin` on the error
response; the same request refused with `internal_error`),
`queue-model-mutation-early` (`premature_session_mutation`),
`queue-mode-refreshed-while-queued` (positive; a `session_mutation`
descriptor, a queued controlled submit, a `capabilities.updated` to
`per_run` while it waits, then promotion with the mutation reflected in
`current_model_id`: judged under the retained mode, no diagnostic),
`queue-per-run-behind-mutation` (positive; a queued `per_run` run's
retained default follows an earlier-admitted `session_mutation` run's
application at promotion, as T1's `per_run` snapshot rule specifies),
`queue-over-limit` (`queue_limit_exceeded`; `max_queued_runs_per_session:
1`, one started run, two queued admissions), `queue-over-active-limit`
(`queue_limit_exceeded`; `max_active_runs_per_session: 1`, a queued
admission while a run is started),
`queue-overlap-unadvertised` (`illegal_run_transition`),
`queue-state-omits-pending-interaction` (`session_state_mismatch`; a
started run blocked on a permission interaction, and an `active_runs`
entry for it without the interaction id),
`queue-explicit-admitted-start` (`illegal_run_transition`; explicit
`queue` on an idle session answered `started`),
`queue-auto-without-resolution` (`illegal_run_transition`; an `auto`
request admitted `queued` without `delivery_resolution: "session_busy"`),
`queue-state-missing-reservation` (`session_state_mismatch`),
`queue-state-omits-active-runs` (`session_state_mismatch`; a queued
reservation and a snapshot without `active_runs`),
`state-omits-active-runs-with-pending` (`session_state_mismatch`; a single
started run with a pending steer and a snapshot without `active_runs`),
`state-omits-active-runs-with-pending-permission`
(`session_state_mismatch`; a queue-advertising endpoint, one started run
waiting on a permission, and a snapshot without `active_runs`),
`queue-state-drops-unsettled-run` (`session_state_mismatch`; a snapshot
omits a queued run whose terminal never arrives),
`queue-state-omits-before-settlement` (`session_state_mismatch`; a
snapshot omits a queued run without claiming it in `as_of.settled`),
`queue-state-false-settled-claim` (`session_state_mismatch`; an
`as_of.settled` entry whose named terminal never arrives),
`queue-state-settled-wrong-sequence` (`session_state_mismatch`; the
terminal arrives at a sequence other than the one claimed),
`queue-state-pending-without-as-of` (`session_state_mismatch`; an entry
carrying `pending_interactions` with no `as_of_sequence`).

### Exit criteria

The five gate items; OpenCode advertises `session.message.delivery.queue`
`native`; Decision 0001's "at most one nonterminal run per session" is
amended to "at most one started run and an advertised number of queued
reservations".

## T3. Tool sources and control-layer tools

Unit names: `tool-sources` (T3a and T3b) and `control-tools` (T3c). Planned
decision: 0008 (one decision, three sub-units, each independently
advertisable).

### T3a. Catalog with sources

Wire, in [`action.schema.json`](../schema/v0.1/action.schema.json) and
[`capabilities.schema.json`](../schema/v0.1/capabilities.schema.json):

- `ToolSourceDescriptor`: `{ "id", "kind": "native" | "local" | "process" |
  "remote" | "hosted", "display_name"?, "protocol"?, "endpoint"? }`, the
  shape of [`examples/tool-source.json`](../examples/tool-source.json).
  MCP sources are `{ "kind": "process" | "remote", "protocol": "mcp",
  "endpoint": "stdio:…" | "https://…" }`.
- `action.tools.list.response` gains `sources: [ToolSourceDescriptor]`;
  `ToolDefinition` gains `source` (a source `id`, never an inline copy) and
  `features` (per-tool `FeatureSupport` map, as in the example). The
  example is aligned with this contract in the same change: it is now a
  session-scoped request and response pair on the flat envelope, each tool
  carries its source id rather than an inline descriptor copy, and each
  carries `execution_owner`. The
  capability descriptor's `tools` and `layers.*.tools` gain `sources` beside
  them.
- `action.call.*` payloads gain optional `source` (the source `id`), so a
  consumer can attribute a call to an MCP server without parsing names.
- `action.tools.list.request` gains an optional `session_id` payload
  member, and both list envelopes carry the envelope `session_id` when the
  catalog is a session's effective catalog; the response repeats it in its
  payload. It also gains an optional `allow_degraded_features`, the
  carrier `session.message.submit.request` and `models.request` have,
  because `action.tools.list` can be advertised `degraded` like any key
  and a list request is a request of its own: without a field to consent
  on, a caller could never construct the consenting query, and the
  endpoint would have to refuse every list with `capability_degraded` or
  serve degraded behaviour without consent, which T1's rule forbids. The
  rule is T1's unchanged — a list request on a descriptor advertising
  `action.tools.list` as `degraded` whose `allow_degraded_features` omits
  the key is refused `capability_degraded` with `details.feature` naming
  it, and a list response correlated to it is `degraded_without_optin`.
  The parameter threads through the surfaces below exactly as T5a's does. Today's empty request stays valid for an endpoint-level catalog
  (a static adapter). A session opened with `tool_sources` or `tools`
  answers session-scoped lists only, so a trace, a stdio consumer, and the
  validator can tie a catalog to the attachment it reflects.

Semantics: a source `id` is unique across a session's catalog and across
the `tool_sources` of one open, so a tool's `source` and a call's `source`
resolve to one descriptor and the hub routes to one endpoint even when one
owner serves several sources: a list declaring two sources with one `id`
is invalid (`duplicate_tool_source`), and an attach whose descriptors
collide with each other or with a declared source is rejected at open
(`unsupported_feature`, `details.reason: "unsatisfiable"`,
`details.source`). Likewise `name` is unique across a session's catalog,
whatever the sources, so a policy entry (T1) and a call resolve to one
catalog entry and one `execution_owner`: a list with two entries of one name is invalid
(`duplicate_tool_name`, the diagnostic T1 introduced for the catalog a
policy is judged against, applied here to every session catalog); an
attach or provide whose tools would collide
with the catalog or with each other is rejected at open with
`unsupported_feature` (`details.reason: "unsatisfiable"`, `details.tool`
naming the collision) rather than shadowed or renamed silently; a harness
that namespaces MCP tools (Claude's `mcp__<server>__<tool>`) exposes that
namespaced string as `name`, and `source` carries the attribution so the
name need not encode it. A tool naming a source must name one the same
response declared, and a call carrying `source` must name the source the
session's catalog
records for that tool, or a declared source when the tool is not in the
catalog (validator diagnostic `unmatched_tool_source` for both); every
session-scoped
list for a session opened with `tool_sources` or `tools` must declare every
attached source and provided tool (diagnostic `catalog_mismatch`), and the
usual scope agreement applies to both list envelopes (`scope_mismatch`); a
source is described, not managed, by this sub-unit; the harness runs the
client. Capability: `action.tools.list` (existing) with sources present.

Evidence: Claude `system/init` carries the tool list and MCP server list per
turn (`degraded` catalog, per-turn refresh); Codex `mcpToolCall.server` gives
the source on the call but no catalog (`source` on calls only). Claude
graduates T3a; its corpus case `tools-catalog-sources` projects the
`system/init` frame into `action.tools.list.response`.

### T3b. Attachment at session open

Wire: `session.open.request` gains `tool_sources: [ToolSourceAttachment]`
— a shape of its own, not the catalog's `ToolSourceDescriptor`. It
carries the descriptor's members and, for a `process` source, the
attachment-only `command`, `args`, and `environment` (the registry's
allowlist form: bare `NAME` forwards from the endpoint's own
environment, `NAME=value` passes literally). The separation is the point:
`environment` can hold a literal credential, `ToolSourceDescriptor` is
what `action.tools.list.response` and `session.state` publish back to
clients, and one schema serving both would make those members legal in a
catalog — so an implementation that reflected the open-time value
straight into its catalog would leak the secret and still validate.
`ToolSourceDescriptor` therefore keeps `additionalProperties: false`
without them, which makes the leak a schema rejection rather than a
convention, and the validator adds `attachment_field_in_catalog` for an
`action.tools.list.response` or `session.state` source carrying
`command`, `args`, or `environment`, so a tolerant or hand-rolled
serializer is caught too (fixture `tools-catalog-leaks-attachment-env`).
Redaction on the daemon's own logging and evidence paths is unchanged and
still required; this rule governs the wire. A `remote`
source carries only `endpoint` and is capability-gated (`mode: "remote"`
on `action.tool_sources.attach`; an open attaching a `remote` source on a
descriptor without that mode is rejected, and the validator diagnoses an
admitted one `unavailable_capability`).
A bare `NAME` resolves only if the adapter's registry entry allowlists it,
so a wire caller cannot read an ambient credential the operator did not
expose; a `NAME=value` literal is the caller's own secret on a loopback,
single-user wire, exactly as it is for the registry document today.

`session.state` gains `sources: [ToolSourceDescriptor]`, the field the
open response and later snapshots report the session's attached and
declared sources in. In Go that is two places, not one:
`protocol.SessionState` gains `Sources`, and so does
`protocol.SessionOpenResponse`, which is a separate struct
(`protocol/control.go:102`) that `serve/servehttp/server.go:237`
constructs on its own path — the schema aliases the two payload shapes
(`session.schema.json`'s `openResponse` is a `$ref` to `state`) but the
Go types do not, and adding the member to one alone would leave an open
carrying `tool_sources` unable to report the sources its own response is
required to carry. The open path populates it from the union the
attachment produced, and T3b's fixtures exercise the open response and a
later snapshot separately for that reason. The same split already
explains `current_model_id`, which T1 adds to `SessionOpenResponse` for
the same reason; `schema/v0.1/session.schema.json`'s state object is
closed and has no such member today, so without adding it an endpoint
would have to choose between omitting state the plan requires and
emitting a schema-invalid response, and the
`attachment_field_in_catalog` check above would have nothing legal to
inspect. It is the descriptor shape, never `ToolSourceAttachment`: the
sanitized projection is exactly the point, so `command`, `args`, and
`environment` cannot reach a client through state any more than through
a catalog.

Semantics: attachment is for the session's lifetime; the open response's
`sources` and the first `action.tools.list.response` reflect the attached
sources; an endpoint that cannot attach at open rejects the open with
`unsupported_feature` (`action.tool_sources.attach`). The two must agree:
the open response's `sources`, every later snapshot's, and the `sources`
of a list under the same revision are the union of the open's attachments
and the descriptor's declared sources, compared by `id` and by each
descriptor's published members, and a snapshot that omits an attached
source, adds one never attached or declared, or describes one
differently is `session_state_mismatch` (fixtures
`open-sources-state-omits-attachment` and
`open-sources-state-disagrees-with-catalog`). Runtime attach and
detach are deferred until evidence beyond Claude's `mcp_set_servers` exists.

Evidence: ACP `session/new.mcpServers` is a required array
(`{ name, command, args, env }` entries) that the adapter already fills from
its Go configuration; T3b moves that descriptor onto the wire at open, with
the adapter resolving allowlisted names into ACP's literal `env` values.
Claude spawns with MCP config. ACP graduates T3b with the corpus case
`open-with-tool-sources` (a stdio MCP descriptor passed through
`session/new`) and its live gate against docker/cagent.

### T3c. Control-layer-provided tools

Wire:

- `session.open.request` gains `tools: [ToolDefinition]` whose
  `execution_owner` must equal the opening participant: on the daemon its
  declared control participant, in a trace the participant
  `protocol.initialize.request` declared (envelopes carry no sender field,
  so the comparison is against the declared control participant, not a
  per-envelope identity). An open supplying a tool owned by anyone else is
  rejected before the session exists (`unsupported_feature`,
  `details.feature: "action.tools.provide"`, `details.reason:
  "unsatisfiable"`, `details.tool`); otherwise the adapter could admit a
  tool it would later classify as harness-owned or route to a participant
  that never provided it. So is a tool whose `source` names a source
  neither the descriptor nor the same open's `tool_sources` declares
  (`details.source`), for the same reason: a dangling source cannot be
  attributed or routed. Both are adapter rules that the validator enforces
  on the admission and on the refusal's code, not on the request
  (validator, below): the typed refusal is conforming and produces no
  diagnostic, a refusal under another code is diagnosed on the error
  response, and an open the adapter admitted despite either condition is
  `wrong_tool_owner` or `unmatched_tool_source` on the
  `session.open.response`, so the negative fixtures for both are traces
  of an adapter that admitted what it should have refused or refused it
  without saying why. Per-submit tool provisioning
  (Makai supplies tools per `agent_start`) is deferred; session-open is what
  Claude and ACP support and what Makai can accept at start.
- New envelope types `action.call.resolve.request` and
  `action.call.resolve.response`, session- and run-scoped, mirroring the
  permission resolve pair: request `{ "interaction_id", "session_id",
  "run_id", "tool_call_id", "requested_by", "responded_by", "started"? |
  "result"? | "error"? }` with exactly one of `started` (an empty object:
  the control participant has begun executing), `result`, or `error`;
  response `{ "interaction_id", "session_id", "run_id", "tool_call_id",
  "accepted", "reason"?, "details"? }` (`reason` present only with
  `accepted: false`, and `details` only alongside it: an object whose one
  member for now is `settled_by`, the envelope id of the settlement a
  `already_resolved` refusal points at, required with that reason and
  absent otherwise — the payload is closed, so the field the validator
  and the client recovery path depend on has to be declared here and on
  `protocol.ActionCallResolveResponse` rather than assumed. The reasons:
  `late_acknowledgement`, `repeated_acknowledgement`, `already_resolved`,
  `wrong_responder`, `unknown_interaction`). A refusal can satisfy
  several at once — a foreign responder sending a second `started` is
  both `wrong_responder` and `repeated_acknowledgement`, a foreign result
  after settlement both `wrong_responder` and `already_resolved` — and
  the response carries one reason, so they are ranked and the adapter
  reports, and the validator requires, the highest the request satisfies:
  `unknown_interaction`, `wrong_responder`, `already_resolved`,
  `repeated_acknowledgement`, `late_acknowledgement`. The order asks
  what the sender most needs to know. Whether the interaction exists
  comes first, then whether this sender may speak for it at all — a
  foreign responder's request is refused for being foreign however the
  interaction stands, since the state of a call it does not own is not
  its business — and only then how far the call has progressed, most
  advanced first. Fixture
  `control-call-foreign-responder-after-settlement` (positive; a foreign
  `result` after an accepted terminal, refused `wrong_responder`).
  `started` is an
  acknowledgement, not a resolution: it may
  appear at most once and only before the resolution.
- New capability key `action.tools.provide`: the control layer may supply
  tool definitions at session open and executes their calls. The existing
  `action.tools.execute` keeps the meaning the `+tools` unit gives it
  (normalized harness-side execution), so an old client reading a new
  descriptor and a new client reading an old descriptor both interpret it
  as today; only the new key gates control-owned execution.
- `active_runs[]` entries' `pending_interactions` (T2, where it lists
  the run's unresolved permission and user-input interactions) extends
  to control-owned calls, so the list names the run's unresolved
  interactions of every kind. It is the
  state surface a resolver that lost its resolve response reads: an
  interaction absent from the list was resolved, one still present was
  not, so a rejected resolution is re-sent and an accepted one is not.

Semantics, on the interaction contract Decision 0001 fixed:

- A call to a control-owned tool is an interaction: `action.call.requested`
  carries `interaction_id`, `requested_by` (the agent), `responded_by` and
  `execution_owner` (the control participant). The call stays `requested`
  until execution is evidenced: the adapter emits `action.call.started`
  when the control participant acknowledges with a `started` resolve, or,
  when the participant resolves with `result` or `error` without having
  acknowledged, immediately before the terminal it derives from that
  resolution (the result is the evidence). `started` is never emitted on
  the adapter's own initiative, so durations and cancellation keep the
  meaning the core gives them; a repeated or late acknowledgement is
  answered `accepted: false` and emits nothing. The validator's transition
  table is unchanged.
- Only the declared responder may resolve; one resolution; the adapter emits
  `action.call.completed` or `action.call.failed` from the resolution and
  feeds the result to the harness through its native bridge.
- Run cancel closes pending control-owned calls with
  `action.call.cancelled` before the run terminal, as the memory adapter
  already does for permission gates; an unacknowledged call goes from
  `requested` to `cancelled`, which the transition table already permits.
  Deadlines remain deferred (PF-2); a harness-side timeout settles an
  unacknowledged call as `cancelled` with the harness's code as the reason
  and an acknowledged one as `failed` with that code.
- A control-owned call is never routed to a harness-side executor, and a
  harness-owned call is never resolvable from the control layer
  (`wrong_interaction_responder`).
- Provided tools join the session's catalog: they are listed by
  `action.tools.list.response` with the opener as `execution_owner`, and a
  submit's `tool_choice` (T1) governs them like any other catalog tool. A
  run whose admitted policy excludes a provided tool never emits
  `action.call.requested` for it (`unapplied_control` otherwise); an
  adapter that cannot withhold a provided tool from the harness for one run
  rejects such a policy as unsatisfiable rather than exposing the tool.
- `tools` is accepted whole or not at all: an adapter that cannot provision
  every supplied definition rejects the open with `unsupported_feature`
  (`details.feature: "action.tools.provide"`, `details.reason:
  "unsatisfiable"`) rather than accepting a subset.
- That refusal has to be bounded, or the capability promises nothing. A
  native name shape, a schema dialect, a cardinality ceiling — any of
  these can make a well-formed array unprovisionable, and an adapter
  permitted to say so about any array could advertise
  `action.tools.provide`, refuse every array it is ever given, and pass
  conformance while honouring nothing. So a constraint must be
  *advertised to be exercised*: the feature's `FeatureSupport` carries
  `limits` declaring the ones the adapter actually has — `max_tools`, a
  `name_pattern`, the accepted schema dialect — and refusing a
  *protocol-valid* array is conforming only where it violates a declared
  limit. The qualifier is load-bearing: an array carrying a foreign
  `execution_owner`, a duplicated tool name, or a `source` that resolves
  to nothing is refused under the rules above, which name the defect and
  validate the refusal, and those refusals stay conforming however
  generous the limits are. What this rule reaches is the array with no
  defect any rule names. Refusing one that also satisfies every
  advertised limit is `undisclosed_provide_limit`, the same diagnostic shape T2 uses for an
  undisclosed queue bound and for the same reason: the refusal is itself
  the evidence that a constraint exists which the caller was never told
  about. The capability key plus its `limits` therefore tells a caller
  exactly which `tools` arrays are honored, and the key alone tells it
  that an unlimited one is.

Evidence: Makai's `tool_execute`/`tool_result` bridge is exactly this
boundary: the adapter's native codec already decodes both frames, every
`agent_message` already carries a `tools` list (empty today), and the ledger
names `tool-bridge-roundtrip` as a required case that the pinned corpus does
not yet contain. Makai graduates T3c by adding that case and advertising
`action.tools.provide`; Claude `sdkMcpServers` with
`mcp_message` reverse control and ACP reverse fs/terminal calls follow.

### Where the MCP client lives

Adapter-side passthrough (T3a and T3b) first: the harness owns the MCP
client; OAP describes, attaches, and observes. Serve-side connector second,
as a `serve` feature on T3c, and only onto adapters that can carry it: the
hub can host the MCP client, but it cannot execute a tool the adapter will
never call. Provisioning requires the adapter to accept
`session.open.request.tools`, to emit `action.call.requested` for a tool it
does not own, and to accept the outcome back through the reverse resolution
channel. Adapters without all three refuse `action.tools.provide` under the
ordinary capability gate, and two of the pinned ledgers already say they
will: DeepSeek has no reverse interaction channel on the selected wire
(`research/deepseek-harness-47f9438-mapping.md:327`) and Pi owns tool
execution with its interaction extensions disabled
(`research/pi-v0.85.1-mapping.md:250-253`). The connector therefore widens
the set of adapters that can host control-owned tools without a native MCP
client of their own; it does not make every adapter a candidate, and the
plan does not claim it does. Where it does apply, the hub hosts an MCP
client as one more
execution owner, provisions its tools through `session.open.request.tools`,
routes `action.call.requested` whose `execution_owner` is the hub to the MCP
server (acknowledging `started` as it dispatches), and resolves through
`action.call.resolve.request`. Ownership is an internal provisioning
boundary, not a second wire identity: on the adapter-facing wire the hub
is the declared control participant (the daemon is what sends
`protocol.initialize.request`), so every provided tool the adapter sees,
whether a client supplied it through `POST /adapters/{name}/sessions` or
the connector provisioned it, carries the hub's participant id as
`execution_owner`, and T3c's ownership rule holds without the hub
borrowing a client's identity. Clients do not supply that id, because nothing on the client-facing
wire tells them what it is: the HTTP binding has no `protocol.initialize`
exchange, and the handler opens every session as a fixed participant
(`serve/servehttp/server.go:209`, `serve.DefaultParticipant` at
`serve/serve.go:44`). Asking a client to name an owner it cannot learn
would make its own configurable participant the only value it has, and
that is a foreign owner the daemon must reject. So the daemon *stamps*
`execution_owner` instead: a client supplying `tools` through `POST
/adapters/{name}/sessions` omits the member, the daemon sets it to its
own participant id before forwarding the open, and a client that
supplies any `execution_owner` at all is refused at the daemon boundary
with the typed `unsupported_feature` (`details.feature:
"action.tools.provide"`, `details.reason: "unsatisfiable"`,
`details.tool` naming the entry), since there is no value it could
legitimately have chosen. The stdio frontend stamps the same way. The
id is then disclosed rather than guessed: `session.open.response` gains
`participant_id`, the identity the daemon acts as for that session, on
the same additive terms T1 uses for `current_model_id` on
`protocol.SessionOpenResponse`, so a client reading
`action.call.requested` can recognise the owner it will see there and a
trace can tie the two together. Ownership on the client-facing wire is
therefore something the daemon asserts and reveals, never something a
client has to know in advance. Fixtures `open-provide-client-owner-set`
(positive; a client-supplied `execution_owner` refused with the typed
refusal) and `open-provide-daemon-stamps-owner` (positive; tools
supplied without an owner, forwarded carrying the daemon's id, and the
open response disclosing that id).
Inside, the hub keeps a provisioning registry (tool name to the client
subscription that supplied it, or to the connector) to route each
`action.call.requested` and to accept a resolution only from the party
that provisioned the tool; a client resolving a connector-backed call is
refused at the hub with `wrong_interaction_responder`, exactly as the
adapter would refuse a foreign responder. The connector
serves adapters that lack a native MCP client but do carry the three
requirements above — the reference `memory` adapter and the
HyperNeo-style embedding — and not the ones that lack the requirements
themselves: DeepSeek and pi refuse `action.tools.provide` for the reasons
given above, and hosting the MCP client elsewhere does not change that.
It adds no wire vocabulary.

### Validator

- `action.tools.list.request` and `.response` are gated on the key the
  profile names for the catalog. Today `validation/state.go` sends both to
  `feature("tools")`, whose aliases resolve to `action.tools` and never to
  `action.tools.list`, so an endpoint advertising only `action.tools.list`
  would fail with `unavailable_capability`. The gate becomes
  `feature("tools.list")`, which accepts `action.tools.list` and nothing
  else. `action.tools` is deliberately not an alias: that family key
  means lifecycle observation today, and several descriptors — ACP, pi,
  Makai — advertise it while stating outright that they expose no
  portable catalog, so aliasing it would let a served catalog pass a
  gate the endpoint never claimed and defeat the rule that catalog
  behaviour is separately advertised. Adapters migrate to
  `action.tools.list` only on listing evidence of their own. The
  positive fixture `tools-catalog-list-only` carries a descriptor
  advertising `action.tools.list` alone.
- The gate is judged on the correlated response, never on the request,
  exactly as the `models.list` rule is. An endpoint that advertises
  neither key may still be asked for a catalog, and answering
  `unsupported_feature` is the correct behaviour — the refusal the
  proposed optional `ToolLister` surface produces when an adapter does
  not implement it — so raising `unavailable_capability` on the
  `action.tools.list.request` would diagnose the one endpoint that got
  it right. The validator instead remembers the unavailable expectation
  on the request (`sessionTrack.pendingToolsList`) and settles it at the
  correlated response: an `action.tools.list.response` that arrives
  anyway is `unavailable_capability` on that response; an
  `error.response` must carry `unsupported_feature` with
  `details.feature` naming `action.tools.list`, exactly as the T1
  control refusal names its control's key — the code alone does not tell
  the caller which capability to stop requesting — so a refusal under
  another code, or under `unsupported_feature` with `details.feature`
  absent or naming an unrelated capability, or without `details.reason:
  "unadvertised"`, is `unavailable_capability`
  on the `error.response`. Fixtures `tools-list-ungated-refused`
  (positive), `tools-list-refused-advertised` (`unhonoured_capability`
  on the `error.response`; a list request refused on an endpoint
  advertising `action.tools.list` — the `honour` fixture T0's
  corpus-completeness check requires), `tools-list-degraded-optin`
  (positive; a list request carrying `allow_degraded_features:
  ["action.tools.list"]` served by a `degraded` catalog),
  `tools-list-degraded-without-optin` (`degraded_without_optin` on the
  list response; the same request without the field, served anyway),
  `tools-list-degraded-refused` (positive; the same request refused
  `capability_degraded` with `details.feature: "action.tools.list"`),
  `tools-list-ungated-wrong-refusal` (another code),
  `tools-list-ungated-wrong-feature` (`unsupported_feature` with
  `details.feature: "run.model_selection"`) and
  `tools-list-ungated-wrong-reason` (`details.reason: "unsatisfiable"`).
- An `action.tools.list.response` correlated to a request that names a
  session (envelope or payload `session_id`) must carry that session on
  both its envelope and its payload; an unscoped response to a scoped
  request is `scope_mismatch`, not an endpoint-level catalog, so an
  adapter cannot evade the lifetime-catalog check by dropping the scope.
- `action.tools.list.response`: `duplicate_tool_name` for two entries
  sharing `name`, whatever their sources.
- `action.tools.list.response` and `session.open.request.tool_sources`:
  `duplicate_tool_source` for two descriptors sharing `id`. For the open,
  the check runs when the open is admitted (`session.open.response`) over
  the union of the attached descriptors and the sources the capability
  descriptor already declares, so an attachment reusing a declared id is
  diagnosed even in a trace that ends at the open or later lists a single
  entry under that id. A refusal is judged too, so the collision cannot
  be hidden behind an untyped failure: the validator retains it from the
  `session.open.request` (the duplicated id and the request's envelope
  id) and, when the correlated response is an `error.response`, requires
  `unsupported_feature` with `details.feature:
  "action.tool_sources.attach"`, `details.reason: "unsatisfiable"` and
  `details.source` naming the offending id; a refusal under any other
  code — `internal_error` included — or under the right code with
  `details.feature` absent or naming an unrelated capability, or without
  either of the other two details, is `duplicate_tool_source` on the
  `error.response`, as the dangling-source clause below validates its own
  refusal. The feature is not optional decoration: the wire's failure
  rules require every `unsupported_feature` to identify the missing
  capability in `details.feature`
  (`drafts/layered-agent-protocol.md:696`), and without it a caller
  cannot tell that attaching sources is what the endpoint refused. The
  check is rung 3, so it runs only once attachment is available: where
  the descriptor omits `action.tool_sources.attach` or advertises it
  `unavailable`, the capability rung owns the response, its
  `unadvertised` refusal is the conforming one, and the collision
  expectation is discharged — the same ordering the remote-mode check
  takes below. Fixture `tool-source-collision-unadvertised-attach`
  (positive). Fixtures `tool-source-collision-refused` (positive),
  `tool-source-collision-wrong-refusal` (another code),
  `tool-source-collision-wrong-feature` (`unsupported_feature` naming
  `action.tools.provide`) and `tool-source-collision-missing-detail`
  (`unsupported_feature` without `details.source`).
- Every accepted `capabilities.response`, initial or refreshed:
  `duplicate_tool_name` across its effective catalog (top-level `tools`
  and every `layers.*.tools`, unioned) and `duplicate_tool_source` across
  its declared `sources`, and `unmatched_tool_source` for each catalog
  tool whose `source` names none of its declared sources (both
  normalized across layers as the catalog is), so a descriptor that is
  ambiguous or dangling on its own is diagnosed before any list, open,
  or call: a session that never lists or selects tools cannot reach a
  call whose owner or source lookup is ambiguous, and a call that agrees
  with a dangling descriptor entry is not excused by that agreement.
- `catalog_mismatch`: `sessionTrack` records the `tool_sources` ids and
  the provided `tools` (name and `execution_owner`) from
  `session.open.request`; every session-scoped
  `action.tools.list.response` for that session must declare every
  recorded source id and list every provided tool under the recorded
  owner, or the diagnostic fires on that response. The comparison is of
  the whole entry, not the identity: an attached source must be listed
  with the `kind`, `protocol`, `endpoint`, and `display_name` it was
  attached with (the attach-only `command`, `args`, and `environment` are
  never listed), and a provided tool with the `name`, `description`,
  `input_schema`, `annotations`, `execution_owner`, and `source` (present
  or absent exactly as supplied) it was supplied with, `features` being
  the adapter's to fill; a redirected endpoint, an altered schema, or a
  tool moved to another declared source is `catalog_mismatch` just as an
  omission is, so attribution and routing cannot change during the
  session. Native
  entries may differ between lists (a harness refreshes its own catalog),
  but the open-time entries never drop out or change, because attachment
  is for the session's lifetime, provisioning is all-or-nothing, and
  runtime attach and detach stay deferred.
- `action.tools.list.response`: `unmatched_tool_source` for a tool naming an
  undeclared source. `action.call.*` payloads carrying `source`: the same
  diagnostic when the trace's catalog lists the named tool under a
  different source, or when the tool is not listed and the source is
  undeclared; the validator keeps the catalog per session (`sessionTrack`
  gains `tools`), so attribution to the wrong MCP server is caught even when
  both sources are declared. As with the models catalog, the session
  catalog is stored with the `capability_revision` it was served under and
  discarded when the active revision changes, so no `tool_choice` or
  sourced call is judged against stale names, owners, or sources until a
  list under the new revision replaces it; the open-time sources and
  provided tools are kept across the refresh, because they are
  session-lifetime facts the next list must still carry. What arrives in
  the gap is not skipped but retained, as T5a retains in-gap model
  selections: once a session has listed its catalog, the list is the
  authority and a gap defers rather than falls back to the descriptor's
  `tools`. The window before the *first* list defers on the same terms,
  not only a post-refresh gap: where `action.tools.list` is advertised
  the session catalog may carry native tools the endpoint descriptor
  does not name — session-specific entries are explicitly allowed to
  differ — so judging an early `tool_choice` against the descriptor
  would call a policy unsatisfiable that the adapter can satisfy
  perfectly well. Where `action.tools.list` is advertised and the trace
  never lists, the end-of-trace sweep does not fall back to the
  descriptor either: the list was the authority and the client simply
  never asked for it, so a retained choice is settled as the listed
  branch, exactly as the model sweep reads silence as the id being
  listed. Inferring a miss from a catalog nobody requested would convict
  an adapter for a query the client chose not to make, and the
  descriptor is known to be incomplete for precisely the tools at issue.
  The descriptor's effective catalog plus the open-time tools remains the
  authority only where no list is advertised at all, as T1 rules, since
  there nothing better exists and none is promised. Where a list is
  advertised, a choice submitted before the first one is
  retained rather than judged, and the first
  `action.tools.list.response` reconciles it exactly as a post-refresh
  gap is reconciled. Fixture `tools-select-before-first-list` (positive;
  `action.tools.list` advertised, a `tool_choice` naming a tool the
  descriptor omits, then a first list carrying it). So
  `sessionTrack.unjudgedTools` keeps every `tool_choice` submitted under
  an available `run.tool_selection` — admitted or refused, with the
  correlating response and, for a refusal, its code and details, as
  `unjudgedModels` keeps refused selections. A choice the capability rung
  already owns (the descriptor omits `run.tool_selection` or advertises
  it `unavailable`) is not retained at all, so a later catalog cannot
  rejudge a correct `unadvertised` refusal as owing `unsatisfiable`; the
  models rule is scoped the same way. The bookkeeping also keeps
  every `action.call.requested` emitted with a `source` or an
  `execution_owner` while the session has no catalog under the active
  revision, and the first `action.tools.list.response` under that
  revision reconciles them: a retained policy that lists or names a tool
  the catalog does not carry, or that is `required` or `named` against an
  empty filtered set, is `unsatisfiable_control` on that response with
  `details` naming the admission when it was admitted, and when it was
  refused is held to the same code-and-detail test the immediately judged
  path applies — `unsupported_feature` with `details.feature:
  "run.tool_selection"`, `details.reason: "unsatisfiable"`, and the
  detail the condition admits of: `details.tool` naming the offending
  tool where the policy names one or filters on one, and, where the
  policy names no tool and is unsatisfiable only because the filtered set
  is empty (`{ "mode": "required" }` against an empty catalog),
  `details.field: "tool_choice"` instead — there is no offending tool to
  name, and demanding one would leave that explicitly listed case with no
  conforming refusal. The immediately judged path takes the same detail
  on the same terms. With anything else
  `unsatisfiable_control` on that `action.tools.list.response`, naming
  the refusal it reconciles. Refusing an unlistable policy with
  `internal_error` in the gap is no safer than admitting it. Fixtures
  `tools-select-in-gap-refused` (positive; the typed refusal precedes the
  list that omits the tool) and `tools-select-in-gap-wrong-refusal`; a retained call whose `name` the
  catalog lists under another source or another owner, or does not list
  while its source is undeclared, is `unmatched_tool_source` or
  `wrong_tool_owner` on that response, naming the call. An adapter
  therefore cannot accept an unlisted policy or route a call under a
  stale attribution in the window between a refresh and its list and
  have the window hide it. Reconciliation runs at `native` and `emulated`
  `action.tools.list`, on the same terms as the models catalog.
- `action.call.requested` with `execution_owner` equal to a declared control
  participant must carry `interaction_id` and `responded_by`
  (`illegal_tool_transition`); `action.call.started` for such a call must
  be preceded by an `action.call.resolve.response` with `accepted: true`
  for its interaction, answering either the acknowledgement or the
  resolution (`illegal_tool_transition` otherwise, so an adapter cannot
  record execution the control participant has not evidenced, and a
  rejected exchange, `accepted: false`, evidences nothing). Acceptance is
  tracked per arm: `action.call.completed` or `.failed` for such a call
  must be derived from an accepted `result` or `error` resolution, so a
  terminal following a rejected result or error response is
  `illegal_tool_transition` even when an earlier acknowledgement was
  accepted. The accepted payload is stored too: the terminal's result
  must equal the accepted `result` and a failure's `error` the accepted
  `error`, compared as canonical JSON, otherwise the new diagnostic
  `resolution_payload_mismatch`, so an adapter that forwards something
  other than the participant's actual outcome to the harness cannot pass
  conformance with an authorized but altered terminal; its resolution
  follows the interaction rules (`unmatched_interaction`,
  `duplicate_interaction`, `wrong_interaction_responder`,
  `pending_interaction_at_terminal`).
- `action.call.resolve.request` and `.response` join correlation and scope
  checks; the resolution's `tool_call_id` must match the interaction's
  binding (`scope_mismatch`); a second `accepted: true` response to a
  `result` or `error`, or to a `started`, for one interaction is
  `duplicate_interaction` whatever the adapter then emits, and so is an
  accepted `started` after an accepted `result` or `error` for the same
  interaction (`interactionState` records the accepted terminal, and
  every later request of any arm must be answered `accepted: false`, as
  the semantics above require of a late acknowledgement), so "one
  resolution", "at most one acknowledgement", and "no acknowledgement
  once resolved" are all enforced at the response, not only at the
  events that follow. `accepted: false` is judged against the same
  state, not waved through: a resolution that the validator's
  `interactionState` says is valid — correctly scoped, from the bound
  responder, the first `started`, `result` or `error` for a pending
  interaction — must be accepted, and a refusal of it is
  `unmatched_interaction` on the `action.call.resolve.response`. Where a
  refusal is legitimate the enumerated reason must name the condition
  the validator observes (a late acknowledgement after an accepted
  terminal, a foreign responder, an unknown `tool_call_id`, a cancelled
  or settled call); a reason that names a different condition is
  `wrong_interaction_responder` or `duplicate_interaction` according to
  the condition actually present. Without this, an adapter could refuse
  the one valid resolution with an arbitrary reason, emit nothing, and
  let a later cancellation settle the call and the run so the trace
  passed. Fixtures `control-call-valid-resolution-rejected`
  (`accepted: false` to the first correctly scoped `result`, then a
  cancel) and `control-call-rejection-wrong-reason`.
- `session.open.request` with `tool_sources` or `tools` is judged through
  the `feature()` gate with `action.tool_sources.attach` or
  `action.tools.provide` on the correlated response, as T1 and T2 judge
  controls and deliveries: an admitted open (`session.open.response`) on
  a descriptor that omits the key or advertises it `unavailable` is
  `unavailable_capability` on the response, and a correlated
  `error.response` must carry `unsupported_feature` with `details.feature`
  naming that key and `details.reason: "unadvertised"`, the reason the
  ladder assigns to every capability-rung failure — a refusal saying
  `unsatisfiable`, or carrying no reason, tells the caller to change a
  value when what it must do is stop using a capability the endpoint does
  not have — and anything else is `unavailable_capability` on the error
  response, so the fail-closed refusal the wire requires is itself
  conforming. Fixture `open-unadvertised-wrong-reason`
  (`unavailable_capability` on the error response; the right code and
  feature under `details.reason: "unsatisfiable"`); a `remote` source additionally requires the attach
  capability to disclose `mode: "remote"`, judged on the same terms but
  only once attachment itself is available. The two are ordered, not
  concurrent: when the descriptor omits `action.tool_sources.attach` or
  advertises it `unavailable`, the gate above owns the response and its
  `unadvertised` refusal is the conforming one, so the mode check does
  not run and its expectation is discharged without diagnosis — a single
  `error.response` cannot carry both `unadvertised` and `unsatisfiable`,
  and a caller told the capability is missing has no use for a detail
  about one of its modes. Where attach is available and the mode is not
  disclosed, the missing mode is remembered from the request, an admitted
  open is `unavailable_capability` on the response, and a correlated
  `error.response` must carry `unsupported_feature` with
  `details.feature: "action.tool_sources.attach"`, `details.reason:
  "unsatisfiable"`, and `details.source` naming the source
  (`unavailable_capability` on the error response otherwise, so a
  refusal under an unrelated code such as `internal_error` cannot pass
  as the typed refusal that tells the caller the input is permanently
  unsupported). Fixture `tool-source-remote-unadvertised-attach`
  (positive; a `remote` source on a descriptor without the attach
  capability, refused with the gate's `unadvertised` refusal, where the
  mode check would otherwise claim the same response). Each supplied
  tool's
  `source`, when present, must name a source in the union of the same
  open's `tool_sources` and the sources the capability descriptor
  declares, checked when the open is admitted (`unmatched_tool_source` on
  the `session.open.response`), so a trace that ends after the open cannot
  carry a provided catalog entry that resolves to no source. An adapter
  that refuses such an open with the typed `unsupported_feature`
  (`details.feature: "action.tools.provide"`, `details.reason:
  "unsatisfiable"`, `details.source` naming the dangling source), as the
  wire rule requires, produces no diagnostic: the condition is remembered
  from the request, and a correlated refusal under any other code or
  without that detail is `unmatched_tool_source` on the error response,
  as the remote-attachment clause validates its refusal. The owner rule
  and the name collision are validated the same way (`unsupported_feature`
  with `details.tool`; `wrong_tool_owner` or `duplicate_tool_name` on a
  refusal under another code).
- Attachment is bounded the same way provision is, and for the same
  reason: every rule above judges a refusal that names a defect the
  validator can see — a dangling source, a duplicate id, an undisclosed
  mode — and none of them reaches a refusal of an attachment with no
  defect at all. An adapter could advertise `action.tool_sources.attach`,
  refuse every well-formed `tool_sources` array as `unsatisfiable`, and
  satisfy every rule here. So the attach capability discloses its
  constraints in `limits` — `max_sources` and the `transports` it
  accepts — and refusing an array that violates none of them is
  `undisclosed_attach_limit` on the `error.response`, the shape the queue
  bound and the provide limits already use. Fixtures
  `tool-source-attach-refused-within-limits`
  (`undisclosed_attach_limit`) and its positive counterpart
  `tool-source-attach-refused-over-limit` (a refusal of an array
  exceeding a disclosed `max_sources`).
- `session.open.request.tools[*].execution_owner` must be the declared
  control participant (`wrong_tool_owner`, diagnosed on the
  `session.open.response` when the open is admitted, on the same terms as
  the source check). The check runs at supply time, before any interaction
  exists, which `wrong_interaction_responder` cannot cover. The same
  diagnostic fires on an `action.call.requested` whose
  `execution_owner` differs from the owner the session catalog records
  for that `name`, so an adapter cannot route a harness-owned tool to the
  control participant, or a provided tool to the harness, under a
  lifecycle that is otherwise consistent; the existing check that the
  owner does not change mid-lifecycle stays as it is.
- `session.open.request.tools[*].name` must be unique within the array
  and against the descriptor's native `tools`, checked when the open is
  admitted (`session.open.response`), so a collision the adapter should
  have refused is `duplicate_tool_name` even in a trace that only opens
  the session and never lists or selects tools. The check is repeated on
  every revision-changing descriptor (`capabilities.updated` followed by
  the new `capabilities.response`): each open session's retained provided
  tools are compared with the new native `tools`, and its retained
  attached source ids with the newly declared sources, diagnosing
  `duplicate_tool_name` or `duplicate_tool_source` on the descriptor
  envelope, since a post-refresh list is not mandatory and an ambiguous
  catalog would otherwise go unnoticed. The supplied-tool source check is
  rerun at the same point: every retained provided tool's `source` must
  still resolve against the retained attachments plus the new
  descriptor's declared sources (`unmatched_tool_source` on the descriptor
  envelope otherwise), so a refresh cannot leave a session with a provided
  tool whose source resolves nowhere. An adapter's refresh therefore
  keeps declaring any source a retained provided tool references, or the
  refresh is a conformance failure. An adapter whose refresh would
  introduce a colliding native tool must namespace it or keep it out of
  the session's catalog; it never shadows a provided tool.
- `session.state` snapshots: T2's equality check on each `active_runs[]`
  entry's `pending_interactions` now counts control-owned calls among
  the unresolved interactions (`session_state_mismatch` on omission or on
  a resolved interaction still listed), the same check T4 applies to
  `pending_steers`.

### Reference adapter

`adapter/memory.go` declares two sources (`native` for `scripted_tool`, a
synthetic `process`/`mcp` source), accepts `tool_sources` at open and lists
them, accepts every control-owned `ToolDefinition` supplied at open (no
cardinality limit; all are listed; a name colliding with `scripted_tool` or
with another supplied definition rejects the open), and after the
permission gate calls the
first provided tool in open order that the run's admitted `tool_choice`
selects: `requested` with the opener as owner, `started` when the opener
acknowledges (or immediately before the terminal when it resolves without
acknowledging), then the terminal from `Resolve`; a cancel before the
acknowledgement settles the call `cancelled` from `requested`. The provided
tools join the reference catalog next to `scripted_tool`, so the T1 rules
apply to them unchanged: `mode: "none"` or a filter that excludes them
skips the call, and `named` naming one of them calls that one.
`InteractionResolution` gains a third arm `ToolCall
*protocol.ActionCallResolveRequest` carrying whichever of the three arms
the request used.

### Surfaces

- `adapter`: `OpenRequest` gains `ToolSources` and `Tools`;
  `InteractionResolution.ToolCall`; optional interface `adapter.ToolLister`
  (`Tools(ctx, protocol.ToolsListRequest) (protocol.ToolsListResponse,
  error)`) for the catalog, the request being the payload struct so the
  adapter applies the opt-in rule on exactly what the wire said.
  `Session.Resolve` keeps its error-only signature, which today cannot
  express the `accepted: false` answer the semantics require for a
  repeated or late `started` (every error becomes an `error.response`,
  `resolution_rejected` for the interaction-state errors, and
  `accepted: true` is built only after a nil return), so T3c adds the
  sentinel `adapter.ErrResolutionRefused`, wrapping one of the typed
  reasons above, for the tool-call arm's protocol-level refusals; the
  hub and every binding (`servehttp`, the stdio frontend, an in-process
  embedder) map it to an `action.call.resolve.response` with
  `accepted: false` and that `reason`, and every other error keeps the
  existing `error.response` mapping. The permission and user-input arms
  keep their current behavior; aligning them is a question for the 0008
  decision, not a change this unit makes. The memory adapter returns the
  sentinel for a repeated or late `started` and for a resolution after
  the interaction settled; `adaptertest` asserts the sentinel, and the
  `servehttp` test asserts the `accepted: false` response on the wire.
- `serve`: `Session.Tools(ctx, protocol.ToolsListRequest)`, forwarded
  unchanged; `Session.Resolve` passes the new arm
  under the same publication gate T4 specifies for steer, held here
  across the whole of `Resolve`: the run's drainer buffers everything it
  reads from arming until `Published`, since no envelope emitted inside
  `Resolve` may precede the accepted response and the resolve response
  names no position for a partial release, because a
  participant that resolves synchronously lets the adapter emit
  `action.call.started` and the terminal inside `Resolve`, before the
  binding has written the accepted resolve response, which is the order
  the validator rejects. The hub gates the run's drainer across
  `Resolve`, the binding lifts it with `Session.Published(token)`, the
  request-unique barrier T4 specifies, serialized per run with every
  other gated operation so that overlapping resolutions never share a
  gate, after writing the response. Cancellation joins that
  serialization. `Session.Cancel` takes the run's gate like any other
  gated operation, because a cancel that wins the adapter's operation
  lock while a resolve gate is armed makes the call cancelled and the
  adapter's `accepted: false` with `already_resolved` correct — while
  the `action.call.cancelled` that proves it sits withheld in the buffer
  until after the resolve response, so the validator would see a pending,
  valid resolution and diagnose the refusal. Serializing it removes the
  race rather than teaching the validator to compensate for it: a cancel
  arriving while a resolution holds the gate waits, subject to its own
  context, so either the resolution settles first and the cancel finds
  the call settled, or the cancel settles first and its
  `action.call.cancelled` is published before the resolve request is ever
  passed to the adapter. Cancellation is not thereby delayed
  indefinitely — the gate is held only across one adapter call — and a
  caller that needs to abandon work regardless keeps the context it
  already had. The fixtures describe the two orders the gate permits and no other,
  since a cancel that arrives while a resolution holds the gate cannot
  overtake it: `control-call-cancel-wins-gate` (positive; a cancel and a
  resolution issued concurrently where the cancel takes the run's gate
  first — its `action.call.cancelled` is published, then the resolution,
  which waited on the gate, is passed to the adapter and answered
  `already_resolved`, correctly, because the cancellation it reflects
  precedes it in the trace) and `control-call-resolution-wins-gate`
  (positive; the resolution takes the gate first, is accepted and
  published, and the cancel, which waited, finds the call settled and
  reports nothing to cancel). A trace in which a resolution holding the
  gate is answered `already_resolved` by a cancellation published
  *during* it is not a third case but a serialization violation, and the
  validator diagnoses it as the pending-resolution refusal above.
  Serialization only reaches what the hub schedules, and a harness-side
  timeout that emits `action.call.cancelled` or `.failed` is not that: it
  can settle the call after the gate is armed and before `Resolve` reads
  adapter state, making `already_resolved` correct while the event
  proving it sits in the buffer. The refusal path therefore takes the
  boundary treatment T4 gives its error path, for the same reason and to
  the same shape. Withholding everything until `Published` is right only
  for an accepted resolution, where no envelope may precede the accepted
  response; a refusal accepts nothing, so there is nothing to order
  behind. On `accepted: false` the hub drains the run's stream without
  blocking and publishes the prefix up to the boundary the adapter states
  — the timeout's terminal among it — ahead of the resolve response, then
  lifts the gate. The boundary has to be stated, exactly as T4's steer
  error path states one and for the same reason: the hub cannot tell from
  the buffer which envelopes the adapter emitted before returning, and
  the gated drainer keeps reading, so a settlement the harness performs
  immediately *after* `Resolve` returns can land in the buffer before the
  drain runs. Publishing it would be worse than useless here, because it
  is the reason the refusal gives that is at stake: an adapter that
  correctly answers `repeated_acknowledgement` or `late_acknowledgement`
  would be read at response time as owing `already_resolved` under the
  reason precedence, and its valid decision rejected. So the refusal
  carries `TargetSequence`, the last sequence of the run the adapter made
  readable before returning; the hub publishes to it and no further, and
  the remainder follows the response when the gate lifts. A refusal that
  states no boundary examined nothing, and nothing emitted since arming
  precedes it. Fixture `control-call-settles-after-refusal` (positive; a
  repeated acknowledgement refused `repeated_acknowledgement` while the
  harness settles the call immediately after the return, the settlement
  published after the response). The validator's response-time reading of the
  interaction then sees the settlement that justifies the refusal,
  whatever settled it, and no rule has to special-case which producer
  did. That orders the hub's trace, and over HTTP the trace is not what
  the client reads: the lifecycle event travels the SSE connection and
  the refusal the POST body, so the refusal can arrive first and a valid
  rejection look arbitrary. Draining cannot fix that, for the reason T4
  gives — the server does not control the client's sockets — so the
  refusal carries its own justification instead of depending on arrival
  order. An `accepted: false` whose reason is `already_resolved` names
  the envelope that settled the call in `details.settled_by`, and the
  validator requires that id to be a settlement the trace actually
  carries for that interaction (`unmatched_interaction` otherwise, so
  the field cannot be invented). A client reading the refusal first has
  the id in hand and can wait for, or look up, the event it names rather
  than concluding the resolution was refused for no reason; a client
  reading the event first was never confused. Fixtures
  `control-call-timeout-races-resolution` (positive; a
  harness timeout settles the call while the resolution is in flight, the
  `action.call.cancelled` precedes the `accepted: false`, which names it)
  and `control-call-refusal-invents-settlement`
  (`unmatched_interaction`; `details.settled_by` names an envelope that
  settled nothing). The context-done fallback reconciles as the
  steer fallback does rather than lifting the gate blind: the hub first
  publishes a `session.state.updated` whose `active_runs` entry for the
  run reflects `pending_interactions` after the request was answered:
  the interaction absent only when an accepted `result` or `error`
  settled it, and still present when the request was rejected or when it
  was an accepted `started` acknowledgement, which is not a resolution
  and leaves the call awaiting its terminal, then lifts the gate. The
  accepted response is recorded in
  the hub's trace before the released events, so the validator's
  accepted-response rule holds in every trace, and a resolver that never
  saw the response learns the outcome from state instead of guessing from
  events it cannot attribute to acceptance. The optional
  MCP connector is a separate package `serve/mcpconnect`, out of scope for
  the 0008 decision but designed against it.
- `serve/servehttp`: `GET /sessions/{id}/tools` returning
  `action.tools.list.response`, with the opt-in as a repeatable
  `?allow_degraded=<key>` query parameter mapped onto the payload before
  the daemon mints the request, as the models GET does; the stdio `tools`
  op takes `allow_degraded_features` directly; `POST /sessions/{id}/resolve` accepts
  `action.call.resolve.request` and calls `Session.Published` after
  writing the response; `POST /adapters/{name}/sessions` forwards
  `tool_sources` and `tools`. Stdio ops `tools` and the extended `resolve`
  (with the same post-write release) and `open`.
- `client` and `clients/ts`: `Open` options for sources and tools;
  `Session.Tools(ctx, ...ToolsOption)` with `AllowDegraded(keys
  ...string)` and `session.tools({ allowDegradedFeatures? })`, sent only
  when given so an unmodified call is byte-identical to today's;
  `Session.ResolveToolCall` in all three arms
  (acknowledge, result, error), so a control layer can report that it has
  begun executing before it has a result. The cross-stream repair T4
  states for steer settlements covers this path too, and for the same
  reason: over HTTP the resolve response is a POST body and the
  `action.call.started` or terminal it releases travels the SSE
  connection, so `Published` orders the hub's trace but not the client's
  two sockets, and a caller could see the call settle before the
  `accepted: true` that authorized it. The correlation has to be at the
  request, not the interaction. `tool_call_id` identifies the call, and
  two resolutions of one call can be outstanding at once — a result and
  its retry — so a client holding on the call alone cannot tell which
  request authorized the event it is holding: it may release on the
  retry's refusal before the original's acceptance arrives, or hold
  forever if the request it happened to pair the event with never
  returns. So a resolve-derived `action.call.started` or terminal carries
  `request_id`, the envelope id of the `action.call.resolve.request` that
  produced it, exactly as a steer settlement names its admitting request,
  and the client rule keys on that: hold an event whose `request_id`
  names a resolve call still outstanding, release it when that call
  returns. The validator requires `request_id` to name a resolve request
  the trace carries for that interaction, and an accepted resolution to
  be followed by events naming it (`unmatched_interaction` otherwise), so
  the field cannot be omitted or invented. Stating it once for both
  settlements and resolutions keeps the
  two arms of the same hazard from drifting apart. Fixtures
  `control-call-overlapping-resolutions` (positive; a result and its
  retry outstanding together, each derived event naming its own request)
  and `control-call-event-unmatched-request`
  (`unmatched_interaction`; a derived event whose `request_id` names no
  resolve request for the interaction). The e2e tests cover
  SSE-first delivery on each.

### Fixtures

Positive: `tools-catalog-with-sources`, `tools-catalog-list-only`,
`tools-catalog-refresh-replaces` (a tool added by the revision 2 list is
selectable after `capabilities.updated`), `open-attach-process-source`,
`control-tool-roundtrip` (acknowledgement, then result),
`control-tool-resolved-without-ack` (`started` immediately before the
terminal), `control-tool-cancelled-with-run` (an unacknowledged call
settles `cancelled` from `requested`),
`control-tool-withheld-by-tool-choice` (a provided tool, a submit with
`mode: "none"`, no call), `open-provide-two-tools` (both listed, the
selected one called). Negative:
`tools-unmatched-source` (`unmatched_tool_source`),
`tools-list-response-unscoped` (`scope_mismatch`; a session-scoped
request answered without `session_id`),
`tools-select-removed-after-refresh` (`unsatisfiable_control`; a tool
dropped by the revision 2 list, then named),
`tools-duplicate-name` (`duplicate_tool_name`),
`tools-duplicate-source-id` (`duplicate_tool_source`; two sources with one
`id` and different endpoints), `descriptor-duplicate-tool-name`
(`duplicate_tool_name`; one name under `tools` and under
`layers.action.tools`), `descriptor-duplicate-source-id`
(`duplicate_tool_source`; two declared sources with one `id`),
`tools-catalog-omits-attachment` (`catalog_mismatch`; an open with a
process source and a provided tool, then a first list omitting the
source), `tools-catalog-drops-attachment-later` (`catalog_mismatch`; a
correct first list, then a second list omitting the provided tool),
`tools-catalog-redirects-source` (`catalog_mismatch`; the attached source
listed with another `endpoint`), `tools-catalog-alters-provided-schema`
(`catalog_mismatch`; the provided tool listed with another
`input_schema`), `tools-catalog-moves-provided-source`
(`catalog_mismatch`; the provided tool listed under another declared
source),
`open-provide-colliding-name` (positive, `valid: true`, no diagnostic;
`error.response` with `unsupported_feature`, then no open),
`open-provide-wrong-owner` (`error.response` with `unsupported_feature`,
`details.feature: "action.tools.provide"`, `details.reason:
"unsatisfiable"`, then no open; positive, `valid: true`, no
diagnostic),
`open-provide-refused-within-limits` (`undisclosed_provide_limit`; an
`unsatisfiable` refusal of a `tools` array that satisfies every limit the
endpoint advertised, which is the check that stops an endpoint
advertising `action.tools.provide` and honouring nothing) and its
positive counterpart `open-provide-refused-over-limit` (`valid: true`, no
diagnostic; the same refusal where the array exceeds an advertised
`max_tools`),
`open-provide-wrong-owner-admitted` (`wrong_tool_owner`; a supplied tool
whose `execution_owner` is not the declared control participant, and the
open admitted),
`open-provide-dangling-source` (positive, `valid: true`, no diagnostic;
`error.response` with `unsupported_feature` and `details.source:
"ghost"`, then no open),
`open-provide-dangling-source-wrong-refusal` (`unmatched_tool_source` on
the error response; the same open refused with `internal_error`),
`open-provide-wrong-owner-wrong-refusal` (`wrong_tool_owner` on the
error response), `open-provide-colliding-name-wrong-refusal`
(`duplicate_tool_name` on the error response),
`open-provide-colliding-name-admitted` (`duplicate_tool_name`; two
supplied tools sharing a name, and the open admitted),
`tools-refresh-collides-with-provided` (`duplicate_tool_name`; a provided
tool `foo`, then a refreshed descriptor whose native tools include
`foo`), `tools-refresh-removes-provided-source` (`unmatched_tool_source`;
a provided tool referencing a descriptor-declared source, then a refresh
that no longer declares it),
`tools-descriptor-dangling-source` (`unmatched_tool_source` on the
`capabilities.response`; a descriptor tool with `source: "ghost"` and no
such declared source),
`tools-select-in-gap-unlisted` (`unsatisfiable_control` on the revision 2
list; a `named` policy admitted between `capabilities.updated` and the
new list, which then omits the tool), `tools-call-in-gap-wrong-source`
(`unmatched_tool_source` on the revision 2 list; a sourced call emitted
in the gap that the new list attributes to another source), and the
positive `tools-select-in-gap-listed` (the retained policy found listed
when the revision 2 list arrives),
`open-attach-colliding-source-admitted` (`duplicate_tool_source`; an
attached source reusing an id the descriptor declares, and the open
admitted),
`tools-call-source-mismatch` (`unmatched_tool_source`; a call naming one
tool with another tool's declared source),
`control-tool-started-before-ack` (`illegal_tool_transition`),
`control-tool-started-after-rejected-ack` (`illegal_tool_transition`;
`accepted: false`, then `started`),
`control-tool-terminal-after-rejected-result` (`illegal_tool_transition`;
an accepted acknowledgement, a rejected `result`, then `completed`),
`control-tool-result-mismatch` (`resolution_payload_mismatch`; an
accepted result X, then `completed` carrying Y),
`control-tool-error-mismatch` (`resolution_payload_mismatch`; an accepted
error, then `failed` carrying a different one),
`control-tool-state-omits-pending` (`session_state_mismatch`; a snapshot
without the unresolved call in `pending_interactions`),
`control-tool-call-owner-mismatch` (`wrong_tool_owner`; the catalog lists
the tool as harness-owned, the call names the control participant),
`control-tool-called-despite-none` (`unapplied_control`),
`control-tool-wrong-owner` (`wrong_interaction_responder`),
`control-tool-pending-at-terminal` (`pending_interaction_at_terminal`),
`open-attach-unadvertised` (`unavailable_capability` on the admission;
the open admitted), the correct rejections
`open-attach-unadvertised-rejected` and
`open-provide-unadvertised-rejected` (`error.response` with
`unsupported_feature`, `details.feature` naming
`action.tool_sources.attach` or `action.tools.provide`, then no open),
`open-attach-remote-unadvertised` (`unavailable_capability` on the
admission; a `remote` source attached on a descriptor whose attach
capability lacks `mode: "remote"`, and the open admitted),
`open-attach-remote-unadvertised-rejected` (`error.response` with
`unsupported_feature`, `details.feature: "action.tool_sources.attach"`,
`details.reason: "unsatisfiable"`, `details.source`; validated as a
correct rejection), `open-attach-remote-wrong-refusal`
(`unavailable_capability` on the error response; the same open refused
with `internal_error`), `open-provide-dangling-source-admitted`
(`unmatched_tool_source` on the `session.open.response`; a provided tool
with `source: "ghost"` that no descriptor or attachment declares, and the
open admitted, the negative counterpart of `open-provide-dangling-source`),
`control-tool-double-accepted-resolution`
(`duplicate_interaction`; two `result` resolutions for one interaction
both answered `accepted: true`), `control-tool-double-accepted-ack`
(`duplicate_interaction`; two `started` acknowledgements for one
interaction both answered `accepted: true`),
`control-tool-ack-after-result-accepted` (`duplicate_interaction`; an
accepted `result`, then a `started` for the same interaction also
answered `accepted: true`).

### Exit criteria

The five gate items per sub-unit; Claude, ACP, and Makai advertise their
sub-unit; the profile draft's feature-gate row for
`action.tool_sources.attach` and a new row for `action.tools.provide` cite
the fixtures; the daemon README documents the credential rule for process
sources.

## T4. Steer

Unit name: `steer`. Planned decision: 0009. This section is a pre-design for
that decision, not a settled shape: it is the one unit that adds a new
pending lifecycle inside a run, and its decision must be written from a pi
corpus case first.

### The question

`steer` injects guidance into an active run. Unlike `queue`, it creates no
run and no sequence domain of its own, so the control layer cannot observe
from `run.started` that its guidance took effect. Every native source makes
the application observable in a different way: pi at the next turn boundary
(`queue_update` shrinks and the injected user message appears in the
stream), OpenCode when the steered input is promoted (`prompted` after
`prompt.admitted`), Hermes immediately (the busy result is `steered`), and,
per unpinned research only, Codex on the `turn/steer` response against an
expected active turn (the pinned Codex ledger defers `turn/steer`, so this
is not evidence until a new pin covers it). pi also lets a
steer be withdrawn before application (`clear_queue`). The protocol
therefore needs a steer settlement, not only a steer admission.

### Proposed shape

- Admission: `session.message.submit.request` with `delivery: "steer"` and
  optional `target_run_id` (additive field). Absent, the target is the
  session's started run; a supplied target must be that run. Any other
  target, or no started run, fails before admission with typed
  `invalid_steer_target` (`details.reason`: `no_active_run`, `terminal`,
  `queued`, `cross_session`, `unknown_target`, `not_steerable`).
  `unknown_target` is the reason for a supplied `target_run_id` the
  endpoint has no run for; it ranks with `cross_session` ahead of every
  lifecycle reason, as the validator section sets out, because both say
  the caller has the wrong run rather than a badly timed one.
  `not_steerable` names
  exactly one lifecycle state: the target is started and nonterminal but
  `cancelling`, where accepted cancellation forecloses further guidance
  (a steer already pending on such a run is dropped with `run_terminated`
  at the terminal, as the barrier rules). A harness-specific inability to
  steer a running run is not a target condition: an adapter that meets
  one reports it as evidence for the 0009 decision, which may extend the
  vocabulary, and until then `not_steerable` for a target that is not
  `cancelling` is a wrong reason. A steer carries no run
  controls: `model_id`, `instructions`, `tool_choice`, and
  `output_schema` on a `delivery: "steer"` submit are rejected before
  admission with `unsupported_feature` (`details.feature` naming the
  control's key, `details.reason: "unsatisfiable"`), because the target
  run's admitted controls are authoritative until its terminal and a
  steer admits no run for new controls to bind. Decision 0003's
  "re-send the controls on each submit" therefore applies to submits that
  admit a run. The validator judges this on the correlated response, not
  on the admission alone, because the refusal is the required behaviour
  and nothing else would catch a wrong one: T1 has no quarrel with a
  `model_id` that is perfectly valid in itself, so an adapter answering
  `internal_error` here would otherwise escape both rules. The
  steer-specific expectation is retained from the request and settled at
  the response — a `steered` admission is `unsatisfiable_control`, and a
  correlated `error.response` must carry `unsupported_feature` with
  `details.feature` naming the control's key and `details.reason:
  "unsatisfiable"` (the control is offered and the request understood;
  it is the combination with a steer that cannot be honoured), with any
  other code, feature, or reason diagnosed `unsatisfiable_control` on the
  `error.response`. Fixtures `steer-with-controls-rejected` (a correct
  rejection), `steer-with-controls-admitted` (`unsatisfiable_control`)
  and `steer-with-controls-wrong-refusal` (`unsatisfiable_control` on the
  error response; the same steer refused `internal_error` on an endpoint
  advertising both steer and the control).
- Response: `admission: "steered"`, `effective_delivery: "steer"`, `run_id`
  set to the target, `target_sequence` (additive, required on a `steered`
  admission) naming the target run's last emitted sequence at admission,
  `status` equal to the target's status after that sequence, which is
  the status after the last target-run envelope the adapter emitted
  before returning, and the `submission_id` that names the pending steer.
  The sequence makes the admission's position in the target's stream an
  explicit fact the adapter states rather than one the hub or the
  validator infers from surrounding transitions.
- Settlement, two new run-scoped events in the target run's sequence domain:
  `run.steer.applied` `{ "session_id", "run_id", "submission_id",
  "request_id", "message_ids", "boundary": "immediate" | "turn" |
  "tool_result" | "unknown" }` and `run.steer.dropped` `{ "session_id",
  "run_id", "submission_id", "request_id", "reason": ProtocolError }`,
  where `request_id` is the envelope `id` of the submit request the
  admission answered, so a settlement is attributable to the caller that
  submitted it without the caller ever having seen the admission. The
  adapter is the party that emits these, so it has to be given the id:
  `adapter.Session.Submit` takes only `protocol.MessageSubmitRequest`
  today (`adapter/adapter.go:20`) and `InteractionResolution` only the
  decoded resolve payload (`:63-68`), with `servehttp` discarding the
  outer envelope before either call, so an adapter that applies a steer
  immediately or settles a tool call synchronously cannot name the
  request it is answering. The id therefore travels through the
  operation rather than being invented at the wire, and each half lands
  in the earliest unit that needs it rather than waiting for T4, so the
  vertical slices stay independently implementable: `Submit` takes an
  `adapter.SubmitRequest { Request protocol.MessageSubmitRequest;
  EnvelopeID protocol.EnvelopeID }` **in T2**, which needs it for the
  capture markers under decision 0007, and `InteractionResolution` gains
  `EnvelopeID` **in T3c**, which needs it for resolve-derived
  `request_id` under decision 0008; T4 adds no API change of its own and
  simply reuses both. Each is populated by every binding — `servehttp` and the
  stdio frontend from the request they decoded, an in-process embedder
  from the envelope it built. The `Submit` change is a compile-time break
  across all nine implementations of `adapter.Session` — the eight pinned
  native adapters (ACP, Claude, Codex, DeepSeek, Hermes, Makai, OpenCode,
  pi) and the memory reference — and is meant to be: an adapter that
  ignores
  the new member keeps compiling only because it does not emit correlated
  events, and one that does emit them cannot silently omit the
  correlation, and because it lands in T2 the break is absorbed by the
  first unit rather than by the last. `adaptertest` asserts the id
  reaches the adapter and
  appears on every settlement derived inside the call. A settlement is never
  observable before the admission that names its `submission_id`: a
  harness that applies immediately emits `applied` as the first envelope
  after the response, never before it (the barrier is specified under
  Surfaces).
- State: the target's `active_runs` entry (T2) gains `pending_steers:
  [{ "submission_id", "request_id" }]` (additive), listing admitted
  steers until they settle, each with the envelope `id` of the submit
  request that admitted it. A submitter that lost the admission response
  therefore recovers by the request id it minted itself: a matching entry
  in `pending_steers` gives it the `submission_id`, and a settlement on
  the stream carrying its `request_id` tells it the outcome. A request id
  found in neither is not conclusive while the target run is nonterminal:
  the submit the caller gave up on may still be in flight in the adapter
  and be admitted after the snapshot was taken (the hub registers a
  pending steer only once the adapter returns, under Surfaces), and a
  resend would then be admitted as a second steer, because envelope ids
  are unique per trace (`duplicate_envelope_id`) and the daemon
  deduplicates nothing: a resend is a new request, not a replay. The
  caller therefore keeps reading state and the stream rather than
  resending, and absence becomes conclusive at the target run's terminal,
  since every pending steer settles before it and a `steered` admission
  is legal only against a nonterminal run: a request id that has appeared
  in neither by then was never admitted. That conclusion holds only for a
  caller whose view of the run is gap-free, and the plan says so rather
  than leaving it implied. A client whose cursor fell outside the bounded
  replay window may reach the terminal having seen neither the admission
  response nor the settlement, and absence from both surfaces then proves
  nothing — the steer may have been admitted and applied inside the gap.
  Two things close that. A resume that cannot be served from the window
  is reported as such rather than silently starting fresh, so a client
  knows its view has a hole and knows not to draw the conclusion; and
  settled steers are retained where a terminated run's history can
  survive. Not on `active_runs`, which T2 defines as the nonterminal set
  and which drops each entry at its terminal — putting the history there
  would lose it at exactly the moment the caller most needs it, since the
  terminal is when absence was supposed to become conclusive. Instead
  `session.state` gains a session-level `settled_steers`, listing each
  settled submission with its `run_id`, `submission_id`, `request_id`,
  and whether it was `applied` or `dropped`, and entries survive their
  run's terminal. It is bounded like any recovery surface — the hub keeps
  the most recent entries per session, the same retention the journal
  uses — so it cannot grow without limit over a long session. The bound
  needs a marker of its own: `oap-replay-gap` reports a run-stream cursor
  that fell outside the window and says nothing about this list, which is
  retained independently and can evict an older entry while every stream
  a caller reads is intact. Without a signal, an eviction is
  indistinguishable from "never admitted" — the one inference this
  surface exists to make safe — and a caller would resend guidance that
  already landed. So `settled_steers` is not a bare list but
  `{ "entries": [...], "complete": bool }`, with `entries` in settlement
  order and `complete` true while nothing has been evicted for the
  session. Recovery reads `complete` and nothing else: while it is true,
  absence is conclusive; once it is false, absence is inconclusive for
  every request, and a caller that did not see its settlement must decide
  as it would on any unknown outcome. A timestamp watermark was the
  obvious alternative and is the wrong tool twice over. For a client it
  would mean comparing its own clock against a server time, with
  `timestamp_ms` optional on the request anyway, and a clock running fast
  makes an evicted settlement look too new to have been evicted —
  precisely the case that causes a duplicate resend. For the validator it
  is no better: `timestamp_ms` is optional on the settlement envelope
  too, and several settlements can share a millisecond, so a conforming
  bounded history could be diagnosed for an omission the rule could not
  place. Eviction is an ordering fact, not a temporal one, and the
  validator already has the order — the trace. Hence the rule below is
  stated over settlement order rather than time. What that removes is the
  *timestamp* watermark, not the completeness marker: `complete` stays on
  the wire and is the whole basis of the recovery rule above, since
  without it an eviction cannot be told from a steer that was never
  admitted and the caller resends guidance that already landed. No
  ordering marker is needed beside it. Pending steers stay on the
  `active_runs` entry, where a live run's state belongs; settled ones
  move to the session, where they outlive it. A caller that missed
  the settlement reads the outcome instead of inferring it from silence,
  which is the same answer T2 gives a reconnecting submitter recovering a
  `submission_id`: state is the recovery surface, and the stream is the
  live one. What to submit to the
  session's next run is a fresh decision. Nothing is ever adopted by
  mistake, because the caller never has to guess which fresh
  `submission_id` is its own; guidance is injected twice only by a caller
  that resends before that bound, which the plan leaves as the caller's
  choice rather than claiming a guarantee the wire does not give. The
  hub's fallback under Surfaces publishes this surface.
- Barrier: every admitted steer settles before the run terminal; a run that
  terminates first drops its pending steers with `run_terminated` before the
  terminal, in the run's sequence.
- `auto` on a busy session never resolves to `steer`; steering is always an
  explicit request (P0.4's rule that explicit modes never change meaning is
  kept, and an accidental steer is worse than a refused submit).

### Validator

`runState` gains `steers` (pending submissions); `steered` admissions are
legal only against a started nonterminal run that is not `cancelling`, in
the same session, with
`session.message.delivery.steer` advertised, only in answer to a request
with `delivery: "steer"` (the `submitResponse` combination table gains
the `steered`/`steer` row and rejects it for `auto`, so "`auto` never
resolves to `steer`" is enforced), and only with a `run_id` equal to the
request's `target_run_id` when one was supplied (`scope_mismatch`) and a
`target_sequence` equal to the target's tracked cursor at the
response and a `status` equal to the status the target held after
exactly that sequence (`illegal_run_transition` otherwise, on
`/payload/target_sequence` when the trace has not reached the named
position or has passed it, on `/payload/status` when the position is
right and the status differs; equality, not an upper bound: a run that
emitted ordinary content or action events during the call but no status
transition still holds the status the older sequence had, so a stale
boundary would pass the status check while naming an envelope that is no
longer the last one emitted before return, and the hub would then place
those already-emitted envelopes after the response, exactly what the
boundary contract forbids. Fixture
`steer-content-advances-in-flight` (positive; content events but no
transition during the call, the response naming the last of them) and
`steer-stale-boundary-nonstatus` (`illegal_run_transition`; the same
trace with the response naming the pre-call sequence); the
validator keeps the target's status history by sequence for this), so
the submitter is never handed a status the target did not hold at the
position the response names: a transition the adapter emitted during
the call precedes the response and lies at or before `target_sequence`,
and one emitted after the adapter returned lies beyond it, whether the
hub published it before or after the response, so the check is exact
without any ordering inference; `applied`/`dropped` must name a
pending steer once (`unmatched_steer`, `duplicate_steer`), and a `steered`
response whose `submission_id` is already pending on that run is
`duplicate_steer` as well, since the pending set could not represent two
steers under one id and the second would never be seen to settle. The
admission's `message_ids` and the admitting request's envelope `id` are
retained with the pending steer: an `applied` settlement must report
exactly those `message_ids` whenever the response supplied them, and
every settlement's `request_id` must equal that envelope id
(`unmatched_steer` in both cases: the settlement does not match the
admission it names), so a submitter is never told that different
guidance was applied than it submitted and can always attribute a
settlement to its own request; a terminal with a pending steer
is `pending_steer_at_terminal`. A settlement whose
`submission_id` no earlier `steered` response in the trace admitted is
`unmatched_steer`, which makes the ordering barrier below a conformance
rule rather than a hub detail. A request with `delivery: "steer"` whose
target (`target_run_id`, or the session's started run when absent) is
not a started nonterminal run of that session that can still take
guidance (queued, terminal, another session's, no started run at all,
or a started run that is `cancelling`) is remembered from the request
and re-evaluated against the target's tracked state at the correlated
response, so a target that reached a terminal or became `cancelling`
while the request was in flight is judged as `terminal` or
`not_steerable` there (an admission against it could not settle before
the already-published terminal), and its correlated `error.response`
must carry `invalid_steer_target`
with the `details.reason` the wire assigns to that condition (`queued`,
`terminal`, `cross_session`, `no_active_run`, `unknown_target`,
`not_steerable` for the `cancelling` case). A target can meet more than one — a queued or
terminal run belonging to another session is both `cross_session` and
`queued` or `terminal` — and these are peers on the state rung sharing a
diagnostic, a pointer, and a value, so the global tie-break cannot
separate them. They are ranked instead, and the ranking turns on whether
the caller named a target. With an explicit `target_run_id` naming a run
that exists, the reason describes *that* run:
`cross_session`, `terminal`, `not_steerable`, `queued` — `no_active_run`
never applies, because the caller asked about a particular run and
telling it the session has no started run answers a question it did not
put. Without a `target_run_id`, or with one naming no run the validator
knows, the order is `cross_session`, `unknown_target`, `no_active_run`,
`terminal`, `not_steerable`, `queued`. `unknown_target` is the reason for
an
explicit `target_run_id` the endpoint has no run for, and it ranks
immediately after `cross_session`, ahead of `no_active_run`, because a
caller that named a run is owed an answer about that run: told
`no_active_run` it would wait for a run to start and retry an id that
will never resolve. Telling the two apart is the hub's job, not the
adapter's, and the plan says which surface owns it. `adapter.Session`'s
registry is session-local and `serve.Session.Submit` delegates straight
through, so an adapter handed another session's run id cannot
distinguish it from a fabricated one and would answer `unknown_target`
for both. The hub can: `serve`'s `sessionRegistry` already holds every
session for the endpoint's lifetime (`serve/session.go:18-21`, entries
surviving close), so it gains a run-owner index over that registry and
classifies an explicit `target_run_id` before delegating — refusing
`cross_session` itself when the id belongs to another of its sessions
and passing the submit through otherwise. The validator's rule follows
the same division: `cross_session` is required where the trace shows the
run in another session of the same endpoint, and `unknown_target` where
the trace shows the run nowhere, so each party is held only to what it
can know. None of the existing conditions describes it: it is not another session's run, has no
lifecycle state to report, and the session may well have a started run,
so `no_active_run` would be false as well as unhelpful. Without it, a
caller naming a run that never existed (a typo, a stale id from a
previous session, a resume against a restarted daemon) leaves the adapter
with no conforming refusal at all. It ranks immediately after
`cross_session`: both say the caller has the wrong run rather than a
badly timed one. Fixture `steer-unknown-target-run` (positive; an
explicit `target_run_id` naming no run while another run is started,
refused `unknown_target`). Ownership outranks lifecycle either way,
because a caller steering another session's
run has the wrong run, not a badly timed one, and must be told so rather
than sent to wait for a state it will never see; among lifecycle
conditions the permanent outranks the transient, on the same reasoning as
the ladder itself. Fixture `steer-named-queued-target-no-started-run`
(positive; an explicit `target_run_id` naming a queued run of the same
session while nothing is started, refused `queued`, not `no_active_run`). The validator requires the highest-ranked reason the
target satisfies and the adapter reports it, so the two cannot diverge.
Fixture `steer-cross-session-terminal-target` (positive; another
session's terminal run, refused `cross_session`). When the same request is also ungated — an explicit
steer to an endpoint that does not advertise
`session.message.delivery.steer` — the delivery gate wins and this rule
stands down, because a single `error.response` cannot carry both codes
and the caller's first duty is to stop sending a delivery the endpoint
does not support: the correlated refusal must be T2's
`unsupported_feature` naming `session.message.delivery.steer`, the
retained target expectation is discharged without diagnosis, and only a
refusal that is neither is diagnosed (`unavailable_capability` on the
error response, by the delivery rule). Fixture
`steer-unadvertised-no-active-run`, where capability and target
validation fail together and the `unsupported_feature` refusal is
validated as correct. An admission is the
`illegal_run_transition` above, and a refusal under any other code, or
under `invalid_steer_target` with a reason that does not match the
condition, is `illegal_run_transition` on the error response
(`/payload/error/code` or `/payload/error/details/reason`), so an
adapter cannot
hide an invalid target behind `internal_error` and a caller always
learns why the steer could not land. Fixtures: positive `steer-immediate`
(response, then `applied` in the target's sequence), `steer-at-boundary`,
`steer-target-queued-rejected` (`error.response` with
`invalid_steer_target`, `details.reason: "queued"`; positive,
`valid: true`, no diagnostic), `steer-target-cancelling-rejected` (`error.response`
with `invalid_steer_target`, `details.reason: "not_steerable"` against a
`cancelling` target; positive, `valid: true`, no diagnostic),
`steer-target-terminated-in-flight-rejected` (`error.response` with
`invalid_steer_target`, `details.reason: "terminal"`; the target was
running at the request and terminal before the response; positive,
`valid: true`, no diagnostic), `steer-target-terminated-in-flight-wrong-refusal`
(`illegal_run_transition` on the error response; the same race refused
with `internal_error`),
`steer-dropped-at-terminal`, `steer-status-advances-in-flight` (a
`run.status.updated` to `waiting_for_input` between the steer request and
its response, the response naming that transition's sequence as
`target_sequence` and reporting `waiting_for_input`),
`steer-status-multi-transition` (`running` to `waiting_for_input` back to
`running` during the call, the response naming the last sequence and
reporting `running`; more than one transition in flight, all of them
leaving the target steerable, so the admission stands); negative
`steer-status-stale` (`illegal_run_transition`; the same transition
before the response, the response naming its sequence but still
reporting `running`), `steer-target-wrong-refusal`
(`illegal_run_transition` on the error response; a queued target refused
with `internal_error`), `steer-target-wrong-reason`
(`illegal_run_transition` on the error response; a queued target refused
`invalid_steer_target` with `details.reason: "terminal"`),
`steer-target-sequence-ahead`
(`illegal_run_transition`; a `target_sequence` beyond the target's
cursor at the response),
`steer-settled-before-response`
(`unmatched_steer`), `steer-duplicate-settlement` (`duplicate_steer`),
`steer-pending-at-terminal` (`pending_steer_at_terminal`),
`steer-unadvertised` (`unavailable_capability` on the admission; the
request admitted `steered`),
`steer-auto-resolved-to-steer` (`illegal_run_transition`; an `auto`
request answered `steered`), `steer-target-mismatch` (`scope_mismatch`;
`target_run_id` naming one run, the response another),
`steer-status-mismatch` (`illegal_run_transition`; the target is
`waiting_for_input`, the response says `running`),
`steer-duplicate-submission-id` (`duplicate_steer`; two `steered`
responses on one run with the same `submission_id`),
`steer-applied-message-ids-mismatch` (`unmatched_steer`; the admission
returns ids A, the settlement reports ids B),
`steer-settlement-wrong-request-id` (`unmatched_steer`; the settlement's
`request_id` is not the admitting request's envelope id),
`steer-with-controls-rejected` (`error.response` with
`unsupported_feature`, `details.reason: "unsatisfiable"`, then no
admission; positive, `valid: true`, no diagnostic),
`steer-with-controls-admitted` (`unsatisfiable_control`; a `steered`
admission of a request carrying `model_id`). `session.state`
snapshots are checked against the same record: each `active_runs[]`
entry's `pending_steers` must equal the pending set in `runState.steers`
for that run, submission and request id alike, so an omitted pending
steer, a wrong `request_id`, or a settled one still listed
is `session_state_mismatch` (fixtures `steer-state-omits-pending`,
`steer-state-retains-settled`); without this the state surface a
submitter recovers the id from could silently lie. The session-level
`settled_steers` is held to the same standard, and for the stronger
reason: a caller consults it precisely when it did not see the
settlement, so it has nothing of its own to check the answer against.
The validator keeps every settlement it observed and requires the
snapshot's `entries` to match that history — each observed settlement
present with the `run_id`, `submission_id`, `request_id`, and
`applied`/`dropped` outcome the trace recorded, no entry the trace never
settled, and omissions only where eviction can explain them. Both of
those run against the trace at the snapshot's capture point, and the
capture point is not the arrival point in either direction. State reads
are not serialized with run-event publication, so a steer can settle
inside the adapter before a concurrent capture and have its run event
drained only after the state response: the snapshot is accurate and the
trace has not caught up. The invented-entry rule therefore defers rather
than fires. An entry naming a settlement the trace has not yet reached
is held, like any other capture marker that runs ahead of its position,
and reconciled when the position arrives — it becomes
`session_state_mismatch` only once the settlement can no longer appear,
which is the run's terminal or the session's close, or once a settlement
does arrive and contradicts the entry's outcome. The symmetric direction
is already covered: an adapter can capture state before a settlement
that enters the trace before the state response is serialized, and
demanding the snapshot already contain it would diagnose a valid stale
read. The window is bounded on both sides and needs no new field, since
the request and the response bracket it — a snapshot must carry every
settlement observed before its `session.state.request` and may carry any
that landed while the request was in flight, either being conforming for
those. Beyond that window the shape is exact. A bounded
history drops its oldest entries, so what survives is a contiguous
*suffix* of the settlements in scope: `entries` must equal the last N of
them, in that order, for some N. That is
the whole omission rule, and it needs no timestamp — an entry missing
from the middle, or an older entry retained while a newer one is gone,
is not something eviction can produce. `complete` must then be true
exactly when N covers every settlement, so `complete: true` alongside
any missing settlement is itself the diagnostic, and `complete: false`
with nothing missing is equally wrong. Fixtures
`steer-settled-history-omits-entry` (a settled steer missing from a
snapshot claiming `complete: true`),
`steer-settled-history-wrong-outcome` (`applied` reported for a dropped
steer), `steer-settled-history-invents-entry` (an entry for a submission
the trace never settled, held until the run's terminal and diagnosed
there) and its positive counterpart
`steer-settled-history-ahead-of-trace` (an entry whose settlement is
drained after the state response, reconciled and accepted), `steer-settled-history-gap` (a middle
settlement omitted under `complete: false`, which no eviction explains)
and `steer-settled-history-truncated` (positive; the oldest settlements
omitted under `complete: false`) — `session_state_mismatch` but for the
two positives.

### Reference adapter and evidence

`adapter/memory.go` accepts a steer while waiting at the permission gate and
applies it at the input gate (`boundary: "turn"`), which also exercises the
drop path under cancel. pi graduates: its corpus already records `steer`,
`queue_update`, and injection as codec evidence (`native-controls`,
`steer-injected` labels); the new case executes them through `Submit` with
an advertised `session.message.delivery.steer`. OpenCode and Hermes
follow from their pinned ledgers; Codex follows only once a new ledger pin
covers `turn/steer`.

### Surfaces

No new operations: `submit` carries the delivery; the events are stream
envelopes. A steer creates no run and no sequence domain, so the adapter
and hub contract is explicit rather than inherited from `start`:

- `adapter`: for an admission of `steered`, `Session.Submit` returns the
  response and a nil `EventStream`; the settlement events are emitted on
  the target run's existing stream, which the adapter already owns, and
  only after `Submit` has returned. An adapter whose harness settles
  synchronously (Hermes answers `steered` in the busy result) holds the
  settlement and releases it onto the target stream after return, and the
  memory adapter does the same, so an in-process consumer that takes the
  response from `Submit`'s return value and then reads the stream sees
  them in that order. Every target-run envelope the adapter emits before
  returning is readable from the target stream when `Submit` returns
  (emission is synchronous, as the adapters' `emit` under their operation
  lock already is), the response's `target_sequence` is the sequence of
  the last of them (the adapter assigns the target's sequences, so it
  states the boundary exactly), and its `status` is the target's status
  after that envelope, so the hub can order those envelopes ahead of the
  response by sequence alone. A non-nil stream with a `steered` admission
  is a contract violation the hub reports as an adapter error;
  `adaptertest` asserts the nil stream, the settlement on the target's
  stream, that the settlement is not readable before `Submit` returns,
  and that a status transition emitted inside the call is readable at
  return with a sequence at or below the returned `target_sequence`.
- `serve`: `Session.Submit` today calls `adoptRun` for every successful
  admission, which allocates an admission serial, counts a reader, and
  starts a drainer; for `steered` it does none of that. It releases the
  reservation it took before calling the adapter, leaves `runID` and the
  serial table untouched, and returns the response; the target run's
  drainer, already running, delivers `run.steer.applied` or
  `run.steer.dropped` to subscribers in the target's sequence. Because
  that drainer is another goroutine, the hub adds a barrier of its own
  rather than trusting the adapter's: before calling the adapter with
  `delivery: "steer"` it arms a gate on the target run's drainer under
  which the drainer reads and buffers but publishes nothing, so that no
  envelope can reach subscribers between the adapter's return and the
  hub's processing of the response; the drainer keeps reading while
  gated, so the adapter's emission never blocks. When the adapter
  returns, the submit goroutine takes the drainer's lock, drains the
  target stream without blocking, publishes the buffered prefix up to
  and including the envelope whose sequence is the response's
  `target_sequence`, and leaves the remainder, and any settlement
  whatever its sequence, withheld, all before handing the response to
  the binding. The handoff is therefore atomic: the boundary is the one
  the adapter stated, not one inferred from intermediate statuses, and
  because the drainer publishes nothing on its own while the gate is
  armed, an envelope the adapter emits immediately after returning
  cannot be published before the boundary is applied. Every envelope the
  adapter emitted before returning is readable at return (the adapter
  contract above) and carries a sequence at or below the boundary, so
  all of them precede the response in the hub's trace, delayed by at
  most the call, the bound the gate already imposes on the settlement;
  anything beyond the boundary follows the response; nothing is
  reordered within the run. A drain that cannot reach `target_sequence`
  (the adapter returned a boundary it had not made readable) and a
  settlement read before the adapter has returned are contract
  violations reported as adapter errors rather than published. When the
  gate lifts, the drainer publishes the withheld remainder in order and
  resumes publishing live. The error path carries a boundary of its own,
  for the same reason the success path does. Releasing the whole buffer
  there would be wrong: the gated drainer keeps reading, so an envelope
  the target emits immediately after the adapter returns can land in the
  buffer before the drain runs, and if that envelope is terminal the
  validator would read a terminal target ahead of the `error.response`
  and demand `invalid_steer_target` for a steer the adapter refused
  while the target was still running. The steer error type therefore
  carries a `TargetSequence` — the last sequence the adapter made
  readable on the target before returning — and the submit goroutine
  applies it exactly as it applies an admission's: drain without
  blocking, publish the buffered prefix up to and including that
  sequence, withhold the remainder, hand the error to the binding, then
  lift the gate so the drainer publishes the remainder in order. An
  error raised before the adapter looked at the target at all (an
  unknown run, an unparseable submission) states no boundary, and the
  hub uses the target's last published sequence at arming, so nothing
  the adapter did not account for precedes the refusal. The hub's trace
  then records every envelope up to the applied boundary — for a
  boundary-stating error, every envelope the adapter emitted before
  returning, including a terminal the target reached while the request
  was in flight — ahead of the `error.response`, and nothing beyond it,
  which is what the validator's response-time re-evaluation of the
  target reads. For an error that states no boundary the adapter
  examined nothing, so nothing emitted since arming precedes the
  refusal, and the re-evaluation reads the target as the adapter left
  it.
  `Session.Resolve` does the same on its error path under T3c. After the adapter returns an admission the
  hub registers the pending steer (`submission_id` to target run,
  reflected in `active_runs`). The gate is not lifted inside `Submit`:
  the hub
  cannot know when the response has reached the caller, and an SSE
  subscriber could otherwise see `run.steer.applied` ahead of the
  `submission_id` in the hub's own trace. `Session.Submit` returns with the
  gate held, together with a request-unique barrier token, and the
   binding that makes the response observable lifts the gate with
  `Session.Published(token)`: `servehttp` after writing and flushing the
  submit response, the stdio frontend after writing the response line,
  an in-process embedder once it has handed the response to its own
  caller.
  What that buys, and what it does not, has to be said plainly. The gate
  orders the hub's own trace — the sequence a subscriber is served, the
  evidence a corpus records, the thing conformance judges — and for the
  stdio frontend and an in-process embedder, where the response and the
  events share one stream, it orders what the caller observes as well.
  Over HTTP it does not: the submit response is a POST body and the
  events are an SSE connection, and writing and flushing the first
  establishes nothing about the order the two connections deliver bytes
  in. No server-side barrier can fix that, because the server does not
  control the client's sockets, so `run.steer.applied` may legitimately
  arrive before the POST response that names its `submission_id`.
  The wire already carries what a client needs to repair the order
  itself: a settlement names the admitting request's envelope id in
  `request_id` and the admission's `submission_id`, and the client knows
  which of its own requests are outstanding. So the obligation is
  client-side and stated as such: a client that receives a settlement for
  a submission it has not yet been told about holds it until the
  correlating response arrives or that request fails, then delivers it in
  order. `client` and `clients/ts` implement this in their stream
  readers, and their e2e tests cover the reordered arrival with the SSE
  event delivered first. The `Published` barrier stays, because the
  hub's trace is what every other rule in this plan is written against
  and because it is exactly right for the two single-stream bindings; it
  is a trace-ordering mechanism, not a delivery guarantee, and the plan
  no longer claims otherwise. Gated operations on one run are serialized, not shared: a run
  has one gate, a gated operation (a steer, or under T3c a resolution of
  any interaction on that run) acquires it before calling the adapter
  and holds it until its own `Published` or its context-done fallback,
  and a second gated operation arriving while the first holds it waits,
  in arrival order and subject to its own context, before it calls the
  adapter. Two overlapping requests therefore never have responses
  awaiting publication at once, the key that lifts the gate is the token
  of the operation holding it rather than an interaction or submission
  id that two requests can share (a `started` acknowledgement followed
  at once by the terminal resolution of the same interaction is the case
  that would otherwise release the second's events on the first's
  `Published`), and a stale or repeated `Published` is a no-op. An
  in-process embedder that issues a second gated operation on a run
  before calling `Published` for the first waits, bounded by the first
  operation's context, which is the contract it accepted by taking the
  response synchronously. Forgetting `Published` must not be able to
  wedge a stream, and for an in-process embedder the obvious usage
  invites exactly that: `Submit` with a long-lived context, then a
  forgotten call, and the target run's subscribers see silence with no
  diagnostic and no context to end. Two mitigations, both in the 0009
  decision. `Submit` returns the barrier as a guard value with a single
  `Publish()` method rather than a bare token, so `defer` is the
  idiomatic use and the right thing is the easy thing; and the gate
  carries a bounded deadline of its own, independent of the caller's
  context, whose expiry runs the same fallback described next and records
  that it fired, so a forgotten call degrades to late, accounted-for
  events instead of a wedged run. The deadline is a backstop, not a
  policy: expiring it is a defect the hub reports, not a supported way to
  use the API. The README's embedding example discards `Submit`'s return
  today and is updated with the unit.
  If the context passed to `Submit` ends before
  `Published` is called (the HTTP client disconnected before the response
  was written, or a binding forgot the call), the hub does not simply
  publish the buffered settlement, since the submitter never learned its
  `submission_id`: it first publishes a `session.state.updated` on the
  session stream whose `active_runs` entry for the target lists the steer
  in `pending_steers`, and only then lifts the gate. Minting that snapshot commits the hub to two things the plan should say
  outright. The session-scoped sequence domain is hub-owned: the schema
  requires `sequence` on `session.state.updated`, so the hub allocates
  it, and adapters never emit sequenced session-scoped envelopes — they
  emit run events, whose domain is the run. Nothing emits
  `session.state.updated` today, so the domain is uncontended now, and
  fixing ownership before T4 lands is what keeps it that way: two
  producers in one domain would show subscribers duplicate or regressed
  positions. `capabilities.updated` is the same at endpoint scope, and
  the same rule applies. And a minted snapshot carries only what the hub
  itself knows: `session_id`, the `status` the hub tracks, and the
  `active_runs` entries with their `pending_steers` and
  `pending_interactions` — the facts the fallback exists to convey. The
  adapter-owned optional members (`current_model_id`, `transcript_cursor`,
  `updated_at_ms` beyond the hub's own clock) are omitted rather than
  reconstructed: guessing them risks contradicting the adapter and
  tripping `session_state_mismatch`, and querying `Session.State` would
  put a blocking adapter call on an error path that runs precisely when
  something has already gone wrong. The validator's state rules read an
  omitted optional member as "not reported", so a minted snapshot is
  never judged against facts it never claimed. A snapshot the hub
  mints is not in the adapter's run journal, which the T2 resume path
  replays from, so the hub journals every snapshot it mints, keyed by the
  run domain and sequence it preceded, and interleaves it at that position
  on replay: a subscriber that drops after the fan-out and before receipt
  resumes into the snapshot first and the settlement after it, and the
  `servehttp` e2e test covers exactly that drop. A journaled snapshot
  keeps the envelope `id` it was first published under, and because a
  session-scoped envelope advances no run cursor, a bare cursor cannot
  say whether the subscriber received it: one that received the snapshot
  and dropped before the settlement resumes with a cursor that precedes
  the snapshot's position. The resume cursor therefore gains an additive
  component for the interleaved position: `?after_interleaved=<envelope
  id>` beside `?after=` and `?run=` (an `after_interleaved` field on the
  stdio `events` op) names the last journaled session-scoped envelope
  the subscriber delivered at that position, and the hub replays only
  the journaled envelopes at that position that follow it in journal
  order. The no-duplicates promise is thus kept by the cursor itself,
  which a caller persists and carries across a process restart, rather
  than by state a client instance keeps in memory (client rule below);
  the e2e test drops between the snapshot and the settlement, resumes
  with the full cursor from a fresh client, and asserts the application
  sees the snapshot once.

  That promise has a floor, because the journal is bounded like every
  other recovery surface here. Once the named envelope has been evicted
  the hub cannot order the position at all: it no longer knows which
  journaled envelopes followed the one the subscriber holds, so replaying
  the position would duplicate what was already delivered and skipping it
  would hide what was not. Neither is something to choose silently, so an
  `after_interleaved` the journal no longer holds is a stated gap, the
  same treatment an expired run-sequence cursor gets: the hub emits
  `oap-replay-gap` carrying `scope: "interleaved"` and replays no
  journaled envelope at that position, then continues the run stream from
  `?after=` normally. `scope` distinguishes it from the run-cursor gap,
  whose members (`ambiguous_run_ids`, and `last_sequence` naming a run
  position) describe a different loss; the interleaved gap carries
  `run_id` and `last_sequence` for the stream it continues on, and no
  candidates. Recovery is not uniform, because the envelopes
  this cursor orders are not all of one kind. T3c's hub-minted
  `session.state` snapshots are superseded exactly by a fresh read: a
  client that sees the gap re-reads `session.state` and holds, by
  definition, something newer than whatever it missed. T2's released
  pre-start terminal of a settled reservation is ordered by the same
  cursor (the client section above) and is not recoverable that way at
  all — the run is terminal, so it is absent from `active_runs`, and its
  outcome and error appear nowhere in the snapshot. A state read would
  look like success while the client had lost a run's only envelope.

  The gap therefore carries what a state read cannot: `skipped_run_ids`,
  the runs whose journaled envelopes at that position were not replayed.
  The client resumes each with `?run=` against that run's own journal,
  retained independently of the hub journal that evicted the cursor, and
  reads the terminal there. The signal thus says which half of the
  recovery applies — re-read state for the snapshots, replay the named
  runs for their terminals — and where `skipped_run_ids` is empty the
  state read alone is exact. A `servehttp` retention test drives the
  journal past its bound with the subscriber disconnected and asserts the
  gap is signalled rather than the position silently replayed or
  silently dropped; a second covers the T2 shape, where an evicted
  released pre-start terminal must appear in `skipped_run_ids` and be
  recoverable from that run's journal. The resolve fallback
  under T3c uses the same journal. The state snapshot is
  the same surface a reconnecting submitter reads to recover the id, so
  every subscriber learns of the admission before the settlement, the
  target run's subscribers are delayed by at most the request's lifetime,
  and nothing is silently discarded. Subscribers, and any trace the hub
  records, therefore see the admission before the settlement even when an
  adapter emits inside `Submit`; the `servehttp` e2e test subscribes
  before a synchronously settling steer and asserts the settlement arrives
  after the response, two status transitions emitted during the call
  both before it (the response naming the second as `target_sequence`),
  and one emitted after `Submit` returned after it. The same branch
  is where a queued admission (T2) differs from `start`: it adopts the
  queued run's stream under a new serial but does not supersede the
  started run.
- `serve/servehttp` and the stdio frontend: pass the delivery and
  `target_run_id` through and call `Session.Published` after writing the
  response; the response is the same `session.message.submit.response`.

The hub's run-qualified cursor from T2 already covers a steer's
events because they live in the target run's domain. Neither client can
be left deciding stream scope by a type list, because a list cannot name
future additions and the two lists fail in opposite directions: the Go
client positions every frame as run-scoped, so a `session.state.updated`
with no `run_id` and a session-domain sequence would be read as a run
switch and end in a `SequenceGapError` (the daemon publishes no such
event today, so nothing exercises the path), while the TypeScript client
treats any type outside `RUN_EVENT_TYPES` (`clients/ts/src/events.ts`)
as session-scoped and does not advance the cursor, so an unlisted
`run.steer.applied`, or any future additive run event, would make the
next known event raise `SequenceGapError` and a reconnect replay it.
Both clients therefore switch to the wire's own scoping in the T2
client slice, the first that changes cursor handling at all (`?run=`,
`?follow=session`), even though T2 itself interleaves no session-scoped
envelope on a run stream, so that T3c's and T4's hub-minted snapshots
find the scoping and the `after_interleaved` cursor member already in place
rather than landing with the units that first need them: an envelope carrying `run_id` is run-scoped and its sequence advances
that run's cursor whatever its type; an envelope carrying `session_id`
without `run_id` is session-scoped and is delivered without touching the
run cursor whatever its type; one carrying neither is endpoint or
protocol scoped and touches no cursor at all. Scope is read from the
scope members, never from `sequence`: `capabilities.updated` is
sequenced and carries no session
(`schema/v0.1/envelope.schema.json:75`), so a sequence-only test would
file it into the session cursor and send an `after_interleaved` naming an
envelope that was never session-scoped. The schema requires `run_id` on every run
event and session events carry `session_id`, so the rule is exact for known
types and correct by construction for unknown ones; it is the same rule
the tolerance step applies, and the type lists stay
for typing only. An envelope interleaved in a run's replay from outside
that run's domain — a session-scoped one, or the pre-start terminal of a
reservation that settled, which T2's delivery rule allows through for
exactly one envelope per released run — is delivered to the application
without advancing the run cursor and without being read as a switch to
another run: the client's run-switch check, which otherwise moves to a
later-admitted run only once the followed run is terminal, admits a
terminal for a run that never started as an interleaved delivery rather
than a mismatch, and rejects it only if that run is one it has already
seen start. Because this first occurs in T2, the cursor member and this
rule land in T2's client slice and the daemon honours the corresponding
query parameter from T2, not from T3c; T3c and T4 then find both in
place for their hub-minted snapshots. Such an envelope is covered by the
cursor, not by
in-memory state: each client's resume
cursor becomes `{ run, sequence, interleaved_envelope_id? }`, where the
third member is the `id` of the last such out-of-domain envelope
delivered
since the last cursor-advancing envelope and is cleared whenever the run
cursor advances; the client sends it as `?after_interleaved=` on every
reconnect and exposes it in the cursor it hands the application
(`EventsAfter` takes the full cursor, with the two-member form kept as
a convenience that resumes without it), so a persisted cursor carries
it across a process restart and a fresh `EventStream` built from it is
not sent the snapshot again. Tests interleave a session snapshot
between run events and across a resume, resume from a fresh stream
built from the persisted cursor after receiving the snapshot and assert
it is not redelivered, and feed an unknown run-scoped type and an unknown
session-scoped type through both clients. The T4 client slice adds
`run.steer.applied` and `run.steer.dropped` to `EnvelopeType` (typing),
with cursor tests: a steer event advances the cursor, a drop after it
resumes after it, and a steer event is never delivered twice. Both
clients' e2e tests drive a steer against the memory adapter and assert
the settlement event in the target run's sequence.

### Questions the 0009 decision must answer from evidence

1. Event naming: `run.steer.applied`/`run.steer.dropped` versus a general
   `run.submission.*` pair that queue promotion could share.
2. Whether the steered messages are echoed as `content.delta` with
   `role: user` in the run stream or referenced by `message_ids` only.
3. Whether `boundary` is worth carrying or `applied` alone suffices.
4. Codex's expected-turn precondition: is `target_run_id` required when the
   harness requires a target, or does the adapter fill it from the started
   run. This is answered by the ledger pin that first covers `turn/steer`,
   not by the current one.

## T5b. Auth state

Unit name: `auth`. Not scheduled. `auth.providers.request`/`.response` as a
read-only listing (`[{ "provider_id", "state": "authenticated" |
"unauthenticated" | "expired" | "unknown", "display_name"? }]`) has evidence
in Claude's `auth_status` frames and OpenCode's `provider.list`. Login flows
(`auth.login.*`) have no pinned evidence and stay in the profile draft. The
unit is designed when a consumer needs to gate a model picker on auth state;
until then `auth_required` on `run.failed` (already a standard code) is the
executable surface.

## Cross-cutting vocabulary

### Extension namespace

The unprefixed namespace is the spec's in its entirety — capability keys,
envelope `type` values, `error.response` codes, and validator diagnostics
alike, whatever their shape. Every name below is unprefixed and therefore
spec-owned. An extension name is one carrying a reverse-DNS prefix
(`com.example.storage.objects`); T0 states the rule, and it is enforced
where it can be enforced once, at pack load, against the pack's own `id`.

### Capability keys introduced or given executable meaning

| Key | Unit | Note |
| --- | --- | --- |
| `run.model_selection` | T1 | new; `mode` discloses `per_run` or `session_mutation` |
| `run.tool_selection` | T1 | existing name; `modes` discloses the `tool_choice` modes the endpoint enforces |
| `run.structured_output` | T1 | existing name, executable gate |
| `run.instructions` | T1 | existing name; gated, but see the note below |
| `models.list` | T5a | existing name |
| `session.message.delivery.queue` | T2 | existing name; `limits` adds the bound |
| `action.tools.list` with sources | T3a | existing name |
| `action.tool_sources.attach` | T3b | existing name; `mode` discloses `session_open` and `remote` |
| `action.tools.provide` | T3c | new; `action.tools.execute` keeps its `+tools` meaning |
| `session.message.delivery.steer` | T4 | existing name |

`run.instructions` is the one key in this phase whose promise is not
executable, and the plan says so rather than implying a gate it does not
have. The capability gate applies — an unadvertised `instructions` is
refused and the refusal is validated — but there is no wire observable
for whether an admitted `instructions` was *applied*: the model's output
is not a conformance surface, unlike a model id echoed in state, a tool
call emitted or withheld, or a structured result checked against its
schema. The reference adapter makes the effect observable by prepending
`instructions` to its scripted text, which pins the reference and
nothing else. An endpoint that advertises `run.instructions`, admits the
control, and ignores it is therefore conforming, and a caller should
read the key as "this endpoint accepts instructions" rather than as a
checked promise that they take effect. Closing it would need an
observable on the wire — an echo of the effective instructions in
`session.state`, say — which is a design question for the 0005 decision
and not something to invent here.

### Typed error codes on `error.response`

`unsupported_feature`, `capability_degraded` (already in the layered draft);
`model_not_found`, `run_active` (adopted from the daemon), `queue_dropped`
(on `run.failed`), `structured_output_failed` (on `run.failed`),
`invalid_steer_target`, `run_terminated` (steer drop reason). Codes stay open
strings in the schema; the validator does not enumerate them.

### Validator diagnostics added

`unapplied_control`, `unsatisfiable_control`, `degraded_without_optin`,
`model_not_in_catalog`,
`ambiguous_default_model`, `duplicate_model_id`,
`unannounced_catalog_change`, `queue_order_violation`,
`queue_limit_exceeded`, `premature_session_mutation`,
`unmatched_tool_source`, `duplicate_tool_source`, `duplicate_tool_name`,
`wrong_tool_owner`, `attachment_field_in_catalog`,
`undisclosed_queue_limit`, `undisclosed_provide_limit`,
`undisclosed_attach_limit`, `undisclosed_selection_modes`,
`unhonoured_capability`,
`resolution_payload_mismatch`, `catalog_mismatch`,
`unmatched_steer`, `duplicate_steer`, `pending_steer_at_terminal`.
Existing codes are reused wherever the
invariant is the same (`unavailable_capability`, `illegal_run_transition`,
`session_state_mismatch`, `scope_mismatch`, `wrong_interaction_responder`,
`pending_interaction_at_terminal`).

### Conformance units

`validation/manifest.go` `knownUnits` gains `extensions`, `run-controls`,
`models`, `queue`, `tool-sources`, `control-tools`, `steer`. The conformance draft's
unit list gains `+run-controls` (the control discipline for all four
controls plus execution of each control the endpoint advertises, per
T1's scope), `+tool-sources`, and `+control-tools`
beside the existing `+models`, `+queue`, and `+steer`; the executable claim
becomes `open-agent-protocol.agent-control-core/0.1-executable` plus the
graduated units, and gains an independent `+ext:<pack id>/<version>` term
per loaded extension pack (T0), which never widens the core claim. The
`extensions` unit above and an `ext:` term are not the same thing and do
not compete: the unit is the core machinery for loading packs, claimed
like any other unit and present in `knownUnits`; an `ext:` term is one
pack's own conformance, admitted dynamically from the packs loaded for
that run, as T0's validator section sets out. Both corpora are held to
T0's corpus-completeness check: every key a claimed unit or a loaded
pack owns must carry a negative `gate` fixture and a negative `honour`
fixture, or the manifest fails to load.

### Schema evolution

Additive on the wire, under the layered draft's extension rules (unknown
fields are ignored; minor revisions are additive only): new optional
fields (`active_runs`, `limits`, `tool_sources`, `tools`, `source`,
`sources`, `features`, `target_run_id`, `target_sequence`, `session_id`
on the tools-list request), new envelope types in the `oneOf` (`models.*`,
`action.call.resolve.*`, `run.steer.*`), and new payload definitions in
existing schema files. No existing field's schema narrows: `tool_choice`
keeps its permissive schema and its typed shape is a validator and adapter
rule of the `run-controls` unit, and the models payloads live in the
control-plane schema file so the bundle inventory and the manifest schema
are unchanged. `version` and `profile` are unchanged.

The bundle itself is not additive, and the plan does not claim it is:
every payload object in `capabilities.schema.json`, `session.schema.json`,
and `action.schema.json` is closed (`additionalProperties: false`) and the
envelope `oneOf` is fixed, so a validator compiled from an older bundle
revision rejects a gated addition as small as `limits` on
`capabilities.response`. The Go client's opt-in `WithEnvelopeValidation`
(dev mode, off by default) would fail against an upgraded endpoint for
that reason; the TypeScript client does no runtime schema validation. The
phase therefore begins with a tolerance step that lands before T1 and that
every later unit relies on:

- `validation.CompileSchemas` gains a tolerant variant, used by the Go
  client's dev-mode validation and by `oap validate` when it is asked
  for it. Which mode applies has to be a choice the caller makes, not
  one inferred from the input: `runValidate` takes file operands only
  (`cmd/oap/main.go:65-90`), and a live envelope saved to a file is
  indistinguishable from a fixture, so inferring would either weaken the
  typo-catching the fixture path exists for or fail the
  forward-compatibility the live path needs. `oap validate` therefore
  gains `-mode strict|tolerant`, defaulting to `strict` so the fixture
  behaviour every existing invocation relies on is unchanged, and the
  tolerant variant is what `-mode tolerant` and the Go client's dev-mode
  validation compile. Fixture validation in CI keeps the default. It compiles the same bundle with
  `additionalProperties: false` lifted from payload objects (unknown
  members are ignored, as the wire rule requires), with extensible leaf
  `enum` constraints inside payloads lifted to their base type (an
  unknown enum value surfaces as a string, as the layered draft's
  extension rule requires, instead of failing the message; a `const` is
  never lifted, as set out below), and with the
  envelope `oneOf` relaxed to "a known `type` must match its branch; an
  unknown `type` must satisfy the common envelope fields only", the same
  forward compatibility both clients already apply to unknown named SSE
  events. Discriminators stay exact: the envelope-level `type`,
  `protocol`, `version`, and `profile`, and every `enum` or `const` that
  guards a conditional or a union branch. `interaction.schema.json` has
  four such guards (`kind: "text"` against `kind: single_choice |
  multi_choice` on `user.input.requested`, `status: "submitted"` against
  `"cancelled"` on its resolution); lifting them would make both branches
  of each `if` match every payload, forbidding and requiring `options` at
  once, so the tolerant compile keeps every `enum`/`const` inside an `if`
  guard exact and lifts the extensible `enum` leaves elsewhere, including
  the outer `question.properties.kind.enum` that those guards compare: an
  unknown `kind: "date"` then matches neither guard, takes neither
  `then`, and surfaces as a string as the extension rule promises, whereas
  exempting the whole property would still reject it. A `const` is never
  lifted: it is a fixed semantic value, not an extensible vocabulary
  (`run.started.status` is `const: "running"`, and a `run.started` with
  `status: "failed"` is malformed under any revision), and the same holds
  for the `protocol`, `version`, and `profile` constants. The step's tests
  validate a payload carrying a new leaf enum value, and a
  `user.input.requested` with an unknown `kind`, against the old bundle
  in both modes (tolerated tolerant, rejected strict), and a `run.started`
  with `status: "failed"` rejected in both. Discriminated unions get an
  explicit fallback rather than exact guards alone: keeping every branch's
  `const` exact would still make `common.schema.json#/$defs/contentPart`
  reject an additive part such as `{ "type": "audio", ... }`, because no
  branch matches, although the layered draft already names `audio` and
  `file` as content kinds. The tolerant compile therefore rewrites each
  `oneOf`/`anyOf` whose branches pin a `const` on one property into the
  same branches plus one fallback branch that accepts an object whose
  discriminator is a string outside the known set (`not: { enum: [known
  values] }`) and that carries only the members every branch requires;
  known branches stay strict, an unknown kind is tolerated as the wire
  rule requires, and a known kind with a malformed body is still
  rejected. The tests add an `audio` content part accepted tolerantly and
  rejected strictly, and a `text` part missing `text` rejected in both,
  and,
  as the regression guard, every fixture in the manifest under the
  tolerant compile, which must accept everything the strict compile
  accepts.
- The stateful validator tolerates on the same terms when it runs in
  tolerant mode, because a schema that admits an unknown envelope is not
  enough on its own: `validation/state.go` advances a run's sequence
  cursor only for the types `isRunEvent` enumerates, so a tolerated
  unknown run envelope at sequence N would be ignored and the next known
  event at N+1 diagnosed as `sequence_gap`. In tolerant mode an envelope
  of unknown `type` is classified by its wire scope, on the same
  principle as the T3c client slice (which keys the client's cursor on
  `run_id` and `sequence`; the validator's classification is finer
  because it must also keep unsequenced operations out of event
  bookkeeping): one carrying both `run_id` and `sequence` is a run-scoped
  event that enters `runEvent` for the type-independent bookkeeping
  (scope agreement, an accepted admission, sequence contiguity and
  regression, nothing after a terminal) and for no type-specific rule,
  including the pre-`run.started` rule, which exempts pre-start
  settlements by type and cannot be applied to a type it does not know;
  one carrying `run_id` without `sequence` is an unsequenced run-scoped
  operation (a future `run.pause.request` or its response) that joins
  the generic scope and correlation checks only and never enters
  run-event bookkeeping, so it can neither be reported as
  `event_after_terminal` nor disturb the cursor; scope below the run is
  decided by `session_id`, not by `sequence`, because a sequenced
  envelope need not be session-scoped — `capabilities.updated` requires
  `sequence` and carries no `session_id`
  (`schema/v0.1/envelope.schema.json:75`), so an additive successor with
  that shape would be misfiled by a sequence-only test and given session
  handling it has no session for. An envelope carrying `session_id` is
  therefore session-scoped, sequenced or not, and joins the scope checks
  only; one carrying neither `run_id` nor `session_id` is endpoint or
  protocol scoped and is passed through whether or not it is
  sequenced. `duplicate_envelope_id` needs no
  classification: the intake pass (`apply`) checks every envelope's `id`
  before type dispatch, known type or not. Strict mode is unchanged: an
  unknown type fails the schema before the stateful pass sees it. The
  step's tests feed a trace with an additive run event at sequence 2
  between `run.started` and `run.completed` (tolerant: valid, with the
  cursor advanced; strict: the schema rejection and nothing else), the
  same event after the terminal (tolerant: `event_after_terminal`,
  because scope classification makes the domain rules apply), and an
  unsequenced run-scoped operation after the terminal (tolerant: valid,
  with the cursor untouched).
- Unknown enum members are opaque to the semantic pass in tolerant mode,
  for the same reason: the tolerant compile lifts an extensible leaf
  enum, but `validation/state.go` still keys rules on the members it
  knows (`legalRunStatusTransition` on `run.status.updated`, the
  `submitResponse` combination table on `admission` and
  `effective_delivery`, the interaction and tool transition tables on
  their `status`, `outcome`, and `kind` values, the `relationship` of an
  `active_runs` entry), so a known envelope carrying a new member, such
  as `run.status.updated` with `status: "paused"`, would pass the schema
  and then be diagnosed `illegal_run_transition`. In tolerant mode a
  value-level rule keyed on a member the validator does not know is
  suspended for that envelope while type-level bookkeeping continues:
  the event still advances the cursor, still counts as the transition it
  is by type (a resolution still settles its interaction, a terminal
  event type still terminates the run, a response still answers its
  request), and the unknown value is recorded as opaque state, so an
  unknown run status is nonterminal (terminality is a property of event
  types, not of status strings) and neither the transition into it nor
  the next transition out of it is judged, an unknown admission or
  delivery suspends the combination-table predicates that read it (never
  the whole table, as below) but keeps the correlation, scope,
  and capability checks, with opaque run bookkeeping so the run's own
  events are not orphaned. Suspension is per member, not per envelope:
  every semantic a known member still carries is applied, and only the
  rules that actually depend on the unknown one stand down. The
  `admission` decides run identity, so it alone decides whether a run is
  created. An accepted response whose `admission` is a known member is
  treated exactly as that member says whatever `effective_delivery`
  holds — `steered` operates on an existing started run and creates
  nothing, so a future delivery value cannot let a malformed response
  conjure a run and make its later events look valid, and `queued` and
  `started` register their run as that kind with the rules keyed on it
  live. The combination table is split on the same principle rather than
  skipped whole: its predicates that read only the request's delivery and
  the admission — T4's rule that an `auto` request is never admitted
  `steered` among them — are known on both sides and stay live under an
  unknown `effective_delivery`, so a future delivery value cannot license
  treating an ordinary submit as a steer; only the predicates that
  actually read the effective delivery are suspended. A tolerant
  validator suspends the rules that need the member it does not
  understand, never the rules that merely sit near them.
  Beyond the table, the rules keyed on the delivery itself (which bound applies,
  whether the delivery was advertised) are quarantined. Opaque run
  creation is reserved for an unknown `admission`: a response carrying
  one that names a `run_id` the validator does not yet track registers
  that run as opaquely admitted (run known, status opaque, the
  pre-`run.started` rule and every rule keyed on the admission kind, such
  as queue order, limits, and steer settlement, quarantined for it), and
  one naming a tracked run is treated as an operation on that run,
  creating nothing; the run's later events then get the type-independent
  bookkeeping and the lifecycle rules that do not depend on the
  admission kind (one terminal, nothing after it, sequence contiguity).
  An unknown interaction `kind`, `status`, or
  `outcome` skips the value-specific branch and keeps the lifecycle
  checks. Strict mode is unchanged. The step's tests feed
  `run.status.updated` with `status: "paused"` followed by a known
  transition (tolerant: valid; strict: the schema rejection), and a
  `session.message.submit.response` with an unknown `admission` and a
  new `run_id`, followed by that run's `run.started` and `run.completed`
  (tolerant: correlation and scope checked, no combination diagnostic,
  the run registered, its events accepted, and its terminal recorded;
  strict: the schema rejection).
- Fixture validation stays strict against the bundle at its own revision.
  That is the conformance validator's job and how a misspelled new field is
  caught; each unit extends the bundle in place under `schema/v0.1`.
- `capabilities.response` gains no schema-revision field: a consumer learns
  what an endpoint will emit from the capability keys the units add, and
  the tolerant compile makes the fields those keys imply harmless to a
  client that predates them.

Until the tolerance step lands, an older strict validator is incompatible
with a newer endpoint's additions. No released consumer exists today, so
sequencing the step first is sufficient, and any external consumer that
ships before it must validate tolerantly or not at all. This is the
answer to issue #13's versioning question: the wire stays `0.1` because
nothing existing changes meaning, and forward compatibility is made a
stated property of validators rather than an assumed property of the
bundle.

## Deliberately later

Unchanged from Decisions 0001 and 0002 and PF-4: `btw` and side runs;
subagent trees and background tasks (a run-tree design comes first);
artifacts; checkpoints, rewind, branch; compaction markers; durable
idempotent admission; cross-process replay and continuity leases; orphan
terminals; retained-interaction reassociation and deadlines; runtime
tool-source attach and detach; per-submit control-layer tools; model
resolution and aliases; auth login flows; skills at the wire level.
Added by T0: pack-supplied stateful validation rules and pack-defined
diagnostics — T0's seam is schema and gate only, since a pack ships no
code and the validator cannot run what it does not contain; a way for a
pack to carry semantics is a separate design with its own evidence
question.

## Open items for the per-unit decisions

- T1: confirm the `tool_choice` shape before any native adapter advertises
  `run.tool_selection`; decide whether `metadata` on the submit request is
  passed through to the harness or reserved.
- T5a: whether `context_window` and `features` are worth requiring, or
  `id` alone is the executable minimum.
- T2: the exact `limits` field names, and whether `queue_position` is
  required or optional on queued `active_runs` entries.
- T3: whether `remote` sources are admitted in the first slice or held until
  a header-free evidence case exists; the resolution response's `accepted`
  semantics when the harness has already timed the call out.
- T4: the four questions listed under the unit.
