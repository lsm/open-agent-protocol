# Validator divergences

Behaviour a validator judges that does not yet trace to one rule: either no
decision text supports it, or the trees disagree on it, or only one tree defines
it. Each item is numbered `OPEN-<decision>-<n>`, gives the options, and
recommends one. An item closes when a decision or a rule row in
[`drafts/validator-rules/`](../drafts/validator-rules/) settles it, and the
fixing pull request names the item. Nothing here is resolved toward any one
tree by default.

## Executable core (0001, 0002)

Rules: [`0001-core.md`](../drafts/validator-rules/0001-core.md). Every tree
judged these and no text stated them, except OPEN-0001-9, which no tree
judged. All nine are settled; what remains is the ports each decision names.

### OPEN-0001-1: is an envelope id unique within a trace?

All three trees raise `duplicate_envelope_id` for a repeated `id`
(`state.go:apply`).

- (a) Unique within the trace, as every tree judges.
- (b) Unique only among requests, the envelopes correlation names.

Recommendation: (a), since a repeat makes every reply to either ambiguous.
Settled by [Decision 0055](../decisions/0055-register-batch-2.md).

### OPEN-0001-2: may a cancel name a queued run?

Go and Zig raise `illegal_run_transition` for a cancel naming a run no
admission created (`state.go:apply`).

- (a) Any admitted run, queued reservations included.
- (b) Only a run that has started.

Recommendation: (a), so a reservation can be withdrawn before it promotes.
Settled by [Decision 0055](../decisions/0055-register-batch-2.md). All three trees already accept a queued run's cancel, pinned by `admission-queued-prestart-cancelled` and `queue-busy-cancelled-prestart`, so no port is owed.

### OPEN-0001-3: does a response stay in its request's scope?

Go and Zig raise `scope_mismatch` when a response, `error.response` included,
names another session, run or interaction than its request
(`state.go:response`).

- (a) Yes, for every scope the response states.
- (b) Only for the session.

Recommendation: (a). Settled, together with the 0008 group's tool-call scope items, by [Decision 0055](../decisions/0055-register-batch-2.md); the
scope checks only Go has go to Zig and Rust.

### OPEN-0001-4: is a run admitted once, into one session?

Go and Zig raise `scope_mismatch` for an admission or run event naming another
session's run, and `illegal_run_transition` for a second admission
(`state.go:admitSubmission`, `state.go:runEvent`).

- (a) Once, into one session, with every event scoped there.
- (b) Leave re-admission to the endpoint.

Recommendation: (a). Settled by [Decision 0055](../decisions/0055-register-batch-2.md).

### OPEN-0001-5: is an interaction id requested once per run?

Go and Zig raise `duplicate_interaction` for a second request under one id
(`state.go:interactionRequested`).

- (a) Once per run, a replay from a retained journal not counting as a second
  request.
- (b) Once per run, counting replays.

Recommendation: (a). Settled by [Decision 0055](../decisions/0055-register-batch-2.md).

### OPEN-0001-6: are input answers judged against the prompt?

Go and Zig raise `unmatched_interaction` for an answer naming a question the
prompt did not ask, or leaving a required one empty
(`state.go:interactionResolutionRequest`, `state.go:validateInputAnswers`).

- (a) Judge each answer against the prompt's questions.
- (b) Judge only that the interaction exists.

Recommendation: (a), with non-empty text owed only for required questions,
choice questions included. Settled by [Decision 0056](../decisions/0056-input-answers-are-judged-against-the-prompt.md).

### OPEN-0001-7: what form does a resume cursor take?

Go and Zig raise `undeclared_replay_gap` for a cursor that is not an increasing
integer (`state.go:apply`, `semantic.zig`), and Rust for one that is not an
unsigned decimal integer no greater than 2^53 − 1
(`state/admissions.rs:open_response`).

- (a) A string holding a monotonically increasing unsigned decimal integer no
  greater than 2^53 − 1.
- (b) Any opaque string.

Recommendation: (a). Settled by [Decision 0055](../decisions/0055-register-batch-2.md).
Rust judges the bound. Go and Zig still parse the cursor as any `u64`
(`strconv.ParseUint(…, 10, 64)` in `state.go`, `std.fmt.parseUnsigned(u64, …)`
in `semantic.zig`), so a cursor from 2^53 to 2^64 − 1 passes there and is
`undeclared_replay_gap` in Rust; each owes the bound. Go and Zig also judge the
cursor's form only on a recovered open, where 0055 binds every `resume_cursor`
and Rust judges it whether or not the open recovered.

