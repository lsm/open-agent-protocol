# Tool sources and provided tools

Decisions 0008, 0011 and 0048, and the tool call lifecycle sites of 0001 that
[the core file](0001-core.md) leaves here: the rows behind every diagnostic the
validators raise for tool sources, the session catalog, call attribution and
control-layer-provided tools. Go and Zig name the function in `go/validation/`
and `zig/src/validation/`, Rust in `rust/validation/src/`. Rust ports the
generic tool lifecycle and the list response's session scope, and none of the
other tool-source or provided-tool rules yet.
The items in
[`research/validator-divergences.md`](../../research/validator-divergences.md)
for this group are all settled: OPEN-0008-1 by 0008's text, and OPEN-0008-2,
OPEN-0008-3 and OPEN-0011-1 by Decision 0055. What remains is port work, named
in the Zig and Rust cells.
The 0001 rows continue that heading's numbering in the core file. Format:
[README](README.md).

### [0008] An adapter that does not attach says so

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#an-adapter-that-does-not-attach-says-so).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#an-adapter-that-does-not-attach-says-so/1` | An open carrying `tool_sources` to an endpoint that does not affirmatively advertise `action.tool_sources.attach` with the `session_open` mode MUST be refused `unsupported_feature`, `details.feature: "action.tool_sources.attach"`, `details.reason: "unadvertised"`. | L54–80, L313–403 | `unavailable_capability` | `toolsources.go:attachExpectations` | `semantic.zig:attachExpectations` | — |

### [0008] The catalog is gated on its own key

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#the-catalog-is-gated-on-its-own-key).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#the-catalog-is-gated-on-its-own-key/1` | An `action.tools.list.request` to an endpoint that does not affirmatively advertise `action.tools.list` MUST be answered `unsupported_feature` naming that key with `details.reason: "unadvertised"`; a served catalog or any other refusal is the defect. | L101–124 | `unavailable_capability` | `toolsources.go:toolsListRequest` | `semantic.zig:dispatch` (gatedRequest) | — |
| `0008#the-catalog-is-gated-on-its-own-key/2` | A list request under a `degraded` `action.tools.list` without `allow_degraded_features` naming it MUST be refused `capability_degraded`. | L127–150 | `degraded_without_optin` | `toolsources.go:toolsListRequest` | `semantic.zig:dispatch` (gatedRequest) | — |
| `0008#the-catalog-is-gated-on-its-own-key/3` | A list request an endpoint advertises, with no defect any rule names, MUST NOT be refused. | L101–124 | `unhonoured_capability` | `toolsources.go:settleToolSourceRefusal` | `semantic.zig:settleControlRefusal` | — |

### [0008] A list request is a request of its own

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#a-list-request-is-a-request-of-its-own).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#a-list-request-is-a-request-of-its-own/1` | A session-scoped catalog MUST name its session on the envelope as well as in the payload. | L127–150; 0055 L30–42 | `scope_mismatch` | `toolsources.go:toolsListResponse` | `semantic.zig:dispatch` | `state.rs:list_scope` |
| `0008#a-list-request-is-a-request-of-its-own/2` | An unscoped list request MUST be answered with the endpoint's own catalog, naming no session. | L127–150, L406–459 | `scope_mismatch` | `toolsources.go:toolsListResponse` | `semantic.zig:dispatch` | `state.rs:list_scope` |

