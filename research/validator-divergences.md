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
- Rust has no catalog, switch or attachment checks. Every row in the file is
  owed there.