### OPEN-0001-8: must a request wait for the refresh `capabilities.updated` owes?

Go and Zig raise `stale_capability_revision` for a submit or compaction made
between `capabilities.updated` and the next `capabilities.response`
(`state.go:apply`, `compaction.go:compactRequest`); Rust judges submits only.

- (a) Every request waits, except those that discover the new descriptor or
  settle work in flight.
- (b) Only submits and compactions, as the trees do.

Recommendation: (a), exempting `protocol.initialize.request`,
`capabilities.request`, `run.cancel.request` and the answers to pending
interactions. Settled by [Decision 0055](../decisions/0055-register-batch-2.md); all three trees owe the port.

### OPEN-0001-9: what does `usage` count?

`usage` carried `input_tokens`, `output_tokens` and `total_tokens`, and no text
said what they count. Harnesses disagree on whether output includes reasoning
and whether input includes cache, and the Go opencode adapter dropped
opencode's reasoning and cache counts entirely.

- (a) Add optional `cache_read_input_tokens`, `cache_write_input_tokens` and
  `reasoning_tokens`, with `input_tokens` excluding cache and `output_tokens`
  including reasoning.
- (b) Keep the three members and only define that `output_tokens` includes
  reasoning, leaving cache to each adapter's extensions.
- (c) Leave usage unspecified.

Recommendation: (b), as the smaller change; the owner chose (a), since a
consumer pricing runs across harnesses needs cache in one place.

**Status.** Settled by [Decision 0063](../decisions/0063-usage-counts-cache-and-reasoning-tokens.md). The schema and every tree's `Usage` type carry the members already; the opencode and claude adapters, and `go/internal/provider`'s Anthropic backfill, follow.

## Queue, open and cancel (0007, 0009, 0041)

Rules: [`0007-queue-open-cancel.md`](../drafts/validator-rules/0007-queue-open-cancel.md).

### OPEN-0007-1: is a no-position entry's pending set judged without the queue?

Decision 0007 reads an `active_runs` entry that states no position "as the
trace stands". Go and Zig judge its `pending_interactions` against the trace
only where `session.message.delivery.queue` is advertised
(`queuestate.go:checkEntryPending`, `semantic.zig:checkEntryPending`); Rust
does not judge it.

- (a) Judge it whatever the descriptor advertises: an entry that exists makes
  the claim whether or not the queue does.
- (b) Judge it only where the queue is advertised, as Go and Zig do.

Recommendation: (a). Settled by
[Decision 0054](../decisions/0054-register-batch-1.md). Go and Zig judge it
without the gate, pinned by
`queue-state-unpositioned-entry-omits-pending-without-queue`; Rust owes the
port.

### OPEN-0007-2: which endpoint "puts entries there at all"?

Decision 0007 requires `active_runs` for a run whose recovery needs an id only
an entry holds "on an endpoint that puts entries there at all". Go and Zig read
that as an endpoint advertising `session.message.delivery.queue`
(`queuestate.go:checkActiveRunsOmitted`, `semantic.zig:checkRequiredRuns`);
Rust does not judge it.

- (a) An endpoint advertising the queue, as Go and Zig do.
- (b) An endpoint that has sent an entry anywhere in the trace.

Recommendation: (a). Under (b) the same snapshot is complete in one trace and
incomplete in another, and an endpoint escapes the rule by never listing a
run. Settled by
[Decision 0057](../decisions/0057-an-endpoint-that-puts-entries-there-advertises-the-queue.md);
Rust owes the port.

## Models and providers (0006, 0014, 0028)

Rules: [`0006-models.md`](../drafts/validator-rules/0006-models.md).

### Closed: OPEN-0028-1: does an unadvertised attachment's refusal owe `details.reason`?

Closed by [Decision 0054](../decisions/0054-register-batch-1.md), option (a),
and 0028's amended text; rule row `0028#optional-sessionproviderattach/5`. Go
now requires the reason, pinned by fixture
`provider-attach-unadvertised-refusal-without-reason`.

