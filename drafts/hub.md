# The Hub: Many Sessions on One Wire

Status: proposed design; the specification both trees are judged against
Date: 2026-09-26
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Reference implementations: `goap hub` (`go/serve`) and `oapx hub` (Zig)
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
| **Loopback bind by default** | The HTTP daemon binds `127.0.0.1:6270`. Pointing it at an external interface is explicitly unsupported and opts out of the single-user model. | `TestServeDefaultAddrIsLoopback` |
| **No authentication** | There is no credential, token or session cookie on either transport. Over stdio, spawning the process *is* the authorization. | structural: no auth code path exists on either transport, which is how the rule is kept — a test asserting an absence would only say the tree had not grown one yet |
| **`Host` allowlisted on a loopback bind** | When the bind address names `localhost`, `127.0.0.1` or `::1`, only requests whose `Host` header names one of those three are served; anything else is refused `403`. Comparison is case-insensitive and the port is stripped. A non-loopback bind has no allowlist. | `TestHostAllowlist`, `TestLoopbackHosts` |
| **`Origin` refused on every route** | A request carrying any `Origin` header is refused `403 cross_origin_request`. The check wraps the whole mux rather than living in the routes that read a body, so it covers `close` and every route not yet written. | `TestEveryRouteRefusesABrowserOrigin`, `TestReadRequestRefusesBrowserOrigins`, `TestTheOriginBoundaryHoldsWithoutAHostAllowlist` |
| **`environment` is an allowlist** | A child process inherits nothing ambient. An adapter entry's `environment` names the variables forwarded: a bare `NAME` forwards the daemon's own value (an unset name is omitted), `NAME=value` passes through literally. A tool source's `environment` takes the same form with one stricter rule — a bare `NAME` the daemon does not carry fails at startup, naming the source and the variable, because that entry is the credential list of one executable the daemon itself launches. | `TestResolveEnvironment`, `TestLoadRegistryToolSourceNeedsEveryNameItLists`, `TestCallerEnvironmentNeverNamesAVariableTwice` |
| **A restart ends every session** | Run children are per-session and no adapter survives the process. Nothing persists across restarts, so a client that reconnects to a restarted hub finds no session. Session entries accumulate for the daemon's lifetime — closed sessions stay listed with their final state — and there is no eviction. | `TestServeSessionsClosedOnShutdown`, `TestHubSessionCloseSemantics`, `TestSessionsListingAcrossLifecycle` |
| **No payload or environment logging** | The hub, its codecs and its clients never log envelope payloads or resolved environment values. | `TestDaemonOutputNeverCarriesEnvironmentValues` (environment values, through the listing and a load failure); no test pins the payload half |

## The registry

`--config` names one JSON document, the shape `examples/oap-serve.json` shows.
It is decoded **strictly**: an unknown member is refused, a member whose name
differs from the documented spelling only by case is refused, a duplicated
member is refused, and the refusal names the first unknown member in sorted
order so it does not depend on map iteration. Trailing data after the object is
refused.

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

A tool source entry takes exactly `kind`, `display_name`, `protocol`,
`endpoint`, `command`, `args`, `environment`. A `process` entry must carry a
`command`; a `kind` outside the protocol's five is refused; an entry naming one
environment variable twice is refused.

