# Models and providers

Decisions 0006, 0014, 0028 and 0035: the rows behind every diagnostic the
validators raise for the session model catalog, the provider descriptors it
publishes, the core model switch and live provider attachment. Go and Zig name
the function in `go/validation/`, `zig/src/validation/` and
`rust/validation/src/`. Rust judges session scope through generic response and
payload checks, plus response-local catalog model IDs, defaults and current-model
membership. Catalog history, model-event positions, switches and attachment
rules beyond session scope remain owed. The attachment rows follow 0028 as
[Decision 0054](../../decisions/0054-register-batch-1.md) amends it, which
closed OPEN-0028-1 to -4 in
[`research/validator-divergences.md`](../../research/validator-divergences.md).
Format: [README](README.md).

### [0006] The catalog is a session-scoped query, and it binds in both directions

Source: [`decisions/0006-models-catalog.md`](../../decisions/0006-models-catalog.md#the-catalog-is-a-session-scoped-query-and-it-binds-in-both-directions).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0006#the-catalog-is-a-session-scoped-query-and-it-binds-in-both-directions/1` | A submission whose `model_id` the catalog bound under the active revision does not list MUST be refused with `model_not_found` naming that id in `details.model_id`. | L56–62 | `model_not_in_catalog` | `controls.go:unsatisfiable`, `controls.go:settleControlRefusal` | `semantic.zig:unsatisfiable`, `semantic.zig:settleControlRefusal` | — |
| `0006#the-catalog-is-a-session-scoped-query-and-it-binds-in-both-directions/2` | An admission MUST NOT accept a `model_id` the catalog bound under the active revision does not list. | L56–57 | `model_not_in_catalog` | `controls.go:unsatisfiable` | `semantic.zig:unsatisfiable` | — |
| `0006#the-catalog-is-a-session-scoped-query-and-it-binds-in-both-directions/3` | A refusal of a submission whose `model_id` the bound catalog lists MUST NOT use `model_not_found`; any other refusal is left alone. | L52–55, L65–76 | `model_not_in_catalog` | `controls.go:settleControlRefusal`, `models.go:diagnoseFalseMiss` | `semantic.zig:settleControlRefusal` | — |
| `0006#the-catalog-is-a-session-scoped-query-and-it-binds-in-both-directions/4` | `models.request` and `models.response` are session-scoped: the envelope and payload session MUST agree, and a response MUST name the session its request named. | L45–46 | `scope_mismatch` | `state.go:checkScope` | `semantic.zig:checkScope` | `state.rs:response`, `state.rs:payload_scope` |

### [0006] The rule stands down where selection is not advertised

Source: [`decisions/0006-models-catalog.md`](../../decisions/0006-models-catalog.md#the-rule-stands-down-where-selection-is-not-advertised).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0006#the-rule-stands-down-where-selection-is-not-advertised/1` | Where `run.model_selection` is not advertised, a submission carrying `model_id` owes Decision 0005's `unsupported_feature` refusal, and the catalog rule raises nothing for it; precedence is fixed when the request is sent. | L80–86 | `unavailable_capability` (0005's gate; this rule adds none) | `controls.go:unsatisfiable` | `semantic.zig:unsatisfiable` | — |

### [0006] A catalog belongs to the revision it was served under

Source: [`decisions/0006-models-catalog.md`](../../decisions/0006-models-catalog.md#a-catalog-belongs-to-the-revision-it-was-served-under).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0006#a-catalog-belongs-to-the-revision-it-was-served-under/1` | Two catalogs served for one session under one capability revision at `native` or `emulated` MUST be equal by value, comparing models and providers by id. | L101–104, L177–186 | `unannounced_catalog_change` | `models.go:modelsResponse` | `semantic.zig:modelsResponse` | — |
| `0006#a-catalog-belongs-to-the-revision-it-was-served-under/2` | A `capabilities.response` repeating the active revision MUST advertise `models.list` at the level that revision already advertised, unless a `capabilities.updated` has invalidated it. | L139–146, L170–176 | `unannounced_catalog_change` | `models.go:checkCatalogAdvertisement` | `semantic.zig:checkAdvertisement` | — |
| `0006#a-catalog-belongs-to-the-revision-it-was-served-under/3` | A catalog citing a revision other than the active one, or arriving before the descriptor of an announced revision is in hand, binds nothing and displaces nothing; its gate reports the stale or missing descriptor. | L106–117, L148–157 | `stale_capability_revision`, `unavailable_capability` | `state.go:featureKeys`, `models.go:modelsResponse` | `semantic.zig:featureKeys`, `semantic.zig:modelsResponse` | — |
| `0006#a-catalog-belongs-to-the-revision-it-was-served-under/4` | A selection made under a revision whose catalog has not been served is held, and the first `native` or `emulated` catalog under that revision judges it as rows /1 to /3 of the binding rule would have. | L195–203 | `model_not_in_catalog` | `models.go:reconcileUnjudgedModels` | `semantic.zig:reconcileUnjudged` | — |
| `0006#a-catalog-belongs-to-the-revision-it-was-served-under/5` | A catalog MUST NOT list two descriptors under one id. | L119–122 | `duplicate_model_id` | `models.go:modelsResponse` | `semantic.zig:modelsResponse` | `state/models.rs:models_response_shape` |
| `0006#a-catalog-belongs-to-the-revision-it-was-served-under/6` | A catalog MUST mark at most one model as the default. | L119–122 | `ambiguous_default_model` | `models.go:modelsResponse` | `semantic.zig:modelsResponse` | `state/models.rs:models_response_shape` |

### [0006] `current_model_id` is judged across the query's window, not at an instant

Source: [`decisions/0006-models-catalog.md`](../../decisions/0006-models-catalog.md#current_model_id-is-judged-across-the-querys-window-not-at-an-instant).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0006#current_model_id-is-judged-across-the-querys-window-not-at-an-instant/1` | A nonempty `current_model_id` MUST name one of the response's own model ids. | L263–266 | `model_not_in_catalog` | `models.go:modelsResponse` | `semantic.zig:modelsResponse` | `state/models.rs:models_response_shape` |
| `0006#current_model_id-is-judged-across-the-querys-window-not-at-an-instant/2` | A catalog without `as_of_model_event` MUST report a `current_model_id` the session held at some point between the query and its response. | L211–219 | `session_state_mismatch` | `models.go:checkCatalogCurrentModel` | — | — |
| `0006#current_model_id-is-judged-across-the-querys-window-not-at-an-instant/3` | A catalog whose `as_of_model_event` the trace has reached MUST report the model the session held at that event; one the trace has not reached is held and judged when the event arrives. | L221–229, L256–261 | `session_state_mismatch` | `models.go:checkCatalogCurrentModel`, `models.go:reconcileHeldCatalogs` | `semantic.zig:close` (switch positions only) | — |
| `0006#current_model_id-is-judged-across-the-querys-window-not-at-an-instant/4` | An `as_of_model_event` MUST name a run of the catalog's own session, whether or not the catalog reports a current model. | L231–254 | `scope_mismatch` | `models.go:foreignPosition` | `semantic.zig:close` | — |
| `0006#current_model_id-is-judged-across-the-querys-window-not-at-an-instant/5` | An `as_of_model_event.switch_request_id` MUST name a model switch of the catalog's own session. | L206–210, L231–233; 0028 L60–64 | `scope_mismatch` | `models.go:foreignPosition` | `semantic.zig:close` | — |

### [0006] The opt-in is per query, because the query is a request of its own

Source: [`decisions/0006-models-catalog.md`](../../decisions/0006-models-catalog.md#the-opt-in-is-per-query-because-the-query-is-a-request-of-its-own).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0006#the-opt-in-is-per-query-because-the-query-is-a-request-of-its-own/1` | A `models.request` to an endpoint advertising `models.list` as `degraded` without the key in `allow_degraded_features` MUST be refused with `capability_degraded` naming the feature; one to an endpoint that does not advertise it MUST be refused with `unsupported_feature`. | L270–277 | `degraded_without_optin`, `unavailable_capability` | `models.go:modelsRequest`, `models.go:settleModelsRefusal` | `semantic.zig:gatedRequest`, `semantic.zig:settleControlRefusal` | — |
| `0006#the-opt-in-is-per-query-because-the-query-is-a-request-of-its-own/2` | A catalog MUST NOT be served in answer to a query that owed the degraded opt-in and omitted it. | L275–277 | `degraded_without_optin` | `models.go:modelsResponse` | `semantic.zig:modelsResponse` | — |
| `0006#the-opt-in-is-per-query-because-the-query-is-a-request-of-its-own/3` | A satisfiable `models.request` to an endpoint advertising `models.list` MUST NOT be refused. | drafted here, from L47–49 (serving the catalog is the act `models.list` governs) and Decision 0001's capability promise; proposed | `unhonoured_capability` | `models.go:settleModelsRefusal` | `semantic.zig:settleControlRefusal` | — |

### [0006] The revision is schema-required, an empty catalog is a list, and a descriptor id is non-empty

Sources: [revision](../../decisions/0006-models-catalog.md#the-revision-is-schema-required-not-only-gated), [empty catalog](../../decisions/0006-models-catalog.md#an-empty-catalog-is-an-empty-list-never-a-null), [descriptor id](../../decisions/0006-models-catalog.md#a-descriptors-id-is-non-empty-by-schema).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0006#the-revision-is-schema-required-not-only-gated/1` | `models.response` MUST carry `capability_revision`. | L405–408 | `schema_invalid` | schema | schema | schema |
| `0006#an-empty-catalog-is-an-empty-list-never-a-null/1` | `models` MUST be an array, empty when the session lists nothing. | L379–381 | `schema_invalid` | schema | schema | schema |
| `0006#a-descriptors-id-is-non-empty-by-schema/1` | A model descriptor's `id`, and `current_model_id` when present, MUST be non-empty. | L427–433 | `schema_invalid` | schema | schema | schema |

The other 0006 headings bind the hub and the Go API rather than a trace: "A
served catalog travels with the revision that governs it" (the hub refuses a
listing with no revision or a foreign scope, pinned by `serve` tests), "The open
response is the state document, so the daemon sends it whole", and
"`adapter.ModelLister` is optional and discovered by type assertion". The open
response's `current_model_id` is still read, as the session default a per-run
selection must not move, which Decision 0005's group judges.

### [0014] A `provider_id` the catalog publishes must resolve

Source: [`decisions/0014-provider-descriptors.md`](../../decisions/0014-provider-descriptors.md#a-provider_id-the-catalog-publishes-must-resolve).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0014#a-provider_id-the-catalog-publishes-must-resolve/1` | Where `providers[]` is present and non-empty, every `provider_id` on a model descriptor MUST name an entry in it, compared exactly. | L88–91 | `unmatched_provider` | `models.go:checkProviders` | `semantic.zig:modelsResponse` | — |
| `0014#a-provider_id-the-catalog-publishes-must-resolve/2` | A catalog MUST NOT list two provider descriptors under one id. | drafted here, from L54 (`id` is what a `provider_id` resolves to, which two entries would make ambiguous); proposed | `duplicate_provider` | `models.go:checkProviders` | `semantic.zig:modelsResponse` | — |

### [0028] Core: `session.model.switch`

Source: [`decisions/0028-live-model-and-provider-control.md`](../../decisions/0028-live-model-and-provider-control.md#core-sessionmodelswitch).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0028#core-sessionmodelswitch/1` | A `session.model.switch.response` MUST name the session its request named. | L25–28 | `scope_mismatch` | `modelcontrol.go:modelSwitchResponse` | `semantic.zig:modelSwitchResponse` | `state.rs:response` |
| `0028#core-sessionmodelswitch/2` | An accepted switch MUST report the requested `model_id` as the new default. | L28–29, L37–38 | `unapplied_control` | `modelcontrol.go:modelSwitchResponse` | `semantic.zig:modelSwitchResponse` | — |
| `0028#core-sessionmodelswitch/3` | A switch accepted while `session.model.switch` is advertised `degraded` MUST carry the key in `allow_degraded_features`. | L39–40 | `degraded_without_optin` | `modelcontrol.go:modelSwitchResponse` | `semantic.zig:modelSwitchResponse` | — |
| `0028#core-sessionmodelswitch/4` | A switch sent while `session.model.switch` is advertised `degraded` without the key in `allow_degraded_features` MUST be refused with `capability_degraded` naming the feature. | L39–40 | `degraded_without_optin` | — | — | — |
| `0028#core-sessionmodelswitch/5` | A switch MUST NOT be accepted to a model the session's bound catalog does not list. | L38–39 | `model_not_in_catalog` | `modelcontrol.go:modelSwitchResponse` | `semantic.zig:modelSwitchResponse` | — |
| `0028#core-sessionmodelswitch/6` | An accepted switch MUST be followed by a `session.state.updated` reporting the new model. | L29–31 | `session_state_mismatch` | `modelcontrol.go:closeSwitchObservations` | `semantic.zig:close` | — |
| `0028#core-sessionmodelswitch/7` | A switch refusal MUST NOT use `model_not_found` for a model the session's bound catalog lists. | L42–47 | `model_not_in_catalog` | `modelcontrol.go:settleModelControlRefusal` | — | — |
| `0028#core-sessionmodelswitch/8` | A `model_not_found` switch refusal for a model the bound catalog does not list MUST name it in `details.model_id`. | L35–37 | `model_not_in_catalog` | `modelcontrol.go:settleModelControlRefusal` | — | — |

### [0028] Optional: `session.provider.attach`

Source: [`decisions/0028-live-model-and-provider-control.md`](../../decisions/0028-live-model-and-provider-control.md#optional-sessionproviderattach).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0028#optional-sessionproviderattach/1` | An advertised `action.providers.attach` MUST disclose the `session_live` mode. | L69–70 | `undisclosed_attach_modes` | `modelcontrol.go:checkProviderAttachModes` | `semantic.zig:checkAttachModes` | — |
| `0028#optional-sessionproviderattach/2` | A `session.provider.attach.response` MUST name the requested session and, as `provider_id`, the requested alias. | L71, L100–101 | `scope_mismatch` | `modelcontrol.go:providerAttachResponse` | `semantic.zig:providerAttachResponse` | `state.rs:response` (session only) |
| `0028#optional-sessionproviderattach/3` | An attachment MUST NOT be accepted unless `action.providers.attach` is advertised with the `session_live` mode. | L69–70, L102–104; 0054 L46–53 | `unavailable_capability` | `modelcontrol.go:providerAttachResponse` | `semantic.zig:gatedResponse`, `semantic.zig:providerAttachResponse` | — |
| `0028#optional-sessionproviderattach/4` | An attachment accepted while the key is `degraded` MUST carry it in `allow_degraded_features`. | L98–100 | `degraded_without_optin` | `modelcontrol.go:providerAttachResponse` | `semantic.zig:gatedResponse` | — |
| `0028#optional-sessionproviderattach/5` | An attachment to an endpoint not advertising the key MUST be refused with `unsupported_feature`, naming `action.providers.attach` in `details.feature` and `unadvertised` in `details.reason`. | L95–98; 0054 L36–44 | `unavailable_capability` | `modelcontrol.go:settleModelControlRefusal` | `semantic.zig:settleControlRefusal` | — |
| `0028#optional-sessionproviderattach/6` | Reusing an attached alias for a different binding MUST NOT be accepted. | L92–94 | `duplicate_provider` | `modelcontrol.go:providerAttachResponse` | `semantic.zig:providerAttachResponse` | — |
| `0028#optional-sessionproviderattach/7` | An attachment MUST NOT be accepted under an alias the session's bound catalog already serves. | L91–92 | `duplicate_provider` | `modelcontrol.go:providerAttachResponse` | `semantic.zig:providerAttachResponse` | — |
| `0028#optional-sessionproviderattach/8` | An accepted attachment invalidates that session's catalog, and the next catalog that binds (citing the active revision with its descriptor in hand) MUST list the attached alias in `providers[]`; a catalog that does not bind owes nothing about the attachment. | L106–115; 0054 L55–64 | `unmatched_provider` | `models.go:modelsResponse` | `semantic.zig:modelsResponse` | — |
| `0028#optional-sessionproviderattach/9` | An attachment sent while the key is `degraded` without it in `allow_degraded_features` MUST be refused with `capability_degraded` naming the feature. | L98–100; 0054 L36–44 | `degraded_without_optin` | `modelcontrol.go:settleModelControlRefusal` | `semantic.zig:settleControlRefusal` | — |
| `0028#optional-sessionproviderattach/10` | An attachment request MUST cite a current capability descriptor: one sent before the descriptor of an announced revision is in hand is `unavailable_capability`, and its answer is judged against no descriptor. | L95–100; 0054 L36–44 | `unavailable_capability` | `modelcontrol.go:providerAttachRequest` | `semantic.zig:gatedRequest` | — |

### [0035] The entry gains facts, and the listing publishes its completeness

Sources: [entry facts](../../decisions/0035-a-model-entry-publishes-its-facts-and-absence-means-unknown.md#the-entry-gains-the-facts-a-caller-cannot-otherwise-learn), [listing](../../decisions/0035-a-model-entry-publishes-its-facts-and-absence-means-unknown.md#the-listing-publishes-its-own-completeness-and-age).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0035#the-entry-gains-the-facts-a-caller-cannot-otherwise-learn/1` | A provider model entry's `cost`, modality lists, `reasoning_levels`, `release_date` and `family` MUST take the published shapes; an empty string or list is not a published fact. | L84–93 | `schema_invalid` | schema | schema | schema |
| `0035#the-listing-publishes-its-own-completeness-and-age/1` | `provider.models.list.response.catalog`, when present, MUST carry `complete`; a partial listing changes no diagnostic. | L131–148 | `schema_invalid` | schema | schema | schema |

0035 is a provider-profile decision and adds no semantic rule: it keeps the
`+models` binding above unchanged, and `cost` is published and never judged.