An attachment sent to an endpoint that does not advertise
`action.providers.attach` must be refused. Go accepts any `unsupported_feature`
refusal naming the feature (`modelcontrol.go:settleModelControlRefusal`). Zig
gates the request like every other control, so it also requires
`details.reason: "unadvertised"` (`semantic.zig:gatedRequest`, then
`conformingRefusal`). Decision 0028 names the code and `details.feature` only,
while Decision 0005's unadvertised refusal carries the reason.

- (a) Require the reason, as Zig does, because 0028 refers to "the normal
  degraded opt-in rule", which is 0005's gate.
- (b) Code and feature only, as Go does, reading 0028's sentence as complete.

Recommendation: (a). It makes a refused attachment look like every other
refused control, and 0028 already defers to 0005 for the degraded half.

### Closed: OPEN-0028-2: is accepting an attachment without `session_live` a diagnostic of its own?

Closed by [Decision 0054](../decisions/0054-register-batch-1.md), option (a),
and 0028's amended text; rule row `0028#optional-sessionproviderattach/3`. Zig
now fails the acceptance, pinned by fixture
`provider-attach-accepted-without-session-live`.

An endpoint that advertises `action.providers.attach` affirmatively without the
`session_live` mode fails `undisclosed_attach_modes` on the descriptor in both
trees. Go also fails an accepted attachment under that descriptor with
`unavailable_capability` (`modelcontrol.go:providerAttachResponse`). Zig judges
the acceptance on the level alone, so it raises nothing more
(`semantic.zig:gatedResponse`).

- (a) Both diagnostics, as Go does: the descriptor hid the mode, and the
  acceptance used a mode that was never advertised.
- (b) The descriptor only, as Zig does: one fault, one diagnosis.

Recommendation: (a). 0028 says an endpoint "advertises
`action.providers.attach` with the `session_live` mode to accept", which makes
the mode a precondition of each acceptance, not only of the descriptor.

### Closed: OPEN-0028-3: is a degraded attachment's refusal judged?

Closed by [Decision 0054](../decisions/0054-register-batch-1.md), option (a),
and 0028's amended text; rule row `0028#optional-sessionproviderattach/9`. Go
now judges the refusal, pinned by fixture
`provider-attach-degraded-refusal-wrong-code`.

When `action.providers.attach` is `degraded` and the request omits the opt-in,
Zig requires the refusal to be `capability_degraded` naming the feature
(`semantic.zig:gatedRequest`). Go judges only an acceptance
(`modelcontrol.go:providerAttachResponse`) and leaves the refusal alone.

- (a) Judge the refusal, as Zig does.
- (b) Judge only the acceptance, as Go does.

Recommendation: (a). 0028 applies "the normal degraded opt-in rule", and 0005's
rule fixes the refusal's code. Go needs the port.

### Closed: OPEN-0028-4: must a stale-revision catalog list attached providers?

Closed by [Decision 0054](../decisions/0054-register-batch-1.md), option (b),
and 0028's amended text; rule row `0028#optional-sessionproviderattach/8`. Go
now checks only a catalog that binds, pinned by fixture
`provider-attach-stale-catalog-omits-attached`.

After an accepted attachment, a catalog that omits the alias fails
`unmatched_provider`. Go checks every catalog for the session, whatever revision
it cites (`models.go:modelsResponse`, before the revision check). Zig checks
only a catalog citing the active revision (`semantic.zig:modelsResponse`, after
it).

- (a) Every catalog, as Go does.
- (b) Only a catalog that binds, as Zig does.

Recommendation: (b). 0006 says a non-current catalog "can bind nothing" and is
recorded nowhere, and whether it lists an attachment depends on session state
that a listing captured under an earlier revision may predate. The checks that
still apply to a stale catalog are properties of the listing alone (duplicate
ids, two defaults, a current model it does not list), and this is not one.

### Ports the decision text already settles

These are disagreements the decisions decide, so each has a rule row and needs
only a port. None is a protocol question.

- 0006 rows `current_model_id…/2` and `/3`: Zig does not judge a catalog's
  `current_model_id` against the query's window or a reached run position. It
  judges the switch-anchored position only (`semantic.zig:close`). Port to Zig.
- 0028 rows `core-sessionmodelswitch/7` and `/8`: Zig does not judge a switch
  refusal's `model_not_found` against the bound catalog. Port to Zig.
- 0028 row `core-sessionmodelswitch/4`: neither tree judges the refusal of a
  degraded switch sent without the opt-in, which 0028's "ordinary gate" fixes
  as `capability_degraded`. Add it to both.