| rule | pinned by |
| --- | --- |
| Unknown adapter member refused, deterministically named | `TestLoadRegistryNamesOneUnknownMemberDeterministically` |
| Case-variant member refused | `TestLoadRegistryRefusesCaseVariantMembers` |
| Duplicated member refused | `TestLoadRegistryRefusesDuplicateMembers` |
| `type` defaults to the entry name | `TestLoadRegistryDefaultsTypeToEntryName` |
| Adapters load in sorted name order | `TestLoadRegistrySortedNames` |
| Without a config, the built-in memory adapter is served | `TestDefaultRegistry`, `TestLoadRegistryMemory` |
| Every adapter constructor is reached, and its requirements surface at startup | `TestLoadRegistryProcessAdapters`, `TestLoadRegistryConstructorErrors` |
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
| An id no source is configured under is refused, whatever its `kind` | `TestOpenRefusesAnUnconfiguredIDWhateverItsKind` |
| A configured source is admitted by its bare id, and the daemon supplies the command the registry holds | `TestDaemonFillsTheRegistrysCommand`, `TestOpenOpMatchesHTTP` |
| The caller's `environment` names extend the operator's list and never replace an entry | `TestCallerEnvironmentNeverNamesAVariableTwice` |
| The attachment gate reads a *layered* disclosure, so a request may be refused a level the descriptor advertises underneath the top one | `TestOpenReadsAttachmentSupportFromALayer`, `TestSharedGateRefusesADisclosureAnOpenCannotElect` |
| The capability rung is answered before the open's own constraints | `TestOpenAnswersTheCapabilityRungBeforeItsOwnConstraint` |
| An adapter's own attachment refusal is relayed, not restated | `TestOpenRelaysTheAdaptersAttachmentRefusal` |
| A degraded attachment refused without the opt-in is relayed | `TestOpenRelaysADegradedAttachRefusal` |
| Only what the request cites is attached, and an open citing a stale revision is refused with both revisions named | `TestAttachingOpenPinsOnlyWhatItCites` |
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
The daemon **fails closed** on all of them: it stops serving and reports a
framing defect naming the line.

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
| An unknown `op` is a correlated request error, not a framing defect | `TestUnknownOpIsARequestError`, `TestOpErrorCodesMirrorHTTP` |
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
| `"event":"oap-overflow"` | the consumer fell behind; `last_sequence` is where a cursor resumes |
| `"event":"oap-replay-gap"` | the `after` cursor is no longer retained; `oldest_available` and `latest_available` bound what is |
| `"event":"oap-session-closed"` | the session closed under the subscription |
| `"event":"oap-frame-limit"` | an envelope this framing cannot carry; `sequence` is where a fresh cursor resumes past it |
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
| The join signal is advisory: it never ends the subscription | `TestAJoinPointTooLargeToFrameFallsBackRatherThanEndingTheSubscription` — **gap G9**: this test also drives the `oap-frame-limit` ending, but asserts only its `event` name |
| Overflow is signalled with the cursor to resume from | the line's shape by `TestEveryEndingFitsTheFrameLimitFloor`; the end-to-end path at the core by `TestHubSubscriptionQueueOverflow`; **gap G9** — no stdio test drives an overflow and reads the line |
| A failed run stream ends the subscription out loud | `TestAFailedRunStreamEndsTheSubscriptionOutLoud` |
| An unencodable envelope ends the subscription out loud | `TestAnUnencodableEnvelopeEndsTheSubscriptionOutLoud` |
| A context failure is still announced | `TestAnAdapterContextFailureIsStillAnnounced` |
| A replay gap is reported with the cursor that was asked for | `TestEventsOpReportsAReplayGap`, `TestAFailedResumeReportsTheRequestedCursor` |
| The session closing under a subscription is signalled | `TestHubCloseReplaySubscriptionEndsPromptly` |
| A live subscription does not stall shutdown | `TestALiveSubscriptionDoesNotStallShutdown` |
| The cursor advances monotonically, holding its high-water mark across a run boundary | `TestAdvanceCursorHoldsItsHighWaterMark` |

### Bounds on the stdio pipe