### [0008] A catalog resolves, or it is not a catalog

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#a-catalog-resolves-or-it-is-not-a-catalog).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/1` | A capability descriptor MUST declare each tool source id once across its top level and layers. | L153–223 | `duplicate_tool_source` | `toolsources.go:checkDescriptorSources` | `semantic.zig:capabilitiesResponse` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/2` | A capability descriptor's effective catalog MUST list each tool name once. | L153–223 | `duplicate_tool_name` | `toolsources.go:checkDescriptorSources` | `semantic.zig:capabilitiesResponse` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/3` | A descriptor tool's `source` MUST name a source the descriptor declares. | L153–223 | `unmatched_tool_source` | `toolsources.go:checkDescriptorSources` | `semantic.zig:checkDeclaredToolSources` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/4` | An `action.tools.list.response` MUST declare each source id once. | L153–223 | `duplicate_tool_source` | `toolsources.go:toolsListResponse` | `semantic.zig:dispatch` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/5` | An `action.tools.list.response` MUST list each tool name once. | L153–223 | `duplicate_tool_name` | `toolsources.go:toolsListResponse` | `semantic.zig:duplicateNames` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/6` | Every tool a served catalog lists MUST name a source. | L153–223 | `unmatched_tool_source` | `toolsources.go:toolsListResponse` | `semantic.zig:checkListedToolSources` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/7` | A listed tool's `source` MUST name a source the same response declares. | L153–223 | `unmatched_tool_source` | `toolsources.go:toolsListResponse` | `semantic.zig:checkListedToolSources` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/8` | An `action.call.requested` that names a `source` MUST name the source the effective published catalog records for the tool (the session's served catalog under the active revision, else the descriptor's). | L153–223 | `unmatched_tool_source` | `toolsources.go:checkCallSource` | `semantic.zig:checkCallSource` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/9` | Where no published catalog lists the tool, a call's `source` MUST name a declared source. | L153–223 | `unmatched_tool_source` | `toolsources.go:checkCallSource` | `semantic.zig:checkCallSource` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/10` | A call from an endpoint advertising `action.tools.list`, whose published catalog attributes the tool, MUST name `source`. | L153–223 | `unattributed_call` | `toolsources.go:checkCallAttributed` | `semantic.zig:checkCallSource` | — |
| `0008#a-catalog-resolves-or-it-is-not-a-catalog/11` | A later event of a call MUST NOT name a source other than the one its `action.call.requested` named. | L153–223 | `unmatched_tool_source` | `state.go:tool` | `semantic.zig:toolTransition` | `state/interactions.rs:tool` |

### [0008] The attachment shape is not the catalog shape

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#the-attachment-shape-is-not-the-catalog-shape).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#the-attachment-shape-is-not-the-catalog-shape/1` | A published tool source (descriptor top level, each layer, list response, open response, session state) MUST NOT carry `command`, `args` or `environment`. | L226–259 | `attachment_field_in_catalog` | `toolsources.go:checkRawSources` | `semantic.zig:checkRawSources` | — |

### [0008] Attachment is bounded by what the endpoint disclosed

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#attachment-is-bounded-by-what-the-endpoint-disclosed).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#attachment-is-bounded-by-what-the-endpoint-disclosed/1` | An attachment array that violates no disclosed `max_sources` or `transports` limit, and no rule above, MUST NOT be refused. | L262–310 | `undisclosed_attach_limit` | `toolsources.go:sessionOpenRequest` | `semantic.zig:sessionOpenRequest` | — |
| `0008#attachment-is-bounded-by-what-the-endpoint-disclosed/2` | A refusal of an array exceeding `max_sources` MUST be `unsupported_feature`, `details.reason: "unsatisfiable"`, `details.source` naming the first entry past the limit. | L262–310 | `unavailable_capability` | `toolsources.go:limitViolation` | `semantic.zig:attachLimitViolation` | — |
| `0008#attachment-is-bounded-by-what-the-endpoint-disclosed/3` | A refusal of an attachment whose `kind` is outside the disclosed `transports` MUST take the same unsatisfiable shape, naming that source. | L262–310 | `unavailable_capability` | `toolsources.go:limitViolation` | `semantic.zig:attachLimitViolation` | — |

### [0008] Attachment discloses a set of modes, not one

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#attachment-discloses-a-set-of-modes-not-one).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#attachment-discloses-a-set-of-modes-not-one/1` | `action.tool_sources.attach` advertised above `unavailable` MUST disclose `session_open` in `modes`. | L313–403 | `undisclosed_attach_modes` | `toolsources.go:checkAttachModes` | `semantic.zig:checkAttachModes` | — |
| `0008#attachment-discloses-a-set-of-modes-not-one/2` | An open attaching tool sources under a `degraded` attach capability without the caller's opt-in MUST be refused `capability_degraded`. | L313–403 | `degraded_without_optin` | `toolsources.go:attachExpectations` | `semantic.zig:attachExpectations` | — |
| `0008#attachment-discloses-a-set-of-modes-not-one/3` | An open attaching a `remote` source to an endpoint whose `modes` omit `remote` MUST be refused `unsupported_feature`, `details.reason: "unsatisfiable"`, `details.source`. | L313–403 | `unavailable_capability` | `toolsources.go:attachExpectations` | `semantic.zig:attachExpectations` | — |

### [0008] Attachment is for the session's lifetime