- Rust judges only the session scope of these envelopes. Every other row in
  the file is owed there, as is the alias half of `optional-sessionproviderattach/2`.

## Tool sources and provided tools (0008, 0011, 0048)

Rules: [`0008-tool-sources.md`](../drafts/validator-rules/0008-tool-sources.md).

### OPEN-0008-1: does a snapshot judge a declared source's description?

**What differs.** After the open, Go holds every source a snapshot reports to
one description: the open response's adopted description for an attached
source, and the descriptor's for a declared one
(`go/validation/toolsources.go:checkPublishedUnion`). Zig compares only attached
sources and skips a declared source's members
(`zig/src/validation/semantic.zig:checkPublishedUnion`, `stated orelse continue`).
So a snapshot that redescribes a declared source fails in Go and passes in Zig.

**Decision text.** 0008 § Attachment is for the session's lifetime: the
snapshot's `sources` are "the union of the open's attachments and the
descriptor's declared sources, compared by `id` and by each descriptor's
published members."

**Options.** (a) Zig judges declared sources too, as Go does. (b) Narrow the
rule to attached sources.

**Recommendation.** (a). The decision text names both halves of the union, so
this is a Zig port gap rather than an open protocol question. It needs a
negative fixture, since none isolates a redescribed declared source.

**Status.** Settled by 0008's existing text, and closed: Zig judges declared
sources too, pinned by `state-redescribes-declared-source`.

### OPEN-0008-2: which scope checks does the tool catalog and resolve pair owe?

**What differs.** Three scope checks exist only in Go:

- A session-scoped `action.tools.list.response` must name its session on the
  envelope (`toolsListResponse`). Zig's `checkScope` fires only when both
  sides are present and differ.
- `action.call.resolve.request` and `.response` must carry the same
  `tool_call_id` on the envelope and in the payload (`controlResolveRequest`,
  `controlResolveResponse`).
- A resolution must name the tool call its interaction is bound to
  (`controlResolveRequest`).

Zig has none of the three.

**Decision text.** 0008 § A list request is a request of its own requires the
response to repeat the session "on its envelope and in its payload". 0011 gives
the resolve pair its members but states no scope check for them.

**Options.** (a) Port all three to Zig and Rust, citing 0008 for the first and
0001's scope agreement for the other two. (b) Port the first and record the
other two as drafted rules.

**Recommendation.** (a). The first has decision text. The other two follow
from the pair being session- and run-scoped, and are drafted in
`drafts/validator-rules/0008-tool-sources.md` as such.

**Status.** Settled by [Decision 0055](../decisions/0055-register-batch-2.md). What remains is the port work the decision names.

### OPEN-0008-3: does a permission resolution keep its tool binding?

**What differs.** A `permission.resolved` event that names a `tool_call_id`
(on the envelope or in the payload) other than the one its request was bound
to is `scope_mismatch` in Go (`state.go:interactionResolved`) and Rust
(`state/interactions.rs:interaction_resolved`). Zig judges neither.

**Decision text.** 0001 § Correlated reverse interactions correlates a
resolution with its request, but no decision says the tool binding must carry
over. Before Decision 0055, no decision said so.

**Options.** (a) Extend 0055's binding rule to every resolution that carries
`tool_call_id`, and port it to Zig. (b) Leave the binding unjudged, and drop
the Go and Rust checks.

**Recommendation.** (a). A resolution that moves its tool binding resolves a
call it was never asked about, the defect 0055 already rules out for the
resolve pair.

**Status.** Settled by [Decision 0055](../decisions/0055-register-batch-2.md). What remains is the port work the decision names.

### OPEN-0011-1: which pattern syntax is `name_pattern`, and is it anchored?

**What differs.** Go refuses a `tools` array naming a tool outside a disclosed
`name_pattern` as a limit violation (`controltools.go:provideLimitViolation`).
Zig reads `max_tools` and `schema_dialect` but not `name_pattern`
(`semantic.zig:provideLimitViolation`).

**Decision text.** 0011 § The capability discloses its limits lists
`name_pattern` among the limits a refusal may rely on. 0019 § A rule the port
cannot reach is recorded, not approximated, already records this as a
deliberate Zig omission. Zig has no regex engine, a hand-written subset would
diverge from RE2, and no fixture discloses `name_pattern`.

**Options.** (a) Vendor an RE2-compatible engine in Zig, which 0019 calls its
own decision. (b) Pin the pattern dialect to a subset both trees can decide,
and amend 0011.

