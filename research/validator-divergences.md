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
judged these and no text stated them. All eight are settled; what remains is
the ports each decision names.

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
Settled by [Decision 0055](../decisions/0055-register-batch-2.md); every tree owes acceptance of a queued run's cancel.

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
integer (`state.go:apply`); Rust does not judge it.

- (a) A string holding a monotonically increasing unsigned decimal integer no
  greater than 2^53 − 1.
- (b) Any opaque string.

Recommendation: (a). Settled by [Decision 0055](../decisions/0055-register-batch-2.md); Rust owes the port.

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
