# The Hub: Many Sessions on One Wire

Status: proposed design; the specification both trees are judged against
Date: 2026-09-26
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Reference implementation: `goap hub` (`go/serve`), the only hub that exists
Written for: `oapx hub` (Zig), which does not exist yet and is the reason this
document does
Decides: the wire the Go hub serves today by behaviour
Governs: the transports, not the profile

Follows [Decision 0032](../decisions/0032-go-and-zig-are-peers.md): where this
document and either tree disagree, this document decides, and the wrong side
is fixed or the divergence recorded in [Divergences](#divergences-from-the-go-tree).
It is composed from
[Decision 0009](../decisions/0009-compound-open.md) (compound open),
[Decision 0026](../decisions/0026-a-resume-cursor-may-name-its-run.md) (a cursor
may name its run), [Decision 0008](../decisions/0008-tool-sources.md) (tool
sources), [Decision 0006](../decisions/0006-models-catalog.md) (the models
catalog) and [Decision 0020](../decisions/0020-error-codes-are-declared.md)
(error codes are declared).

## Why this document exists

The hub's wire is defined by three things and no fourth: `go/serve/servestdio`
and `go/serve/servehttp`, the README's daemon sections, and `clients/ts`. A Zig
port has nothing else to be judged against, and
[Decision 0032](../decisions/0032-go-and-zig-are-peers.md) put an end to "match
Go" as a rule. So the wire is written down here first, and both trees are held
to it.

Every rule below names the Go test that pins it today. A rule with no pinning
test says **none** — those are the places a port can drift without either tree
noticing, and each carries a numbered entry in [Known gaps](#known-gaps).

## What the hub is

One long-running local process that runs many agent sessions at once across any
mix of harness backends, and answers clients on two transports:

- **HTTP + Server-Sent Events**, for any client, and
- **a stdio pipe of transport objects**, for a host that would rather spawn a
  child than manage a port.

Both are codecs over one transport-neutral core: a **registry** of adapters, a
**session** per open, **fan-out** from each run's stream to every subscriber,
and **cursor replay** for a client that reconnects. The core adds no protocol
semantics of its own and never validates an envelope; a transport validates
forwarded input against the bundled `schema/v0.1` before acting on it, exactly
as `servehttp` does.

The hub is a layer above an [endpoint](endpoint-stdio.md). An endpoint is one
agent loop carrying raw OAP envelopes; the hub is a registry of many, carrying
those envelopes *inside* transport objects with their own correlation. A hub
wire is not an endpoint wire and an implementer should not build one expecting
the other.

## The trust model

The hub is a single-user local service. These rules are the whole of its
security posture, and a port carries all of them or is not conformant.

| rule | detail | pinned by |
| --- | --- | --- |
| **Loopback bind by default** | The HTTP daemon binds `127.0.0.1:6270`. Pointing it at an external interface is explicitly unsupported and opts out of the single-user model. | `TestServeDefaultAddrIsLoopback`. Zig: `oapx hub` with no transport flag binds `127.0.0.1:6270`, pinned by `the default bind is loopback, so every default user keeps the allowlist`; `the loopback allowlist is the three loopback spellings and nothing else` pins which binds are loopback at all, `a bind is read as a host and a port, and a malformed one says which part it is` pins every `--addr` refusal, and `the banner names the bound address, IPv4 plainly and IPv6 bracketed` pins what the banner prints |
| **No authentication** | There is no credential, token or session cookie on either transport. Over stdio, spawning the process *is* the authorization. | structural: no auth code path exists on either transport, which is how the rule is kept — a test asserting an absence would only say the tree had not grown one yet |
| **`Host` allowlisted on a loopback bind** | When the bind address names `localhost`, `127.0.0.1` or `::1`, only requests whose `Host` header names one of those three are served; anything else is refused `403`. Comparison is case-insensitive and the port is stripped, and **a bracketed host has to carry a port**, which is what Go's split produces: `Host: [::1]` with no port is refused, and so is one that opens a bracket it never closes. A non-loopback bind has no allowlist. | `TestHostAllowlist`, `TestLoopbackHosts`. Zig: `a Host header is compared without its port and without case` and `a bind that is not loopback has no allowlist, so nothing is refused`, against a real listener — refused as an `error.response` carrying `unrecognized_host`, which is what `D1` fixed in Go |
| **`Origin` refused on every route** | A request carrying any `Origin` header is refused `403 cross_origin_request`. The check wraps the whole mux rather than living in the routes that read a body, so it covers `close` and every route not yet written. | `TestEveryRouteRefusesABrowserOrigin`, `TestReadRequestRefusesBrowserOrigins`, `TestTheOriginBoundaryHoldsWithoutAHostAllowlist`. Zig: `a request carrying both an Origin and a foreign Host is refused the Origin first` and `the daemon answers over a real socket, and the bytes say which refusal it was` — both over a real listener, so the order and the bytes on the wire are pinned rather than asserted |
| **`environment` is an allowlist** | A child process inherits nothing ambient. An adapter entry's `environment` names the variables forwarded: a bare `NAME` forwards the daemon's own value (an unset name is omitted), `NAME=value` passes through literally. A tool source's `environment` takes the same form with one stricter rule — a bare `NAME` the daemon does not carry fails at startup, naming the source and the variable, because that entry is the credential list of one executable the daemon itself launches. | `TestResolveEnvironment`, `TestLoadRegistryToolSourceNeedsEveryNameItLists`, `TestCallerEnvironmentNeverNamesAVariableTwice`. Zig: the transport builds the child's environment from the list alone rather than from its own, pinned by `the child sees exactly the environment it was given and nothing ambient`, and the hub's registry hands each adapter exactly what the document resolved, pinned by `the hub's registry builds every entry a document names, and a child inherits only the variables its entry lists` |
| **A restart ends every live session** | Run children are per-session and no adapter survives the process, so a restart ends every harness process. Under [Decision 0039](../decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md) a session outlives its process and close releases it, so no entry accumulates; reopening one is staged as T7, and until it lands a client that reconnects to a restarted hub finds no session. | The restart: **none** — a restart ending every live session has no pinning test. `TestServeShutdownSweepIsNotAbandoned` is the closest, but as the sweep row below measures, it stays green when `hub.CloseSessions` is never invoked, so no test observes a session reaching a closed state (see that row for the full limit). The release: none yet — `TestHubSessionCloseSemantics` and `TestSessionsListingAcrossLifecycle` still pin the kept entry, which D2 records. |
| **A binding record holds no credential** | `--session-history <path>` appends one record per open: the session id, the adapter and its pin, the home, the working directory the adapter entry was configured with (omitted when the hub does not know one — never the daemon's own), the model, and the tool source ids. It never records the request's environment, a credential, or a resolved environment value — a record of what was asked must not become a copy of what was secret — and the file is created `0o600`. History is appended rather than replaced, and a torn tail is truncated on the next write or the next open, keeping the longest prefix of records that decode, so one crash cannot leave the log permanently unreadable | `TestAnOpenIsRecordedAsABindingAndNothingSensitiveIs` (opens a session and asserts the written bytes carry neither), `TestTheStoreFileIsNotWorldReadable` (the mode), `TestATornTailIsTruncatedSoTheStoreKeepsWorking`, `TestATornTailIsTruncatedWhenTheStoreIsOpenedAgain`, `TestACorruptLineThatKeepsItsNewlineIsTruncatedToo` and `TestACorruptLineIsTruncatedWhenTheStoreIsOpenedAgain` (the repair), `TestACloseAndADuplicateOpenCannotInterleaveTheirRecords` (a close never lands between an open and its registration), and `TestARolledBackOpenIsRecordedAsOpenedAndThenClosed` and `TestARefusedDuplicateOpenIsRecordedAsARefusalAndNothingElse` and `TestTheRecordsDirectoryIsTheAdaptersOwnAndIsOmittedWhenUnknown` (what a record claims) |
| **No payload or environment logging** | The hub, its codecs and its clients never log envelope payloads or resolved environment values. | `TestDaemonOutputNeverCarriesEnvironmentValues` (environment values, through the listing and a load failure); no test pins the payload half |

## The registry

`--config` names one JSON document, the shape `examples/oap-serve.json` shows.
It is decoded **strictly**: an unknown member is refused, a member whose name
differs from the documented spelling only by case is refused, a duplicated
member is refused, and the refusal names the first unknown member in sorted
order so it does not depend on map iteration. Trailing data after the object is
refused.

`oapx hub` reads the same document through the same decoder, so the two trees
judge one document the same way. Without `--config` the hub serves the built-in
memory reference adapter alone; with it, every entry is constructed at startup
and an entry whose own requirements are not met is refused before the hub
serves anything, named by its entry name.

```
{ "adapters": { "<name>": { ... } }, "tool_sources": { "<id>": { ... } } }
```

An adapter entry takes exactly these members: `type`, `executable`, `args`,
`environment`, `working_directory`, `model`, `journal_capacity`,
`allowed_tools`, `unrestricted_tools`, `approval_policy`, `sandbox`,
`provider`, `max_tokens`, `agent_config`, `system_prompt`, `endpoint`, `agent`.
`type` defaults to the entry's own name, so a registry of one memory adapter
needs nothing but its name. Without `--config` the hub serves the built-in
memory reference adapter alone.

An entry of type **`oapx`** serves oapx's own agent loop, built the way
`oapx serve agent --backend oapx` builds it: the providers, credentials and
model catalog are the ones `~/.oapx` holds, shared by every `oapx` entry in the
document, and each session gets its own runtime. A session's settings come from
its open's `metadata.oapx`, the keys `oapx tui` sends — `thinking_level`,
`context_window`, `output`, `permission_mode`, `workspace_root` and
`user_input` — plus `model`, a `provider/api@id` reference as the session's
model catalog prints it, so two sessions on one hub can run different providers
and models. An open naming a model the catalog lacks is refused
`model_not_found` with the `model_id` it named. The model is fixed for the
session's life over the hub: the adapter does not advertise
`run.model_selection`, so a submit carrying `model_id` is refused
`unsupported_feature`, and the hub has no op for `session.model.switch`.
This type is Zig's alone: Go has no counterpart adapter, so `goap hub` refuses
the entry as an unknown type, and `examples/oap-serve.json` does not carry one
because `goap hub` reads that file too (D27).

A tool source entry takes exactly `kind`, `display_name`, `protocol`,
`endpoint`, `command`, `args`, `environment`. A `process` entry must carry a
`command`; a `kind` outside the protocol's five is refused; an entry naming one
environment variable twice is refused.

| rule | pinned by |
| --- | --- |
| Unknown adapter member refused, deterministically named | `TestLoadRegistryNamesOneUnknownMemberDeterministically`; Zig: `the first unknown field named is the first in sorted order, wherever the object is` |
| Case-variant member refused | `TestLoadRegistryRefusesCaseVariantMembers`; Zig: `an unknown field is refused wherever it appears, naming the field and where` |
| Duplicated member refused | `TestLoadRegistryRefusesDuplicateMembers`; Zig: `the file must be one JSON object, once, with no key twice; a null file is empty` |
| `type` defaults to the entry name | `TestLoadRegistryDefaultsTypeToEntryName` |
| Adapters load in sorted name order | `TestLoadRegistrySortedNames` |
| Without a config, the built-in memory adapter is served | `TestDefaultRegistry`, `TestLoadRegistryMemory` |
| Every adapter constructor is reached, and its requirements surface at startup | `TestLoadRegistryProcessAdapters`, `TestLoadRegistryConstructorErrors`; Zig: `the hub's registry refuses an entry of a type it does not know, naming the entry and the type` and `the hub's registry reports a known adapter's own requirement once, not as an unknown type` |
| **Each entry gets its own adapter instance**, so a document naming two entries of one type serves two adapters rather than one of them twice | Zig: `two entries of one type are two adapters, each with its own executable`. Go's `buildAdapter` returns a fresh adapter per entry, so no Go test names this and the two trees are here by the same rule rather than by a shared test |
| A refusal from the loader names the entry it came from | Go: `goap hub` refuses at decode, naming the adapter. Zig: `Hub.load` writes into a `config.Diagnostic` before answering `ConfigRefused`, so the operator is told which entry — pinned by the negative half of `the registry takes the first journal capacity an entry names, a zero keeps the default, and a negative is refused` |
| A document that is not one JSON object is refused | `TestLoadRegistryDocumentErrors` |
| A tool source loads into the registry and is attachable by id | `TestLoadRegistryToolSources` |
| A tool source needs a kind; a `process` one needs a command | `TestLoadRegistryToolSourceNeedsKind`, `TestLoadRegistryProcessToolSourceNeedsCommand` |
| A programmatically registered tool source is judged as the loader would judge it | `TestRegisterToolSourceJudgesTheEntryTheLoaderWouldHaveJudged` |
| Registering a name twice is refused | `TestRegistryRegister` |

### Tool sources on the wire

A client attaches a tool source **by `id` and nothing else**, under
[Decision 0008](../decisions/0008-tool-sources.md). The wire form of an
attachment is not a way to configure the daemon:

| rule | pinned by |
| --- | --- |
| A wire-supplied `command` or `args` is refused `unsupported_feature`; name an operator-configured source by id | `TestDaemonRefusesWireSuppliedProcessCredentials` |
| A `NAME=value` literal in a wire attachment's `environment` is refused; only the bare `NAME` allowlist form is accepted | same test, and `TestCallerEnvironmentNeverNamesAVariableTwice` |
| A wire-supplied `kind`, `display_name`, `protocol` or `endpoint` on a **configured** source is refused; the operator's value stands | `TestOpenRefusesAWireSuppliedKind`, `TestOpenRefusesAWireSuppliedDescriptorMember` |
| An id no source is configured under is refused **only when it claims `process`** — the one kind the daemon would have to supply an executable for. Any other kind passes through to the adapter, which may accept it | `TestOpenRefusesAnUnconfiguredIDWhateverItsKind` — the name reads broader than the rule, and its body pins the opposite for `local`: an unconfigured `local` attachment opens `200` |
| A configured source is admitted by its bare id, and the daemon supplies the command the registry holds | `TestDaemonFillsTheRegistrysCommand`, `TestOpenOpMatchesHTTP` |
| The caller's `environment` names extend the operator's list and never replace an entry | `TestCallerEnvironmentNeverNamesAVariableTwice` |
| The attachment gate reads a *layered* disclosure, so a request may be refused a level the descriptor advertises underneath the top one | `TestOpenReadsAttachmentSupportFromALayer`, `TestSharedGateRefusesADisclosureAnOpenCannotElect` |
| The capability rung is answered before the open's own constraints | `TestOpenAnswersTheCapabilityRungBeforeItsOwnConstraint` |
| An adapter's own attachment refusal is relayed, not restated | `TestOpenRelaysTheAdaptersAttachmentRefusal` |
| A degraded attachment refused without the opt-in is relayed | `TestOpenRelaysADegradedAttachRefusal` |
| Only what the request cites is attached; a stale citation is refused with both revisions named, and an open citing none is admitted | `TestAttachingOpenPinsOnlyWhatItCites` |
| A probe the daemon could not read is reported as one | `TestOpenReportsAProbeItCouldNotRead` |
| Every advertising adapter admits tool sources; every unadvertising one refuses | `TestEveryAdapterRefusesUnadvertisedToolSources`, `TestAdvertisingAdaptersAdmitToolSources` |

## The transports

Both transports are the same hub. They differ in framing, in what a refusal
looks like, and in which bounds apply. Where a rule is one rule, it is stated
once and both transports obey it; the tables below say which shapes differ.

### What is shared

- **OAP operations exchange verbatim `schema/v0.1` envelopes.** A request
  envelope arrives exactly as a body would and is validated against the same
  bundled schema before the hub acts on it. Schema validity is judged *before*
  the control gate, on both transports.
  Pinned: `TestSchemaValidityPrecedesTheControlGate` (both).
- **The daemon is participant `user`.** An interactive gate opened over a
  session resolves with `responded_by: "user"`. Pinned: the golden transcript,
  `TestGoldenSessionTranscript`, which is the one place the whole op set's
  answer shapes are pinned line for line. `examples/oap-stdio-session.ndjson` is
  the host's half of a full session — listings, open, subscription, an
  interactive run with both gates answered, cursor replay, close — and
  `TestStdioExampleSessionRuns` drives it against the built binary, so it cannot
  drift from the surface it documents.
- **A session-scoped request may not address another session.** A payload
  naming a different `session_id` than the one addressed is refused
  `scope_mismatch`, on both the submit and the cancel path, and on the model and
  tool listings. Pinned: `TestOpErrorCodesMirrorHTTP`, `TestSubmitRejections`,
  `TestCancelRejections`, `TestModelsRouteRefusesAMisscopedCatalog`,
  `TestHubRefusesAMisScopedToolCatalog`.
- **A response to a request that carries no envelope mints its own correlation
  id.** `state`, `tools`, `models` and `capabilities` have no request envelope
  to reply to, so the hub writes `in_reply_to: "oap-request-N"`. A client
  correlates those on its own request id, not on `in_reply_to`.
  Pinned: `TestStateEndpoint`, `TestToolsRouteServesTheSessionCatalog`,
  `TestModelsRouteStampsTheListersRevision`, `TestCapabilitiesEndpoint`.
- **A listing is complete, sorted, and never partial.** Both listings answer
  every entry, sorted by name or by id, and an adapter whose probe fails is
  listed with an `error` member rather than failing the whole listing.
  Pinned: `TestAdaptersOpListsRegistry`, `TestAdaptersListing`,
  `TestSessionsOpListsTrackedSessions`, `TestSessionsListingAcrossLifecycle`,
  `TestListingsMatchHTTP` (the two listings are byte-equal modulo
  `created_at`).
- **A served catalog is checked before it reaches a codec.** A tool or model
  catalog must carry the `capability_revision` it was served under and must be
  scoped to the session the request addressed, in both directions; a mis-scoped
  catalog is refused rather than relabelled, and an absent tool list is
  repaired to an empty one, because only one of those is legal on the wire.
  Pinned: `TestHubRefusesACatalogItCannotBindToADescriptor`,
  `TestHubRefusesAMisScopedToolCatalog`, `TestHubRepairsAnAbsentToolList`,
  `TestModelsRouteRefusesAnUnlabelledCatalog`,
  `TestModelsRouteRefusesAMisscopedCatalog`,
  `TestModelsRouteServesAnEmptyCatalogAsAnEmptyList`,
  `TestModelsRouteStampsTheListersRevision`.

### The stdio transport-object wire

One JSON object per line, UTF-8, `\n`-terminated. **Stdout carries protocol
lines and nothing else**: the banner, the adapter list and every diagnostic go
to stderr, so a host may parse stdout strictly.

A request line is `{"id":N,"op":"...",...}`. `id` is a **signed 64-bit integer**
that correlates the answer; the host picks the value, negatives are fine, and a
fractional number or one outside the int64 range is a framing defect rather than
a bad request. So is an unknown field, a repeated key, a key that differs from
its exact protocol spelling, a missing `id`, a missing `op`, an empty line, a
line carrying a carriage return, a line that is not UTF-8, an unterminated
final line, a line over the frame limit, and trailing data after the object.
So is a parameter whose **declared type** the line does not carry: a number for
`adapter`, a list for `session_id`, a string for `allow_degraded_features`. The
daemon **fails closed** on all of them: it stops serving and reports a
framing defect naming the line.

A parameter that is present and **null** is not among them. A null member counts
as supplied, so an op that does not define that parameter refuses it
`invalid_request` on presence, and an op that does define it treats it as absent.
The distinction is the type, not the presence: `null` is a value every parameter
admits, and a number where a string belongs is a line the daemon cannot read.

The **frame limit bounds the payload**, not the line: the terminating `\n` is
framing and does not count, so a line whose payload is exactly the limit is
served and one byte more is a defect. A host computes its own line against the
same number.

The eight members a request line may carry are `id`, `op`, `adapter`,
`session_id`, `run_id`, `after`, `request` and `allow_degraded_features`. An op
that takes a parameter it does not define is refused `invalid_request`, and the
check is on **presence**, so a supplied-but-null parameter still counts as
supplied.

| framing rule | pinned by |
| --- | --- |
| A malformed line stops serving, naming the line | `TestMalformedLinesFailClosed` |
| An oversized line stops serving | `TestOversizedLineFailsClosed` |
| A frame limit below 256 bytes is refused at construction | `TestFrameLimitFloorRejectsUnusableLimits` |
| Every ending a host could not otherwise observe still fits that floor | `TestEveryEndingFitsTheFrameLimitFloor` |
| A form too large to frame is shed, never dropped | `TestAnEndingTooLargeToFrameStillArrives`, `TestAJoinPointTooLargeToFrameFallsBackRatherThanEndingTheSubscription`, `TestATypedRefusalShedsItsDetailsRatherThanItsCode` |
| An error message is bounded, so a refusal always frames | `TestErrorMessagesAreBounded`, `TestOpenRefusalsAreBounded` |
| A response that will not fit is reduced, then reduced again, then `response_too_large` | `TestOversizedOutputRefused` |
| An idle pipe is interruptible | `TestContextEndInterruptsIdleInput` |
| A failed write ends serving | `TestWriterFailureEndsServing` |
| An unknown `op` is refused `unknown_op` — a correlated request error, not a framing defect, so the frontend keeps serving | `TestUnknownOpIsARequestError`, `TestOpErrorCodesMirrorHTTP` |
| Exactly one writer emits every line, so no line is broken by interleaving | structural: a single writer goroutine; `TestWriterDrainsQueuedLinesOnStop`, `TestWriterNeverClosesTheLineChannel` |
| A read failure that is not a framing defect is reported as itself | `TestReadFailureIsNotAMalformedLine`, `TestPartialReadFailurePassesThrough` |

A response line is `{"id":N,"ok":true,"result":...}` or
`{"id":N,"ok":false,"error":{"code":...,"message":...,"details":...}}`. `result`
is the operation's answer; a failure writes `result: null` and an `error`.

A subscription line carries `"event"` instead of `"ok"`, and every line of a
subscription is tagged with the `events` request's `id`, so overlapping
subscriptions on one session stay attributable.

| subscription line | meaning |
| --- | --- |
| `"event":"oap-subscribed"` | the subscription joined a run already in progress; `joined_after` is the last sequence of that run it missed |
| `"event":"envelope"` | one event, with `sequence` repeated outside the envelope so a host can resume without decoding it |
| `"event":"oap-overflow"` | the consumer fell behind; `last_sequence` is where a cursor resumes, and `run_id` and `session_id` say which — `TestEventsSignalAnOverflowWithItsRunAndCursor` |
| `"event":"oap-replay-gap"` | the `after` cursor is no longer retained; `oldest_available` and `latest_available` bound what is |
| `"event":"oap-session-closed"` | the session closed under the subscription, named by `session_id` — `TestEventsSignalAClosedSessionByName` |
| `"event":"oap-frame-limit"` | an envelope this framing cannot carry; `sequence` is where a fresh cursor resumes past it, with the `run_id` it stopped in — `TestTheFrameLimitSignalCarriesItsRunAndSequence` |
| `"event":"oap-stream-failed"` | the run's event stream failed; `run_id` and `sequence` are the last position delivered |

`envelope` and `oap-subscribed` are the only two that do not end the
subscription. The other five each end it, and each is a named line because this
framing has no end to observe: the SSE body simply stops, while the pipe here
stays open and carries every other subscription.

Two endings need no line. A healthy stream ends at the run's terminal envelope
— `run.completed`, `run.failed` or `run.cancelled` — delivered as an ordinary
envelope line, which is the marker, exactly as the SSE response ends after it.
And the daemon's own shutdown ends the session the host is watching, which the
host observes directly.

| subscription rule | pinned by |
| --- | --- |
| The acknowledgement is always written before the first envelope | `TestEventsAcknowledgementPrecedesTheStream`, `TestTheSubscribedSignalPrecedesEveryEnvelope` |
| The acknowledgement is `result: null`, matching the SSE route's bodyless response | `TestEventsOpDeliversTheRunStream`, `TestSubscribedSignalMatchesHTTP` |
| `oap-subscribed` is written first, and only when the run had already emitted | `TestEventsReportsWhereALateSubscriptionJoined`, `TestSubscribedSignalMatchesHTTP` |
| A subscription that begins at the start of its run gets no join signal | `TestEventsReportsNoJoinPointWhenNothingPrecededTheSubscription`, `TestSubscribingBetweenRunsReportsNoJoinPoint` |
| The join signal is advisory: it never ends the subscription | `TestAJoinPointTooLargeToFrameFallsBackRatherThanEndingTheSubscription`; the `oap-frame-limit` ending it also drives has its members pinned by `TestTheFrameLimitSignalCarriesItsRunAndSequence` |
| Overflow is signalled with the cursor to resume from | the line's shape by `TestEveryEndingFitsTheFrameLimitFloor`; the end-to-end path at the core by `TestHubSubscriptionQueueOverflow`; read off the stdio wire by `TestEventsSignalAnOverflowWithItsRunAndCursor`, which pins `session_id`, `run_id` and the resume cursor against the last sequence delivered |
| A failed run stream ends the subscription out loud | `TestAFailedRunStreamEndsTheSubscriptionOutLoud` |
| An unencodable envelope ends the subscription out loud | `TestAnUnencodableEnvelopeEndsTheSubscriptionOutLoud` |
| A context failure is still announced | `TestAnAdapterContextFailureIsStillAnnounced` |
| A replay gap is reported with the cursor that was asked for | `TestEventsOpReportsAReplayGap`, `TestAFailedResumeReportsTheRequestedCursor` |
| The session closing under a subscription is signalled | `TestHubCloseReplaySubscriptionEndsPromptly`, `TestEventsSignalAClosedSessionByName` (the stdio line, with the session and message it carries) |
| A live subscription does not stall shutdown | `TestALiveSubscriptionDoesNotStallShutdown` |
| The cursor advances monotonically, holding its high-water mark across a run boundary | `TestAdvanceCursorHoldsItsHighWaterMark` |

### Bounds on the stdio pipe

A pipe gives no back pressure, so the stdio frontend bounds everything a socket
would bound by refusing. Each bound is a **correlated `busy` refusal**, not a
stall, because waiting is what would stop the daemon reading the host's end at
all. The message ends with `send this request again`, and that is the whole
recovery: the request was never served, so resending it is safe.

**The in-flight bounds apply to every op, not to a few.** Admission is decided
as each line is decoded, before the op is even looked at, so any op past the
16-op or 16-MiB ceiling is refused `busy` — `adapters`, `sessions` and
`capabilities` included. The subscription ceiling is the one bound only `events`
and a subscribing `open` can reach.

| bound | value | pinned by |
| --- | --- | --- |
| Concurrent ops in flight | 16 | `TestInFlightOpsAreBounded` |
| Request bytes held in flight | 16 MiB, with one oversize request admitted so it can be refused | `TestAdmissionBudgetsRequestBytes`, `TestAdmissionAdmitsOneOversizeRequest` |
| Concurrent subscriptions | 64 | `TestSubscriptionsAreBounded` |
| Queued output lines before the writer is abandoned | 256 | `TestRefusalHeldThroughAFullQueueStillArrives` |
| Frame limit | 16 MiB envelope + 2 MiB wrapper, floor 256 | `TestOversizedLineFailsClosed`, `TestFrameLimitFloorRejectsUnusableLimits` |
| Per-subscription mailbox | 64 envelopes | `TestHubSubscriptionQueueOverflow`, `TestHubSubscriberQueueOverflow` |
| Bounded shutdown | 5 s per stage | `TestShutdownBoundedWhileWorkerStuck`, `TestShutdownDoesNotWaitOnStalledOutput`, `TestTeardownSettlesAdmittedWork` |

| bound rule | pinned by |
| --- | --- |
| Subscriptions are not charged against the in-flight op bound, because a subscription is not an op that finishes | `TestSubscriptionsAreNotChargedAgainstTheInFlightBound` |
| A refusal names the bound that refused it | `TestRefusalNamesTheBoundThatRefused`, `TestRefusedRequestNamesItself` |
| A host that pipelines past the bound is answered, not stalled | `TestSlowWorkersBehindTheBoundAreAnswered` |
| The in-flight bounds refuse **any** op, since admission is decided before the op is read | `TestRefusedRequestNamesItself` — it parks a `capabilities` op at the ceiling and asserts the `adapters` line behind it is refused `busy`; the bound's mechanism by `TestInFlightOpsAreBounded` |
| A refusal that cannot be written is reported as dropped, not swallowed | `TestQueuedRefusalWithdrawnUnwrittenIsReported`, `TestMalformedLineSurvivesASaturatedBound` |
| A saturated bound does not hide a framing defect behind it | `TestBufferedDefectIsJudgedBehindASaturatedBound` |
| Admission closes at teardown, and a request arriving after it is dropped unserved | `TestAdmissionClosesAtTeardown`, `TestDisconnectIsObservedWhileAdmissionIsFull` |
| The frontend serves again after a teardown | `TestServerServesAgainAfterTeardown` |
| An abandoned writer carries nothing further | `TestWriterAbandonedCarriesNothingMore`, `TestStalledDrainCarriesNothingLater` |
| A cancelled request emits no size refusal | `TestCancelledRespondEmitsNoSizeRefusal` |
| The host's end is observed at any pipeline depth | `TestHostsEndIsObservedAtAnyPipelineDepth`, `TestHostsEndIsObservedBehindABlockedOutput` |
| A stall and a malformed line both survive, and both are reported | `TestStallAndMalformedLineBothSurvive` |
| Every fact about a bad teardown stays findable in the returned error | `TestNoteKeepsEveryFactFindable`, `TestAbandonedTeardownStillReportsTheWriteFailure` |

### Shutdown and exit on stdio

Shutdown is stdin EOF or SIGINT/SIGTERM. The daemon stops admitting, settles
the work it already admitted inside its bounded window, closes every session so
child agent processes are not orphaned, and exits zero. The session sweep runs
on every exit, including the failures below, so a child is never orphaned by
one.

`--session-history <path>` names the session history: a file the hub appends
one binding record to per open, reopen and close. `oapx hub` keeps it at
`~/.oapx/sessions.jsonl` when the flag is absent, beside the rest of its state,
and an empty `--session-history=` turns it off. `oapx hub` holds an exclusive
lock on `<path>.lock` while it runs, so a second hub pointed at the same history
is refused and told to name its own, rather than both rewriting the file and
dropping each other's records — Zig: `a second store on one session history is
refused while the first holds it, and taken once it closes`. `goap hub` has no
default, so without the flag it records nothing and reports no binding. It is the host's file and the host's decision
where it lives, and `Decision 0040` is what a record must say — which harness
ran which session, under which pin, in which home and directory, with which
model, reasoning level and compaction policy — and what it must never hold. A reopen reads it: when the hub holds no
record for the session in memory (it restarted), the latest entry in the file
names the adapter and the native session id handed back to it. Both hubs take
the flag (`goap hub` and `oapx hub`) and write the same line, a CRC-32 of the
JSON entry and the entry, so either hub reads the other's file. `oapx` rewrites
the file atomically on every append, so its own writes are never torn, and
refuses a reopen when the file holds a record that does not check out rather
than reading past it. `oapx` records the adapter's configured working directory but not
the home or the tool source ids `goap` also writes, and a write it could not
make is reported by the next reopen that finds no binding instead of passing
as an unknown session.

`--stdio` and `--addr` name two transports and are mutually exclusive, and the
hub takes no positional argument. Both are usage errors, refused before
anything is served.

A non-zero exit means the session did not end cleanly, and stderr carries one
bounded diagnostic saying which:

| exit reason | means | pinned by |
| --- | --- | --- |
| a malformed line | the host's framing defect, naming the line | `TestMalformedLinesFailClosed` |
| the output could not be written | the host closed its end of stdout, or the write failed | `TestWriterFailureEndsServing` |
| requests were dropped unserved | the host ended the session while work it had sent was still unadmitted or unanswered; those requests were never served and may be sent again | `TestQueuedRefusalWithdrawnUnwrittenIsReported`, `TestAbandonedWorkersLeaveNoWriterBehind` |
| shutdown stalled | a stage outlived its bounded window and was abandoned rather than waited on | `TestShutdownBoundedWhileWorkerStuck` |

A host that classifies every non-zero exit as bad input misdiagnoses its own
closed pipe, or an adapter that outlived the shutdown window, as a protocol
error. That is stated because the four reasons are the whole set.

| exit rule | pinned by |
| --- | --- |
| A host that hangs up exits cleanly, with no signal needed | `TestHungUpHostExitsWithoutASignal` |
| The shutdown window opens at the disconnect, not at the last answer | `TestShutdownWindowOpensAtDisconnect` |
| A write that parks forever is abandoned rather than waited on | `TestTeardownStopsWhenAWriteParksForever`, `TestShutdownDoesNotWaitOnStalledOutput` |
| A cancellation returns even once output has stopped | `TestCancellationReturnsDespiteStoppedOutput` |
| A writer that failed keeps draining rather than stalling | `TestWriterRemembersFailureAndKeepsDraining` |
| The scheduler is given a chance before teardown gives up on a worker | `TestCollectLoopWaitsOutTheScheduler`, `TestCollectLoopBoundsTheWait`, `TestCollectLoopOnlyAsksWhenNothingReleasedIt` |
| A pipelined request stays cancellable while the writer is parked | `TestPipelinedRequestsStayCancellableWhileTheWriterIsParked` |
| `--addr` with `--stdio` is a usage error | `TestStdioRefusesAListenAddress` |
| An unknown flag is a usage error, and the usage names the hub | `TestHubFlagErrors`, `TestUsageMentionsHubAndServe` |
| The verb is `hub`, not `serve` | `TestServeWithoutRoleNamesHub` |
| The built binary drives a full session, and fails closed on a malformed line | `TestStdioBinaryDrivesAFullSession`, `TestStdioBinaryFailsClosedOnAMalformedLine` |
| The full lifecycle runs against a real listener | `TestServeLifecycleOverRealListener` |
| The hub's stdio wire is the thirteen ops, not the endpoint's raw envelopes | `TestCapabilitiesNameTheStdioBinding` |

### The HTTP routes and SSE framing

| method and path | operation | answer | errors |
| --- | --- | --- | --- |
| `GET /adapters` | `adapters` | `{"adapters":[...]}` | — |
| `GET /adapters/{name}/capabilities` | `capabilities` | `capabilities.response` | `unknown_adapter` 404, `probe_failed` 500, `internal` 500 |
| `POST /adapters/{name}/sessions` | `open` | `session.open.response`, and a held subscription when the request set `subscribe` | see [open](#open) |
| `GET /sessions` | `sessions` | `{"sessions":[...]}` | — |
| `GET /sessions/{id}/state` | `state` | `session.state.response` | `unknown_session` 404, `state_failed` 500, `internal` 500 |
| `GET /sessions/{id}/tools` | `tools` | `action.tools.list.response` | see [tools](#tools) |
| `GET /sessions/{id}/models` | `models` | `models.response` | see [models](#models) |
| `POST /sessions/{id}/submit` | `submit` | `session.message.submit.response`, or `session.compact.response` for a compaction | see [submit](#submit) |
| `POST /sessions/{id}/resolve` | `resolve` | the matching resolve response | see [resolve](#resolve) |
| `POST /sessions/{id}/cancel` | `cancel` | `run.cancel.response` | see [cancel](#cancel) |
| `POST /sessions/{id}/settings` | `settings` | `session.settings.update.response` | see [settings](#settings) |
| `POST /sessions/{id}/close` | `close` | `204 No Content`, no body | `unknown_session` 404, `run_active` 409, `session_closed` 409, `request_cancelled` 400, `internal` 500 |
| `GET /sessions/{id}/events` | `events` | an SSE stream, adopting a held subscription when the request named no cursor | see [events](#events) |

**The daemon serves a bounded number of connections at once, and a stream is
not a connection to itself.** One connection at a time is enough for the
read-only routes and wrong for `events`: a stream holds its connection for as
long as the client listens, so a single-connection daemon answers exactly one
SSE subscriber and then serves nothing else, forever. So the number of
connections open at once is bounded, and the bound is a transport fact rather
than a protocol one — the client cannot observe how many are open, only that
its own was answered.

**Reaching the bound does not get a new wire code**, and this is the part that
was wrong in the first draft of this paragraph. The rule below is that a port
may not invent an admission ceiling, and inventing one is exactly what a
`busy` with a `Retry-After` would be: a client that receives it has no way to
know whether to wait or to reconnect, because the draft's codes are the ones
the core defines and none of them means "the daemon is full". A port that
reaches its bound therefore refuses the connection the way it refuses anything
else, and **what a client is told at the bound is not decided here** — it needs
a code the core defines, which is [G13](#known-gaps). Until then a port may
choose, and the choice is a divergence rather than a rule.

Go has no bound of its own: `go/cmd/goap/serve.go` runs a plain `http.Server`
over `net.Listen` and `servehttp` does no connection accounting, which the
bounds section below already records. So this rule is new, it is this port's
rule, and a reader sent to Go for the numbers will find none.

The Zig daemon serves at most **64** connections at once (`max_connections`
in `zig/src/hub/daemon.zig`), all on the hub's one thread, as `DESIGN.md` §8.6
requires: one loop polls the listener, every connection's socket and every
session's child output together, reads and writes each socket without
blocking, runs a request against the hub when its body is complete, and pumps
the hub once per cycle. No lock appears, because no second thread touches the
hub. At the bound the loop stops polling the listener rather than answering, so
a 65th client waits in the kernel's listen backlog until a connection ends — no
status is sent for having reached the bound, which is what the rule above asks
of a port. A stream's socket stays in the poll while it has nothing to write, so
a client that hangs up mid-stream releases its connection and its subscription
without an event having to fail first; a client that stops reading only stops
its own stream, whose buffer is bounded at 256 KiB before the hub's own mailbox
takes over and ends it with `oap-overflow`. A stop closes every connection on
the next cycle. Zig: `an open event stream does not hold the daemon: another
connection is answered while it streams`, `a client that hangs up mid-stream
releases its stream without an event to write`, and `stopping the daemon ends
an open stream rather than waiting it out`. A platform that cannot poll a
socket — Windows, #460 — serves connections one at a time instead, and a stream
there writes what is queued and ends.

Every route that reads a body requires `Content-Type: application/json`, and
the body is read as UTF-8 whatever `charset` the header names. RFC 8259 §11
records that no `charset` parameter is defined for `application/json` — the
media type registers no parameters at all — so a sender that names one is
describing something the grammar does not carry, and a receiver that refuses it
is refusing a request it can parse. The two trees admit every charset,
including `latin1` and a name that is not a charset at all. What the gate still
refuses is a media type that is not `application/json`, because that is a
different grammar rather than a different spelling of this one. Validity is the
decoder's business and not the gate's: a body that is not valid UTF-8 is read
with U+FFFD in place of the bad bytes, as the JSON decoder does, and a byte
that a `latin1` header would have decoded cleanly is still replaced. A pipe has
no `Content-Type` at all, so the stdio transport has no counterpart to any of
this. The daemon also caps
the body at 16 MiB, and answers a refusal as an `error.response` envelope
carrying the request's `in_reply_to`, `session_id` and `run_id` so a client can
correlate it. The two listings answer plain JSON rather than an envelope,
because they are daemon management rather than an OAP operation.

The three codes this gate can answer — `unsupported_media_type`,
`request_too_large` and `request_read` — belong to the transport, not to any
operation, so they are stated here once rather than repeated in every
operation's list below. Only two are HTTP's own: `unsupported_media_type` and
`request_read` have no stdio counterpart, because a pipe has no `Content-Type`
and its read failures are framing defects that stop serving rather than refusals
the host may retry.

`request_too_large` is the exception and both transports answer it, for the
same budget. An HTTP body over 16 MiB is refused `413`; a stdio `request`
envelope over 16 MiB is refused `request_too_large` while the line itself still
fits the larger frame limit — which is reachable, because the frame limit admits
2 MiB of wrapper the envelope cap does not. `TestRequestBudgetMatchesHTTP` pins
both answers, at the budget and one byte over it.

| HTTP rule | pinned by |
| --- | --- |
| A wrong `Content-Type` is refused `415` | `TestReadRequestRefusesBrowserOrigins` (the status), `TestARequestTheDaemonWillNotParseIsRefusedWithItsCode` (the code, and the absent `Content-Type`; a `charset` is no longer a reason to refuse, and the row below says which test pins that)  ·  Zig: `a body must declare application/json, and only that` |
| Any `charset` is admitted and the body is read as UTF-8 — no `charset` means UTF-8, and `utf-8`, `utf8`, `UTF-8`, `latin1`, `us-ascii`, `iso-8859-1` and a name that is not a charset all behave alike | `TestAnyCharsetIsAdmittedAndTheBodyIsReadAsUTF8` — each case's body names a session whose id carries non-ASCII text, and the answer must echo those characters unchanged, so a body transcoded per the header could not pass  ·  Zig: `a charset is admitted, because application/json registers no parameters` |
| A body that is not valid UTF-8 is read with the replacement character, whatever `charset` it claims | `TestABodyThatIsNotUTF8IsReadWithTheReplacementCharacter` — a body with a raw invalid byte is admitted and the echoed id carries U+FFFD. The gate does not police UTF-8 validity; refusing it would be the new refusal the charset decision removed |
| A body that cannot be read at all is refused `400 request_read` | `TestATruncatedRequestBodyIsRefusedWithItsOwnCode` — a client that hangs up mid-body over a raw connection, so the daemon's read fails rather than the client's write. Zig: `a client that hangs up mid-body is refused request_read, as the draft pins it`, which half-closes a real socket after a short prefix and reads the answer. A hangup **mid-header** is a different thing and gets a bare `400`, because no body was promised and there is nothing to have failed to read |
| A body over 16 MiB is refused `413 request_too_large` | `TestRequestBudgetMatchesHTTP` (which pins the stdio `request_too_large` for the same budget) |
| A body's refusing status and code match the stdio op's | `TestRequestBudgetMatchesHTTP`, `TestOpErrorCodesMirrorHTTP` |
| A `GET /adapters/{name}/capabilities` response cites a daemon-minted correlation id | `TestCapabilitiesEndpoint` |
| A descriptor carrying no capability revision is refused | `TestCapabilitiesRequiresDescriptorRevision` |
| The whole lifecycle is a valid `session.open.request` exchange | `TestValidationGatedLifecycleMemory`, `TestValidationGatedLifecycleFakeAdapter`, `TestOpenExchangeValidatesAsATrace` |
| An open's response carries the whole state document | `TestOpenResponseCarriesTheWholeState` |
| An open naming no id is assigned one | `TestOpenSessionAssignsIdentifier` |
| A queued submission round-trips with its queue position | `TestQueuedSubmissionRoundTrips` |
| A close after a cancel succeeds | `TestCloseAfterCancellation` |

### SSE framing

`GET /sessions/{id}/events` answers `200` with `Content-Type:
text/event-stream` and `Cache-Control: no-cache`, flushed before the first
frame. An envelope is written `id: <sequence>\ndata: <envelope>\n\n`; `id` is the
bare sequence, so a client may reconnect with `Last-Event-ID` and land on the
same cursor. A named signal is written `event: <name>\ndata: {...}\n\n` with no
`id`, because a signal is not a position in the stream.

| signal | meaning | pinned by |
| --- | --- | --- |
| `oap-subscribed` | the subscription joined a run already in progress; `joined_after` is the last sequence it missed. Advisory — it opens the stream, never ends it. | `TestSSELiveSubscriptionMidRun`, `TestSubscribedSignalMatchesHTTP` |
| `oap-overflow` | the connection's bounded buffer fell behind; `last_sequence` names the last sequence this connection delivered | `TestSSEOverflowSignalLive`, `TestSSEOverflowSignalReplay` |
| `oap-replay-gap` | the requested cursor is no longer retained; `oldest_available` and `latest_available` bound what is | `TestSSEReplayGap` |

A stream ends at the run's terminal envelope, at an overflow or a replay gap, at
the client's hangup, or when the session closes — a stream open when the session
closes receives the events already in flight and then ends. A connection made to
a session that has closed is refused `404 unknown_session` rather than parked:
close releases the session ([Decision 0039](../decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md)).

**The three signals above are the whole set.** A session closing under a live
stream ends it *silently* — a socket has no end to announce — so a port must not
emit an `oap-session-closed` event here, and the stdio transport's
`oap-session-closed` and `oap-frame-limit` lines have no SSE counterpart to grow.

| SSE rule | pinned by |
| --- | --- |
| An envelope arrives in emission order with its sequences | `TestSSEEnvelopeOrderAndSequences`, `TestClientRejectsFrameIDDisagreement` |
| The `id:` field and the envelope's own `sequence` agree; a frame whose `id` disagrees is refused | `TestClientRejectsFrameIDDisagreement` |
| An `after` cursor replays the journal's retained suffix | `TestSSEReplayAfterCursor` |
| `Last-Event-ID` reconnects | `TestSSELastEventIDReconnect` |
| An explicit `?after=` wins over the header | `TestSSEQueryCursorWinsOverHeader` |
| A live subscription mid-run is not handed a prefix it did not ask for | `TestSSELiveSubscriptionMidRun` |
| A stream ends when the client closes the connection | structural: the request context — `TestACancelledSubscriptionEndsTheStream` (a cancelled context ends the stream and leaves it finished) and `TestASubscriptionClosedByItsOwnerEndsTheStream` (`Close` ends it with `io.EOF`) at the hub, where it is observable; nothing in the hub reports subscriber accounting, so the HTTP handler is not itself under test |
| A stream ends when the session closes | `TestSSEStreamEndsOnSessionClose` |
| A connection to a session that has closed is refused `404 unknown_session` | `TestSSEOnClosedSession`, for a live stream and a cursor stream |
| A cursor that is not an unsigned sequence is refused `400 invalid_cursor` | `TestSSECursorErrors` |
| A session with no run to replay is refused `409 no_run_to_resume` | `TestSSENoRunToResume` |
| An unknown session is refused `404` | `TestSSEUnknownSession` |
| A `run_id` with no cursor is refused `400 invalid_cursor` | `TestSSERefusesARunWithoutACursor`, and the stdio side `TestEventsRefusesARunWithoutACursor` |
| A `run_id` the session never had is refused `404 run_not_found` | `TestSSERefusesARunTheSessionNeverHad`, and the stdio side `TestEventsRefusesARunTheSessionNeverHad` |
| The two transports return the same first envelope for the same request | `TestRunQualifiedCursorMatchesHTTP` |

### Bounds on HTTP

A socket gives back pressure, so the HTTP codec bounds nothing the way the pipe
must: there is no concurrent-op ceiling, no in-flight byte budget and no
subscription ceiling, and a connection that stops reading is ended by
`oap-overflow` at its own per-subscription mailbox. The only wire bounds are
the body cap, a 30 s header read and a 2 min idle timeout, none of which is
protocol. **A port may not invent an admission ceiling on the HTTP surface**,
because a client that receives `busy` over HTTP would have no way to know
whether to wait or to reconnect. That rule is about the **wire**, and it
stands: no port may answer a client with a status the draft does not define.

It is *not* a statement that a port may serve an unbounded number of
connections. A port must bound them, because a stream holds its connection
for as long as the client listens and a daemon that serves one connection at
a time answers exactly one subscriber and then nothing else, forever. **The
bound itself is a transport choice and belongs to the table below rather than
to the wire**: a port picks the number, and what a client is told when the
bound is reached is undefined until a code for it exists, which is
[G13](#known-gaps). Neither half contradicts the other — a port bounds its
concurrency and invents no code for having done so.

| bound | value | pinned by |
| --- | --- | --- |
| Request body | 16 MiB | `TestRequestBudgetMatchesHTTP`; Zig: `a body over the cap is refused before it is read` |
| Per-subscription mailbox | 64 envelopes, as the core defines it | `TestHubSubscriptionQueueOverflow` |
| Accept poll | 10 ms at most; the loop wakes on any socket or child becoming ready and falls back on this bound for a session that has no handle to poll, so a signal is noticed by an idle daemon | Zig: `hub_accept_poll_ms` and `the accept poll reports an idle listener as idle and a waiting one as waiting`, which drives a real listener. This is not protocol and is not in Go, whose `Serve` returns a listener a runtime polls for it |
| Accept failure | A failure the peer caused is served past; a failure of the listener stops the daemon | Zig: `an accept a peer aborted before the call is served again, not obeyed` and `a client that resets a connection the listener had not taken yet does not stop the hub`. The rule is not cosmetic: a daemon that stops on any accept error can be stopped by any local process that opens a connection and resets it, which a port scanner or a health check does by accident and an attacker does on purpose — and stopping it sweeps every session |
| Request headers | 16 KiB | Zig: `headers over the cap are refused rather than buffered`. Go's net/http carries its own default and names no rule, so this is a Zig choice within "a port may choose its own" |
| Concurrent connections | a port's own number; not on the wire, and no code for reaching it. Zig: 64, and at the bound it stops polling the listener | Zig: `the connection bound is the number the draft's G13 row records`. Go has no bound at all (`serve.go` runs a plain `http.Server`), and this draft has no code for reaching one. [G13](#known-gaps) names what a port does meanwhile |

A 30 s header read and a 2 min idle timeout keep a socket from being held open
forever. Neither is protocol — no client observes them, and a port may choose
its own — so they are named here only so a port knows they exist. Zig takes both
numbers as written, and the one that matters is the header: **a peer that
connects and then says nothing is given up on rather than waited on**, because
a stalled read would otherwise hold one of the daemon's bounded connections
forever. Pinned by `a peer that
connects and never sends a request is given up on, not waited on forever`;
Go's bound is `ReadHeaderTimeout` on the same server.

**Both bounds are a property of a platform that can wait on a socket**, and
Zig's is a `poll`, so neither holds on Windows: a read there blocks with no
deadline, and a peer that connects and stays silent wedges the daemon. Rather
than claim a bound it does not have, the Zig daemon says so on stderr when it
starts there, and the two tests that pin the bound skip. Closing it is #460's
work, not this route's; the macOS and Linux builds are the ones the release
covers.

**The header budget is absolute, and the body budget is not.** Go's
`ReadHeaderTimeout` runs from the first byte, so a peer that sends one byte
per window never completes a request; the `IdleTimeout` restarts on every read,
so a body read the same way can go on for as long as the peer keeps dribbling.
Zig takes that split as it stands — a per-read budget on the header would let
exactly the wedge the bound exists to prevent — and `the header budget is the
whole request's, not a fresh one per byte` pins the first half. The second half
is a hole the draft names rather than closes, and a port that wants it closed
needs a whole-body deadline, which is not what either tree has.

**A path no route names answers a plain `404`, not a refusal.** Go's mux does
the same for a path no pattern matches, and the draft's codes are
`invalid_request`, `unknown_adapter` and `unknown_session` — none of which is
"this URL does not exist". So the answer carries no `error.response` envelope.
All thirteen routes are written in both trees: Zig dispatches each to the same
`Frontend` operation the stdio op runs, so an answer's shape is shared rather
than written twice. A path that is a route asked with another method answers
`405 Method Not Allowed` with an `Allow` header naming the one it takes, which
is what Go's method-qualified patterns answer, and a `HEAD` is routed as the
`GET` it describes. Zig: `a known path asked with the wrong method is 405 naming
the method it takes, and an unknown path is a plain 404`; Go pins the table by
`TestTheRouteTableIsComplete`.

Three records that existed only in the historical review of #656 and were **verified against
current `main` before being recorded here**, rather than carried over on the strength of the old
thread. They are measurements and an open question. None of them settles anything, and none of them
was settled by #656 either.

- **A wrong-media request to a path no route matches is `415` here and `404` in Go — MEASURED, both
  sides, from the source.** `answer()` tests the media gate at `http.zig:388` and only then returns
  `.not_found` at `:389`, so on this tree an unrouted path carrying a body and a non-JSON
  `Content-Type` is answered `415 unsupported_media_type`, **not** the plain `404` the paragraph
  above describes. Go cannot reach the same answer: its media check is at
  `servehttp/server.go:796`, inside `readRequest`, which is called from the **five**
  body-reading handlers at `:191`, `:327`, `:400`, `:514` and `:562` — five of the thirteen operations
  registered on the mux across `:69`-`:81`, `:68` being the `http.NewServeMux()` construction rather
  than a registration. So a path matching no pattern is answered by the mux before any handler runs,
  and the other eight registrations read no body at all. So the two trees disagree on one request, and the paragraph above is right about
  the rule and incomplete about the case. **Neither tree is wrong against the draft**: the draft
  states the media rule for routes that read a body, and states no rule for a path that does not
  exist. Which answer an unrouted path should give is **not decided here**. The `415`-on-unrouted
  behaviour is a consequence of the gate's position in `answer()`, not a stated policy, and narrowing
  it would be a policy change this table does not make. Carried forward from #656.
- **A wrong method on a known path — RESOLVED as `405`, matching Go.** The routes are now wired, and
  the running hub answers a wrong method on a known path `405` with an `Allow` header, as Go's mux
  does; the record below is kept because it is what this row said while the question was open.
  **Previously an unresolved question, and deliberately not merged with the row
  above.** Go registers patterns that include the method (`mux.HandleFunc("GET /adapters", ...)`,
  `servehttp/server.go:69`) and the module targets `go 1.26`, where `http.ServeMux` answers a request
  whose path matches but whose method does not with `405 Method Not Allowed` and an `Allow` header.
  The `http.zig` response path has no `405` — `answer()` (`http.zig:378-383`) never
  inspects `request.method`, and `bodyAllowedFor` at `:368` only excludes `HEAD`. The **router**,
  however, does distinguish it: `route()` in `zig/src/hub/routes.zig:174` returns
  `.method_not_allowed` for a known path with the wrong method, distinct from `.not_found`. That
  distinction is **not wired into the running hub**: no module imports `routes.zig` — `build.zig`
  builds `hub_routes_mod` for its standalone test only — and `runHubHttp`
  (`zig/src/tools/makai.zig:1582-1594`) never calls `route()`, answering `.not_found` past the gates.
  So the running hub answers a wrong method on a known path `404`, and whether it should answer
  `405` is the open question; it is not settled here, so the open question is what the draft says a
  wrong method should answer. **It is recorded here as a separate question on purpose.** The
  fact that both this and the row above are decided by the order of checks inside one function is a
  *coincidence of implementation*, and the unproved suggestion that the `405` and the media-precedence
  questions must therefore be settled as **one** decision is **not** carried forward as settled
  policy. They are separate rows because they are separate questions, and nothing here establishes
  that answering one constrains the other. Carried forward from #656, which raised the `405` and left
  it open.
- **No proof that a media-refused request with a body over 1 MiB still receives a complete answer —
  an explicit gap, and the stated reason for it has changed.** `drain` is bounded at
  `drain_total_cap_bytes = 1 MiB` (`:207`) while `max_body_bytes` is 16 MiB (`:7`), so a request
  declaring between 1 MiB and 16 MiB with a non-JSON `Content-Type` is refused `415` by the gate and
  then drained **short**, leaving bytes unread at close — which is the condition the drain paragraph
  below, at `:645`-`:649`, identifies as able to reset the connection and discard the refusal. **No test exercises that
  case.** The `415` proof sends bodies of `"xx"`, `"{}"` and `""`; the over-cap declared sizes in
  `hub_toobig_test.go` are the `413` path, not this one. One correction to the reason this gap was
  previously carried with: it used to be blamed on a test helper that capped writes at 256 KiB, and
  **that cap no longer exists** — `writeBody` at `hub_largebody_test.go:190` writes the full declared
  amount in 32 KiB blocks. So the absence is not a helper limitation any more; it is simply that no
  test asks the question. **What is still unknown is unchanged and is not claimed either way here:**
  whether the daemon's `415` survives the close in that window. The `413` complete-answer proof,
  the `413` real-socket proof (`TestHubAddrAnswersAComplete413BeforeTheWholeDeclaredBodyIsSent`),
  the `403` real-socket proof (`TestHubAddrRefusesALargeRefusedHeadOverARealSocket`) and the
  drain-cap proof cover their own cases and do not cover this one. Carried forward from #656.

**A body the daemon refused to read is drained before the socket closes.** A
`403` or a `413` is answered without reading the body the head declared, and a
close on a socket whose receive queue still holds those bytes is answered with a
reset, which on Linux can discard the refusal the client has not read yet — the
client sees a connection error rather than the reason. So every path that
answers without consuming a body drains what the head declared, and gives up rather
than waiting on a peer that sends nothing more. The bound is **both** a byte cap and an
elapsed-time budget, and it is the *total* over the whole drain that bounds it: a per-round cap
with an unbounded round count is not a bound. The Zig port reads in 64 KiB rounds, stops at
**1 MiB in total**, and stops at **2500 ms elapsed from that drain's own start** — elapsed, not the process's uptime, so a daemon that has been up
for hours still drains. A drain that cannot read its own clock stops rather than draining without a
bound, and that rule covers the **whole helper path**: `readUntil` returns its existing `Timeout` rather
than substituting `0` for a clock it could not read, which kept `left_ms` positive and renewed the
silent-socket poll forever; `readHead` and `readBody` do the same; and the round deadline **reuses the
`now` that round already read**. The classification is unchanged — a clock it cannot
read is a wait it cannot honour, which is the `Timeout` those sites already had — so no wire code or
status is added.

An earlier revision claimed each of the four bounds is pinned by a test that fails if the bound is
removed. **That was false, and was measured rather than assumed.** The pinning that exists is not uniform:

- **the 1 MiB total — pinned by removal at this head.** `a drain stops at its byte cap and reports
  what it consumed` carries its own `wanted_cap = 1024 * 1024`, asserts the production constant
  equals it, and uses the literal throughout, so constant and expectation cannot move together;
  raising `drain_total_cap_bytes` to 1 GiB fails it with `expected 1048576, found 1073741824`. The real-socket test separately pins that a cap
  is *observable from outside the process*, asserting a lower bound on transferred bytes plus a
  complete 403 — never an upper one
- **the 64 KiB round cap, and the round's own deadline — pinned as _configuration_ at this head, which is
  a weaker claim than the bullets around it and is not a removal proof.** Of the four
  bounds the row above names, the 64 KiB round cap was **the one no bullet covered**: that row says the
  port "reads in 64 KiB rounds". It does discuss the round deadline at `:601` — that the deadline
  **reuses the `now` that round already read** — but only as a property of the clock; it never gives a
  round a **time bound of its own**, where it names the 1 MiB and the 2500 ms outright. So the narrow
  claim is that `drain_cap_bytes` and `drain_cycle_ms` are two bounds the row leaves **unnamed**, and
  until now each was named only in the source: `drain_cap_bytes` at its definition (`:205`) and its use
  (`:219`), `drain_cycle_ms` where `drain` builds the round deadline from it at `:220`
  (`const deadline = now + ...drain_cycle_ms...`). Neither was asserted anywhere, so the ledger stated
  a bound no test would notice losing. `a drain stops at its byte cap and reports what it consumed` now
  carries `wanted_round = 64 * 1024` and `expectEqual(@as(i32, 50), drain_cycle_ms)` beside the
  1 MiB assertion it already had, in the same carrying-its-own-literal shape, so the constant and the
  expectation cannot move together. `a drain gives up rather than waiting on a peer that sends nothing
  more` no longer accepts any elapsed time under 2000 ms for a silent peer, which was 40× the 50 ms
  round it is supposed to honour and let a 1500 ms cycle pass; it now requires the drain back within
  **10 round cycles** of the constant, so the bound tracks the constant instead of drifting from it —
  a response-time bound **derived** from the constant, not evidence that the per-round deadline is read.
  **What these equalities do not prove, stated plainly because it is the limit of the evidence:** they
  pin the two constants' **values**, not that `drain` *uses* them. Bypassing the use while keeping the
  constants — taking `owed` from `drain_total_cap_bytes` instead of `drain_cap_bytes` at `:219` — **compiles
  and leaves every assertion here green**, and no such control is presented, because the per-round cap is
  not independently observable through `drain`'s surface: the 1 MiB total and the 2500 ms total both
  dominate it, so a wider round changes no observable byte count. The round deadline is **not** in that
  position, and the row should not have put it there. A `:220` substitute that keeps both constants but
  builds the deadline from something larger — `drain_total_ms` is the case that matters — blocks a silent
  peer for ~2500 ms and so **fails** the 10-cycle bound, and that is **run, not inspected**: building the
  deadline from `drain_total_ms` instead compiles clean and fails `EXIT=1` in `a drain gives up rather
  than waiting on a peer that sends nothing more`, with both constants still holding 64 KiB and 50, so
  the `expectEqual` assertions pass and only the derived bound objects. What the derived bound pins is
  therefore a **ceiling**: a round deadline substituted at **500 ms or more** is caught, and one
  substituted **below 500 ms** — a hardcoded shorter wait, say — is not. So the bound does real work on
  the deadline, and none at all on the round cap. So this bullet says only that the ledger's
  64 KiB and one-cycle figures are now **asserted rather than merely stated**, and it does not close the
  per-round cap or the per-round deadline the way the 1 MiB bullet and the guard-presence row do theirs.
  **Both measured, not assumed:** against `main` as it stood, raising `drain_cap_bytes` to 1 MiB left
  the suite `EXIT=0` and raising `drain_cycle_ms` to 1500 left it `EXIT=0` — both compile, and neither
  was caught. With these assertions the same two mutations fail, with `expected 65536, found 1048576`
  and `expected 50, found 1500`. The round cap is a **shape** bound rather than a total, so it is
  asserted as the constant it is; the total remains pinned separately by the 1 MiB bullet above
- **the 2500 ms elapsed budget, and that it is elapsed rather than uptime** — `a drain reads on a
  long-lived process, because its budget is elapsed not uptime` seeds the clock, waits
  `drain_total_ms + 200` under a bound, asserts the clock is past the budget, and drains again;
  restoring the old `elapsedMs() catch 0 -| started` fails it with `expected 4096, found 0`. **It pins
  the comparison, not the guard's presence:** neutralising the guard is green, so a deleted time bound
  would go unnoticed **by that test**. The stall case stays covered by `a drain gives up rather than
  waiting on a peer that sends nothing more`. **The guard's presence is pinned separately, by
  `a drain whose elapsed budget is already spent consumes nothing, though the bytes are buffered and
  reachable`, recorded below; this row and that one are complementary, and neither alone closes the other.**
- **stop on an unreadable clock — NO exercising test, recorded as a gap.** The port returns the
  bytes consumed from `drain` and its existing `Timeout` from the read helpers, rather than reading
  an unreadable clock as `0`. Nothing reaches it: the clock is `std.Io.Timestamp` against a monotonic
  source and, per the owner's check of `compat/time.zig:25-35`, is currently **infallible**, so no test
  can reach the branch on this platform, in CI, or on the others this port runs on. **This is a latent
  error-contract mismatch, not a reproduced clock failure.** Stated by the port and unproven by
  execution, the same shape as the `type_mismatch` gap D20 records. An earlier revision of this row
  cited the long-lived test as pinning it; that was a coverage claim with no test behind it. Closing
  it needs a seam substituting a failing clock, a redesign rather than a test, so it stays a gap
  rather than invented.
- **the 413 path answers completely, before the whole declared body is sent** —
  `TestHubAddrAnswersAComplete413BeforeTheWholeDeclaredBodyIsSent` drives the branch a plain media
  refusal does not reach: `readHead` refuses `413 request_too_large` on the declared length alone,
  and the daemon writes that refusal and then drains what the head declared. Against declarations of
  16 MiB+1 and 16 GiB it is answered 413 with a **complete, parsed** envelope — status,
  `Content-Length` match, envelope type and the `request_too_large` code — and the client was **still
  writing** when the writer stopped, having sent only a fraction of what it declared. That is the
  whole claim, and it is phrased in what a client can observe on purpose: **every number this test
  reports is a client write count**, which bounds when the writer stopped and not how many bytes the
  daemon read, because what the client pushes before the close lands is kernel buffering and
  scheduling. **It does not pin the drain's cap or budget on this path, and the mutation says so:**
  lifting `drain_total_cap_bytes` to 8 MiB, lifting `drain_total_ms` to 60000, and lifting **both
  together** all leave the test **green**. The transferred bytes do move — 1.7–3.1 MB bounded against
  9.2–10.8 MB unbounded — but a threshold on them would be a machine-dependent constant rather than a
  bound, and the same build produced 1.70 MB and 3.13 MB on two runs of one case, so none is asserted.
  **What the existing unit tests do and do not cover, stated precisely so this row does not overstate
  them:** `http.zig:1371` pins the **byte cap** — it asserts `drain_total_cap_bytes` against its own
  literal and drains past it — while `http.zig:1417` pins only that the budget is **elapsed rather
  than uptime**, exactly as the row above records, and that row's finding stands unchanged:
  **neutralising the time guard leaves it green, so the time bound's presence was a gap until the
  already-spent-budget proof below closed it.** What
  is **not** pinned anywhere is that the 413 path *reaches* `drain` at all, and no client can observe
  that without the threshold just declined. An earlier revision of this work claimed the mutation
  failed when both bounds were removed, and a second claimed the budget was pinned at `:1417`. The
  first does not hold and the second contradicts the row above. Both are withdrawn.
- **the `readBody` failure path answers completely, and the daemon gives up on a peer that stops** —
  `makai.zig:1591` is the third and last `drain` call site, and the only one none of the rows above
  reached. `TestHubAddrAnswersACompleteTransportFailureWhenTheBodyStopsShort` reaches it with a head
  the gate admits (`Content-Type: application/json`, so `answer()` does not refuse and the request goes
  on to `readBody`), a declared 8 MiB, 4096 body bytes sent, and then a **TCP half-close**. `readBody`
  sees `n == 0`, returns `error.BodyTruncated`, and the loop writes the transport failure and drains
  `request.content_length -| request.filled`. What is pinned, all of it observable: a **complete**
  answer — status `400`, `Content-Length` matching the 265-byte body, `type: error.response`,
  `code: request_read`, `protocol: open-agent-protocol`, `version: 0.1`,
  `profile: open-agent-protocol.agent-control-core`, and the synthetic correlation `oap-error-1`
  replying to `oap-request-1` that `refusalEnvelope` builds at `http.zig:292-298` and the existing
  unit test `a refusal is an error.response naming its code, correlated to a request that never
  arrived` at `:1617` already pins for all four refusals — delivered while the client had sent
  **4096 of the 8 MiB it declared**. The answer arriving at all is the "gave up" claim: a
  daemon waiting for the declared body would have said nothing and the read would have timed out.
  **Two negative controls fail it:** `readBody` treating a short body as complete answers `404`, and
  mapping `BodyTruncated` to a different refusal answers `413`. **The drain on this path is NOT
  pinned, and the mutation says so: deleting `makai.zig:1591` outright leaves the test `EXIT=0`**
  green, because after a half-close the drain's first read returns EOF immediately, so nothing about
  the drain reaches this client. So the **`content_length -| filled` arithmetic and this site's
  reachability are unproved** by this test, by the rows above, and by anything else on main, and no
  threshold is inferred from transferred bytes to stand in for them. **Of the two drain bounds, the
  byte cap is pinned** — `:1371` asserts `drain_total_cap_bytes` against its own literal and drains
  past it — and the **elapsed-versus-uptime comparison is pinned** at `:1417`, but **the time guard's
  presence was NOT pinned by this row**, exactly as the row above then stated; it is now pinned by the
  already-spent-budget proof, which is about the guard itself and not about any client's drain amount.
  Two things a reader might assume are covered here are not: this site's drain amount, and the
  existence of the time bound.
- **the total-time guard's presence, through the injected callback that already exists** — the row at
  `:655-663` pins that the 2500 ms budget is **elapsed rather than uptime** and is explicit that it does
  **not** pin the guard's presence, because that test's bytes are already buffered when `drain` starts.
  `a drain whose elapsed budget is already spent consumes nothing, though the bytes are buffered and
  reachable` pins the presence, through the **existing public `KeepGoing`** at `:104-110` and the injection
  pattern the other test already uses. `drain` takes `started` at `:214`, calls `keep_going.yes()` at
  `:216`, checks the guard at `:218`, and only then computes the round `deadline` at `:220` — so an
  injected callback can **spend the budget and then leave bytes where the inner read will find them**.
  The callback takes its **entry timestamp on its first entry**, waits `drain_total_ms` from that
  timestamp, writes 4096 bytes to the peer and returns true; the guard on that same iteration returns
  before any read, so `drain` returns **0** with the bytes unread. **Why the timestamp is taken inside
  the callback, which is the whole correctness of this test:** `drain` reads its own `started` at
  `:214` and only then makes the first `keep_going.yes()` call at `:216`, so the callback's entry
  instant is **necessarily later** than `drain`'s — `drain.started <= entry <= write`. The elapsed the
  callback waits out is therefore measured from a **later** origin than the guard's, which makes it the
  **smaller** of the two, and `final_now - drain.started >= final_now - entry >= drain_total_ms`. The
  guard at `:218` compares against `:217`'s `now`, which is read after the callback returns, so the
  inequality the guard needs holds by **ordering**, with no margin and no assumption about scheduling.

  **This replaced a version that was wrong in a way worth recording.** It previously took the timestamp
  from the test's `before`, read **outside** `drain`, and added a flat 200 ms of skew. That bought
  nothing structural: the gap between `before` and `drain`'s `:214` is **unbounded** under preemption,
  so `observed >= drain_total_ms + 200` never established `now - drain.started >= drain_total_ms`, and
  the 200 ms only *assumed* the gap was smaller than itself. The claim that the test "observes its own
  precondition rather than trusting it" was false for the same reason — it observed an elapsed from the
  wrong origin. A/B run, with the **only** difference being which side of the gap the clock is read:
  timestamp outside `drain` plus a 3 s stall before `:214` fails with `EXIT=1` and
  `expected 0, found 4096` — the negative control's exact signature, on the unmutated budget; the
  in-callback entry timestamp passes the identical stall with `EXIT=0`. That is why the constant is
  gone and not merely enlarged.

  The test also checks the rest of its preconditions rather than assuming them: the callback was polled
  **once**, the write is **not** allowed to fail, and the test **reads the 4096 bytes back off
  `pipe.accepted` and asserts every one of them is still queued**, byte for byte. `drain` returned 0
  having read nothing, so the socket must still be holding all 4096 `q`s; that is an observation of the
  socket, not the callback's own bookkeeping, and a failed write can no longer leave the test green.
  Inverting that read-back expectation fails the test with `expected 0, found 4096` on the queued
  count. The
  **negative control is still the whole point**: making the budget unreachable
  (`now -| started >= std.math.maxInt(u64)`) leaves the same test failing with `expected 0, found 4096`,
  so the 0 is attributable to the guard rather than to a starved reader, and the inner deadline being
  computed *after* the callback is what lets the un-guarded build read at all. Teardown is bounded by
  construction: the callback writes to the peer itself, so there is no writer thread to join.
  **What this is not.** It is an **already-spent wall-clock-budget** test. It does **not** show a
  continuous daemon giving up on a trickling peer, does **not** exercise the budget elapsing across
  rounds of real I/O, and makes **no** claim about `drain_total_ms`, `drain_cycle_ms` or
  `drain_total_cap_bytes` — all three are unchanged, and no clock seam was added. An earlier revision of
  this work claimed the guard was "structurally unreachable" from a bound on the requested I/O round
  deadlines; that was **withdrawn**, because those deadlines bound requested waits and not elapsed
  execution across preemption, inter-round scheduling, or the work an injected callback does.
- **the refusal order, decided outside the process** — `answer()` at `http.zig:378-383` checks
  **Origin, then Host, then the media type**, and what was missing was a **separate daemon process**
  deciding it over a socket. Two in-process tests already covered the ordering inside one test binary:
  the test at `:1155` calls `answer()` directly on hand-built `Request` values and uses **no sockets at
  all**, and the test at `:1169` does use real sockets, with its `cross_origin_request` cases at
  `:1175-1176`. No `go/cmd/goap` test named `cross_origin_request`, so nothing outside the test binary
  had ever seen this refusal. Two tests now settle it over a real
  socket. `TestHubAddrRefusesAnOriginHeaderBeforeItLooksAtTheHostOrTheMediaType` sends four requests
  that each carry an `Origin` header and asserts a **complete** 403 — status, `Content-Length` match,
  `error.response`, `cross_origin_request`, the fixed `open-agent-protocol` / `0.1` /
  `open-agent-protocol.agent-control-core` triple, and the synthetic correlation `oap-error-N` replying
  to `oap-request-N` for the same `N` — including one beside a **Host the hub refuses** and one beside
  a body with a **refused media type**, so the ordering is pinned in both directions.
  `TestHubAddrRefusesTheSameTwoRequestsDifferentlyOnceNoOriginHeaderIsPresent` is its counterexample,
  and each of its two requests is the **same request as the second and third of the four the first
  test sends, with the `Origin` header removed and nothing else changed** — those two, not the first
  two, are the precedence cases, the first being the Origin header alone and the fourth the
  matching-`Origin` case: `Host: evil.test` with `application/json` is refused `403
  unrecognized_host`, and `Host: 127.0.0.1:1` with `text/plain` and a body is refused `415
  unsupported_media_type`. That is what makes the ordering a measurement rather than an assertion —
  each precedence case has a twin that differs only in the header under test, so the first test cannot
  pass on a build that refuses everything. Both counterexamples parse the envelope and assert `type`,
  `code`, `Content-Length` and the same `open-agent-protocol` / `0.1` /
  `open-agent-protocol.agent-control-core` triple and `oap-error-N` / `oap-request-N` correlation as
  the primary proof; an earlier revision of this row checked the code with a substring search over the
  raw body, which would have accepted a different `payload.error.code` that merely mentioned the
  string elsewhere. **Three mutations fail them:** checking `Host` before `Origin` answers
  `unrecognized_host` on the second case, checking the media type first answers `415` on the third,
  and deleting the `Origin` gate answers `404`. One thing this pins that is worth stating plainly,
  because it is stricter than the name suggests: the gate fires on the **presence** of an `Origin`
  header, not on a comparison. A request with `Host: 127.0.0.1:1` and `Origin: http://127.0.0.1:1` —
  matching — is still refused `cross_origin_request`. That is the observed contract, recorded rather
  than judged, and narrowing it would be a policy change this table does not make.
- **the media gate answered by the daemon itself, with nothing else wrong** — the third gate in
  `answer()` and the only refusal no `go/cmd/goap` test named, so the parity D26 records was pinned
  in-process only. `TestHubAddrRefusesABodyWhoseMediaTypeIsNotJSONAndNothingElseIsWrong` sends five
  requests over a real socket, each with a **Host the hub accepts and no `Origin` at all**, so nothing
  but the media type can decide the answer. `text/plain` with a body is refused a **complete 415** —
  status, `Content-Length` match, `error.response`, `unsupported_media_type`, the fixed
  `open-agent-protocol` / `0.1` / `open-agent-protocol.agent-control-core` triple, and the synthetic
  correlation `oap-error-N` replying to `oap-request-N`. **Three of the five are counterexamples**, and
  they are what keep the first from passing on a build that refuses everything: `application/json` with
  a body is not media-refused, `application/json; charset=utf-8` with a body is not media-refused —
  so the real daemon agrees with the parameter parity D26 pinned against Go — and `text/plain` with **no
  body declared** is not media-refused, because the gate reads length. **Three mutations fail it, each
  on the case it should:** neutralising the gate answers `404` on the refused case, keying the gate on
  the header instead of the length answers `415` on the body-less case, and admitting a parameter with
  no `=` again answers `404` on `application/json; charset`. That third one is why that case is here —
  without it, dropping the parameter validation was **invisible to this test**, which I found by running
  the mutation rather than by assuming it would be caught.
- **the bound holding against a real process** —
  `TestHubAddrRefusesALargeRefusedHeadOverARealSocket` transfers 1,052,672 /
  1,719,800 / 1,799,224 bytes against declarations of 1 MiB+4096, 4 MiB and 16 MiB
  and is answered 403 with a complete body each time, so the cap is visible from
  outside the process. Its clock is seeded through a **refused** request, which is
  the only shape that reaches a drain, and `boundAt` is taken after that
- **the refusal answered before the body, and an exit proof that can fail** —
  `TestHubAddrFinishesTheRefusalAndStopsTheBodyWhenItsSignalArrives` declares 8 MiB, paces the
  writer, and asserts the complete refusal is read while bytes are still unwritten, that the writer
  is still running then, and that after the signal it stops with fewer than `declared` bytes ever
  written. Every number it reports is a **client write count**: it bounds when the answer arrived
  and when the writer stopped, and **no threshold on it counts bytes the daemon read**. It then
  dials under a bound and **fails if the daemon still accepts**. It does **not** show the custom
  `SIGINT` handler stopped the daemon — the default disposition of `SIGINT` terminates the process
  regardless, so removing the handler changes nothing observable. That exit proof is only worth
  something if it can tell cooperation from a kill, so `TestHubAddrSignalProofReportsADaemonThatIgnoresTheSignalAsAlive`
  runs `testdata/fakehub`, a separate program that installs `signal.Ignore`, announces its address, and
  never exits on its own: the test confirms it is **accepting before the signal**, sends the same `SIGINT`,
  and requires the proof to report it alive, failing **before any kill**, and then asserts it **waited
  the bound out** and logs the elapsed figure. **Four** mutations fail it — cleanup running first (the
  defect fixed here), a proof reporting an exit when the bound elapses, the helper no longer ignoring
  `SIGINT`, and a proof returning "alive" without waiting, which the elapsed assertion catches and which
  nothing else here would. A **test binary** dies on `SIGINT` despite the ignore and a standalone one survives, which is why the helper is a separate program.

- **the shutdown sweep is not abandoned — pinned, and this is a narrower claim than the row it
  replaces.** The previous wording said the session "is actually closed" and that "the wiring" is
  pinned; **both overstated what any test here can show, and the skipped-call control below is the
  measurement that says so.** `TestServeSessionsClosedOnShutdown` opened a
  session, asserted it appeared in `/sessions` **before** shutdown, called `cancel()` and then
  `expectServeExit` — which checks only that `runHub` returned `nil`. It never asserted anything
  about the session being **closed**, so the test's name claimed a fact its body never looked at. The
  production line it exists to cover is `serve.go:120`, `hub.CloseSessions(sessionShutdown)`, and
  nothing in the tree constrained it. `startServe` also sent `runHub`'s stderr to `io.Discard`, so
  even the sweep's own abandonment log — `serve.go:251`, "shutdown budget exhausted before closing
  session" — was invisible to every test. **The captured buffer is read only after the writer is finished**, and
  that is structural rather than a sleep: `startServe` sends on `done` only after `runHub` returns
  (`serve_test.go:128`), `expectServeExit` receives from `done` (`:154`), and the test reads the buffer
  after that, so every write the daemon made happens-before the read. `syncBuffer` is mutex-guarded as
  well, and the test is green under `go test -race` unmutated and red under it when the sweep is
  abandoned, with no data race in either case. **The mutation, run:** giving that site a
  `context.WithTimeout(context.Background(), 0)`, so the sweep context is born expired and
  `CloseSessions` closes **zero** sessions. It **compiles**, and on `main` as it stood
  `TestServeSessionsClosedOnShutdown` was **green** with that defect in place. `startServe` now
  returns a captured `*syncBuffer` for stderr, and the test fails if the sweep was abandoned, so the
  same mutation now fails it. **What this proves, stated at its limit:** it pins that the shutdown
  sweep is **not abandoned** — and the second control below is the measurement that bounds how far
  that goes.
  **The second control was run, not argued:** replacing `hub.CloseSessions(sessionShutdown)` at
  `serve.go:120` with `_ = sessionShutdown` — a source-valid change that leaves `sessionShutdown`
  used and **skips the call entirely** — also **compiles**, and the test is **green** with the sweep
  never invoked at all. So the passing case does **not** distinguish "swept" from "never called", and
  this row does not claim it does.
  **What is pinned, exactly:** when `CloseSessions` **is** invoked, its budget is not spent before the
  first session — the one decision the `serve.go:118`-`:120` wiring makes that any observable here can
  reach. **What is not pinned here:** that `runHub` invokes it at all, and that any session reached a
  closed state. Neither is observed by this PR's test: the HTTP listener is already shut down at
  `serve.go:113` before `CloseSessions` at `:120`, so no request can ask afterwards; the `Hub` and
  its registry are locals of `runHub`; and the memory adapter's `Close` is silent. `startServe`
  retains stdout and now stderr, and `go/serve/registry.go:327`-`:371` builds a configurable
  `executable` for the process-backed kinds, so a **future** test can host a helper that records its
  own EOF or exit through a temporary file or an inherited descriptor and make a skipped
  `CloseSessions` observable **without** a production diagnostic. Closing the gap that way, or with a
  close diagnostic in `go/serve/serve.go`, is follow-up work and is not taken here; this row records
  only that the current control does not cover it. Every layer **below** `runHub` is covered directly in
  `go/serve/session_test.go`, so the sweep's own per-session behaviour is well covered; what is not
  covered is that `runHub` reaches it. The stdio sibling at `serve.go:138` uses the same pattern and
  is **not** covered either; it is recorded here rather than left implied.

The two pre-existing tests — `a body the daemon refused to read is drained before
the socket closes, or the close resets the answer away` and `a drain gives up
rather than waiting on a peer that sends nothing more` — pin the qualitative rule
that a refused body is drained and that a silent peer is given up on. They do not
pin any of the four numbers above. Measured, forty consecutive refused POSTs with
their bodies sent in full all arrive as `403`.

**A read in flight is bounded, and a signal is noticed inside the bound.** The
daemon polls a connection for at most 50 ms at a time and re-checks whether it
should stop between polls, so an interrupt during a slow or stalled request ends
the daemon in well under a second rather than at the end of the request's own
budget. A read also returns **whatever has arrived** rather than waiting for its
buffer to fill: a client that promises 4 KiB and sends 5 bytes must not hold
the daemon until it sends the rest. Zig: `a poll waiting
on a silent peer is re-checked inside its cycle, not held to its deadline` and
`a body that arrives in part is taken as it comes, and never waited on for the
rest`, plus the same two measured against the built binary.

**The trust model is decided on the head, before any body is read.** A
cross-origin or foreign-`Host` request that declares a body and withholds it is
refused as soon as its head is parsed, which is what Go's middleware does — so it
neither spends the 16 MiB scratch on a request the posture exists to refuse, nor
holds one of the bounded serve slots for the idle budget. A body is read only by a route
that will consume one. Zig: `the trust model is decided on the head, so a refused
request never waits on a body it will not read`, and measured: a refused
cross-origin POST that declares 4 KiB and sends 5 bytes is answered `403` in
under a millisecond.

**A `HEAD` is answered with the headers a `GET` would send and no body.** RFC
9110 forbids a body in a HEAD response and net/http suppresses one, so a port
that sends one is wrong rather than merely different — and the `Content-Length`
still says what a `GET` would return, which is the only reason a client sends
a HEAD at all. This holds for a read that failed after the method was parsed
too, so a `HEAD` with a body over the cap gets a bare `413` and a `HEAD` with
a duplicate header gets a bare `400`. Zig: `a HEAD is answered with the length
a GET would send and no body at all` and `a HEAD whose read fails after its
method gets no body either`.

## Cursor and replay

Resume, reconciliation and replay are distinct, and an expired cursor is
`oap-replay-gap` rather than fake continuity.

A cursor is `(run, sequence)`. **Sequences are per-run and restart at 1**, so a
cursor without a run is ambiguous. A request may therefore name the run it was
cut from; an unqualified cursor resolves onto the session's current run, which
is right until a second run is admitted. Naming a settled run replays that
run's suffix — a host that names a run has said exactly which events it wants,
and the run it wants is usually the one that just ended under it.

| rule | pinned by |
| --- | --- |
| A cursor may name its run, and naming a settled run replays it | `TestSSECursorFollowsTheRunItNames`, `TestEventsCursorFollowsTheRunItNames`, `TestRunQualifiedCursorMatchesHTTP` |
| An unqualified cursor resolves onto the current run | `TestEventsCursorWithoutARunStillFollowsTheCurrentOne`, `TestHubCursorResumeBindsRun` |
| `run_id` without a cursor is `invalid_cursor` — a live subscription is always the current run, so a run on a request with no cursor names a position that does not exist | `TestSSERefusesARunWithoutACursor`, `TestEventsRefusesARunWithoutACursor` |
| A run the session never had is `run_not_found` | `TestSSERefusesARunTheSessionNeverHad`, `TestEventsRefusesARunTheSessionNeverHad` |
| `run_id` belongs to the `events` op alone; no other op accepts it | `TestRunIDBelongsToTheEventsOpAlone` |
| A resumed stream delivers the replayed suffix, then live events, and ends at the run's terminal | `TestHubResumeFromCursorMidStream`, `TestSSEReplayAfterCursor` |
| A cursor the journal no longer retains is `oap-replay-gap`, naming what was lost and where to resume | `TestHubReplayGap`, `TestEventsOpReportsAReplayGap`, `TestSSEReplayGap` |
| An overflow names the run it happened on, so a reconnect reaches the right events | `TestHubOverflowSignalNamesOverflowedRun` |
| An overflow during replay seeds the cursor rather than losing it | `TestHubReplayOverflowSeedsCursor` |
| A queue overflow's cursor is where the dropped run's last delivered event ended | `TestQueueOverflowCursorTracksPosition`, `TestQueueOverflowCursorRecoversDroppedRun` |
| Closing a session ends a replaying subscription promptly | `TestHubCloseReplaySubscriptionEndsPromptly` |
| Fan-out reaches every subscriber of a run | `TestHubFansOutToSubscribers` |
| A subscriber's context cancellation ends its subscription | `TestHubSubscriptionContextCancel` |
| A live stream error surfaces to the subscriber as itself | `TestHubLiveStreamErrorSurfaces` |
| An adapter's own stream overflow reaches only the subscribers exposed to that run | `TestHubAdapterStreamOverflow`, `TestAdapterOverflowScopedToExposedSubscribers` — **D7**: the Zig contract cannot say whose stream failed, so a stream failure ends the session |
| Overflow follows the run the subscriber actually read | `TestOverflowFollowsDeliveredRuns`, `TestAdapterOverflowScopesByAcknowledgedRun` — ported for the run a **queue** overflow names (`lossRun`); the per-run scoping half is **D7** |
| An acknowledged position overrides a stale pending one | `TestAcknowledgedPositionOverridesStalePending` — **D7**: a single mailbox has no stale pending position to override |
| A late overflow does not cut a newer run | `TestLateOverflowDoesNotCutNewerRun` — **D7**: a late stream failure ends the whole session |

## Compound open and the held subscription

A host can pipeline `open`, `events` and `submit`, and the subscription then
loses the race with its own submit and silently omits the run's opening
envelopes. `session.open.request` carries two optional members to close that,
per [Decision 0009](../decisions/0009-compound-open.md):

- **`subscribe`** — the hub registers the session's subscription as part of the
  open, before the response is produced. Default false, and the default is
  load-bearing: an unwanted subscription over stdio writes into the same output
  the host must drain, and a host that does not read them fills the writer's
  queue and enters the refusal path.
- **`message`** — an optional first submission, admitted as part of the open.

They are independent. `subscribe` carries no cursor, because the session is
being created and has no journal to replay.

The acknowledgement for `message` rides in the open response's
`state.active_runs`, as `run_id`, `status`, `queue_position` when queued, and
`admitted_submit_requests`. `session.open.response` is the state document
unchanged — no member is added to it.

**Failure is atomic.** A compound open either opens the session and admits the
message, or does neither; a message that cannot be admitted closes the session
again before the refusal is sent. No partial-failure vocabulary exists because
no partial outcome is reachable.

A `subscribe` election takes the ladder every optional feature takes, under the
`session.open.subscribe` key. A compound open **citing** the active capability
revision must cite the current one, or it is refused `stale_capabilities`. An
open that cites none is admitted and answered with the revision it was admitted
under — the citation is checked, never required.

| rule | pinned by |
| --- | --- |
| The open carries its message, and the state names the run it admitted | `TestOpenOpCarriesItsMessage`, `TestOpenCarriesItsMessage`, `TestOpenCarryingAQueuedMessageReportsItsQueuePosition` |
| The subscription registers before the message runs, so it misses nothing | `TestOpenOpSubscribesBeforeItsMessageRuns` |
| An open without `subscribe` streams nothing | `TestOpenOpWithoutSubscribeStreamsNothing` |
| A message that cannot be admitted rolls the session back | `TestOpenRollsBackWhenItsMessageCannotBeAdmitted` |
| An unschematic message is refused at the gate | `TestOpenCarryingAnUnschematicMessageIsRefusedAtTheGate` |
| `subscribe` against an endpoint that never advertised it is refused | `TestOpenRefusesSubscribeAgainstAnEndpointThatNeverAdvertisedIt` |
| A degraded `subscribe` is refused without the opt-in, and admitted with it | `TestSharedGateRefusesADisclosureAnOpenCannotElect` |
| An open citing a stale revision is refused `stale_capabilities`, naming both revisions; an open citing none is admitted under the revision it was gated with | `TestAttachingOpenPinsOnlyWhatItCites` (both halves, end to end on HTTP); `TestOpenOpRefusesAStaleRevisionOnTheSubscribePath` (the same comparison on the `subscribe` path, and the stdio op's own mapping of it) |
| An open response that cannot be encoded rolls the session back and says so | `TestOpenRollsBackWhenItsResponseCannotEncode`, `TestAnOpenThatCannotEncodeIsRolledBack` |
| An open whose id the host named is **kept** when its response cannot be framed, and the refusal says which | `TestAnOpenTheHostNamedIsKeptAndSaidSo` |
| A submit acknowledgement that will not frame rolls its run back and names the outcome | `TestSubmitRollsBackUnframableAcknowledgement`, `TestSubmitRollbackWaitsForSettlement` |

### The held subscription

A transport whose response carries no stream holds the subscription. The SSE
route has no such shape: `POST /adapters/{name}/sessions` answers with one
response body and cannot also carry a stream. So the route registers the
subscription during the open and **holds** it for the `events` request that
follows, which adopts it instead of subscribing anew. The host sees what
stdio's host sees: a stream beginning at the run's first envelope.

A held subscription is bounded at **30 s**. It is released if no `events`
request adopts it within that window, if the open's own response cannot be
delivered and the session is rolled back, or if the adopting request ends. An
`events` request carrying a cursor does **not** adopt it — that host is
resuming a stream it already had and says so by naming a position — so the held
subscription is released and the cursor served as it always was. What the hub
buffers while holding is its ordinary bounded mailbox, so a host that opens and
never connects overflows exactly as a slow consumer does, and the adopter reads
`oap-overflow` with the cursor to resume from.

| rule | pinned by |
| --- | --- |
| A held subscription is adopted by the adopting `events` request | `TestOpenSubscriptionIsAdoptedByTheEventsRequest` |
| A held subscription nothing adopts is released | `TestOpenSubscriptionNotAdoptedIsReleased` |
| The hold is bounded, and expiry releases it | `TestOpenSubscriptionNotAdoptedIsReleased` (a 50 ms hold, polled to release) |
| The default hold is 30 s, and an explicit window still wins | `TestASubscriptionIsHeldForThirtySecondsByDefault` |

## The operations

Thirteen ops. Each row gives the request line's parameters, the answer, and the
errors. A refusal the row does not name is `internal` on both, with two
exceptions stated once here rather than repeated per row:

- **The stdio transport answers four codes no HTTP route can.** `unknown_op` for
  an op it does not serve, `invalid_request` for a parameter an op does not
  define, `busy` for the admission and subscription bounds, and
  `response_too_large` for a result its frame limit cannot carry. All four
  exist because a pipe gives no back pressure and has one shape a socket does
  not; a port must not produce any of them over HTTP.
- **The HTTP body gate answers three codes no operation owns.**
  `unsupported_media_type`, `request_too_large` and `request_read`, stated once
  in [the HTTP rules](#the-http-routes-and-sse-framing). `request_too_large` is
  the one of the three stdio answers too, for the same 16 MiB budget.

**A session that is not open is released, not kept** ([Decision 0039](../decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md)).
A session stops being open when its `close` succeeds, or when its adapter
reports the session closed. A run's stream failing does not end the session:
it is reported against that run, and the session takes the next submit. From
then on every op naming the session answers `unknown_session` (404), a second
`close` included, and `sessions` no longer lists it. `session_closed` (409) in
the rows below is a session that stops being open while the request is in
flight.

### `adapters`

- **params:** none.
- **answer:** `{"adapters":[{"name":…,"capability_revision":…,"capabilities":{…}}]}`,
  where an adapter whose probe fails or carries no revision is listed with an
  `error` member instead of the other two.
- **errors:** `invalid_request` (a parameter was supplied).
- **pinned by:** `TestAdaptersOpListsRegistry`, `TestAdaptersOpRefusesParams`,
  `TestAdaptersListing`, `TestListingsMatchHTTP`.

### `capabilities`

- **params:** `adapter` (required). Over HTTP the name is the path segment.
- **answer:** a `capabilities.response` envelope carrying the probed descriptor
  and its revision.
- **errors:** `invalid_request` (no `adapter`, or a parameter the op does not
  define), `unknown_adapter`, `probe_failed`, `internal`.
- **pinned by:** `TestCapabilitiesEndpoint`, `TestCapabilitiesRequiresDescriptorRevision`,
  `TestOpErrorCodesMirrorHTTP`, `TestListingsMatchHTTP`,
  `TestCapabilitiesOpReportsAProbeFailure`,
  `TestCapabilitiesOpReportsAnUnencodableDescriptorAsInternal` — the last two
  drive the op's own `probe_failed` and `internal` mappings, the second by
  making the response impossible to encode.

#### What `open` decides, and in what order

The row above lists the codes but not the order, and the order is observable: a
request that is refused twice has one answer. **This is the spec both trees answer
to**, written here because Go's behaviour is evidence and not authority (Decision
0032). Each step below is pinned by the Go test named against it.

| # | decided | codes | pinned by |
| --- | --- | --- | --- |
| 1 | the nested envelope | `invalid_request`, `request_too_large`, `malformed_json`, `schema_invalid`, `type_mismatch` | `TestOpenOpRefusals` |
| 2 | the adapter exists | `unknown_adapter` | `TestOpenOpRefusals` |
| 3 | the **attachment** cites a current revision and is elected | `stale_capabilities`, `unsupported_feature`, `capability_degraded` | `TestAttachingOpenPinsOnlyWhatItCites` (both halves, end to end on HTTP), `TestOpenOpRefusesAStaleRevisionOnTheSubscribePath` |
| 4 | the **subscription**, same two checks, on `session.open.subscribe` | `stale_capabilities`, `unsupported_feature`, `capability_degraded` | `TestOpenOpRefusesAStaleRevisionOnTheSubscribePath`, `TestSharedGateRefusesADisclosureAnOpenCannotElect` |
| 5 | the adapter's own refusals, for anything else the request elected | `unsupported_feature`, `capability_degraded`, `probe_failed` | `TestHubOpenRejections`, `TestOpenRefusalsAreBounded` |
| 6 | the id is free | `session_exists` | `TestHubOpenRejections` |
| 7 | anything else the adapter reports | `session_closed`, `open_failed` | `TestOpenSession`, `TestHubOpenClosesSessionWhenStateFails` |

**Two rules the ordering makes explicit, and both are places the trees had
diverged:**

- **The attachment is gated before the subscription**, because the attachment is
  the larger ask and a request that both subscribes and attaches is refused for
  the attachment first.
- **The adapter's own refusals come before `session_exists`** (D15). A host is told
  its id is taken when the real reason its request cannot be served is that the
  adapter will not attach what it asked for — and correcting the name does not
  help, so the actual refusal is never reported. The duplicate is detected by
  running the adapter and seeing what it made, not by looking the name up first.

#### The `details` a refusal carries, and the `reason` vocabulary

`details` is a wire member, so this table is the contract for both trees — not a
description of what Go happens to emit. **The `reason` set is closed**: a tree that
needs a value outside it has a spec question, not a code change.

| code | `details` carries | `reason` may be |
| --- | --- | --- |
| `unsupported_feature` | `feature`, `reason`, and `tool` or `field` or `source` where the refusal names one | `unadvertised`, `unsatisfiable` |
| `capability_degraded` | `feature` | — |
| `stale_capabilities` | `expected_revision`, `current_revision` | — |
| `model_not_found` | `model_id`, the reference the request named | — |

`expected_revision` is **the adapter's** revision and `current_revision` is **the
one the request cited**, which is the pair that makes the refusal actionable. The
hub has to report both, so the refusal cannot be a bare error from a set with
nowhere to put a reason (D12).

### `open`

- **params:** `adapter` (required) and `request`, and nothing else.
- **answer:** a `session.open.response` envelope carrying the whole state
  document, with `in_reply_to` set to the request envelope's `id` and
  `capability_revision` set to the revision the open was gated under.
- **errors:** `invalid_request`, `malformed_json`, `schema_invalid`,
  `type_mismatch`, `invalid_payload` (a `metadata` value that is not JSON, or a
  payload that will not decode), `unknown_adapter` (404), `session_exists`
  (409), `stale_capabilities` (409, with `expected_revision` and
  `current_revision` in `details`), `model_not_found` (400, with `model_id` in
  `details`, an `oapx` open whose `metadata.oapx.model` its catalog lacks),
  `unsupported_feature` (400, for a tool
  source it will not attach), `capability_degraded` (400, for a feature the
  request did not opt into), `session_closed` (409, a session that was already
  closed when the open probed it), `probe_failed`, `open_failed` (502),
  `request_cancelled`, `internal` — and, when the request set `subscribe`, the
  subscription bound can refuse this op specifically.
- **reopen:** a request setting `reopen` is gated on `session.open.reopen` as
  `subscribe` is on its key. One naming a session still open is `session_exists`,
  checked before the adapter is asked. A hub records, for every session it
  opens, the adapter and the native id the session reports (Go in its binding
  store, Zig in memory for the hub's lifetime). A reopen with no record, or a
  record naming another adapter, is `unknown_session` (404) without asking the
  adapter; otherwise the adapter is handed the recorded native id, and a record
  the adapter can no longer load is `unsupported_feature` (400) naming
  `session.open.reopen`, because the code comes from the record's existence and
  not from the adapter's reply. A successful reopen's state declares
  `recovery.recovered`, and a binding store records it as `reopened`. Close is an
  ordinary close of the adapter's session in both trees, so an adapter that keeps
  what it closed can be asked to reopen it. Pinned by
  `TestAReopenIsRecordedAsReopenedAfterTheClose`,
  `TestAReopenOfALiveSessionIsSessionExistsBeforeTheAdapterIsAsked`,
  `TestTheElectionGateRefusesAReopenTheAdapterDoesNotAdvertise`,
  `TestAReopenHandsTheAdapterTheNativeIDItsBindingRecorded`,
  `TestAReopenAfterARestartTheAdapterCannotLoadIsUnsupportedFeature` and the Zig
  hub's `a closed session reopens through the hub once`, `a reopen hands the
  adapter the native id its open recorded` and `a reopen the hub holds a record
  for but the adapter has lost` tests.
- **pinned by:** `TestOpenOpOpensASession`, `TestOpenOpRefusals`,
  `TestOpenRefusalsAreBounded`, `TestHubOpenRejections`,
  `TestHubOpenDefaultsParticipant`, `TestHubOpenClosesSessionWhenStateFails`,
  `TestOpenSession`, `TestOpenSessionAssignsIdentifier`, `TestOpenResponseCarriesTheWholeState`,
  `TestOpenOpMatchesHTTP`, `TestAttachingOpenPinsOnlyWhatItCites`,
  `TestHubOpenRefusesASessionThatWasAlreadyClosedAndLeavesItsIDFree` and
  `TestTheHubReleasesASessionTheAdapterReportsClosed`. The release through an
  open is pinned: a session its adapter confirms closed is never kept, its id is
  free at once, and the open itself answers `session_closed`.

### `sessions`

- **params:** none.
- **answer:** `{"sessions":[{"session_id":…,"adapter":…,"status":…,"active_run_id":…,"active_runs":[…],"created_at":…}]}`,
  sorted by session id. Only live sessions are listed: a closed session is
  released. `created_at` is **RFC 3339 in UTC at whole-second precision**, with a
  trailing `Z` and no fractional part — `2023-11-14T22:15:23Z`, which is what Go's
  `time.RFC3339` writes. A sub-second remainder is truncated, not rounded, so two
  trees listing the same session at the same instant write the same byte.
- **errors:** `invalid_request` (a parameter was supplied).
- **pinned by:** `TestSessionsOpListsTrackedSessions`, `TestSessionsListingAcrossLifecycle`,
  `TestListingsMatchHTTP`, `TestHubSessionsListingAcrossAdapters`. The release is
  pinned through the listing: `TestSessionsListingAcrossLifecycle` finds a closed
  session absent, `unknown_session` afterwards, and reopens the same id, and
  `TestHubSessionsListingAcrossAdapters` leaves the closed one out while the live
  one is still listed.

### `state`

- **params:** `session_id`.
- **answer:** a `session.state.response` envelope carrying the whole state
  document, with a daemon-minted `in_reply_to`.
- **errors:** `unknown_session` (404), `invalid_request`, `state_failed` (500),
  `internal` (500).
- **pinned by:** `TestStateEndpoint`, `TestStateReportingClosedClosesEntry`,
  `TestOpErrorCodesMirrorHTTP`,
  `TestTheHubReleasesASessionTheAdapterReportsClosed`. The release is pinned
  through the state op: the in-flight request still answers `200` with the
  session's final closed document, as `state` always has for a session its
  adapter reports closed, and the *next* `state` on the same id is
  `unknown_session` because the entry is gone. A port that answered
  `409 session_closed` to the in-flight request would diverge from both trees
  here.

### `models`

- **params:** `session_id` and `allow_degraded_features` (a list of feature
  keys). Over HTTP the opt-in is the repeatable query parameter
  `?allow_degraded=<key>`; the two spellings are one request.
- **answer:** a `models.response` envelope carrying the catalog, stamped with
  the revision the lister served it under.
- **errors:** `unknown_session` (404), `invalid_request`,
  `unsupported_feature` (400, an adapter with no `models.list`), `capability_degraded`
  (400, without the opt-in), `model_not_found` (400), `scope_mismatch` (400),
  `session_closed` (409), `request_cancelled` (400), `internal` (500).
- **pinned by:** `TestModelsOpServesTheCatalog`, `TestModelsOpRefusesAMisscopedCatalog`,
  `TestModelsRoute`, `TestModelsRouteCarriesTheDegradedOptin`,
  `TestModelsRouteRefusesAnAdapterWithoutACatalog`,
  `TestModelsRouteStampsTheListersRevision`,
  `TestModelsRouteRefusesAnUnlabelledCatalog`,
  `TestModelsRouteRefusesAMisscopedCatalog`,
  `TestModelsRouteServesAnEmptyCatalogAsAnEmptyList`.

### `tools`

- **params:** `session_id` and `allow_degraded_features`, as for `models`.
- **answer:** an `action.tools.list.response` envelope carrying the catalog,
  stamped with the revision the lister served it under.
- **errors:** `unknown_session` (404), `invalid_request`, `unsupported_feature`
  (400, an adapter with no tool catalog, with `feature` and `reason` in
  `details`), `capability_degraded` (400), `tools_failed` (502), `session_closed`
  (409), `request_cancelled` (400), `internal` (500).
- **pinned by:** `TestToolsOpMatchesHTTP`, `TestToolsRouteServesTheSessionCatalog`,
  `TestToolsRouteRefusesAnEndpointWithNoCatalog`, `TestHubRefusesAMisScopedToolCatalog`,
  `TestHubRefusesACatalogItCannotBindToADescriptor`, `TestHubRepairsAnAbsentToolList`.

### `submit`

- **params:** `session_id` and `request`, and nothing else.
- **answer:** the response matching the request's type, carrying the
  admission, with `in_reply_to` set to the request envelope's `id` — a
  `session.message.submit.response` for a `session.message.submit.request`, and
  a `session.compact.response` for a `session.compact.request`
  ([Decision 0044](../decisions/0044-compaction.md)). Both are accepted on the
  same op, and the request's type selects the path, as on `resolve`: a
  compaction is admitted as a run under submit's rules, so it shares submit's
  errors, its ordering behind the response, and its rollback when the
  acknowledgement cannot be framed. A session whose adapter cannot compact
  answers `unsupported_feature` naming `session.compact`.
- **errors:** `unknown_session` (404), `invalid_request`, `malformed_json`,
  `schema_invalid`, `type_mismatch`, `invalid_payload`, `scope_mismatch` (400,
  a payload naming another session), `run_active` (409), `invalid_submission`
  (400), `unsupported_feature` (400), `capability_degraded` (400),
  `model_not_found` (400), `session_closed` (409), `request_cancelled` (400),
  `internal` (500).
- **pinned by:** `TestSubmitRejections`, `TestOpErrorCodesMirrorHTTP`,
  `TestQueuedSubmissionRoundTrips`, `TestQueuedSubmissionOverStdio`,
  `TestSubmitRollsBackUnframableAcknowledgement`,
  `TestSubmitRollbackWaitsForSettlement`, `TestSubmitErrorStreamStillDrains`,
  `TestRejectedSubmitKeepsSubscriptions`, `TestRequestBudgetMatchesHTTP`; the
  compaction path by `TestCompactionIsAdmittedOnTheSubmitRouteAndItsRunReachesSubscribers`,
  `TestACompactionTheAdapterRefusesAnswersWithTheSubmitRoutesRefusal`,
  `TestASessionThatCannotCompactRefusesTheRequestNamingTheFeature`,
  `TestCompactionIsAdmittedOnTheSubmitOpAndItsRunReachesSubscribers` and
  `TestACompactionTheAdapterRefusesAnswersWithTheSubmitOpsRefusal`, and in
  Zig's HTTP hub by the daemon's two compaction tests.

### `resolve`

- **params:** `session_id` and `request`, and nothing else.
- **answer:** the response matching the request's type —
  `action.permission.resolve.response` for a permission gate,
  `user.input.resolve.response` for a user-input gate, and
  `action.call.resolve.response` for a client-provided tool call. All three are
  accepted on the same op, and the request's type selects the path.
- **errors:** `unknown_session` (404), `invalid_request`, `malformed_json`,
  `schema_invalid`, `type_mismatch`, `invalid_payload`, `scope_mismatch` (400,
  a payload naming another session), `run_not_found` (404),
  `resolution_rejected` (409, an interaction that is not found, already
  resolved, answered by the wrong responder, or refused as invalid),
  `unsupported_feature` (400, a session with no `CallResolver`), `session_closed`
  (409), `request_cancelled` (400), `internal` (500).
- **pinned by:** `TestResolveRejections`, `TestUnadvertisedControlRefusalKeepsItsWireShape`,
  `TestResolveOpReportsAnUnknownRun`, `TestResolveOpReportsARefusedResolution`
  — the last two drive the stdio `resolve` op's own `run_not_found` and
  `resolution_rejected` mappings, which until now only the HTTP route
  covered.

### `cancel`

- **params:** `session_id` and `request`, and nothing else.
- **answer:** a `run.cancel.response` envelope. It is **intent, not settlement**:
  a run settles `cancelled` only once an accepted cancel exchange is the
  evidence, so the host watches for `run.cancelled` on its subscription.
- **errors:** `unknown_session` (404), `invalid_request`, `malformed_json`,
  `schema_invalid`, `type_mismatch`, `invalid_payload`, `scope_mismatch` (400,
  a payload whose `session_id` is not the addressed session), `run_not_found`
  (404), `run_terminal` (409, a run that already settled), `session_closed`
  (409), `request_cancelled` (400), `internal` (500).
- **pinned by:** `TestCancelRejections`, `TestOpErrorCodesMirrorHTTP`,
  `TestCloseAfterCancellation`.

### `settings`

- **params:** `session_id` and `request`, and nothing else.
- **answer:** a `session.settings.update.response` envelope reporting the
  reasoning level and compaction policy now in force, from the session's
  adapter, which judges the update against its own descriptor. The hub adds
  nothing: it refuses an update for a session it does not hold or whose payload
  names another session, and an update to an adapter with no settings update at
  all is `unsupported_feature` naming the first setting it carries. The
  `session.state.updated` an endpoint publishes after the response has nowhere
  to go, because the hub's streams are a run's; a host reads `state`.
- **errors:** `unknown_session` (404), `invalid_request`, `malformed_json`,
  `schema_invalid` (400, which is also the answer to an update naming no
  setting, since the schema requires one), `type_mismatch`, `invalid_payload`,
  `scope_mismatch` (400), `unsupported_feature` and `capability_degraded`
  (400), `run_active` (409, a run in flight), `session_closed` (409),
  `request_cancelled` (400), `internal` (500).
- **pinned by:** `TestSettingsOpMatchesHTTP`; Zig: `a settings update is
  answered on its own route, and one naming nothing, an unadvertised setting or
  an absent session is refused` and `a settings update reaches the session's
  adapter, and one naming nothing or another session is refused before it`.
  `oapx hub --stdio` serves the op through the same `Frontend.settingsControl`
  the daemon route runs.

### `close`

- **params:** `session_id`. Over HTTP the id is the path segment and no body is
  read.
- **answer:** `{"id":N,"ok":true,"result":null}` over stdio; `204 No Content`
  with no body over HTTP.
- **errors:** `unknown_session` (404), `invalid_request`, `run_active` (409, a
  run still in flight — a host cancels first), `session_closed` (409, a session
  that closes while the request is in flight), `request_cancelled` (400),
  `internal` (500). A second `close` is `unknown_session`, because the first
  released the session; a host that retries a close whose answer it lost treats
  that as done.
- **pinned by:** `TestSessionsListingAcrossLifecycle`, `TestHubSessionCloseSemantics`,
  `TestListingsMatchHTTP`, and `TestOpErrorCodesMirrorHTTP` for both of a
  `close`'s refusals — `run_active` on a running session and `unknown_session`
  on a second one. The release is pinned through a `close`:
  `TestHubSessionCloseSemantics` finds the session absent from the listing and
  the hub answering `unknown_session` for it.

### `events`

- **params:** `session_id`, `after` (an unsigned sequence) and `run_id`, and
  nothing else. `after` may be JSON `null`, which is the same as absent.
- **answer:** `{"id":N,"ok":true,"result":null}` as the acknowledgement, always
  before the first envelope, then the subscription lines above. Over HTTP the
  acknowledgement is the empty body of a `200` whose headers have already been
  flushed.
- **errors:** `unknown_session` (404), `invalid_request` (a parameter the op
  does not define), `session_closed` (409, a session that closes while the
  request is in flight; one already closed is `unknown_session`), `invalid_cursor` (400, a cursor that is not an
  unsigned sequence, or a `run_id` with no cursor), `replay_cursor_future` (400),
  `run_not_found` (404), `no_run_to_resume` (409, a cursor on a session with no
  run to replay), `request_cancelled`, `internal` (500). A **replay gap is not
  an error**: the op acknowledges `null`, then writes `oap-replay-gap` and ends
  the subscription.
- **pinned by:** `TestEventsOpDeliversTheRunStream`,
  `TestEventsAcknowledgementPrecedesTheStream`, `TestEventsOpRefusals`,
  `TestEventsOpReportsAReplayGap`, `TestEventsReportsWhereALateSubscriptionJoined`,
  `TestEventsReportsNoJoinPointWhenNothingPrecededTheSubscription`,
  `TestEventsCursorFollowsTheRunItNames`,
  `TestEventsCursorWithoutARunStillFollowsTheCurrentOne`,
  `TestEventsRefusesARunWithoutACursor`, `TestEventsRefusesARunTheSessionNeverHad`,
  `TestRunIDBelongsToTheEventsOpAlone`, `TestSubscriptionsAreBounded`,
  `TestSSECursorErrors`, `TestSSEReplayGap`, `TestSSENoRunToResume`,
  `TestSSEUnknownSession`, `TestSSEOnClosedSession`, `TestEventsOpRefusals`
  (the `busy` refusal at the subscription ceiling, whose message names the
  ending paths rather than implying the host can bring one about). The release
  is pinned: `TestEventsOpRefusals` answers a session that has been released
  with `unknown_session`, apart from the one that never existed, and
  `TestSSEOnClosedSession` answers a live stream and a cursor stream on a
  released session with `404 unknown_session` rather than `409 session_closed`.

## The clients are the far-side proof

`go/client` and `clients/ts` drive this wire as a consumer would, and their
tests are the strongest evidence the rules above hold. They are **not** the
specification: where a client refuses something this document permits, the
client is what a real host does and a port must satisfy it too.

The `clients/ts` integration suite runs against **both** hubs: `ci.yml` builds
`goap hub`, and the `hub-clients-ts` job in `ci-zig.yml` runs the same suite
unchanged against the built `oapx hub`, selected by `OAP_TS_HUB`. It covers a
whole lifecycle with both gates answered, a queued run, a reconnect after a
client drops its stream mid-run (G2, #399), and a request body cut short by a
half-close, which both hubs refuse `400 request_read` (G10).

| rule | pinned by |
| --- | --- |
| A whole lifecycle — discovery, open, submit, gates, terminal, close — round-trips | `TestClientLifecycleGolden`, `TestClientDiscovery` |
| An open naming no id is answered with one, and one naming another session is refused | `TestClientOpenGeneratesSessionID`, `TestClientRejectsAnOpenResponseThatNamesAnotherSession`, `TestClientRejectsAnOpenResponseWhoseEnvelopeNamesNoSession` |
| An unknown adapter is refused | `TestClientOpenUnknownAdapter` |
| Sources attach by id and the session's catalog reads back | `TestClientAttachesSourcesAndReadsTheCatalog`, `TestClientRejectsACatalogItCannotBindToADescriptor` |
| A resume after a drop is **invisible**: a client that holds its cursor sees no gap | `TestClientInvisibleResumeAfterDrop`, `TestClientResumeAfterRunSettled`, `TestClientEventsAfterSuffix` |
| A speculative reconnect between a drop and the resume loses nothing | `TestClientSpeculativeReconnectLosesNothing` |
| A client that insists on strict resume is told the drop rather than healed | `TestClientStrictResumeReportsDrop` |
| A stream against an unknown session is refused | `TestClientUnknownSessionStream` |
| An overflow signal's cursor is trusted over what the client had read | `TestClientOverflowSignal`, `TestClientOverflowCursorTrustsSignal` |
| A replay gap surfaces as a typed error, and a gap after a zero cursor is still refused | `TestClientReplayGapSignal`, `TestClientRejectsGapAfterZeroCursor` |
| A bad resume suffix is refused rather than silently accepted | `TestClientRejectsBadResumeSuffix` |
| A run that changed under the cursor is detected, not spliced | `TestClientRunChangedUnderCursor`, `TestClientRejectsRunMismatchOnManualResume` |
| A live sequence gap, a duplicate sequence, a zero sequence and an unsequenced envelope are each refused | `TestClientRejectsLiveSequenceGap`, `TestClientRejectsDuplicateSequence`, `TestClientRejectsZeroSequence`, `TestClientRejectsUnsequencedEnvelope` |
| Foreign-session events, a late run start and a misrouted answer are each refused | `TestClientRejectsForeignSessionEvents`, `TestClientRejectsLateRunStart`, `TestClientRejectsMisroutedGetResponses`, `TestClientRejectsMisroutedPostResponses` |
| An answer the client did not ask for, or one out of scope, is refused | `TestClientRejectsUncorrelatedResponse`, `TestClientRejectsUncorrelatedErrorEnvelope`, `TestClientRejectsOutOfScopeResponse`, `TestClientRejectsAnUnscopedAnswerToItsScopedCatalogRequest` |
| An error envelope scoped to another session or run is refused, and a correctly scoped one surfaces | `TestClientRefusesAnErrorEnvelopeScopedToAnotherSession`, `TestClientRefusesAnErrorEnvelopeScopedToAnotherRun`, `TestClientSurfacesACorrectlyScopedErrorEnvelope` |
| An error envelope carrying no code is surfaced as a plain failure rather than a protocol one | `TestClientErrorCodeAbsentForPlainFailures` — the rule every refusal now obeys, the `Host` refusal included |
| A stream answering with a non-200, a non-`text/event-stream` content type, or a non-204 close is refused | `TestClientRejectsNon200StreamStatus`, `TestClientRejectsNonEventStreamContentType`, `TestClientRejectsNon204Close` |
| The SSE parser reads the framing as specified: a simple frame, field rules, comments and keepalives, multiple `data` lines, a named event with an `id`, a `NUL`-bearing id, a leading BOM, either line terminator, an unterminated tail, and a dispatch with no data | `TestScanSSESimpleFrames`, `TestScanSSEFieldRules`, `TestScanSSECommentsAndKeepalives`, `TestScanSSEMultiLineData`, `TestScanSSENamedEventAndID`, `TestScanSSEIDWithNULDiscarded`, `TestScanSSELeadingBOM`, `TestScanSSELineTerminators`, `TestScanSSEUnterminatedTailDiscarded`, `TestScanSSENoDataNoDispatch`, `TestScanSSEStopsWhenHandlerDeclines` |
| A mid-run join is accepted, and the client is told where it joined | `TestClientMidRunJoinAccepted` |
| A queued submission round-trips, and a close with a live run is refused | `TestClientQueuedSubmission`, `TestClientCloseRefusesActiveRun` |
| Cancel, run controls and submit scoping each round-trip or refuse as specified | `TestClientCancelPath`, `TestClientDrivesRunControls`, `TestClientSubmitScopeMismatch` |
| Two clients on one hub hold distinct request ids | `TestClientRequestIDsUniqueAcrossClients` |
| Every error response is validated like any other envelope | `TestClientValidationAppliesToEveryErrorResponse`, `TestClientValidationRejectsInvalidErrorEnvelope`, `TestClientValidationRejectsIdlessErrorEnvelope`, `TestClientValidationRejectsInvalidEnvelope` |
| An event whose payload leaves its envelope is refused | `TestClientRejectsAnEventWhosePayloadLeavesItsEnvelope` |
| Malformed frames are refused | `TestClientRejectsMalformedFrames` |
| An error envelope the request scoped to no run passes through | `TestClientLetsAnErrorPassWhenTheRequestNamesNoRun` |
| The models listing carries the degraded opt-in and its revision | `TestSessionModels`, `TestModelsOptionAddsTheDegradedOptin`, `TestModelsRejectsAnUnlabelledCatalog` |

## Fan-out

One run's stream feeds every subscriber, and each subscriber gets its own
bounded mailbox. Three rules make the fan-out safe, and they are the hub's
whole reason for existing:

1. **Fan-out is per subscription, not per session.** Any number of subscribers
   may attach to one session; each has its own mailbox and its own cursor.
   Pinned: `TestHubFansOutToSubscribers`, `TestHubConcurrentSessions`.
2. **A subscriber that falls behind is dropped, not waited on.** When its
   mailbox is full the hub detaches it and ends it with `oap-overflow` naming
   the run and the last sequence it read. The run is never blocked on a slow
   consumer. Pinned: `TestHubSubscriptionQueueOverflow`,
   `TestHubSubscriberQueueOverflow`, `TestQueueOverflowIncludesCurrentRun`,
   `TestQueueOverflowPrefersAttachedRun`, `TestQueueOverflowPrefersNewerQueuedRun`,
   `TestQueueOverflowPreservesNewerRun`, `TestOverflowRecoversFromTheLiveRunNotASettledReservation`,
   `TestSSEOverflowSignalLive`, `TestSSEOverflowSignalReplay`. The candidate set and
   the preference are ported in Zig as `lossRun`, which considers the dropped run,
   every run with a queued event, the run the subscription is reading and the
   session's current run, preferring a run the session has not finished; the cursor
   is the position the client stopped at after draining rather than where the queue
   filled. Pinned in Zig by the four overflow tests in `zig/src/hub/hub.zig`.
   **D7** covers what a *stream* overflow cannot do here.
3. **An overflow is scoped to the run the subscriber was actually reading.** A
   subscriber that has moved on is not told about an earlier run's overflow, and
   one still reading it is. Pinned: `TestOverflowFollowsDeliveredRuns`,
   `TestAdapterOverflowScopedToExposedSubscribers`,
   `TestAdapterOverflowScopesByAcknowledgedRun`, `TestNewerPendingRunExposesOverflow`,
   `TestAcknowledgedOrderStaysMonotonic`, `TestExposureByAdmissionOrder`.

A subscription ends at exactly one of: its run's terminal envelope, an overflow,
a stream failure, its session closing, the frontend tearing down, or — over
HTTP — the client's hangup. **Over stdio there is no host-initiated way to end
one subscription**, and that is a decision rather than an omission: see
[G3](#known-gaps), where [#53](https://github.com/lsm/open-agent-protocol/issues/53)'s
three options are weighed and the third is taken in both trees.

The rest of the fan-out is about the runs, not the subscribers: an admitted run
is ordered by admission, a run that settles is remembered as settled, a queued
run is promoted when the started one settles, and a rejected submit does not cut
a session's subscribers. A lost stream is reported against **its own** run, and
a second submit does not open a second reader over one run. Pinned:
`TestHubConcurrentSessions`,
`TestALostStreamNamesItsOwnRun`, `TestASecondSubmitDoesNotDuplicateTheStream`,
`TestSubmitReservationBridgesAdmission`, `TestSubmitReservationReleasesOnFailure`,
`TestDeferredFinishSurvivesLaterReservations`, `TestDeferredFinishSparesLaterSubscribers`,
`TestDeferredRunEndSparesLaterSubscribers`, `TestRejectedSubmitKeepsSubscriptions`,
`TestTerminalSubmitErrorClosesEntry`, `TestOverlappingReadersDeliverCurrentRunEnd`,
  `TestMarkClosedDefersFinishToReader`, `TestMarkClosedFinishesImmediatelyWithoutReader`,
`TestEmptyErrorStreamKeepsSubscriptionsParked`,
`TestCloseDuringEmptyErrorStreamEndsSubscribers`,
`TestCloseEndsSubscribersRegisteredAfterDeferredRun`, `TestEmptyOrphanAppliesDeferredRunEnd`,
`TestOrphanBecomesCurrentOverCompletedRun`, `TestCloseGivesNewcomersCleanEndOverDeferredError`,
`TestCloseOverReservationErrorSplitsCohorts`, `TestDeferredEndDoesNotClobberOverflowTerminal`,
`TestDeferredErrorSurvivesNewAdmission`, `TestStaleRunErrorReachesExposedSubscribers`,
`TestAcknowledgedRunStaysPairedWithSerial`, `TestAttachmentRunOrdersPendingExposure`.

## Shutdown

Every exit closes every session, so a child agent process is never orphaned —
including a failed exit. A **restart ends every harness process**, and with it
every live session. [Decision 0039](../decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md) makes a session
outlive its process, but reopening one is staged as T7, so until it lands a
reconnecting client finds no session.

`CloseSessions` divides its window across the sessions it still has to close, so
one stuck child cannot consume the whole budget and leave the rest orphaned. A
session whose run is still active is cancelled before it is closed, up to three
attempts, because a harness that refuses `Close` while a run is in flight must
first be asked to stop.

**One rule here is where the trees differ in kind, not in answer.** Go's
`CloseSessions` can only wait: it holds a context, so a session it is waiting on
gives the budget back. The Zig sweep is not waiting on anything — a Zig
`Session.close` is synchronous — so its bound is a deadline checked per session
rather than a cancellation, and the wait it does do between two attempts is the
adapter's own `pump`, capped at the share that session was given. A sweep that
has used its window stops and says so, and a session that refused every attempt
is **torn down** rather than left open: a refusal is a live run the cancel did
not settle, and a child that outlived three cancels inside a bounded window is
reaped rather than orphaned. That last step is a consequence of freeing one's
own memory, where Go exits the process and the operating system reclaims it.

A sweep reports the two ways it can come up short **separately**, because they
are different facts about the same run: a session it *attempted* and that
refused every attempt, and a session it *never reached* because the window was
already spent. The second is reachable and it is the honest cost of the
difference above — a Zig session's `state` and `cancel` are not cancellable, so
a slow one overruns the share it was given and the sessions behind it are left
for the next exit. Go's context cancels the same call, which is why Go's
`CloseSessions` can only ever be short by refusal.

| rule | pinned by |
| --- | --- |
| Every session is attempted, not just the first | `TestCloseSessionsAttemptsEverySession`; Zig: `closeSessions settles every session` |
| The budget is split per session | `TestCloseSessionsSplitsBudgetPerSession`; Zig: `one wedged session is given a share of the window, and the session beside it is still closed` |
| The total sweep is bounded | `TestCloseSessionsBoundsTotalSweep`; Zig: `the sweep waits no longer than the window it was given` |
| Active runs are settled before a close | `TestCloseSessionsSettlesActiveRuns`; Zig: `the shutdown sweep cancels a live run before it releases the session` |
| A close that refuses because a run is active is retried through a cancel | `TestCloseRetriesThroughAsyncCancel`; Zig: `a close that refuses while a run is live is retried through a cancel until it lands` |
| A close that never stops refusing is given the attempts, and the session is torn down and reported | Zig: `a close that never stops refusing is given the attempts the draft names, and the session is torn down rather than left behind` — no Go test, because Go's answer is that its entry is left and the process exits |
| A sweep that ran out of window before a session says so, and separately from a refusal | Zig: `a session whose own state call overruns its share leaves the rest unattempted, and the sweep says so`. No Go counterpart: Go's context cancels the call, so the case cannot arise |
| A close stops at its context deadline | `TestCloseStopsAtContextDeadline`; Zig: the same per-session deadline, `the sweep waits no longer than the window it was given` |
| The reservations a snapshot names are cancelled | `TestCloseCancelsReservationsTheSnapshotNames` |
| Every run a snapshot lists is cancelled | `TestCloseCancelsEveryRunTheSnapshotLists` |
| A run named only as the active run is still cancelled | `TestCloseFallsBackToTheNamedActiveRun` |
| The window is 10 s for the hub, 5 s per stdio stage | every shutdown test drives a short custom window (`TestShutdownBoundedWhileWorkerStuck`, `TestTeardownStopsWhenAWriteParksForever`); **gap G8** — only the defaults are unpinned |
| SIGINT and SIGTERM end every session inside that window and exit zero | Go: `runHub` returns nil on either. Zig: `oapx hub` installs both handlers before it serves, and the serve loop ends on either, so the sweep runs on the signal path exactly as it runs on a hangup |
| **Where a signal can be taken, it is; where the loop cannot observe one, the default disposition stands** | Zig: `hubTakesSignals` gates the handler and the pollable handle on the same answer, because installing a handler the loop never polls for is worse than not installing one. On Windows the stdio read is not pollable, so the hub installs no console handler, says so on stderr, and Ctrl+C terminates the process as it did before — a bounded sweep on a signal is [#460](https://github.com/lsm/open-agent-protocol/issues/460)'s work, not this rule's |

## Known gaps

The rules this document specifies that a Go test did not pin when it was
written, and where each is pinned now. Every entry but G3's names the test
that closes it, because a port implementing this draft should be able to check
itself against the same list; G3 is a decided question with no rule left to
pin, and points at the issue that carries it. A port must implement every rule
here; each was a place a differential test would otherwise not see.

- **G1 — closed.** The status was pinned and nothing else: a `text/plain`
  open is refused `415`, and the assertion was on the status alone, so
  `unsupported_media_type` was written nowhere. `TestARequestTheDaemonWillNotParseIsRefusedWithItsCode`
  now pins the code and the absent `Content-Type`, both of which take the
  same branch. The stdio side still has no counterpart for either, which
  is a property of the transport rather than a gap. The **charset** half is
  decided the other way from what it first was: the hub admits every
  `charset` and reads the body as UTF-8, per RFC 8259 §11, which defines no
  `charset` parameter for `application/json` at all. A refusal turned a
  sender's redundant parameter into an error the schema never described, so
  `TestAnyCharsetIsAdmittedAndTheBodyIsReadAsUTF8` pins the admission
  instead.
- **G2 — structural, pinned at the hub.** A client that drops its connection
  ends the stream by construction — the request context cancels the
  subscription — and that is now asserted where it is observable:
  `TestACancelledSubscriptionEndsTheStream` cancels the context a subscription
  was given and pins that `Next` returns the context's error rather than
  blocking, and stays finished. Two things it deliberately does **not** claim.
  The HTTP handler is not under test: nothing in the hub reports subscriber
  accounting, so "this subscription ended" has no observable from outside the
  server, and the owner declined to add any. And the error is the context's
  own (`context.Canceled`), not `io.EOF`, because `io.EOF` is this API's
  "the run reached its terminal" signal and a client that cancelled mid-run
  must be able to tell the two apart. The stdio side still has no way to
  express a hangup at all. The Zig HTTP stream is observable where Go's is not:
  `a client that hangs up mid-stream releases its subscription without an event
  to write` closes the client's socket while nothing is being written and waits
  for the hub's subscription count to reach zero ([#399](https://github.com/lsm/open-agent-protocol/issues/399)).
- **G3 — #53, decided: stdio has no `unsubscribe`, and the ceiling is not a
  trap.** The pipe carries every subscription at once and has no per-stream
  hangup, and no op detaches a single pump. [Issue
  #53](https://github.com/lsm/open-agent-protocol/issues/53) weighed an
  `unsubscribe` op, id-reuse replacement, and leaving it; **this draft takes the
  third, in both trees.**

  The reason #53 had a case was that a host at the ceiling could only free a slot
  through a side effect on a session it might not want to end. That is no longer
  true, and the reason is a rule this draft already had: **a subscription ends
  at its run's terminal envelope.** A host that subscribes per run — the shape
  that actually fills the ceiling — has each subscription end on its own, without
  touching the session, and it ends without the host asking. The ceiling of 64
  therefore bounds live streams.

  The ceiling holds because an ended subscription stops counting. Go needs nothing
  from the consumer: once a session's last reader has gone and no reservation is
  outstanding, `detachSubsLocked` takes every subscriber off it. The Zig core
  leaves the session's subscriber list at the moment a subscription ends — when its
  consumer reads the terminal envelope, and, for a replay that already overflowed,
  before it is ever added — and neither needs a `close()` from anyone, so a
  finished subscription is reclaimed rather than retained and the ceiling of 64
  bounds live streams.

  The remaining two options both cost more than they buy. An `unsubscribe` op is
  a wire verb with no HTTP counterpart, and the honest HTTP form of "drop the
  connection" is not expressible on a pipe that carries every subscription at
  once — so it would be the one op the parity job could never check, which is the
  thing the stdio transport exists to prevent. Id-reuse replacement overloads
  correlation with lifecycle and still cannot reach zero subscriptions.

  So the ending paths stay as they are, the `busy` refusal at the ceiling names
  them, and `close` — with its `POST /sessions/{id}/close` counterpart — remains
  the one verb that ends every subscription on a session, in both trees. **A
  port may not add an `unsubscribe` op alone**; if this decision is ever revisited
  it has to be revisited for both, and the HTTP form has to be settled first.
- **G4 — closed, and one claim in it was wrong.** The gate compares a
  request's cited revision against the probed one on both the attachment and
  the subscribe path, and the **attachment** path was pinned end to end on
  HTTP: a stale citation is refused `409` with both `expected_revision` and
  `current_revision` in `details`. This entry used to say a `subscribe` open
  citing a stale revision "is refused on the capability rung first, so the
  comparison is never made". That is not what the code does: `ElectionGate`
  compares the revision *before* it asks whether `session.open.subscribe` is
  advertised, so a stale citation on the subscribe path is refused
  `stale_capabilities` like any other. `TestOpenOpRefusesAStaleRevisionOnTheSubscribePath`
  now drives it over stdio and pins both revisions in `details`, which also
  pins the **stdio `open` op's own mapping** that no stdio test reached.
- **G5 — closed.** A held subscription nothing adopts **is** released on
  expiry, and `TestOpenSubscriptionNotAdoptedIsReleased` drives that: it builds
  the server with a 50 ms hold and polls until the held set empties, failing if
  it never does. The **default** was the unpinned half — a port could choose
  any window and nothing said a host had to wait for it.
  `TestASubscriptionIsHeldForThirtySecondsByDefault` pins the exported
  `DefaultSubscriptionHold` at 30 s, that a server built with no hold option
  holds for exactly that, and that an explicit window is still honoured.
- **G6 — closed.** The HTTP route's `probe_failed` was pinned and the stdio
  op's own mapping was not.
  `TestCapabilitiesOpReportsAProbeFailure` drives it and pins the wire's
  message bound on the way through, and
  `TestCapabilitiesOpReportsAnUnencodableDescriptorAsInternal` reaches
  `internal` with a descriptor whose feature constraints carry invalid JSON,
  so the response cannot be encoded at all — the one path that had no
  coverage because nothing could reach it.
- **G7 — closed.** Only the HTTP route's refusals were covered.
  `TestResolveOpReportsAnUnknownRun` pins `run_not_found` and
  `TestResolveOpReportsARefusedResolution` pins `resolution_rejected` on a
  live run, resolving it as a participant the session never declared.
- **G8 — closed.** The *mechanism* was well covered: every shutdown test builds
  the frontend with a short custom window (100 ms or 250 ms) and measures
  against it. The **default** was the unpinned half — a port could choose any
  default and nothing said a host had to wait that long.
  `TestTheHubSweepsForTenSecondsByDefault` pins the hub's 10 s and
  `TestEachTeardownStageWaitsFiveSecondsByDefault` the stdio frontend's 5 s per
  stage, each also checking that an explicit window still wins.
- **G9 — closed.** `oap-overflow` and `oap-session-closed` had their minimal
  shape pinned by `TestEveryEndingFitsTheFrameLimitFloor`, which encodes each
  real line, but no stdio test produced either ending and read it off the wire.
  `TestEventsSignalAnOverflowWithItsRunAndCursor` now floods a session behind a
  one-deep write queue and pins the overflow's `session_id`, `run_id`,
  `last_sequence` and `message`;
  `TestEventsSignalAClosedSessionByName` closes a session under a live stream
  and pins the closing signal's `session_id` and `message`. The third,
  `oap-frame-limit`, **was** driven by
  `TestAJoinPointTooLargeToFrameFallsBackRatherThanEndingTheSubscription` but
  only its `event` name was asserted;
  `TestTheFrameLimitSignalCarriesItsRunAndSequence` now pins its `run_id`,
  `sequence` and `message` against the run and cursor the stream stopped at.
  Over HTTP, `oap-overflow` is pinned, and a session closing under a stream is
  pinned too but as a **silent end** with no named signal — the SSE layer
  defines exactly three, `oap-subscribed`, `oap-overflow` and `oap-replay-gap`.
  `oap-session-closed` and `oap-frame-limit` have no SSE counterpart at all,
  because a socket neither frames nor has to announce its own close.
- **G10 — closed.** It was the one code in the HTTP body gate with no
  coverage at all, so a port could spell it differently and nothing would
  say so. `TestATruncatedRequestBodyIsRefusedWithItsOwnCode` drives a
  truncated body over a raw connection — the client half-closes so the
  daemon's read fails rather than the client's write — and pins both the
  `400` and the code. The differential job for #388 should still carry a
  truncated-request case: this test pins the code, not the parity.

- **G13 — open for the code only; the Zig bound is chosen.** The Zig daemon
  serves at most 64 connections at once and, at the bound, stops polling its
  listener until one ends, so a client is told nothing and waits in the listen backlog; that is
  the choice this row asks a port to record, and it invents no status. What is
  still open is the code. A port must bound how many connections it serves at once,
  because a stream holds its connection for as long as the client listens and
  an unbounded daemon is one nobody can bound, but **nothing says what a client
  is told when the bound is reached**. The draft's codes are the ones the core
  defines and none of them means "the daemon is full", and the rule above
  forbids inventing one — so a port that reaches its bound has no correct
  answer available to it. Go has the same absence in a worse form: it has no
  bound at all. What closes this is a code in the core for a daemon at its
  connection bound, and a `Test` that pins it on both trees; until then a port
  refuses the connection the way it refuses anything else and records the
  choice as a divergence. The Zig daemon's bound is pinned by `the connection
  bound is the number the draft's G13 row records`, and that it holds a stream
  without holding the daemon by `an open event stream does not hold the daemon:
  another connection is answered while it streams`.

One asymmetry is deliberate and is **not** a gap: a cross-origin refusal exists
on the HTTP transport alone. A stdio peer is a separate process on the far side
of a pipe, so the `Origin` check is HTTP's alone — and what stdio carries in its
place is the wire-supplied-credential refusal, which applies there too, because a
stdio peer is not an in-process embedding of the core.

## Divergences from the Go tree

Recorded rather than fixed here, per Decision 0032: the draft decides, the wrong
side is fixed, and where it cannot be fixed yet the divergence is written down.

**D1 is fixed.** The `Host` refusal is an `error.response` carrying
`unrecognized_host` in Go as it is in the draft, and `TestHostAllowlist` asserts
the body, so [#387](https://github.com/lsm/open-agent-protocol/issues/387) and
[#388](https://github.com/lsm/open-agent-protocol/issues/388) no longer fail a
byte-for-byte comparison on their first request.

**D2 is fixed.** Both hubs release a session once it stops being open
([#452](https://github.com/lsm/open-agent-protocol/pull/452) in Go,
[#455](https://github.com/lsm/open-agent-protocol/pull/455) in Zig), so a session that is not open answers
`unknown_session` to every later operation, a second `close` included, and its id is free.

**D3 is fixed** ([#500](https://github.com/lsm/open-agent-protocol/pull/500)): a catalog is
stamped with the revision its *lister* served it under, which is what makes the
draft's "refuse one that disagrees with the descriptor" check possible at all.

**D21 — a chunked body is refused, where Go de-chunks it.** A request carrying
any `Transfer-Encoding` is answered a bare `400`; `net/http` decodes chunked
transparently, so a streaming client works against `goap hub` and not against
`oapx hub`. This is a gap rather than a decision. The routes that read a body are
written now, so the decoder has a consumer; it belongs beside `readBody`, under
the same 16 MiB cap, and is not part of the change that wired the routes. Until then the refusal is
the safe one: a body whose framing this daemon cannot read is not a body it
should guess at. Pinned by `a chunked body is refused rather than guessed at`.

**D22 — no `100 Continue` is sent, where Go sends one when the handler reads the
body.** A client that sends `Expect: 100-continue` waits for the interim answer;
`curl` waits about a second and sends anyway, a stricter client waits out the
idle budget. The routes that read a body are written, so the interim answer
now has somewhere to go: a route that reads a body would answer `100 Continue`
first, and one that refuses the head would answer the refusal instead. That is
not done yet, which is the whole of this divergence. Noted here
so the difference is a recorded one rather than a surprise in a differential run.

**D5 is fixed** ([#516](https://github.com/lsm/open-agent-protocol/pull/516)): a session
open's `metadata` reaches an adapter through the core, which before it **had no member to
carry it** — so the member could not arrive rather than arriving and being dropped. The
one surface that did accept a metadata-carrying open and discard it is the endpoint, in
**both** trees, and that is D9.

**D10 is fixed** ([#524](https://github.com/lsm/open-agent-protocol/pull/524)): an open that subscribes or attaches is gated on the revision the host asked for, so
`stale_capabilities` is reachable and the answer's `capability_revision` reports a
revision that was checked rather than one that was merely sent.

**D4, D7, D8 and D9 are what is left.** Three of them are the Zig side and all of one
kind: each names something the Zig tree cannot carry that the draft specifies — a member
that does not exist, or a signal with nowhere to report it. None of them changes a byte
on the wire today, and each is a small contract change rather than a re-decision, so they
are queued rather than fixed here: D7, D8 and D9 in
[#407](https://github.com/lsm/open-agent-protocol/issues/407). D7 is the per-run exposure
a stream failure needs; D8 is that `request_cancelled` has no signal to come from; D9 is
a gap **both** trees share, which is why fixing it on one side would be the wrong move.
D4 is different in one respect: its negative-capacity half is a Go change, queued in
[#406](https://github.com/lsm/open-agent-protocol/issues/406). **D6 is fixed**, and the
one place the two trees' shutdowns still differ in kind rather than in answer is written
down in [Shutdown](#shutdown).

### D3 — a served catalog's revision comes from the lister

**Fixed**, in [#500](https://github.com/lsm/open-agent-protocol/pull/500).
`contract.Catalog` and a `contract.ToolSet`, each pairing the response with the
revision the lister says it served it under, as `base.Catalog` and
`base.ToolCatalog` do in Go. The hub stamps the answer with **that** revision
rather than with the adapter’s descriptor revision, and refuses an empty one — which
is Go’s own rule, `an adapter served a model catalog with no capability revision`.
`catalog_unlabelled` is the core’s name for the condition and has **no wire code
of its own**: both trees answer `internal`, so they do not disagree by disagreeing
about the name. A lister that served a catalog under one revision
while its descriptor claimed another is now visible to the hub rather than
silently restamped.

The two rows that depended on it, `models` and `tools`, carry this: both answers
are "stamped with the revision the lister served it under", and both name
`unsupported_feature` for an adapter that cannot serve one at all.

### D8 — `request_cancelled` has nowhere to come from in Zig

| | |
| --- | --- |
| **The draft says** | Both `models` and `tools` name `request_cancelled` (400), and both say a catalog the adapter cannot serve is `unsupported_feature` or `tools_failed`. |
| **Go does** | `modelsError` and `toolsError` both map `context.Canceled` and `context.DeadlineExceeded` to `request_cancelled`, so a caller who gave up is told they gave up rather than that the backend failed. |
| **Zig does** | `contract` has no cancellation signal and `hub.Failure` has no error for one, so a lister that was cancelled arrives as whatever the adapter chose — `BackendFailed`, most often — and the wire answers `tools_failed`. |
| **Why it matters** | A host that cancelled a catalog request and one whose backend failed are told the same thing, so a host cannot tell "I stopped listening" from "the adapter broke". The same gap applies to every op the draft lists `request_cancelled` for, so it is not specific to the catalog operations. |
| **Why it is not fixable here** | It needs `contract`'s slots to report cancellation, which is a member that does not exist — the same kind of gap as D3 and D5, and the reason it is recorded rather than papered over with a mapping the hub cannot actually reach. |
| **The fix** | `contract`'s slots report cancellation as its own error, as Go's context does, and the transports map it to `request_cancelled`. |

### D9 — the endpoint surface drops an open's `metadata`, in **both** trees

| | |
| --- | --- |
| **The draft says** | `session.open.request` carries `metadata`, and the schema has the member. |
| **Go does** | Only the **endpoint** surface drops it: `serveendpoint`'s open path builds a `base.OpenRequest` with no `Metadata`. The other two frontends read it and enforce the draft's `invalid_payload` — `servehttp` and the stdio op both decode each value and refuse one that will not parse. |
| **Zig does** | The same, and one layer deeper: `oap_types.SessionOpenRequest` has no `metadata` member either, so it cannot reach `endpoint.zig`'s `adapter.open` even now that D5 gave `contract.OpenRequest` one. |
| **Why it matters** | D5 fixed the core's path, so a metadata-carrying open reaches an adapter when it is driven **through the core** and is still dropped when it is driven through an **endpoint**. Two paths to the same adapter, one of which silently discards a request member. |
| **Why it is not fixed here** | **Both** trees do it. Fixing it on the Zig side alone would make the two *diverge* — the Zig endpoint would forward metadata and the Go one would not — which is the opposite of what Decision 0032 is for. It needs to be a change to both trees, and it is a change to the shared surface rather than to the hub. |
| **The fix** | `oap_types.SessionOpenRequest` gains `metadata` and both trees' **endpoint** surfaces read it into their `OpenRequest`, in one step — the only two places either tree drops it. The Zig stdio `open` op, when it is written, has to read `metadata` off the wire the way Go's does, or the Zig line becomes a *third* dropper rather than a second reader. |


### D10 — nothing gated an open on the revision the host asked for

| | |
| --- | --- |
| **The draft says** | `open`'s errors include `stale_capabilities` (409, with `expected_revision` and `current_revision` in `details`), and its answer carries `capability_revision` "set to the revision the open was gated under". |
| **Go does** | The comparison lives in **two** gates, and the draft's own line puts the subscribe one second: `AttachmentGate` runs when the request carries tool sources, `ElectionGate` when it set `subscribe` or `reopen`, and both return early otherwise. Each probes, compares the request envelope's `capability_revision` against the descriptor's, and returns `StaleRevisionError{Expected: the descriptor's, Current: the request's}`, which the stdio op turns into `stale_capabilities` with both in `details`. `AttachmentGate` runs first, so a request that both attaches and subscribes is gated on the attachment. On success a gate returns the **descriptor's** revision, and that is what stamps the answer. |
| **Zig does** | `Failure.StaleCapabilities` was **declared and never returned**. The election order named `subscribe` first where Go's wire names the attachment first, and both trees asked whether an attach member was *present* rather than whether it carried entries. Nothing anywhere compared a revision, and `hub.OpenRequest` had no member to carry one, so the refusal had no arm and the gate had no value. |
| **Why it matters** | Two consequences, and the second is the one that would have shipped quietly. `stale_capabilities` was unreachable, so a host that gated its open on a revision got a session opened against whatever the adapter happened to be serving — a silent disagreement where the draft specifies a refusal. And the answer's `capability_revision` had no gated revision to report: the only value available was the request's own, which is exactly the value the gate exists to check, so a "success" would have been stamped with the number that was never verified. |
| **The fix** | `hub.OpenRequest` carries `capability_revision`. An open that **subscribes or attaches tool sources** compares it against the registered adapter's revision and returns `StaleCapabilities` on a disagreement — **after** the adapter's probe and **before** the `session_exists` lookup, so a probe failure answers ahead of it (`probe_failed` in Go, and the same here) and the gate still wins over a name collision and over an unadvertised feature. A request that states no revision is not gated, which is Go's own `revision != ""` guard and not a hole. The two Go gates differ only in which support feature they then check, and that half already exists as the election check, so one comparison covers both. The `expected`/`current` pair is assembled by the frontend, which reads the registered revision from `hub.listing` — the same route the `adapters` op already uses. |
| **What it is not** | The stdio `open` arm, which is [#387](https://github.com/lsm/open-agent-protocol/issues/387)'s next step and depends on this. |

### D11 — a `submit` on an open is refused in Zig and admitted in Go

| | |
| --- | --- |
| **The draft says** | `open`'s params are `adapter` and `request`, and the request's `message` is a first-class member of `openRequest`. A message admitted at open time is **queued**: the answer carries `admitted_submit_requests` and the submission runs under the session it was admitted into. |
| **Go does** | `OpenCompound` admits it: it opens, submits the message, and reports the admission in the answer. The subscription is registered *before* the message runs, so the open misses nothing. |
| **Zig does** | Refuses it `unsupported_feature`. `hub.OpenRequest` has **no `message` member**, so there is nothing to admit into; the stdio op says so rather than dropping the submission. |
| **Why it matters** | The refusal is the honest answer and the alternative is worse — Go's `refuseUnadvertisedOpen` exists and refuses a message on the *endpoint* surface, so "a message is unsatisfiable here" is already a shape this tree knows. What is missing is the machinery, not the will. |
| **The fix** | `hub.OpenRequest` gains `message_json`, the hub registers the subscription before running the submission, and the answer carries `admitted_submit_requests`. For the subscribing case, the `Frontend` needs a held subscription — the same thing `events` needs — so both unblock together. This is `submit`'s work, not `open`'s: the same PR that serves `submit` on the wire is the one that can admit a message at open time. |
| **The refusal's precedence** | It is checked **before** the adapter is looked up and before the revision gate runs, so an open naming an unregistered adapter *and* carrying a message answers `unsupported_feature` where Go answers `unknown_adapter`, and a subscribing open citing a stale revision answers `unsupported_feature` where Go answers `stale_capabilities`. Go's `openOp` looks the adapter up first and gates second, so both of those win there. Hoisting the refusal means it lives in the hub rather than the frontend, which is where it stops existing: the moment `submit` admits a message and `events` admits a subscription, neither refusal is there to be out of order. It is recorded rather than fixed because every input that reaches the difference is already divergent under this row. |
| **Over HTTP** | The subscribing half is served: `POST /adapters/{name}/sessions` holds the subscription the open registered, and the `events` request with no cursor adopts it — Zig: `a subscribing open holds its subscription, and the events request with no cursor adopts it from the first envelope`. The `message` half is still refused on both transports, and the stdio op still refuses `subscribe`, because stdio has no `events` op to drain it. |
| **Stdio controls, 2026-10-06** | `oapx hub --stdio` now serves `submit`, `resolve`, `cancel` and `settings`, each through the `Frontend` function the daemon route calls (`submitControl`, `resolveControl`, `cancelControl`, `settingsControl`), so the two transports answer one implementation. Zig: `submit, resolve and cancel answer over stdio as they do over HTTP, each correlated to its request` and `the control ops refuse as the HTTP routes do`. **`events` is still not served over stdio**, so a run admitted there can be cancelled and answered but not watched, and the two refusals below stand until it is. |
| **Also refused here** | **A subscribing open, for the same reason and a sharper one.** `hub.open` registers a `Subscription` when `subscribe` is set, and the stdio op discarded it — the wire accepted a subscription it cannot deliver, and once `submit` lands its envelopes would queue against a subscription nothing drains. Go holds the subscription in its `Frontend`; there is no equivalent here yet, so the honest answer is to refuse until `events` exists. **This is a second divergence from the same cause** and it is why `open`'s parity cases carry no `subscribe` at all. |

### D27 — an `oapx` registry entry is served by `oapx hub` and refused by `goap hub`

| | |
| --- | --- |
| **The draft says** | A registry entry's `type` names an adapter the hub constructs; [the registry](#the-registry) lists `oapx` as Zig's own agent loop. |
| **Go does** | Refuses the entry: `buildAdapter` has no `oapx` case, because there is no Go adapter over oapx's loop. |
| **Zig does** | Builds it from the same production runtime `oapx serve agent --backend oapx` uses. |
| **Why it matters** | One document is not portable between the two hubs once it names an `oapx` entry, which is why `examples/oap-serve.json` does not name one. Closing it needs a Go adapter that spawns `oapx serve agent --backend oapx` over the endpoint binding, which is adapter work rather than hub work. |

### D28 — a control that finds its session closed answered `unknown_session` in Zig — CLOSED

| | |
| --- | --- |
| **Go does** | `serve.Session` marks itself closed when the adapter answers `ErrSessionClosed`, and `submit`, `resolve`, `cancel` and `settings` answer `409 session_closed`, as their rows say. |
| **Zig did** | `Hub.submit`, `compact`, `resolve`, `resolveCall` and `cancel` released the session on the adapter's `SessionClosed` and answered `404 unknown_session`. |
| **Now** | They release it and answer `session_closed`, as `Hub.updateSettings` already did. Pinned by `a control the adapter finds closed answers session_closed and releases the session, as Go does`. A later request for the released session is `unknown_session` in both trees. |

### D12 — an open's `unsupported_feature` and `capability_degraded` carried no `feature`

| | |
| --- | --- |
| **The draft says** | `open`'s errors include `unsupported_feature` (400, for a tool source it will not attach) and `capability_degraded` (400, for a feature the request did not opt into), both naming the feature. |
| **Go does** | `ControlRefusal` turns the adapter's refusal into the code **and its details**, so both arms carry `feature` and `reason` on the wire. |
| **Zig does** | **Fixed.** The same two codes with no `details`: `hub.open` returned a bare error from its `Failure` set, and the `contract.Refusal` — which is where the feature and reason live — was a local in the hub, gone by the time the transport built the answer. `hub.openReporting` now takes a `*OpenRefusal`, the hub writes the adapter's own words into it at every slot that can refuse, and the transport answers from what it was given rather than re-deriving them. `open` keeps its signature and passes `null`. |
| **What is still not fixed here** | The **order** those refusals arrive in, which is D18: the hub now says why, but the frontend check still answers ahead of it. |
| **Why it matters** | The differential comparison keeps `details` and drops only `message`, so the two trees will disagree on the first open whose elections are unadvertised or degraded. Unreachable today — the only registered adapter advertises all three — and live the moment #389's registry registers a real one. |
| **The fix** | The hub has to surface the refusal, not just the error: either a `Failure` that carries the `Refusal`, or a variant of `open` that returns it. That is a core change, and it is the same change `submit`, `resolve` and `cancel` will each want, so it belongs in the PR that serves the first of them. |

### D13 — an ill-typed envelope member answers `schema_invalid` where Go answers `malformed_json`

| | |
| --- | --- |
| **The draft says** | `open`'s errors distinguish `malformed_json` (the envelope will not decode) from `schema_invalid` (it decodes and does not satisfy the schema). The distinction is the draft's, and it is meaningful. |
| **Go does** | `gateRequest` runs `ParseEnvelope` first and only then `schema.Validate`. `ParseEnvelope` is "can `encoding/json` unmarshal this into the `Envelope` struct", so a **wrong-typed** member fails it (`json: cannot unmarshal number into Go struct field plain.id`) and answers `malformed_json`, while a **missing** member or an off-schema value parses and answers `schema_invalid`. |
| **Zig does** | `schema_invalid` for both. `oap_envelope.deserializeEnvelope` decodes *and* dispatches on the type in one step, so the two questions Go asks separately cannot be asked separately here. |
| **Why neither order fixes it** | Validate-then-decode gives `schema_invalid` for the ill-typed member, as now. Decode-then-validate gives `schema_invalid` for the ill-typed member too — but it *also* answers `malformed_json` for `{"id":"x","type":"nonesuch"}`, where Go answers `schema_invalid`, because the Zig decode rejects the unknown type and Go's parse does not. Measured, not reasoned: both orders were tried and each is wrong on one of the two inputs. |
| **The fix** | The envelope module needs a parse step that is "shape only" — unmarshal into the struct without the type dispatch — so the gate can ask Go's two questions in Go's order. That is a change to `protocol/oap/envelope.zig`, shared by every consumer, and it is worth doing once rather than per op. |
| **Reach** | Reachable today: any open with a number where a string belongs. The differential scenario sends `{"request":7}` and `{"request":[]}`, which both agree on, but not an ill-typed *member*. |

### D14 — an unframable open answer keeps the session silently

| | |
| --- | --- |
| **The draft says** | "An open response that cannot be encoded rolls the session back and says so", and "an open whose id the host named is **kept** when its response cannot be framed, and the refusal names it". |
| **Go does** | `refuseOversizedOpen` and `refuseUnencodableOpen`: a **host-named** session is left open and the refusal says so; an **adapter-minted** one is rolled back, because nobody learned its id and an unreachable session must not leak. |
| **Zig does** | The answer goes through the generic `answerLine` path, which answers `response_too_large` and **keeps the session in both cases**, with no distinction named. |
| **Why it matters** | A `sessions` listing after such a refusal would show a session the host has no id for. Unreachable with today's registry — one adapter, an 18 MiB frame limit and a small state document — and live the moment a real adapter's state does not fit. |
| **The fix** | Belongs with the `response_too_large` work, which is its own PR: the rollback needs the hub's own allocator and a shutdown-bounded close, which is the shape D6 gives hub-2 for #389. Recording it here so the follow-up PR starts from the rule rather than from this paragraph. |

### D15 — a colliding session name is refused before the adapter ever runs

| | |
| --- | --- |
| **The draft says** | `session_exists` (409) is one of `open`'s errors, and `unsupported_feature` (400) is another. The draft does not say which wins when a request is both. |
| **Go does** | The adapter runs **first**. `hub.Open` opens the session and only detects a duplicate at `serve.go:102`, so the adapter's own refusals — an unattachable tool source, a bound it will not take — are what answer. Measured: opening `s1`, then opening `s1` naming three local tool sources answers `unsupported_feature` in Go. |
| **Zig does** | `hub.open` pre-checks the name (`hub.zig:448`) and answers `session_exists` before the adapter is asked anything. The same input answers `session_exists`. |
| **Why it matters** | The host is told its id is taken when the real reason its request cannot be served is that the adapter will not attach what it asked for. Fixing the name is then useless and the actual refusal is never reported. Reachable with both trees' own memory adapter, so it needs no exotic adapter to appear. |
| **The fix, and why it is one change with four others** | Match Go: call `adapter.open`, then check for the duplicate, then close the session just opened. That is a **core** change, and it is the same one **D11** (admit a message, report `admitted_submit_requests`), **D12** (surface the `contract.Refusal` so `feature` and `reason` reach the wire), **D14** (roll an unnamed session back) and the ordering half of **D13** all want. Five rows, one change: `hub.open` should do what Go's `hub.Open` does — run the adapter, apply the checks in Go's order, and report *which* refusal and why. That belongs in its own PR before `submit`, and it is the reason `submit`'s PR is worth its size. |

### D16 — the envelope size budget is measured on re-serialized bytes

| | |
| --- | --- |
| **The draft says** | "A stdio request envelope over 16 MiB is refused" — which reads as the bytes the host sent. |
| **Go does** | `gateRequest` compares `len(payload)` of the raw `json.RawMessage`, so the budget is the member's **byte span in the line**. |
| **Zig does** | `Request.payload` is a parsed `std.json.Value`, and `gateRequest` re-encodes it with `ownedRawJson` — **compact** — before measuring. A pretty-printed or escaped envelope spanning 16–18 MiB compacts to under 16 MiB, so it is served. |
| **Why it matters** | The frame limit is 18 MiB, so such a line is accepted by the wire and the two trees then disagree about it: oapx serves the open, goap answers `request_too_large`. It is the one budget where "what the host sent" and "what we re-printed" differ, and the draft's wording is about the first. |
| **The fix** | The serve loop's `decode` has the raw line; it would need to record the member's byte span and hand it to the gate alongside the parsed value. That is a `Request` member and a change in `decode` — cheap, and local to this file. |
| **Why it is not fixed here** | Not a size question: no test can send a 16 MiB envelope through the differential without making the job enormous, so a fix here would be asserted only by a unit test with a hand-built span, and the differential could not confirm it. It is also a wire detail rather than a core one, so it is not part of the `hub.open` refusal change that D11, D12, D14 and D15 share. |

### D17 — a wire-named tool source was attached without the daemon's trust checks

| | |
| --- | --- |
| **The draft says** | The daemon's trust model: a host may not hand the hub a command to run. An attachment that names a `local` source, `args`, a `NAME=value` `environment` entry, or an unconfigured process id is refused, not attached. |
| **Go does** | `ResolveAttachments` (`serve/attach.go:58-71`) applies the three checks before the attachment is admitted, and an open naming a `local` source answers `unsupported_feature`. Measured: a source with `kind: "local"` and a `command` is refused. |
| **Zig does** | **Fixed.** `openSession` handed `tool_sources_json` straight to `hub.open` with no counterpart, so the same open **succeeded** and the command was silently dropped. `attachmentRefusal` now applies all four checks in the frontend, and consults the hub's **registry-configured** sources — not the adapter's advertised ones — for the member comparison, so a source the operator configured is named by id and not re-described from the wire. |
| **Why it matters** | This is the one divergence found so far that is about a *host being untrusted* rather than about a host being misinformed. Every other row is a wrong code or a missing reason; this one is a session that reports a command it never ran. It is reachable today, with both trees' own memory adapter, and four differential scenarios now attach a source — the one named "an attachment that names something to run is refused by both" sends five — so both trees are held to the same four refusals. That scenario is what caught the substitution gap recorded as D19. |
| **The fix** | The three checks belong in the frontend, before the attachment is admitted, because that is where the wire is untrusted: a `kind` that names an executable, `args`, a `NAME=value` `environment` entry, and a process id the registry has not configured. That is the same place Go puts them, and it is the reason the trust model is a frontend concern rather than a hub one. |

### D18 — a frontend refusal answers ahead of every hub refusal

| | |
| --- | --- |
| **The draft says** | The order this page now specifies, read off the step table above: the adapter exists (**step 2**), the **attachment** cites a current revision and is elected (**step 3**), the **subscription** takes the same two checks (**step 4**), and the adapter's own refusals come only after all of that (**step 5**). Every later check is downstream of every earlier one. |
| **Go does** | `openOp` looks the adapter up at `ops.go:334` and refuses `unknown_adapter` there, runs `AttachmentGate`'s stale comparison at `ops.go:337` and refuses `stale_capabilities` or `probe_failed` through it, and only calls `ResolveAttachments` at `ops.go:359`. The three are in that order, and the attachment check is **last**. |
| **Zig does** | `openSession` calls `attachmentRefusal` at `stdio.zig:602` **before** `hub.open`, so the one refusal living in the frontend outranks **all three** the hub owns. An open naming an unregistered adapter and a wire-described attachment answers `unsupported_feature` where Go answers `unknown_adapter`; a failing probe is outranked the same way; a stale revision is outranked the same way, which is what the row said when it covered only that one. D11's `message` refusal has the identical shape and its own cell. |
| **Why it is not fixed here** | One cause, four symptoms, and the cause is structural: the checks are **split across two owners** and `openSession` can only order them by *where they run*. The gate, the lookup and the probe are inside `hub.open`; the attachment check is in the frontend, before it. No ordering of two calls puts a frontend call after something inside a call it precedes, short of duplicating the gate or hoisting the check into the hub — and hoisting it is wrong, because the check is the one place the wire is untrusted and it belongs where the wire arrives. All of it is the refusal PR's work: a hub that reports its refusals **in this page's order** is a hub a frontend can ask about without re-implementing the gate. Recording one row rather than four keeps the ledger pointing at one fix instead of four, and names D11's cell as the same inversion. |

### D19 — a configured tool source is validated, then handed to the adapter as the wire wrote it

| | |
| --- | --- |
| **The draft says** | A host names a tool source by the id the operator configured; the daemon substitutes what the operator declared and the adapter never sees the wire's own description. |
| **Go does** | `ResolveAttachments` (`serve/attach.go:89`) replaces the wire entry with `hub.Registry().ToolSource` and merges the operator's `environment` before the adapter runs. |
| **Zig does** | **Fixed.** `openSession` handed the raw `tool_sources_json` to `hub.open`, so the adapter saw the host's own description of a source the operator had pinned. `substitutedSources` now runs between the trust check and the hub: the check still reads the **wire**, which is the untrusted thing, and the adapter receives the **registry**. A source the operator left unconfigured is passed through exactly as written, so a daemon with no registry behaves as it did. |
| **How it is tested, and what is not** | A Zig unit test sends an open naming a pinned source and an unconfigured one, and asserts on what the adapter was handed: the operator's `endpoint`, the operator's `environment` values kept even where the wire named the same variables, the caller's bare names appended, and the unconfigured source verbatim. |
| **The differential case that is owed** | **There is no differential scenario for it, and that is a real gap rather than an impossibility.** I first recorded this as unreachable on the grounds that `oapx hub` refuses `--config` and `adapter/memory` refuses every unconfigured source. **Both were wrong.** `runHub` parses `--config` and feeds `file.tool_sources` into `Hub.init` (`makai.zig:2641`), and `adapter/memory` admits any id outside its own declared set whose kind it can attach (`adapter.zig:1098`). So a config declaring `pinned` as a `local` source and an open naming `{"id":"pinned","kind":"local"}` is admitted today, and before this change the session carried the wire's description of a source the operator had pinned. **The case is therefore expressible, and writing it is owed.** It needs the differential harness to pass a per-tree `--config`, which it cannot today — `hubCommand` runs `hub --stdio` with no arguments. That is the follow-up, and until it lands this row is the only place the divergence is recorded, which is a weaker guarantee than the other rows have. |

### D23 — the media gate sits on the head, so 413 wins where Go answers 415

| | |
| --- | --- |
| **The draft says** | Every route that reads a body requires `Content-Type: application/json`, and the rows above pin `415` for a wrong type and `413 request_too_large` for a body over 16 MiB. It does not say which wins when a request is both, because a request cannot be both before the length is read. |
| **Go does** | `readRequest` (`go/serve/servehttp/server.go:747`) parses the media type **first** and answers `415`; the `MaxBytesReader` is only reached after that. A wrong-media body over 16 MiB is therefore `415` in Go. |
| **Zig does** | The gate is in `answer()` (`zig/src/hub/http.zig:381`, inside the function declared at `:378`), which is called on the head **after** `readHead` has read the length. So the length check fires first and a wrong-media body over 16 MiB answers `413`, and a wrong-media request at or under the cap answers `415`. |
| **The divergence** | One request, two answers: `Content-Type: text/plain` with `Content-Length` over 16 MiB is `413` in Zig and `415` in Go. Neither is wrong against the draft, which pins both statuses and states no precedence. |
| **Why it is not decided here** | Choosing would be **settling a precedence the draft does not state**, and a wrong-media *body* has to be either refused or buffered to get there, which is a routing decision this ledger does not own. A draft row saying "a wrong media type is refused before the body is measured" or the reverse would close it; that is a spec change and unaccepted. |
| **Anchor correction** | These citations were **wrong on arrival on `main`, not staled by later edits.** They landed on `main` in the squash `1ee4a53` (#685), and in **that commit's own tree** `answer()` already stood at `:378` and the media gate at `:381`, while `:364` was a struct field (`refusal: Refusal,`) and `:367` was blank — so `:364`/`:367` were off by 14 the moment they landed. `git diff 1ee4a53 d63e6d8 -- zig/src/hub/http.zig` is **empty**, where `d63e6d8` is the tip of `main` immediately before the #730 squash, so nothing had shifted them as of the point these rows were written. That comparison is **deliberately bounded to `d63e6d8`** and is **not** a claim that the file is unchanged since: #730 has since added 61 lines to `http.zig`, so the same diff against a later `main` is no longer empty. The narrower statement is the one carrying the point — the anchors were off by 14 on arrival, not stale afterwards. They appear to have been carried over from an unlanded pre-squash branch revision, where the numbers did match that tree; that revision is not reachable from `main`, so it is deliberately **not** cited here, and the checkable fact is the squash itself. The two anchors are now given separately on purpose, so a reader following either lands on what the claim names. |

### D26 — the media gate's predicate differs between the trees in two corners the draft leaves open

| | |
| --- | --- |
| **The rule, which is not in doubt** | `drafts/hub.md:461`: a wrong `Content-Type` is refused `415`, and `:462-463` admit any `charset`. That is the whole pin — it does not say *which* requests the gate applies to, and the draft names no predicate for that. |
| **What the Zig port does** | `zig/src/hub/http.zig:381` gates on `carriesBody(request)` — declared at `:385`, returning `content_length > 0` at `:386` — and it does so in `answer()`, so it is decided on the head for **every** request. The companion `declaresJson` it calls is declared at `:389`. A `POST` with `Content-Length: 0` and `Content-Type: text/plain` passes the gate; a `GET` carrying a body with `text/plain` is refused. |
| **What Go does** | `servehttp/server.go:747` gates inside `readRequest`, which is called by the four body-reading routes only — `handleOpen` at `:191`, `handleSubmit` at `:321`, `handleResolve` at `:382`, `handleCancel` at `:496` — and the check reads only `r.Header.Get("Content-Type")`, with no reference to length. So a `POST` to `close` (`:643`), which never calls `readRequest`, is not gated at all, and a `POST` with `Content-Length: 0` and `text/plain` **is** refused 415, because length is never consulted. |
| **The two corners, stated** | **Empty body, wrong type, on a body-reading route.** `POST /adapters/{name}/sessions` — `handleOpen`, one of the four that call `readRequest` — with `Content-Length: 0` and `Content-Type: text/plain`: **Go answers 415**, because its check reads the media type and never the length, and **Zig passes the gate**, because `carriesBody` is false, and the route then refuses the empty body `400 malformed_json`. **Body on a listing.** `GET /adapters` carrying a body with `text/plain`: **Zig answers 415**, decided on the head, and **Go answers the listing**, because no listing route calls `readRequest`. |
| **A route that reads no body, so it is not a corner** | `POST /sessions/{id}/close` with `Content-Length: 0` and `text/plain`. Go **registers the route** (`mux.HandleFunc("POST /sessions/{id}/close", s.handleClose)`, `server.go:80`) and `handleClose` at `server.go:643` never calls `readRequest`, so it answers the close — `204`, per `server_test.go:591`. **Zig now routes it too**: the gate passes it, because `carriesBody` is false, and the daemon dispatches the close and answers `204` — Zig: `a close answers no content, and the session is unknown to every route after it`. So both trees answer the close, and neither refuses it `415`, Zig because `carriesBody` is false and Go because the gate is never reached. Before the routes were wired, Zig answered this request `404 not found`, which is what an earlier revision of this row recorded. The empty-body corner is only real on a route that reads a body, and the four are `handleOpen` `:191`, `handleSubmit` `:321`, `handleResolve` `:382`, `handleCancel` `:496`. |
| **Why they are not rows here** | Neither tree is wrong against the draft: `:461` pins the status for a wrong `Content-Type` and is silent on the predicate, so both answers are consistent with the text. Deciding which predicate is correct would be **choosing a precedence the draft does not state**, and this table does not make that choice. A draft row naming the predicate — "a request whose method reads a body" or "a request that carries one" — would settle it, and that is a spec change and unaccepted. |
| **What is recorded instead** | The divergence itself, in both directions and with both call sites, so a reader comparing the trees sees two answers and knows the cause is an unpinned predicate rather than a bug in either. |
| **What is now pinned, on the Zig side** | Both corners are executable rather than described. `a zero-length body with a wrong media type is not gated, because the gate reads length` sends a `POST /adapters/a/sessions` with `Content-Type: text/plain` at both `Content-Length: 0` and with the header absent, asserts `carriesBody` is false and that the answer is `.not_found` — **Go answers 415 for both.** `a listing carrying a body with a wrong media type is gated, because the gate reads the head` sends a `GET /adapters` with `Content-Length: 4` and `text/plain`, asserts `carriesBody` is true and that the answer is `unsupported_media_type` — Go serves the listing. Changing `carriesBody` from `content_length > 0` to `content_type != null` makes the first fail, so the tests pin the predicate rather than restate it. **Go still has no test for either corner**, which is the same gap D20 records for `type_mismatch`; these tests fix the Zig half and leave the Go half recorded rather than silently equal. |
| **A third corner, closed rather than recorded** | `declaresJson` used to cut at the first `;`, so `application/json; charset` — a parameter with no `=` — was admitted while Go's `mime.ParseMediaType` errors on it and the gate answers 415. **The Zig gate now validates the parameter section**, aligned to **Go's `mime.ParseMediaType`, not the bare grammar of RFC 9110 §5.6.6** — the two are not the same, and citing the grammar would misdescribe the result, since Go's parser **admits optional whitespace around `=`** and **quoted-pairs**. The test, `a parameter list that Go refuses is refused here too, and one it admits is admitted`, pins **thirteen refused and twenty admitted** spellings, each **executed against Go 1.27's parser and matched against what it answered**. Duplicate names compare case-insensitively and values **after unescaping quoted-pairs**, because a raw slice comparison diverges from Go on exactly those spellings. **Go still has no test for any of these**, the same gap D20 records for `type_mismatch`. |
| **Storage, and the bound that replaced two bad ones** | The duplicate check compares **decoded** values, so each parameter is appended to one store as two big-endian u16 lengths, the name, and the decoded value. The store is `2 * max_header_bytes` — 32 KiB — which is the only bound there is, and it is the header bound the draft already states. **An earlier revision imposed two refusals of its own, a 64-parameter cap and a 256-byte value limit.** Both were mine, neither is in the draft, and both refused requests Go accepts, so both are gone. The 256-byte one existed only because the buffer was 256 bytes and the bare-token branch copied into it unchecked, so a 257-character token indexed past the end — a **panic in the daemon's request path**, an abort and every session with it. Sizing the store from the header removes the overflow *and* the refusal at once. `sixty-five parameters and a long value are admitted, because the header bound is the limit` pins 64, 65 and 200 parameters and a 2048-character value, each **executed against Go 1.27 first**. |
| **A backslash is an escape only before a tspecial** | The decoder is aligned to `mime/mediatype.go:304-312`, which
  consumes a backslash **only when the byte it precedes is a tspecial** — `( ) < > @ , ; : \ " / [ ] ? =` —
  and deliberately preserves one before a letter or digit, so an MSIE path survives a round trip. Two earlier
  attempts got this wrong in opposite directions: one dropped every escaped byte, the next consumed every
  backslash pair, and **both make values Go treats as different compare equal**, admitting a duplicate the
  draft-facing gate should refuse. `a backslash is consumed only before a tspecial, which is what Go does` pins
  **eleven header spellings, two admitted and nine refused**, each reproduced against Go 1.27 and matched
  against what it answered; the bytes are **runtime header bytes**, not Zig source. Admitted:
  `a="C:\\path"; a="C:\\path"` and `a="x\\qy"; a="x\\qy"`. Refused: those two against a shorter value
  (`a="C:path"`, `a="xqy"`), a second literal backslash (`a="C:\\path\\x"; a=C:pathx`), a literal one before a
  digit or space (`a="x\\1"; a="x1"`, `a="x\\ "; a="x "`), a doubled against a single one quoted or bare
  (`a="x\\\\"; a="x\\"`, `a="x\\\""; a="x\""`, `a="x\\\""; a=x\"`), and `a="a\\;b=c"; a="a;b=c"`, where the
  doubled backslash leaves a literal one after decoding. **Both mutations fail it:** consuming every pair
  regardless of `isTspecial`, and never consuming one. |

### D20 — `type_mismatch` is answered by no test in either tree

| | |
| --- | --- |
| **The draft says** | An envelope whose payload is not a `session.open.request` is refused `type_mismatch`, distinct from `schema_invalid` (the envelope violates its schema) and from `invalid_request` (the request field is missing or the wrong type). |
| **Go does** | Answered from `serve/servestdio/ops.go`, with no test that sends a schema-valid non-`open` payload to the `open` route. |
| **Zig does** | Answered at `zig/src/hub/stdio.zig:505`, after the envelope is validated and before it is dispatched, and with no test that reaches it. `malformed_json` is covered by the Zig unit table; `type_mismatch` is not, in either tree. |
| **Why it is not fixed here** | The line that reaches it does not exist yet in either tree, so the check is correct by inspection and unproven by execution. A row that claims coverage it does not have is the same defect as a name that claims an unexercised check, and the ledger is where that claim has to be visible. Renaming the differential scenario that claimed five gate refusals and exercised two is the other half of the same fix. |

### D24 — five codes the operation rows name without a status a port may use

| | |
| --- | --- |
| **The rule, which is not in doubt** | `drafts/hub.md:743-748`: the stdio transport "answers four codes no HTTP route can" — `unknown_op`, `invalid_request`, `busy`, `response_too_large` — and "a port must not produce any of them over HTTP". The HTTP refusal table therefore carries no row for those four, and asserts all four null. |
| **The scope that overlaps it** | `adapters` at `:769`, `capabilities` at `:778` and `open` at `:839` name `invalid_request`, `malformed_json`, `schema_invalid`, `type_mismatch` and `invalid_payload` in their error lists **with no status attached**; the first status any of those lists pins belongs to a later code, `unknown_adapter` (404) at `:841`. Naming a code in an operation's error list says what the operation refuses, not what status a port may answer. |
| **The emission path, for the code both scopes name** | **Every `invalid_request` either hub frontend emits is from the stdio dispatcher** — `go/serve/servestdio/ops.go:1067, 130, 187, 257` — and `servehttp` emits none, handling invalid payload, unreadable and malformed bodies, and schema failures at `server.go:197, 245, 327, 749-775` instead. Outside the hub frontends the endpoint surface emits it too, at `go/serve/serveendpoint/dispatch.go:46, 52, 522` and `replay.go:58`; those are a different role and a different table, and an earlier revision of this row claimed a universal "every `invalid_request` Go emits", which is false at those sites. The parameter-shape check that produces it is a property of a stdio request object where an op declares which parameters it takes; a port declares a path and a method. |
| **The current scope of this table** | **Eight** distinct codes are named by operation rows without a status and are therefore **not rows here**, asserted null: the four of `:743-748` plus `malformed_json`, `schema_invalid`, `type_mismatch` and `invalid_payload`. `invalid_request` appears in both halves of that sum and is counted once, which is why five operation-row names plus four stdio-only names is five and eight, not nine. A table row is an authorisation to emit, and the draft has authorised none of the eight. Go's `servehttp` answers three of the four decode codes at 400 (`server.go:764-779`) and `invalid_payload` at 400 separately (`server.go:197`), which is evidence about Go and not a pin — Decision 0032 does not count it as protocol behaviour. |
| **A proposed change, unaccepted** | Anyone wanting a port to answer any of these eight would be **changing the spec**, by pinning statuses in the operation rows and relaxing `:743-748` for the four it names. **That change is unaccepted**, and nothing in this table authorises emitting any of the eight while the present text stands. This row records the current scope and the fact that a change would be needed; it asks the owner for nothing the existing rule leaves open. |

### D25 — four decode codes are answered 400 by Go and pinned by nothing, and one has no Zig hub emission

| | |
| --- | --- |
| **What the draft says** | `malformed_json`, `schema_invalid`, `type_mismatch` and `invalid_payload` are named bare in the operation error lists at `:839-841`, `:931`, `:951` and `:970`, where the `(400)` parenthetical attaches to `scope_mismatch`, a later entry. **No status is pinned for any of the four.** |
| **What Go does** | `servehttp/server.go:764-779` answers `malformed_json`, `schema_invalid` and `type_mismatch` at 400, and `invalid_payload` at 400 from a separate site, `server.go:197`, which is the payload decode in `handleOpen` and not the shared reader. |
| **What Zig does, and the gap** | The Zig hub answers the first three — `zig/src/hub/stdio.zig:655-671` — and has **no `invalid_payload` emission site anywhere under `zig/src/hub/`**: the only `invalid_payload` **emission sites** in the Zig tree are in the endpoint-role adapter at `zig/src/adapter/endpoint.zig:243, 349, 355`, which is a different role. The literal also appears in test and assertion code — `endpoint.zig:1435` and this ledger's own null-assertion list at `zig/src/hub/stdio.zig:2396` — so the claim is about emission, not about the string; an earlier revision said "the only in the Zig tree", which is false at head. Go's stdio dispatcher does emit it (`go/serve/servestdio/ops.go:332`). So a payload that will not decode as a `session.open.request` is answered by Go and **not** by the Zig hub. That is an implementation gap on the Zig side, recorded rather than closed: emitting it over HTTP would be a port producing a code this table does not authorise, which is the change D24's last row says is unaccepted. The gap is named so it is not silent, and no behaviour is invented to fill it. |
| **Why they are not rows here** | A row authorises a port to answer, and the draft pins no status, so the four are asserted null like the stdio-only set — see D24. Go's behaviour is the evidence and the draft is the decision, so following Go here would be following a tree rather than the page. |
| **What would close it** | Pinning the four in the operation error lists, which is a spec change and unaccepted. Until then the gap is here rather than silent, and a port that needs to answer them cannot without that change. |

### D4 — the registry's `journal_capacity` is hub-wide in Zig, per-adapter in Go

| | |
| --- | --- |
| **The draft says** | `examples/oap-serve.json` carries `journal_capacity` per adapter entry, and a cursor older than a session's journal is `oap-replay-gap`. |
| **Go does** | The registry passes each entry's `journal_capacity` to that adapter, so two adapters can retain different depths. |
| **Zig does** | One hub owns every session's journal, so the core takes a single `journal_capacity`; `load` adopts the first entry that names one, which is deterministic because the loader sorts entries by name. |
| **Why it matters** | A client resuming against session A and session B can be told the same bound where Go would tell it two. The recovery rule is unaffected — a gap names `oldest_available` either way — but a port and Go would disagree about *which* cursors expire. |
| **The two zeros are not the same zero** | A **document's** `journal_capacity: 0` keeps the default in both trees, because every Go constructor treats `<= 0` as "unspecified" (`go/adapter/memory.go:123`) and the Zig `load` does the same. The Zig **core's** own `Options.journal_capacity = 0` means *retain nothing*, and no config document can reach it — a host that wants no journal sets the option, and a host that writes `0` gets the default. Go has no equivalent: a `0` reaching an adapter always becomes that adapter's own capacity. So the two trees agree on every document, and differ only on a value only a Zig host can set. |
| **A negative is refused, not defaulted** | Both trees refuse it. The Zig `load` refuses a negative with `ConfigRefused`, and the Go registry refuses a document naming one, because a negative capacity is a malformed document and defaulting it would report success for something the operator did not write. A document naming `0` still keeps the default in both, because that is a request for the default rather than a malformed value. |

### D5 — `session.open.request`'s `metadata` never reached an adapter

**Fixed.** `contract.OpenRequest` and `hub.OpenRequest` both carry a `metadata`
member and the hub forwards it, so a session open's metadata is a value an
adapter receives rather than one the core drops. It is a `std.json.Value`, which
is what Go hands an adapter: `base.OpenRequest.Metadata` is a `map[string]any`,
parsed, not raw text. What this names is that the core had nowhere to put it: a
host that sent metadata to a Zig hub could not have it delivered, because the
member the draft specifies did not exist between the wire and the adapter. The
one surface that *did* accept such an open and drop it is the endpoint, in both
trees, and that is D9 rather than this row.

**The value's lifetime is the call's, and that is a rule rather than an accident.**
`metadata` is a `std.json.Value`, which is a *shallow* copy: its `.object` is a
pointer into the arena the value was parsed in, so the value is only as long as
that arena. The repo has no deep-copy helper for one — the idiom is to re-parse —
which makes the rule simple to state and easy to break: **an adapter may read
`request.metadata` for the duration of the call and must not retain it**, and the
hub forwards without retaining.

Nothing dangles today, and it is worth being precise about why. The hub core hands
the value straight through and holds nothing, and the one test double that *does*
retain it (`Flaky.saw_metadata`) is read back inside its caller's own scratch
arena, so the read is in lifetime. The trap is ahead, not behind: the stdio
frontend parses each line into a scratch arena that is `defer`-deinit'd when the
line is done, so **the `open` wire op has to decide this deliberately** — either
re-parse for the adapter, which is the repo's idiom and costs one parse, or hold
the line's arena for as long as the session lives. Writing that op without
deciding is how a use-after-free gets in, and it would be invisible in every test
that keeps the caller alive.

**The rule is about the whole request, not just `metadata`, and serving `open` is
what proved it.** An adapter may borrow *any* of `request` for the duration of the
call — `session_id`, `participant`, the raw JSON — and must not retain any of it.
The `open` op borrowed `envelope.id` for the answer's `in_reply_to` and read
`envelope.capability_revision` for a refusal's `details` after releasing the
envelope, and both were reads of memory the line's arena had already given back.
The second is the subtler one: `refusalWith` dupes the `DetailEntry` array, and a
**shallow dupe of a struct array does not dupe the slices inside it**, so copying
the entry did not copy the string.

`adapter/memory` is the reference implementation of the rule and was already right:
`Session.create` dupes what it keeps into its own arena and uses the caller's only
for the duration of the call. The test double in `hub/stdio.zig` was not, and
`open` is the first op that creates a session, so it is the first to reach it — a
stored `request.session_id` outlived the arena it came from and the next `sessions`
listing read freed memory. A double that keeps what it is given is a real defect
once an op retains anything, and cheaper to find at the double than in CI.

Whether the contract should instead offer a *clone path*, so an adapter can keep a
metadata value past the call, is a contract question rather than an `open` one, and
a contract change belongs in [#407](https://github.com/lsm/open-agent-protocol/issues/407)
rather than in a hub PR.

The draft's `invalid_payload` for a value that is not JSON is enforced where the
value is read, which is the wire op and not the core: the core's member is
already a parsed value, so there is nothing left for it to refuse. That is
recorded rather than left implied, because "the core does not validate it" and
"nothing validates it" are different sentences and only the first is true.

### D6 — the Zig shutdown sweep cannot retry a close that refuses

**Fixed.** `contract.Session.close` reports whether a run is still live — it
answers `error.RunActive` and does not destroy — and the sweep cancels the runs
it can see and tries again, up to `close_attempts`, waiting between attempts on
the adapter's own `pump` capped at that session's share of the window.
`Hub.closeSessions` returns what it managed, so a caller can say what it did
not.

Two things the fix had to add that the recorded version did not name, and both
are stated in [Shutdown](#shutdown) rather than only here:

- **A refusal must be escapable.** A Zig adapter's refusal destroys nothing, so
  the sweep's last resort tears the session down whether it closed or not. Go
  can leave the entry and exit, because the process exit reclaims everything; a
  Zig caller has to free it itself, and leaving an adapter session alive is
  exactly the orphaned child the rule exists to prevent.
- **Only one adapter refuses today.** The contract can now report a live run and
  the sweep can act on it, and the memory adapter does — its `close` refuses
  while `active` or `reserved` holds a live run, which is Go's rule. The seven
  process-backed adapters still close unconditionally: each one's `close` is a
  `destroy` that reaps the child, and a cancel followed by a close is the same
  outcome, so a refusal there would only spend the bound. A harness that grows a
  graceful close can refuse from here without a contract change.

| | |
| --- | --- |
| **The draft says** | `closeSessions` divides its window across the sessions it still has to close, and a close that refuses because a run is active is retried through a cancel — up to three attempts — because a harness that refuses `Close` while a run is in flight must first be asked to stop. |
| **Go does** | `Session.Close` answers `base.ErrRunActive` while a run is live and does not close, and `closeForShutdown` retries: close, and on that answer read the state, cancel every live run, wait 100 ms, close again, three times. Every Go adapter can refuse. |
| **Zig does** | `contract.Session.close` takes a `force` flag and may answer `error.RunActive`; the sweep cancels, pumps within the session's share, and closes again, three attempts in all. `Session.teardown` is the same call with `force`, and it is what the hub's own teardown uses, so a refusal can never leak. The memory adapter refuses while a run is live; the seven process-backed adapters destroy unconditionally. |
| **Why it mattered** | Before this, the Zig sweep cancelled and released in one pass, so a harness that needed a moment to stop was released anyway and the retry the draft names had nowhere to live. |
| **Where the two still differ** | Go's window is a context it can cancel; Zig's is a deadline it checks, because a Zig `close` is synchronous and returns rather than waiting, and because a Zig session's `state` and `cancel` cannot be interrupted at all. So a Zig sweep can come up short in two ways where Go's can come up short in one, and it reports which: a session it attempted and that refused, and a session it never reached. Both then exit, and both reap the child. |

Pinned in Zig by `a close that refuses while a run is live is retried through a
cancel until it lands`, `a close that never stops refusing is given the attempts
the draft names, and the session is torn down rather than left behind`, `one
wedged session is given a share of the window, and the session beside it is still
closed` and `the sweep waits no longer than the window it was given`, all four in
`zig/src/hub/hub.zig`.

### D7 — a stream failure ends the whole session in Zig, not the subscribers exposed to the run

| | |
| --- | --- |
| **The draft says** | Four overflow rules, each pinned by a Go test: *an adapter's own stream overflow reaches only the subscribers exposed to that run* (`TestHubAdapterStreamOverflow`, `TestAdapterOverflowScopedToExposedSubscribers`); *overflow follows the run the subscriber actually read* (`TestOverflowFollowsDeliveredRuns`); *an acknowledged position overrides a stale pending one* (`TestAcknowledgedPositionOverridesStalePending`); and *a late overflow does not cut a newer run* (`TestLateOverflowDoesNotCutNewerRun`). |
| **Go does** | A subscriber tracks exposure **per run** — the run it is attached to, the run it has acknowledged, and every run with a queued event — and `exposedTo` decides whether a given run's stream failure reaches it at all. A run-a stream overflow therefore never reaches a subscriber that has acknowledged run-b, and a late overflow on an older run cannot cut a newer one. |
| **Zig does** | One mailbox per subscription and no per-run exposure. `contract.Session.drain` reports a failure, not *whose* stream failed, so a stream failure is not an overflow and not attributable to a run: `pump` ends the **session** with `.stream_failed` and every subscription on it. `lossRun` names the right run for a *queue* overflow, and the cursor is the post-drain position, but there is no way to express "this run's stream overflowed, and only its readers care". |
| **Why it matters** | A backend that overflows one run's stream takes down every subscriber on the session in Zig, where Go confines it. A host watching a healthy newer run is disconnected by an older run's failure. The rules are not merely unimplemented — the contract has no member that would let a port implement them, which is the same class as D3 to D6. |
| **The fix** | `contract`'s drain reports the run that failed and whether it was an overflow, beside the failure, and `Subscription` tracks the run set it is exposed to the way Go's `subscriber` does. Two members, and all four rules become implementable. |
| **What *is* ported** | Which run a **queue** overflow names, and where its cursor points. `lossRun` considers the dropped event's run, every run with a queued event, the run the subscription is reading and the session's current run, preferring a run the session has not finished — Go's candidate set, including the current run. The cursor is the position the client stopped at **after** draining, not where the queue filled, so a resume neither skips nor repeats. Pinned by `zig/src/hub/hub.zig`'s `a subscriber that falls behind is ended with a cursor on the run that overflowed`, `a loss on a newer run names the newer run rather than the one being read`, `the overflow cursor is where the client stopped after draining, not where the queue filled` and `a loss on a settled run names the live current run, which is what Go's candidate set reaches`. |

### Recorded, and not divergences

Three places where the two trees will *look* different and none of them is a
wrong answer. A differential test compares the members, not the prose, at the
first two; the third is about bytes and is named here so nobody reads the
member comparison as a byte comparison.

- **An envelope's member order.** Go's `protocol.Envelope` struct writes
  `payload` straight after `id`; `zig/src/protocol/oap/envelope.zig` writes it
  last. Same members, same values, different bytes. The ids, the codes, the
  wording and every member agree, and the zig stdio frontend mints the same
  `oap-request-N` and `oap-response-N` in the same per-op order Go spends its
  counter in — so a differential test that compares members is satisfied, and one
  that compares bytes is not, and the two are not the same test.

- **A signal's wording.** The stdio `oap-overflow` message says `resume with a
  cursor after this sequence`; the SSE one says `reconnect with a cursor after
  this sequence`. Two transports, two verbs for the same fact. The
  `last_sequence` member carries the fact; every message this document
  mentions is **opaque prose a host may display and must not match on**.
- **A probe failure's wording.** An `adapters` listing entry whose probe failed
  carries an `error` member holding the adapter's own diagnostic. Where that
  diagnostic is a Go runtime string it is a
  [Decision 0032](../decisions/0032-go-and-zig-are-peers.md) quirk reaching the
  wire, not protocol behaviour, so the two trees are not required to spell it
  alike. The listing's *shape* — one entry per registered adapter, sorted, with
  `error` in place of the descriptor and revision — is specified, and a parity
  script should use adapters that probe cleanly rather than lean on this.