**Recommendation (before 0055).** Keep 0019's record and leave the item open
until an engine is chosen. Decision 0055 then chose the dialect: the pattern
is RE2 and must match the whole name.

**Status.** Settled by [Decision 0055](../decisions/0055-register-batch-2.md). What remains is the port work the decision names.

### OPEN-0011-2: may a provided tool's resolution carry content parts?

[Decision 0060](../decisions/0060-a-tool-result-may-carry-content-parts.md)
lets a `tool_result` part and `action.call.completed` carry a content-part
list, images gated on `content.image`. `action.call.resolve.request`, by which
a control layer answers a call to a tool it provides, also carries a free JSON
`result`, and nothing said whether it takes the same form. No tree judges the
shape today.

- (a) The same reading as 0060: a content-part list is allowed, its images
  gated on the endpoint's `content.image`.
- (b) JSON only, so a provided tool cannot return an image.

Recommendation: (a). The endpoint reports a provided tool's outcome on
`action.call.completed`, which 0060 already lets carry an image, so (b) would
leave a control layer unable to supply what the endpoint is allowed to report.
Settled by
[Decision 0060](../decisions/0060-a-tool-result-may-carry-content-parts.md).

### OPEN-0011-3: what does a `name_pattern` that is not RE2 mean?

A descriptor can publish a `name_pattern` that does not compile. Go skips the
whole limit, so it is neither enforced nor diagnosed, and a refusal that
honours it draws `undisclosed_provide_limit` (`controltools.go`). Zig does not
read the pattern yet, and Rust has no provided-tool limits.

- (a) The descriptor is defective: diagnose it on its `capabilities.response`,
  as `undisclosed_provide_limit` at the pattern.
- (b) The pattern discloses no limit, so a refusal relying on it is
  `undisclosed_provide_limit`.
- (c) The schema refuses it, which JSON Schema cannot express.

Recommendation: (a). A limit nobody can evaluate discloses nothing, and (a)
surfaces the defect when the descriptor arrives rather than when something is
refused.

**Status.** Settled by [Decision 0062](../decisions/0062-a-name-pattern-that-is-not-re2-is-a-defective-limit.md). Go, Zig and Rust follow it in their `name_pattern` ports.

## Session lifecycle (0040, 0044–0046, 0049–0051)

Rules: [`0040-session-lifecycle.md`](../drafts/validator-rules/0040-session-lifecycle.md).

### OPEN-0050-1: may a recovered reopen list restored queued runs?

0040 has a successful reopen list no run under way. 0050 § 5 amends that: a
recovered reopen (`recovery.recovered: true`) may list restored runs as
`queued`, in their original order, and "the validator accepts restored queued
runs in a recovered open response and nowhere else". Go
(`reopen.go:reopenResponse`) and Zig (`semantic.zig:reopenResponse`) still
diagnose any listed run on a reopen that carried no message.

- (a) Both trees accept `queued` restored runs on a recovered reopen, and
  nowhere else, as 0050 says.
- (b) Keep 0040's rule unamended.

Recommendation: (a). 0050's text decides it, so this is a port fix in both
trees. It lands with restart recovery, with a positive fixture and a negative
one that lists a restored run on an unrecovered reopen.

**Status.** Settled by 0050 § 5. What remains is the port fix.

## Model provider profile (0016, 0030)

Rules: [`0016-provider.md`](../drafts/validator-rules/0016-provider.md). Go
and Zig judge all seven items alike over the provider corpus, the sixth and
seventh through the schema, and Rust has no provider validator. No item is a disagreement
between trees. Each is a rule that only [`drafts/model-provider-core.md`](../drafts/model-provider-core.md)
states, because Decision 0016 leaves the profile's validator rules to a later
decision (0016 L187–195). The recommendation throughout is to adopt the draft's
text as written, so the trees keep what they judge and Rust ports it.

### OPEN-0016-1: is a streamed terminal the assembly of its ended parts?

The draft makes `inference.part.ended` authoritative over its deltas, and
`inference.completed.message` the assembly of the ended parts in `part_index`
order where parts were streamed. A trace with no parts is outside the rule
(L624–651). Both trees raise `terminal_not_assembly` (`provider.go:assemblyDefect`,
`provider_semantic.zig:Machine.close`).