Source: [`decisions/0008-tool-sources.md`](../../decisions/0008-tool-sources.md#attachment-is-for-the-sessions-lifetime).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0008#attachment-is-for-the-sessions-lifetime/1` | An open MUST NOT attach a source under an id the same open or the descriptor already uses. Such an open is refused unsatisfiable with `details.source`. | L153–223, L406–459 | `duplicate_tool_source` | `toolsources.go:attachExpectations` | `semantic.zig:attachExpectations` | — |
| `0008#attachment-is-for-the-sessions-lifetime/2` | A refreshed descriptor MUST NOT declare a source id an open session already attached. | L406–459 | `duplicate_tool_source` | `toolsources.go:checkDescriptorSources` | `semantic.zig:checkAttachedCollision` | — |
| `0008#attachment-is-for-the-sessions-lifetime/3` | A session-scoped catalog MUST list every source the open attached. | L406–459 | `catalog_mismatch` | `toolsources.go:toolsListResponse` | `semantic.zig:checkSessionCatalog` | — |
| `0008#attachment-is-for-the-sessions-lifetime/4` | A session-scoped catalog MUST describe an attached source exactly as the open response adopted it. | L406–459 | `catalog_mismatch` | `toolsources.go:toolsListResponse` | `semantic.zig:checkSessionCatalog` | — |
| `0008#attachment-is-for-the-sessions-lifetime/5` | An open response or snapshot MUST declare each source id once. | L406–459 | `duplicate_tool_source` | `toolsources.go:checkPublishedUnion` | `semantic.zig:checkPublishedUnion` | — |
| `0008#attachment-is-for-the-sessions-lifetime/6` | An open response or later snapshot of a session with attachments MUST report every attached source and every source the descriptor declares. | L406–459 | `session_state_mismatch` | `toolsources.go:checkPublishedUnion` | `semantic.zig:checkPublishedUnion` | — |
| `0008#attachment-is-for-the-sessions-lifetime/7` | The open response MUST agree with every member an attachment stated; it may fill members the attachment left blank. | L406–459 | `session_state_mismatch` | `toolsources.go:checkPublishedUnion` | `semantic.zig:checkPublishedUnion` | — |
| `0008#attachment-is-for-the-sessions-lifetime/8` | A later snapshot MUST describe each reported source exactly as the open response adopted it (attached) or the descriptor declares it (declared). | L406–459 | `session_state_mismatch` | `toolsources.go:checkPublishedUnion` | `semantic.zig:checkPublishedUnion` | — |
| `0008#attachment-is-for-the-sessions-lifetime/9` | A snapshot MUST NOT report a source that was neither attached nor declared. | L406–459 | `session_state_mismatch` | `toolsources.go:checkPublishedUnion` | `semantic.zig:checkPublishedUnion` | — |

### [0011] Tools are provided at session open, and the open is provisioned whole

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/1` | An open carrying `tools` to an endpoint that does not affirmatively advertise `action.tools.provide` MUST be refused `unsupported_feature`, `details.reason: "unadvertised"`. | L83–104 | `unavailable_capability` | `controltools.go:provideExpectations` | `semantic.zig:provideExpectations` | — |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/2` | An open carrying `tools` under a `degraded` provide capability without the caller's opt-in MUST be refused `capability_degraded`. | (drafted here: 0005's opt-in rule applied to 0011's key) | `degraded_without_optin` | `controltools.go:provideExpectations` | `semantic.zig:provideExpectations` | — |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/3` | An open MUST NOT supply a tool under a name the session's catalog already resolves. It is refused unsatisfiable with `details.tool`; an admission or another refusal is the defect. | L83–104, 0008 L153–223 | `duplicate_tool_name` | `controltools.go:provideExpectations` | `semantic.zig:provideExpectations` | — |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/4` | A session-scoped catalog MUST list every tool the open provided. | L83–104 | `catalog_mismatch` | `controltools.go:checkCatalogProvided` | `semantic.zig:checkSessionCatalog` | — |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/5` | A session-scoped catalog MUST describe a provided tool exactly as it was supplied. | L83–104 | `catalog_mismatch` | `controltools.go:checkCatalogProvided` | `semantic.zig:checkSessionCatalog` | — |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/6` | A refreshed descriptor MUST NOT declare a native tool under a name an open session provided. | L83–104 | `duplicate_tool_name` | `controltools.go:checkRefreshAgainstProvided` | `semantic.zig:checkRefreshAgainstProvided` | — |
| `0011#tools-are-provided-at-session-open-and-the-open-is-provisioned-whole/7` | A refreshed descriptor MUST keep declaring every source a provided tool references. | L83–104 | `unmatched_tool_source` | `controltools.go:checkRefreshAgainstProvided` | `semantic.zig:checkRefreshAgainstProvided` | — |

### [0011] Ownership is checked where it is stated, not where it fails

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#ownership-is-checked-where-it-is-stated-not-where-it-fails).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#ownership-is-checked-where-it-is-stated-not-where-it-fails/1` | Where the trace declared a control participant, a supplied tool's `execution_owner` MUST be that participant. Otherwise the open is refused unsatisfiable with `details.tool`; an admission or another refusal is the defect. | L107–138 | `wrong_tool_owner` | `controltools.go:provideExpectations` | `semantic.zig:provideExpectations` | — |
| `0011#ownership-is-checked-where-it-is-stated-not-where-it-fails/2` | A supplied tool's `source` MUST name a source the descriptor declares or the same open attaches. Otherwise the open is refused unsatisfiable with `details.source`. | L107–138 | `unmatched_tool_source` | `controltools.go:provideExpectations` | `semantic.zig:provideExpectations` | — |
| `0011#ownership-is-checked-where-it-is-stated-not-where-it-fails/3` | An `action.call.requested` MUST name the `execution_owner` the catalog in force records for the tool. | L107–138 | `wrong_tool_owner` | `controltools.go:checkCallOwner` | `semantic.zig:checkCallOwner` | — |

### [0048] 1. An endpoint that advertises `action.tools.list` requires every provided tool to name a source

Source: [`decisions/0048-a-provided-tool-names-a-source-when-tools-are-listed.md`](../../decisions/0048-a-provided-tool-names-a-source-when-tools-are-listed.md#decisions).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0048#1/1` | An endpoint advertising `action.tools.list` MUST refuse an open supplying a tool with no `source`, as `unsupported_feature`, `details.feature: "action.tools.provide"`, `details.reason: "unsatisfiable"`, `details.tool`. | L32–46 | `unmatched_tool_source` | `controltools.go:provideExpectations` | `semantic.zig:provideExpectations` | — |

### [0011] A call to a provided tool is an interaction

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#a-call-to-a-provided-tool-is-an-interaction).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#a-call-to-a-provided-tool-is-an-interaction/1` | An `action.call.requested` for a control-owned tool MUST carry `interaction_id` and `responded_by`. | L141–178 | `illegal_tool_transition` | `controltools.go:controlCallRequested` | `semantic.zig:controlCallRequested` | — |
| `0011#a-call-to-a-provided-tool-is-an-interaction/2` | A control-owned call's `interaction_id` MUST NOT reuse one already requested. | L141–178 | `duplicate_interaction` | `controltools.go:controlCallRequested` | `semantic.zig:controlCallRequested` | — |

### [0011] The resolution is a request/response pair with three arms

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#the-resolution-is-a-requestresponse-pair-with-three-arms).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#the-resolution-is-a-requestresponse-pair-with-three-arms/1` | `action.call.resolve.request` and `.response` MUST carry the same `tool_call_id` on the envelope and in the payload. | L185–191; 0055 L30–42 | `scope_mismatch` | `controltools.go:controlResolveRequest`<br>`controltools.go:controlResolveResponse` | `semantic.zig:callAgreement` | `state.rs:payload_scope` |
| `0011#the-resolution-is-a-requestresponse-pair-with-three-arms/2` | A call resolution MUST answer a tool-call interaction. | L181–207 | `unmatched_interaction` | `controltools.go:controlResolveRequest` | `semantic.zig:judgeResolve` | — |
| `0011#the-resolution-is-a-requestresponse-pair-with-three-arms/3` | A call resolution MUST name the tool call its interaction is bound to. | L185–191; 0055 L30–42 | `scope_mismatch` | `controltools.go:controlResolveRequest` | `semantic.zig:judgeResolve` | — |
| `0011#the-resolution-is-a-requestresponse-pair-with-three-arms/4` | An `already_resolved` refusal's `details.settlement_id` MUST name an envelope that settled the interaction in this trace. | L181–207, L210–288 | `unmatched_interaction` | `controltools.go:controlResolveResponse` | `semantic.zig:resolveResponse` | — |

### [0011] The refusal reasons are ranked, and the endpoint reports the highest

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#the-refusal-reasons-are-ranked-and-the-endpoint-reports-the-highest).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#the-refusal-reasons-are-ranked-and-the-endpoint-reports-the-highest/1` | A resolution of no pending interaction MUST be refused `unknown_interaction`. | L210–288 | `unmatched_interaction` | `controltools.go:controlResolveResponse` | `semantic.zig:resolveResponse` | — |
| `0011#the-refusal-reasons-are-ranked-and-the-endpoint-reports-the-highest/2` | A resolution that satisfies one of the five conditions MUST NOT be accepted. The diagnostic is the condition present. | L210–288 | `wrong_interaction_responder` / `duplicate_interaction` / `unmatched_interaction` | `controltools.go:controlResolveResponse` (resolveReasonDiagnostic) | `semantic.zig:resolveResponse` (reasonDiagnostic) | — |
| `0011#the-refusal-reasons-are-ranked-and-the-endpoint-reports-the-highest/3` | A resolution satisfying none of the five conditions MUST be accepted. | L210–288 | `unmatched_interaction` | `controltools.go:controlResolveResponse` | `semantic.zig:resolveResponse` | — |
| `0011#the-refusal-reasons-are-ranked-and-the-endpoint-reports-the-highest/4` | A refusal MUST report the highest-ranked condition the request satisfies. The diagnostic is the condition present. | L210–288 | `wrong_interaction_responder` / `duplicate_interaction` / `unmatched_interaction` | `controltools.go:controlResolveResponse` (resolveReasonDiagnostic) | `semantic.zig:resolveResponse` (reasonDiagnostic) | — |

### [0011] A terminal must derive from an accepted resolution, and carry what it said

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said/1` | `action.call.started` for a control-owned call MUST follow an accepted resolution of its interaction. | L291–318 | `illegal_tool_transition` | `controltools.go:controlCallEvent` | `semantic.zig:controlCallEvent` | — |
| `0011#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said/2` | `action.call.completed` MUST derive from an accepted `result` and `action.call.failed` from an accepted `error`. | L291–318 | `illegal_tool_transition` | `controltools.go:controlCallEvent` | `semantic.zig:controlCallEvent` | — |
| `0011#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said/3` | A completion's `result` MUST equal the accepted `result`, as canonical JSON. | L291–318 | `resolution_payload_mismatch` | `controltools.go:checkResolutionPayload` | `semantic.zig:checkResolutionPayload` | — |
| `0011#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said/4` | A failure's `error` MUST equal the accepted `error`, as canonical JSON. | L291–318 | `resolution_payload_mismatch` | `controltools.go:checkResolutionPayload` | `semantic.zig:checkResolutionPayload` | — |
| `0011#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said/5` | A resolve-derived `action.call.started` or terminal MUST carry `request_id`. | L291–318 | `unmatched_interaction` | `controltools.go:checkDerivedRequestID` | `semantic.zig:checkDerivedRequest` | — |
| `0011#a-terminal-must-derive-from-an-accepted-resolution-and-carry-what-it-said/6` | That `request_id` MUST name a resolve request the trace carries for the interaction. | L291–318 | `unmatched_interaction` | `controltools.go:checkDerivedRequestID` | `semantic.zig:checkDerivedRequest` | — |

### [0011] The capability discloses its limits, or it promises nothing

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#the-capability-discloses-its-limits-or-it-promises-nothing).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#the-capability-discloses-its-limits-or-it-promises-nothing/1` | A `tools` array that violates no disclosed `max_tools`, `name_pattern` or `schema_dialect`, and no rule above, MUST NOT be refused. | L321–346 | `undisclosed_provide_limit` | `toolsources.go:sessionOpenRequest` | `semantic.zig:sessionOpenRequest` | — |
| `0011#the-capability-discloses-its-limits-or-it-promises-nothing/2` | A refusal of a `tools` array that exceeds `max_tools`, names a tool outside `name_pattern`, or declares a `$schema` other than the disclosed dialect MUST be `unsupported_feature`, `details.reason: "unsatisfiable"`, `details.tool`. | L321–346; 0055 L74–82 | `unavailable_capability` | `controltools.go:provideLimitViolation` | `semantic.zig:provideLimitViolation`<br>`semantic.zig:namePatternViolation` | — |

### [0062] An uncompilable `name_pattern` makes the descriptor defective (OPEN-0011-3)

Source: [`decisions/0062-a-name-pattern-that-is-not-re2-is-a-defective-limit.md`](../../decisions/0062-a-name-pattern-that-is-not-re2-is-a-defective-limit.md#an-uncompilable-name_pattern-makes-the-descriptor-defective-open-0011-3).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0062#an-uncompilable-name_pattern-makes-the-descriptor-defective-open-0011-3/1` | A `capabilities.response` that discloses an `action.tools.provide` `name_pattern` that is not valid RE2 is defective. | L28–33 | `undisclosed_provide_limit` | `controls.go:checkProvideLimits` | `semantic.zig:checkProvideLimits` | — |

### [0011] Recovery reads two lists, because one cannot answer the question

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#recovery-reads-two-lists-because-one-cannot-answer-the-question).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#recovery-reads-two-lists-because-one-cannot-answer-the-question/1` | Every id in an entry's `acknowledged_interactions` MUST also be in its `pending_interactions`. | L349–378 | `session_state_mismatch` | `controltools.go:checkEntryAcknowledged` | `semantic.zig:checkEntryAcknowledged` | — |
| `0011#recovery-reads-two-lists-because-one-cannot-answer-the-question/2` | Every id in `acknowledged_interactions` MUST have an accepted `started` acknowledgement the trace carries. | L349–378 | `session_state_mismatch` | `controltools.go:checkEntryAcknowledged` | `semantic.zig:checkEntryAcknowledged` | — |

### [0011] A recovered call is judged on what the trace can still see

Source: [`decisions/0011-control-layer-provided-tools.md`](../../decisions/0011-control-layer-provided-tools.md#a-recovered-call-is-judged-on-what-the-trace-can-still-see).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0011#a-recovered-call-is-judged-on-what-the-trace-can-still-see/1` | An entry MUST list a pending interaction whose `started` acknowledgement this trace saw accepted, including one accepted after a reattach. | L349–378, L381–408 | `session_state_mismatch` | `controltools.go:checkEntryAcknowledged` | `semantic.zig:checkEntryAcknowledged` | — |

### [0001] One foreground invocation is one run

Source: [`decisions/0001-agent-control-v0.1-executable-core.md`](../../decisions/0001-agent-control-v0.1-executable-core.md#one-foreground-invocation-is-one-run).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0001#one-foreground-invocation-is-one-run/5` | A tool call MUST follow `requested`, then `started`, then `progress` any number of times, then one of `completed` or `failed`. `cancelled` is allowed from any state that is not terminal. | L51–68 (drafted here as the transition table) | `illegal_tool_transition` | `state.go:tool` | `semantic.zig:toolTransition` | `state/interactions.rs:tool` |
| `0001#one-foreground-invocation-is-one-run/6` | A tool event other than `requested` MUST name a call the run requested, unless the run was recovered. | L51–68, L121–131 (drafted here: a call's events follow the call the run requested, except where recovery left the request behind the cursor) | `unmatched_tool` | `state.go:tool` | `semantic.zig:toolTransition` | `state/interactions.rs:tool` |
| `0001#one-foreground-invocation-is-one-run/7` | A call's `execution_owner` MUST NOT change during its lifecycle. | 0011 L107–138 | `scope_mismatch` | `state.go:tool` | `semantic.zig:toolTransition` | `state/interactions.rs:tool` |

### [0001] Correlated reverse interactions

Source: [`decisions/0001-agent-control-v0.1-executable-core.md`](../../decisions/0001-agent-control-v0.1-executable-core.md#correlated-reverse-interactions).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0001#correlated-reverse-interactions/5` | A run MUST NOT reach its terminal with a tool call that has no terminal. | L83–102, 0011 L141–178 | `pending_tool_at_terminal` | `state.go:runEvent` | `semantic.zig:checkPendingAtTerminal` | `state/interactions.rs:pending_at_terminal` |
| `0001#correlated-reverse-interactions/6` | A run MUST NOT reach its terminal with an interaction pending. | L83–102 | `pending_interaction_at_terminal` | `state.go:runEvent` | `semantic.zig:checkPendingAtTerminal` | `state/interactions.rs:pending_at_terminal` |
| `0001#correlated-reverse-interactions/7` | A permission or user-input resolution MUST NOT name, on the envelope or, where carried, in the payload, a `tool_call_id` other than the one its request was bound to. | L83–102; 0055 L30–42 | `scope_mismatch` | `state.go:interactionResolved` | `semantic.zig:interactionResolved` | `state/interactions.rs:interaction_resolved` |

## Port work

- Rust lacks every tool-source check and the call resolution's binding check
  (OPEN-0008-2, 0055).
- Rust owes the `name_pattern` check (OPEN-0011-1, 0055, 0062).
