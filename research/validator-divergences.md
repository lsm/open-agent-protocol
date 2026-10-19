# Validator divergences

Behaviour a validator judges that does not yet trace to one rule: either no
decision text supports it, or the trees disagree on it, or only one tree defines
it. Each item is numbered `OPEN-<decision>-<n>`, gives the options, and
recommends one. An item closes when a decision or a rule row in
[`drafts/validator-rules/`](../drafts/validator-rules/) settles it, and the
fixing pull request names the item. Nothing here is resolved toward any one
tree by default.

## Models and providers (0006, 0014, 0028)

Rules: [`0006-models.md`](../drafts/validator-rules/0006-models.md).

### OPEN-0028-1: does an unadvertised attachment's refusal owe `details.reason`?

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

### OPEN-0028-2: is accepting an attachment without `session_live` a diagnostic of its own?

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

### OPEN-0028-3: is a degraded attachment's refusal judged?

When `action.providers.attach` is `degraded` and the request omits the opt-in,
Zig requires the refusal to be `capability_degraded` naming the feature
(`semantic.zig:gatedRequest`). Go judges only an acceptance
(`modelcontrol.go:providerAttachResponse`) and leaves the refusal alone.

- (a) Judge the refusal, as Zig does.
- (b) Judge only the acceptance, as Go does.

Recommendation: (a). 0028 applies "the normal degraded opt-in rule", and 0005's
rule fixes the refusal's code. Go needs the port.

### OPEN-0028-4: must a stale-revision catalog list attached providers?

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

**Status.** Settled by 0008's existing text. What remains is the Zig port fix
and its fixture.

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