- (a) Adopt the two rules and the code as the draft states them.
- (b) Leave the terminal's relation to its parts unstated, so a consumer
  holding the parts cannot tell a lost frame from a changed answer.

Recommendation: (a).

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

### OPEN-0016-2: is a credential-shaped header refused?

The draft makes a credential in `inference.create.request.headers`
non-conformant. The validator rejects a header named `Authorization`,
`Proxy-Authorization`, `X-Api-Key` or `Api-Key` (case-insensitive), or one
whose value starts with `Bearer ` after leading spaces and tabs (also
case-insensitive), on a call or in a descriptor's published `headers`
(L1588–1597). Both trees raise `credential_in_headers`
(`provider.go:credentialHeaders`, `provider_semantic.zig:Machine.credentialHeaders`).

- (a) Adopt the predicate as stated, recorded as a floor that does not catch a
  bare key in an unnamed header.
- (b) Leave headers unjudged.

Recommendation: (a).

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

### OPEN-0016-3: is a grant request carrying a `value` invalid in a trace?

The draft's tier-2 grant carries the credential on
`provider.credential.grant.request`, never journalled or traced, so a trace
holding one that carries `value` is invalid (L1391–1395). Both trees raise
`credential_in_trace` on the presence of `value` and judge that envelope no
further (`provider.go:Validate`, `provider_semantic.zig:Machine.apply`).

- (a) Adopt it as stated.
- (b) Leave it to the binding, so a trace that recorded a secret validates.

Recommendation: (a).

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

### OPEN-0016-4: is an inference's `sequence` contiguous from 1?

Every event scoped to one inference carries a positive, contiguous `sequence`
in that inference's own domain (L260–263), opening at 1 and never repeating or
going back (L1864–1865). Both trees raise `sequence_gap` for a first event other
than 1 or a skip, and `sequence_regression` for a repeat or a step back
(`provider.go:Validate`, `provider_semantic.zig:Machine.apply`).

- (a) Adopt it, with agent control's two codes split as the trees split them.
- (b) Adopt it with one code, since the draft's error taxonomy folds both into
  `protocol_violation` (L1737–1741). That passage is about error codes an
  implementation sends, not validator diagnostics.

Recommendation: (a).

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

### OPEN-0016-5: is a scoped event after a terminal invalid?

The draft allows exactly one terminal per accepted inference (L667–668, L1861).
Both trees raise `event_after_terminal` for any scoped event, a second
terminal included, after `inference.completed` or `inference.failed`
(`provider.go:Validate`, `provider_semantic.zig:Machine.apply`).

- (a) Adopt it as stated.
- (b) Adopt it and give a second terminal a code of its own.

Recommendation: (a). The trees already agree on one code, and a fault is not
identified by how many diagnostics it yields.

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

### OPEN-0016-6: may a refused inference carry an id?

The draft says a refused `inference.create.response` carries the typed error
and no `inference_id`, since an inference exists only if it was accepted
(L653–656, L1861–1862). Both trees judge it through the schema, whose
`inference.create.response` envelope forbids `inference_id` unless `accepted`
is true (`provider-envelope.schema.json`, fixture
`refusal-allocating-an-inference`), so it draws `schema_invalid`.

- (a) Adopt it as the schema constraint it already is.
- (b) Move it to a semantic diagnostic of its own.

Recommendation: (a). The rule is decidable from one frame, and the provider
corpus judges frame-scoped defects through the schema.

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

### OPEN-0016-7: do the provider schemas bind?

Decision 0016 leaves the profile's schemas, and not only its validator rules,
to a later decision (L187–195). Both trees still judge every provider frame
against `schema/v0.1/provider-envelope.schema.json` and the schemas it
references, and 16 fixtures under `fixtures/provider/schema-invalid/` pin what
that rejects. Among them, `tool-call-start-without-identity` and
`text-start-carrying-identity` pin the draft's conformance item 5
(L1866–1869), and `error-code-outside-the-set` pins its closed error-code set.
Item 6 is one of these constraints, listed apart because the draft states it
as a rule of its own.

- (a) Adopt the provider schemas as they stand on main as normative for the
  profile.
- (b) Adopt them constraint by constraint, one item per fixture.

Recommendation: (a). The schemas and the draft were written together, and
both trees already apply them whole. Any constraint a later reading finds the
draft does not support becomes an item of its own then.

**Status.** Settled by [Decision 0061](../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md). Go and Zig judge it; Rust owes the port.