A pipe gives no back pressure, so the stdio frontend bounds everything a socket
would bound by refusing. Each bound is a **correlated `busy` refusal**, not a
stall, because waiting is what would stop the daemon reading the host's end at
all. The message ends with `send this request again`, and that is the whole
recovery: the request was never served, so resending it is safe.

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
| The hub's stdio wire is the twelve ops, not the endpoint's raw envelopes | `TestCapabilitiesNameTheStdioBinding` |

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
| `POST /sessions/{id}/submit` | `submit` | `session.message.submit.response` | see [submit](#submit) |
| `POST /sessions/{id}/resolve` | `resolve` | the matching resolve response | see [resolve](#resolve) |
| `POST /sessions/{id}/cancel` | `cancel` | `run.cancel.response` | see [cancel](#cancel) |
| `POST /sessions/{id}/close` | `close` | `204 No Content`, no body | `run_active` 409, `session_closed` 409, `request_cancelled` 400, `internal` 500 |
| `GET /sessions/{id}/events` | `events` | an SSE stream, adopting a held subscription when the request named no cursor | see [events](#events) |

Every route that reads a body requires `Content-Type: application/json`, caps
the body at 16 MiB, and answers a refusal as an `error.response` envelope
carrying the request's `in_reply_to`, `session_id` and `run_id` so a client can
correlate it. The two listings answer plain JSON rather than an envelope,
because they are daemon management rather than an OAP operation.

| HTTP rule | pinned by |
| --- | --- |
| A wrong or absent `Content-Type` is refused `415 unsupported_media_type` | **none — gap G1** |
| A body over 16 MiB is refused `413 request_too_large` | `TestRequestBudgetMatchesHTTP` (the stdio side of the same budget) |
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
an already-closed session is refused `409 session_closed` rather than parked.

| SSE rule | pinned by |
| --- | --- |
| An envelope arrives in emission order with its sequences | `TestSSEEnvelopeOrderAndSequences`, `TestClientRejectsFrameIDDisagreement` |
| The `id:` field and the envelope's own `sequence` agree; a frame whose `id` disagrees is refused | `TestClientRejectsFrameIDDisagreement` |
| An `after` cursor replays the journal's retained suffix | `TestSSEReplayAfterCursor` |
| `Last-Event-ID` reconnects | `TestSSELastEventIDReconnect` |
| An explicit `?after=` wins over the header | `TestSSEQueryCursorWinsOverHeader` |
| A live subscription mid-run is not handed a prefix it did not ask for | `TestSSELiveSubscriptionMidRun` |
| A stream ends when the client closes the connection | structural: the request context; **gap G2** |
| A stream ends when the session closes | `TestSSEStreamEndsOnSessionClose`, `TestSSEOnClosedSession` |
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
whether to wait or to reconnect.

| bound | value | pinned by |
| --- | --- | --- |
| Request body | 16 MiB | `TestRequestBudgetMatchesHTTP` |
| Per-subscription mailbox | 64 envelopes, as the core defines it | `TestHubSubscriptionQueueOverflow` |

A 30 s header read and a 2 min idle timeout keep a socket from being held open
forever. Neither is protocol — no client observes them, and a port may choose
its own — so they are named here only so a port knows they exist.

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
| An adapter's own stream overflow reaches only the subscribers exposed to that run | `TestHubAdapterStreamOverflow`, `TestAdapterOverflowScopedToExposedSubscribers` |
| Overflow follows the run the subscriber actually read | `TestOverflowFollowsDeliveredRuns`, `TestAdapterOverflowScopesByAcknowledgedRun` |
| An acknowledged position overrides a stale pending one | `TestAcknowledgedPositionOverridesStalePending` |
| A late overflow does not cut a newer run | `TestLateOverflowDoesNotCutNewerRun` |

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
`session.open.subscribe` key. A compound open exercising an optional feature
must cite the active capability revision, or it is refused
`stale_capabilities`.

| rule | pinned by |
| --- | --- |
| The open carries its message, and the state names the run it admitted | `TestOpenOpCarriesItsMessage`, `TestOpenCarriesItsMessage`, `TestOpenCarryingAQueuedMessageReportsItsQueuePosition` |
| The subscription registers before the message runs, so it misses nothing | `TestOpenOpSubscribesBeforeItsMessageRuns` |
| An open without `subscribe` streams nothing | `TestOpenOpWithoutSubscribeStreamsNothing` |
| A message that cannot be admitted rolls the session back | `TestOpenRollsBackWhenItsMessageCannotBeAdmitted` |
| An unschematic message is refused at the gate | `TestOpenCarryingAnUnschematicMessageIsRefusedAtTheGate` |
| `subscribe` against an endpoint that never advertised it is refused | `TestOpenRefusesSubscribeAgainstAnEndpointThatNeverAdvertisedIt` |
| A degraded `subscribe` is refused without the opt-in, and admitted with it | `TestSharedGateRefusesADisclosureAnOpenCannotElect` |
| An open citing a stale revision is refused `stale_capabilities`, naming both revisions | `TestAttachingOpenPinsOnlyWhatItCites` (the attaching open, end to end on HTTP); **gap G4** — the same comparison on the `subscribe` path, and the stdio op's own mapping of it, are unpinned |
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
| The hold is bounded | the 30 s constant in `hold.go`; **gap G5** — no test waits the window out |

## The operations

Twelve ops. Each row gives the request line's parameters, the answer, and the
errors; the two transports differ only where noted. A refusal the row does not
name is `internal` on both.

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
  `TestOpErrorCodesMirrorHTTP`, `TestListingsMatchHTTP` — **gap G6**: no test
  drives the stdio `capabilities` op's own `probe_failed` or `internal` path.

### `open`

- **params:** `adapter` (required) and `request`, and nothing else.
- **answer:** a `session.open.response` envelope carrying the whole state
  document, with `in_reply_to` set to the request envelope's `id` and
  `capability_revision` set to the revision the open was gated under.
- **errors:** `invalid_request`, `malformed_json`, `schema_invalid`,
  `type_mismatch`, `invalid_payload` (a `metadata` value that is not JSON, or a
  payload that will not decode), `unknown_adapter` (404), `session_exists`
  (409), `stale_capabilities` (409, with `expected_revision` and
  `current_revision` in `details`), `unsupported_feature` (400, for a tool
  source it will not attach), `capability_degraded` (400, for a feature the
  request did not opt into), `probe_failed`, `open_failed` (502), `busy`,
  `request_cancelled`, `response_too_large`, `internal`.
- **pinned by:** `TestOpenOpOpensASession`, `TestOpenOpRefusals`,
  `TestOpenRefusalsAreBounded`, `TestHubOpenRejections`,
  `TestHubOpenDefaultsParticipant`, `TestHubOpenClosesSessionWhenStateFails`,
  `TestHubOpenMarksClosedOnClosedConfirmation`, `TestOpenSession`,
  `TestOpenSessionAssignsIdentifier`, `TestOpenResponseCarriesTheWholeState`,
  `TestOpenOpMatchesHTTP`, `TestAttachingOpenPinsOnlyWhatItCites`.

### `sessions`

- **params:** none.
- **answer:** `{"sessions":[{"session_id":…,"adapter":…,"status":…,"active_run_id":…,"active_runs":[…],"created_at":…}]}`,
  sorted by session id, with a closed session listed under its final state.
- **errors:** `invalid_request` (a parameter was supplied).
- **pinned by:** `TestSessionsOpListsTrackedSessions`, `TestSessionsListingAcrossLifecycle`,
  `TestListingsMatchHTTP`, `TestHubSessionsListingAcrossAdapters`.

### `state`

- **params:** `session_id`.
- **answer:** a `session.state.response` envelope carrying the whole state
  document, with a daemon-minted `in_reply_to`.
- **errors:** `unknown_session` (404), `invalid_request`, `state_failed` (500),
  `internal` (500).
- **pinned by:** `TestStateEndpoint`, `TestStateReportingClosedClosesEntry`,
  `TestOpErrorCodesMirrorHTTP`.

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
- **answer:** a `session.message.submit.response` envelope carrying the
  admission, with `in_reply_to` set to the request envelope's `id`.
- **errors:** `unknown_session` (404), `invalid_request`, `malformed_json`,
  `schema_invalid`, `type_mismatch`, `invalid_payload`, `scope_mismatch` (400,
  a payload naming another session), `run_active` (409), `invalid_submission`
  (400), `unsupported_feature` (400), `capability_degraded` (400),
  `model_not_found` (400), `session_closed` (409), `request_cancelled` (400),
  `response_too_large`, `internal` (500).
- **pinned by:** `TestSubmitRejections`, `TestOpErrorCodesMirrorHTTP`,
  `TestQueuedSubmissionRoundTrips`, `TestQueuedSubmissionOverStdio`,
  `TestSubmitRollsBackUnframableAcknowledgement`,
  `TestSubmitRollbackWaitsForSettlement`, `TestSubmitErrorStreamStillDrains`,
  `TestRejectedSubmitKeepsSubscriptions`, `TestRequestBudgetMatchesHTTP`.

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
- **pinned by:** `TestResolveRejections`, `TestUnadvertisedControlRefusalKeepsItsWireShape`
  — **gap G7**: no test drives the stdio `resolve` op's `resolution_rejected`
  or `run_not_found` path; only the HTTP route's refusals are covered.

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

### `close`

- **params:** `session_id`. Over HTTP the id is the path segment and no body is
  read.
- **answer:** `{"id":N,"ok":true,"result":null}` over stdio; `204 No Content`
  with no body over HTTP.
- **errors:** `unknown_session` (404), `invalid_request`, `run_active` (409, a
  run still in flight — a host cancels first), `session_closed` (409),
  `request_cancelled` (400), `internal` (500).
- **pinned by:** `TestSessionsListingAcrossLifecycle`, `TestOpErrorCodesMirrorHTTP`
  (a `close` of a running session is `run_active`; a second `close` of a closed
  session is `ok`), `TestHubSessionCloseSemantics`, `TestListingsMatchHTTP`.

### `events`

- **params:** `session_id`, `after` (an unsigned sequence) and `run_id`, and
  nothing else. `after` may be JSON `null`, which is the same as absent.
- **answer:** `{"id":N,"ok":true,"result":null}` as the acknowledgement, always
  before the first envelope, then the subscription lines above. Over HTTP the
  acknowledgement is the empty body of a `200` whose headers have already been
  flushed.
- **errors:** `unknown_session` (404), `invalid_request` (a parameter the op
  does not define), `session_closed` (409, a session that is already closed —
  refused rather than parked), `invalid_cursor` (400, a cursor that is not an
  unsigned sequence, or a `run_id` with no cursor), `replay_cursor_future` (400),
  `run_not_found` (404), `no_run_to_resume` (409, a cursor on a session with no
  run to replay), `busy`, `request_cancelled`, `internal` (500). A **replay gap
  is not an error**: the op acknowledges `null`, then writes
  `oap-replay-gap` and ends the subscription.
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
  ending paths rather than implying the host can bring one about).

## The clients are the far-side proof

`go/client` and `clients/ts` drive this wire as a consumer would, and their
tests are the strongest evidence the rules above hold. They are **not** the
specification: where a client refuses something this document permits, the
client is what a real host does and a port must satisfy it too.

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
| An error envelope carrying no code is surfaced as a plain failure rather than a protocol one | `TestClientErrorCodeAbsentForPlainFailures` — the rule D1 would end for the `Host` refusal |
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
   `TestSSEOverflowSignalLive`, `TestSSEOverflowSignalReplay`.
3. **An overflow is scoped to the run the subscriber was actually reading.** A
   subscriber that has moved on is not told about an earlier run's overflow, and
   one still reading it is. Pinned: `TestOverflowFollowsDeliveredRuns`,
   `TestAdapterOverflowScopedToExposedSubscribers`,
   `TestAdapterOverflowScopesByAcknowledgedRun`, `TestNewerPendingRunExposesOverflow`,
   `TestAcknowledgedOrderStaysMonotonic`, `TestExposureByAdmissionOrder`.

A subscription ends at exactly one of: its run's terminal envelope, an overflow,
a stream failure, its session closing, the frontend tearing down, or — over
HTTP — the client's hangup. **Over stdio there is no host-initiated way to end
one subscription**; see [gap G3](#known-gaps) and
[#53](https://github.com/lsm/open-agent-protocol/issues/53).

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
including a failed exit. A **restart ends every session**; nothing persists
across a restart, so a reconnecting client finds no session.

`CloseSessions` divides its window across the sessions it still has to close, so
one stuck child cannot consume the whole budget and leave the rest orphaned. A
session whose run is still active is cancelled before it is closed, up to three
attempts, because a harness that refuses `Close` while a run is in flight must
first be asked to stop.

| rule | pinned by |
| --- | --- |
| Every session is attempted, not just the first | `TestCloseSessionsAttemptsEverySession` |
| The budget is split per session | `TestCloseSessionsSplitsBudgetPerSession` |
| The total sweep is bounded | `TestCloseSessionsBoundsTotalSweep` |
| Active runs are settled before a close | `TestCloseSessionsSettlesActiveRuns` |
| A close that refuses because a run is active is retried through a cancel | `TestCloseRetriesThroughAsyncCancel` |
| A close stops at its context deadline | `TestCloseStopsAtContextDeadline` |
| The reservations a snapshot names are cancelled | `TestCloseCancelsReservationsTheSnapshotNames` |
| Every run a snapshot lists is cancelled | `TestCloseCancelsEveryRunTheSnapshotLists` |
| A run named only as the active run is still cancelled | `TestCloseFallsBackToTheNamedActiveRun` |
| The window is 10 s for the hub, 5 s per stdio stage | the `DefaultShutdownTimeout` constants; **gap G8** |

## Known gaps

Rules this document specifies that **no Go test pins today**, and one gap the
draft is asked to carry. A port must implement every one of them; each is a
place a differential test would otherwise not see.

- **G1 — the `415` on a wrong `Content-Type` is unpinned.** Every
  body-reading route requires `application/json`, but no test asserts the
  `415 unsupported_media_type` answer. The stdio side has no counterpart.
- **G2 — SSE's hangup ending a stream is unpinned.** A client that drops its
  connection ends the stream by construction — the request context cancels the
  subscription — but no test asserts it, and the stdio side has no way to
  express it at all.
- **G3 — #53: stdio cannot end one subscription on request.** The pipe carries
  every subscription at once and has no per-stream hangup, and no op detaches a
  single pump. A pump on a live, idle session ends only at its run's terminal, an
  overflow or stream failure, its session closing, or the frontend tearing down.
  The `busy` refusal at the subscription ceiling names those paths rather than
  implying the host can bring one about, which is accurate but is not a release
  valve. This is carried as a gap in **both** trees: neither has an `unsubscribe`
  op, and neither is permitted to grow one alone.
  [Issue #53](https://github.com/lsm/open-agent-protocol/issues/53) weighs the
  three options; the `close` op and its `POST /sessions/{id}/close` counterpart
  are the only wire verbs that end a stream today, and they end the whole
  session with it.
- **G4 — the `stale_capabilities` refusal is only half pinned.** The gate
  compares a request's cited revision against the probed one on both the
  attachment and the subscribe path. The **attachment** path is pinned end to
  end on HTTP: a stale citation is refused `409` with both
  `expected_revision` and `current_revision` in `details`. What no test reaches
  is the **subscribe** path — a `subscribe` open citing a stale revision is
  refused on the capability rung first, so the comparison is never made — and
  the **stdio `open` op's own mapping** of the same error, which no stdio test
  drives on any path.
- **G5 — the 30 s held-subscription window is unpinned.** The hold is bounded
  and released on expiry, but no test waits the window out and asserts the
  release.
- **G6 — the stdio `capabilities` op's `probe_failed` and `internal` paths are
  unpinned.** The HTTP route's `probe_failed` is pinned; the stdio op's own
  mapping is not.
- **G7 — the stdio `resolve` op's `resolution_rejected` and `run_not_found`
  paths are unpinned.** Only the HTTP route's refusals are covered.
- **G8 — the shutdown window's value is unpinned.** Both constants are correct
  and neither has a test that measures the wall clock against them; the *split*
  and the *bound* are pinned, the number is not.
- **G9 — two subscription endings are never driven over stdio, and a third is
  driven only by its name.** `oap-overflow` and `oap-session-closed` have their
  minimal shape pinned by `TestEveryEndingFitsTheFrameLimitFloor`, which encodes
  each real line, but no stdio test produces either ending and reads it off the
  wire — so the members they carry when written in anger are unpinned.
  `oap-frame-limit` **is** driven, by
  `TestAJoinPointTooLargeToFrameFallsBackRatherThanEndingTheSubscription`, but
  only its `event` name is asserted, so its members are unpinned the same way.
  Over HTTP, `oap-overflow` and `oap-session-closed` are pinned and
  `oap-frame-limit` has no counterpart at all, because a socket does not frame.

One asymmetry is deliberate and is **not** a gap: a cross-origin refusal exists
on the HTTP transport alone. A stdio peer is a separate process on the far side
of a pipe, so the `Origin` check is HTTP's alone — and what stdio carries in its
place is the wire-supplied-credential refusal, which applies there too, because a
stdio peer is not an in-process embedding of the core.

## Divergences from the Go tree

Recorded rather than fixed here, per Decision 0032: the draft decides, the wrong
side is fixed, and where it cannot be fixed yet the divergence is written down.
One entry today, and **it must be fixed in Go before
[#387](https://github.com/lsm/open-agent-protocol/issues/387) and
[#388](https://github.com/lsm/open-agent-protocol/issues/388) grow a
byte-for-byte differential job**, because a port that obeys the draft and a Go
that does not would fail that job on its first request.

### D1 — the `Host` allowlist refusal is not a typed error

| | |
| --- | --- |
| **The draft says** | Every refusal on every route of every transport is an `error.response` envelope carrying a declared `code` and a bounded `message`. The `Host` refusal is `403 unrecognized_host`. |
| **Go does** | `servehttp` answers the `Host` refusal with a bare `{"error":"unrecognized Host header; this daemon serves loopback clients only"}` — an object with one prose string and **no code** — while every other refusal on both transports is a typed envelope. |
| **Why Go is the wrong side** | A client cannot branch on a refusal carrying no code, and [Decision 0020](../decisions/0020-error-codes-are-declared.md) exists because the codes are what a host acts on. The status and the prose are right; only the body is wrong. |
| **The fix** | Answer `error.response` with code `unrecognized_host`, keeping the 403 and the wording. One envelope shape, one code to match on. |
| **Pinned today** | `TestHostAllowlist` asserts the 403 and nothing about the body, which is why the shape drifted unpinned. |

### Recorded, and not divergences

Two places where the two trees will *look* different and neither is wrong. A
differential test compares the members, not the prose, at both.

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
