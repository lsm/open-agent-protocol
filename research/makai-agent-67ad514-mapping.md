# Makai agent-protocol mapping ledger

Status: pinned evidence boundary for the third production OAP adapter. This is
an implementation input, not an interoperability claim.

## Provenance

- Repository: `https://github.com/lsm/makai`
- Release: `v0.2.0` (first release with official binaries:
  linux/macos/windows × amd64/arm64)
- Commit: `9f351fe12448f86b94498b4dfc4f6dfdaf5f1df5`
- Commit tree: `41b5793e363647314d93f929b014fda23d4a4aa6`
- `zig/src/protocol/agent/` tree:
  `3d3f7a767fe24b1363f2f1f51531426a1eebebb8`
- `zig/src/tools/makai.zig` blob:
  `b2cb0c58abd20a1691c19f97386d88bf58123cc8`
- `typescript/src/execution_client.ts` blob:
  `a23cf6c230b14044f1f6a4ca4cd730db4e68ab5b`
- Makai's own OAP deviations ledger at this pin:
  `docs/oap-alignment.md` (blob `f86af24d5e3fb6f440dc9aa61a17a11ee32cd0f3`)
- Normative session-lifecycle counterpart:
  `docs/v1-sdk-agent-provider-spec.md` §13
  (blob `720f638af319b83eb8048d3347ee42a39227d1cd`)
- OAP convergence contract:
  `https://github.com/lsm/open-agent-protocol/issues/3`

Reproduce the pin with:

```sh
git clone https://github.com/lsm/makai.git
git -C makai checkout v0.2.0
git -C makai rev-parse HEAD 'HEAD^{tree}' \
  HEAD:zig/src/protocol/agent \
  HEAD:zig/src/tools/makai.zig \
  HEAD:typescript/src/execution_client.ts
```

Previous pin (2026-09-10, superseded): `67ad51420c3f4d7918218573366fde7db8c35b9c`,
tree `27d32e64ff5efed88c1302de0e25c1acdb9373b2`.

Normative inspected sources are:

- `docs/v1-sdk-agent-provider-spec.md` §13 (session lifecycle, frame routing,
  sequence discipline, admission/settlement and the single terminal arbiter in
  §13.4) and §3.5 (stream lifecycle and error propagation, the event-stream
  projection of §13.4.2's two failure shapes)
- `docs/oap-alignment.md` (makai's deviations ledger against OAP, including
  the RESIDUAL-1..6 catalogue)
- `zig/src/protocol/agent/types.zig`
- `zig/src/protocol/agent/envelope.zig`
- `zig/src/protocol/agent/server.zig`
- `zig/src/protocol/agent/runtime.zig`
- `zig/src/agent/types.zig`
- `zig/src/tools/makai.zig`
- `typescript/src/execution_client.ts`

Moving `main` and issue descriptions are design signals only. They do not
establish behavior at this pin.

## Native boundary

Makai exposes a versioned agent protocol rather than native OAP. Its envelope
contains `version`, `session_id`, `message_id`, `sequence`, optional
`in_reply_to`, timestamp, and a typed payload (`protocol/agent/types.zig`,
`Envelope`). The stable request surface includes:

- `agent_start`
- `agent_message`
- `agent_stop`
- `agent_status`
- `tool_list`
- `models_request`

Responses and observations include `agent_started`, `agent_event`,
`agent_result`, `agent_stopped`, `agent_error`, `session_info`, tool bridge
frames, and model-discovery frames. `makai --stdio` is the preferred
language-neutral process boundary. The TypeScript `createMakaiClient`/
execution client is useful reference behavior, but it is not the production Go
adapter boundary.

There is no portable initialize/capability exchange. The adapter must synthesize
its OAP descriptor, validate native framing and ordering independently, and
advertise only fixture-proven behavior.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| Makai process | endpoint and participant IDs | Adapter allocates typed identities. |
| `session_id` | `session_id` | Keep a typed, endpoint-scoped association. It is correlation identity, not proof of persistence or recovery. |
| accepted `agent_message` | `submission_id` | Allocate after the chosen admission boundary; Makai has no submission ID. |
| one `agent_message` execution | `run_id` | Allocate separately. Never reuse `session_id` or envelope `message_id`. |
| envelope `message_id` | private frame/request correlation | Never expose as an OAP transcript message ID. |
| envelope `in_reply_to` | private reply correlation | Never reuse as a run or interaction ID. |
| envelope `sequence` | native ordering evidence | Validate natively, then generate contiguous OAP run sequence. |
| assistant message lifecycle | `message_id` | Allocate one portable ID across start/update/end. |
| native `tool_call_id` | `tool_call_id` | Namespace by endpoint and session; preserve across both bridge and observation frames. |

Normal outgoing session frames are sequenced, but the pinned server's
request-validation error path emits sequence zero. Native sequence is
therefore not a valid substitute for OAP's positive contiguous per-run
sequence. At v0.2.0 §13.1 formalizes the full discipline: the adapter's
outbound counter (sequence 1 for `agent_start`, +1 per accepted
`agent_message`, consumed by an accepted `agent_stop`, unchanged by rejected
requests and by the non-consuming request types) still matches the server's
accepted-only inbound semantics, and the adapter's inbound validation now
treats receive order as the only ordering authority (see the re-pin section
below).

## Lifecycle mapping

Fidelity is `native`, `normalized`, `synthesized`, `lossy`, or `unsupported`.
Fixture names are requirements, not claims that captures already exist.

| Native observation | OAP meaning | Fidelity | Initial support | Required fixture |
|---|---|---|---|---|
| process starts | initialize response and descriptor | synthesized | emulated | `initialize-minimal` |
| `agent_start` / `agent_started` | associate an OAP session; not a run | normalized | native normalization | `session-start` |
| duplicate live `agent_start` / `agent_busy` | reject before submission admission | normalized | concurrent same-session work unavailable | `duplicate-session-start` |
| validated `agent_message` write/acceptance | allocate submission and run; admit work | synthesized | emulated | `message-admitted` |
| first `agent_start` or `turn_start` event | `run.started` | normalized or synthesized if absent | degraded pending evidence | `completed-text` |
| assistant `message_start` | begin portable message lifecycle | normalized | degraded | `completed-text` |
| assistant text `message_update` | `content.delta` | normalized | native once exercised | `completed-text` |
| assistant `message_end` | close/assemble message, not run terminal | normalized | degraded | `completed-text` |
| `turn_start` / `turn_end` | internal provider-turn diagnostics | lossy/observed-only | no core claim | `multi-turn-tools` |
| `context_usage` event | observed-only: discarded, not folded anywhere at this pin | lossy | no usage claim from events | `usage-fold` |
| `prompt_segment_usage` event | observed-only: discarded, not folded anywhere at this pin | lossy | no usage claim from events | `usage-fold` |
| `tool_streaming` envelope | observed-only: dead wire vocabulary, no producer at this pin | unsupported | no claim | n/a (unproducible) |
| `tool_execution_start` | action requested then started | normalized | degraded | `tool-completed` |
| `tool_execution_update` | action progress | normalized | degraded | `tool-progress` |
| successful `tool_execution_end` | action completed | normalized | degraded | `tool-completed` |
| failed `tool_execution_end` | action failed | normalized | degraded | `tool-failed` |
| `tool_execute` / `tool_result` | private client-hosted execution bridge for the same action | normalized | degraded | `tool-bridge-roundtrip` |
| successful `agent_result` | result evidence; settlement still awaits terminal arbitration | normalized | pending | `result-before-agent-end` |
| `agent_result` with `stop_reason: "error"` | provider-originated failure arriving through the success shape (§13.4.2); one typed `run.failed`, never `run.completed` | normalized | degraded | `provider-error-result` |
| normal `agent_end` | one `run.completed` after child settlement | normalized | terminal normalization | `completed-text` |
| `agent_end` with `max_turns` | completed with explicit limit reason | normalized | terminal normalization | `max-turns` |
| `agent_end` with cancellation | `run.cancelled` only after authoritative settlement | normalized, session-scoped intent | degraded | `cancel-confirmed` |
| error `agent_end` | one typed `run.failed` | normalized | degraded | `failed-run` |
| `agent_error` during active work | candidate failure through the same terminal arbiter | normalized | degraded | `error-plus-agent-end` |
| `agent_stop` / `agent_stopped` | destructive session teardown; correlated stop response is cancellation settlement fallback at this pin when no earlier terminal won | normalized | session stop native, run cancel degraded | `cancel-active` |
| successful run returns session to ready | session remains reusable at server boundary | normalized | native server behavior | `second-message-same-session` |
| TypeScript SDK sends terminal `agent_stop` | one-attempt SDK teardown, not durable session semantics | lossy boundary | unavailable through SDK wrapper | `sdk-terminal-teardown` |
| EOF/process exit before terminal | settle children, then one `run.failed` | synthesized | transport failure handling | `process-exit` |
| duplicate native terminal | diagnose and suppress | synthesized safeguard | terminal invariant | `duplicate-terminal` |

### Terminal authority

Makai defines a single terminal arbiter of its own at this pin, and the adapter
maps onto it rather than substituting for it. §13.4.3: a run that reaches its
own outcome settles exactly once, via result XOR error, never both; children
settle first; the trailing `agent_end` is an aggregate restatement of the same
settlement for event-stream consumers, not a second settlement; and duplicate or
late frames after settlement must not produce a second one. §13.4.2 names the
settlement frames: `agent_result` for success, and for loop-internal failure the
`agent_event` terminal `error` plus settlement `agent_error` envelope, which are
ONE settlement delivered as two frames — a consumer terminates on whichever
arrives first and must not count them twice.

The adapter's reducer therefore must:

1. retain result evidence without independently creating a second terminal;
2. settle every open action before the parent terminal;
3. choose exactly one completed, cancelled, or failed terminal;
4. accept compatible later evidence only as corroboration; and
5. diagnose and suppress duplicate or contradictory evidence.

A non-streaming TypeScript return becoming available at `agent_result` does not
weaken the OAP terminal rule.

Two run-terminal deviations survive the single arbiter at this pin. Neither is a
multiplicity of terminal channels; both are the arbiter settling somewhere other
than where OAP reads a run terminal.

**Cancellation settles the session, not the run (§13.4.4).** A validated
`agent_stop` removes the session mid-run and cancels the run, and the cancelled
run's later result or error publications are discarded because the session no
longer exists. A cancelled run therefore produces NO run settlement frame at
all: the spec states plainly that makai has no run-scoped cancelled terminal,
that OAP's `run.cancelled` is a ledger deviation, and that there is nothing for
a consumer to wait on after `agent_stopped`. The correlated `agent_stopped`
reply is the client's whole terminal observation, and even that has an exception
— when the reply's own publication or direct synchronous write fails, teardown
still completes and the client sees only an uncorrelated runtime error or a
timeout. The adapter consequently synthesizes the run terminal: `Cancel` settles
open tools and emits `run.cancelled` on the correlated `agent_stopped`
(`adapter/makai/session.go`, `Cancel`), which is adapter-owned settlement
standing in for a native frame that does not exist, not a normalization of one
that does. Corpus: `confirmed-destructive-cancel`, plus
`post-stop-stale-publication` for the trailing discarded frames.

**Provider failures settle through the success shape (§13.4.2).** A provider
turn that fails on auth, network, or an invalid URL is converted into a result
message with `stop_reason: "error"` and the provider's own `error_message`; the
loop then completes normally and the run settles through the SUCCESS path — an
`agent_result` frame carrying `stop_reason: "error"`, followed by the trailing
`agent_end`, which per §3.5 carries the same error detail. No `agent_error`
envelope is emitted for these at all. The spec names the failure mode directly:
an adapter that treats every `agent_result` as success will misreport them. Frame
type is therefore not sufficient to classify a settlement here; the payload's
`stop_reason` is load-bearing, and the two failure shapes (this one and the
loop-internal `error`-event/`agent_error` pair) are not interchangeable. The
adapter reads the payload: `finishRun` folds `result.StopReason` into the
terminal decision, fails on a result/end `stop_reason` contradiction, and routes
`error`, `aborted`, and `content_filter` to `run.failed`
(`adapter/makai/session.go`, `finishRun`). Corpus: `provider-error-result`.

## Admission

Makai has no native `submission_id`/`run_id` split and no direct successful
response to `agent_message`. The server accepts the message by mutating its
session to processing and starting execution. The implementation must pin one
precise admission point. A safe initial rule is admission after the complete
request frame is written while retaining the run reservation until native
settlement. Ambiguous post-write failures fail the reserved run rather than
allowing a second submission to inherit late observations.

The accepted native session starts with request sequence `1` for `agent_start`;
the first `agent_message` must therefore use request sequence `2`. A rejected
message can return a synchronous sequence-zero `agent_error` correlated to the
`agent_message` frame even though message submission is otherwise one-way. The
transport retains sent-frame identity long enough to classify that exact
correlated error as an ordered observation; correlation to any other completed
or unknown one-way request remains fatal.

Only one in-flight run is allowed per mapped session. This is both the initial
OAP policy and protection against the pinned TypeScript waiter's session-based
routing problem tracked by Makai issue #201.

## Cancellation and teardown

`agent_stop` removes a session and indirectly raises the active agent's
cancellation flag. It is session-scoped teardown, not native run-targeted
cancellation, and §13.4.4 makes the consequence normative: a cancelled run
produces no run settlement frame, so `agent_stopped` is the only terminal
observation a client gets. Therefore:

- natural completion or failure observed before the stop response may win the
  race;
- a correlated `agent_stopped` acknowledges destructive teardown;
- stop removes the session before the detached execution publishes its final
  `agent_end`, so that later publish is rejected and discarded — at v0.2.0 the
  discard is bound to the session's registration generation (#204), not merely
  the id lookup, and one exception exists: frames the old registration already
  committed to its outbox before the stop still drain afterward, arriving
  after the correlated `agent_stopped` with lower allocated sequence numbers
  (§13.1: consumers must not order frames by observed allocated sequence);
- the adapter must therefore settle cancellation from correlated
  `agent_stopped` when no earlier terminal evidence won, and ignore trailing
  stale-registration frames for the already-settled run;
- stale cancellation must never target a replacement run; and
- a successful run cannot be reported as cancelled merely because stop was
  requested.

Initial OAP cancellation support is degraded and session-destructive. The
`agent_stopped` fallback is a pinned implementation impedance mismatch, not a
claim that the native frame is generally run-targeted terminal authority.

## Session state and recovery

At this pin, `session_id` names an in-memory correlation/container. An unknown
ID creates fresh state and a live ID may be busy. It does not imply transcript
load, resume, reconciliation, event replay, or cross-process recovery. At
v0.2.0 an idle session is also silently evicted after the TTL (default 30
minutes; in-flight runs are never selected), so a previously-live ID can turn
session-gone between messages — the only evidence is the correlated
`agent_not_found` on the next send. The server can reuse a successful session,
while the TypeScript execution client tears down each attempt. The adapter
implements and documents the server model; it must not blend these two
behaviors.

Native load, resume, replay, and durability are unavailable. If adapter-owned
journaling is added, its bounded persistence scope is a separate degraded OAP
capability.

## Tool policy

The native `tool_execute`/`tool_result` bridge and `tool_execution_*`
observations describe one logical invocation. The adapter correlates both using
the namespaced portable `tool_call_id` and never emits duplicate OAP actions.
A parent terminal closes any unfinished tool before settlement.

The mapped agent protocol does not expose a general user permission
interaction. Permission support remains unavailable rather than inferred from
tool hosting.

## Residual observation classification (P0 #12)

P0 #12 requires every native observation to be classified. Three went unwritten
and are recorded here. All three are `observed-only`: the reducer discards them
(`adapter/makai/session.go`, the `tool_streaming` arm of the frame switch and
the discard arm of `applyEvent`), and no OAP envelope depends on them.

| Native observation | Where it lives | Classification | Basis |
|---|---|---|---|
| `tool_streaming` | top-level envelope payload | observed-only, `unsupported` | no producer anywhere in makai at this pin (see below) |
| `context_usage` | agent event inside `agent_event.event_json` | observed-only, `lossy` | produced; deliberately unmapped — OAP usage is reported from `agent_result` totals only |
| `prompt_segment_usage` | agent event inside `agent_event.event_json` | observed-only, `lossy` | produced; deliberately unmapped — per-segment prompt accounting has no core carrier |

`tool_streaming` is dead wire vocabulary. It is fully serialized in both
directions — a payload union member in `zig/src/protocol/agent/types.zig` with
a deinit arm, plus a serializer arm and a parser arm in
`zig/src/protocol/agent/envelope.zig` — and nothing produces one. Verified two
ways: reading `types.zig`, `envelope.zig`, `server.zig`, `runtime.zig`, and
`client.zig` at `9f351fe…` finds no construction site outside the codec, and a
repository-wide search returns those two protocol files and nothing else — no
agent or tool path, no SDK, no test. Being unproducible, it cannot be pinned by
a corpus case without
hand-fabricating a frame makai never sends; classifying it here is the whole
obligation P0 #12 imposes on it. Our decoder must keep accepting it — a frame
the codec rejects is a fail-closed transport error, and a type the wire
vocabulary defines must not become one — and the reducer must keep discarding
it. Should makai ever give it a producer, it is a tool-progress carrier
overlapping `tool_execution_update`, and P0 #11's deduplication rule governs
it.

`context_usage` and `prompt_segment_usage` are produced: both are `AgentEvent`
union members in `zig/src/agent/types.zig`, emitted from `agent_loop.zig` and
consumed by makai's own TUI. They reach this adapter inside `agent_event`, and
it drops them. The adapter's usage claim comes from `agent_result`'s `input`,
`output`, `cache_read`, and `cache_write` totals on the terminal, never from
these events, so the ledger makes no streaming-usage claim and advertises none.
Their byte and estimated-token fields are provider-side estimates of prompt
composition, not settled accounting, and folding an estimate into a terminal
`usage` a consumer reads as authoritative would be exactly the silent
compensation this ledger forbids.

## Re-pin to v0.2.0 (2026-09-12)

The re-pin gate below is met at `v0.2.0` (`9f351fe…`): the v1.1 lifecycle and
frame-routing specification (§13) with makai's own deviations ledger
(`docs/oap-alignment.md`), #198's rename, #201's correlated waiter routing,
and #202's idle-session eviction all landed. v0.2.0 is also the first release
shipping official binaries, which the live process gate can bind by digest
(`OAP_MAKAI_SHA256`). The wire is largely compatible; this re-pin applies
targeted updates and records the deltas below. No OAP protocol or schema
change is proven by any finding at this pin, so none is raised. Advertised
capabilities are unchanged, so the capability revision string
(`makai-agent-67ad514-oap-v1`) and corpus directory name are retained as the
adapter's stable identities, matching the DeepSeek re-pin convention; the
descriptor version now reports `9f351fe`.

### `agent_start` key rename (#198)

The canonical payload key is now `session_id`; `resume_session_id` survives as
a permanent server-side parse alias carrying the same value, and makai's own
emitters (Zig serializer, TS SDK) send both keys transitionally so
pre-rename servers keep binding the caller's id. The adapter mirrors both
sides: emission sets both keys to one value (`AgentStart.SessionID` +
`AgentStart.ResumeSessionID`), strict decode accepts either key with the
canonical one winning when both appear (makai's deserializer semantics), and
the envelope-agreement check applies to whichever key carried the id
(`AgentStart.EffectiveSessionID`). Semantics were already fixed: the key is
correlation only, never a resume handle. The adapter never omits the payload
id, so the server's id-generating exception does not apply.

### Sequence discipline (§13.1)

Inbound (adapter→makai): only an ACCEPTED `agent_message` advances the
server's expected counter; an accepted `agent_stop` consumes it with the
session; rejected requests and the non-consuming request types (`agent_status`,
`ping`, `tool_list`, `models_request`, `goodbye`) never advance it. This
matches the adapter's existing numbering (start at 1, +1 per written message,
+1 on stop), so the outbound counter is unchanged. Known limitation, unchanged
from the previous pin and now documented rather than compensated: a
request-correlated rejection of our `agent_message` fails the reserved run
without rolling the adapter's counter back, so the burned sequence leaves the
mapped session unable to admit a further native message (the server would
answer `invalid_request`). Makai's own clients reconcile exactly this case
(#210 gap 7 tracker rollback); an adapter-side retry policy would be new
behavior, not a re-pin, and stays out until demanded with evidence.

Outbound (makai→adapter): allocated frames (`agent_started`, `agent_event`,
`agent_result`, `agent_stopped`, settlement `agent_error`, `tool_execute`,
`ack`, `nack`) draw one monotonic per-registration counter describing
ALLOCATION order, not observed wire order — verified from the pinned sources:
`dispatchInboundLine` writes synchronous replies directly (`runtime.zig`
`pumpClientMessages`) while queued run output flushes later
(`pumpServerOutbox`), so the correlated `agent_stopped` can legally arrive
before an already-queued lower-numbered `agent_event`, and a retried
publication may burn a counter value leaving a gap (#210 gap 5). Echo replies
(`session_info`, `pong`, `tool_list_response`) copy the request's inbound
sequence verbatim, and request-validation `agent_error` carries `sequence: 0`.
Consumers MUST NOT order echo replies against allocated frames by sequence.

Adapter impact: the transport's strict per-session `+1` continuity check over
allocated frames — written against the old pin, where allocation-failure paths
could only produce duplicate sequences — is retired. Receive order is the
adapter's only ordering authority (it already renumbers into contiguous OAP
run sequence from receive order); duplicate-frame rejection by `message_id`
and the zero-reserved-for-correlated-`agent_error` rule remain enforced. Echo
reply types cannot reach this adapter on its request mix (it never sends
`agent_status`/`ping`/`tool_list`), and the reducer already classifies them
observed-only if they ever appear.

### `tool_result` correlation (#210 gap 6)

The stdio host now correlates each `tool_result` by `in_reply_to` against the
current outstanding `tool_execute`'s `message_id` and discards
mismatched/absent/unsolicited replies. No adapter impact: the adapter never
emits `tool_result` (client-hosted execution is unavailable) and treats the
frame type as observed-only. Recorded per the feedback rule; no OAP change.

### New observable server behaviors

- Idle-session TTL eviction (#202/#206): silent sweep (30-minute default,
  `MAKAI_AGENT_SESSION_IDLE_TTL_MS`, `0` disables), never selecting sessions
  with in-flight runs; the first observable evidence is a request-correlated
  sequence-zero `agent_error` `agent_not_found` ("session not found") on the
  next `agent_message`. The adapter treats that correlated answer as
  session-retirement evidence — the run settles through the ordinary failure
  terminal and the mapped session becomes closed to further submissions and
  state reads (`ErrSessionClosed`), since every later native message would
  fail against the dead association (and the adapter's burned outbound
  sequence can never resynchronize). Corpus: `evicted-session-gone`
  (ledger fixture `idle-eviction-session-gone`), whose
  `retire_after_terminal` check drives a further submission and a state read
  after the terminal.
- Registration generations (#204): stale publications from a stopped or
  evicted session's registration are discarded with no cross-registration
  attribution; the one wire-visible residue is outbox frames the old
  registration already committed draining after the correlated
  `agent_stopped`. Corpus: `post-stop-stale-publication`
  (ledger fixture `post-stop-stale-publication`) — the trailing stale
  `agent_end` neither re-settles the cancelled run nor leaks an event.
  The adapter never re-registers an id, so counter restarts across
  registrations are out of scope.
- Transactional publication / outbox retry (#210 gaps 1–5): retries are not
  re-publication, session status flips ride the committed frame, and legal
  allocated-sequence gaps/reordering may appear on the wire. The gap and
  reorder tolerance is pinned by transport tests
  (`TestClientToleratesAllocatedSequenceGapsAndReorder`); the corpus cases
  above carry the observable shapes.

### Cross-references into makai's ledger

Makai's `docs/oap-alignment.md` at this pin records the same contract from
the makai side, including RESIDUAL-1..6. Of those, RESIDUAL-5 (`agent_result`
carries no run identity — settlement attributes to the oldest pending
message) is structurally absorbed by the adapter: it never overlaps
same-session sends (one in-flight run enforced), so the adapter's own run
identity is authoritative. RESIDUAL-1/3/4 (stale admission/output across a
silently reused id) cannot arise on this adapter's exclusive-id,
no-re-registration usage. RESIDUAL-2's local mitigation (per-request
correlation via `in_reply_to`) is exactly the correlated-rejection routing
the transport already performs. RESIDUAL-6 is SSE-transport-only and outside
this stdio adapter. Per the feedback rule, any future mismatch that cannot be
resolved by re-pinning resolves as an OAP issue/decision or a makai issue —
never silent adapter compensation.

## P0 mismatches

1. **No submission/run split:** allocate distinct typed IDs in the adapter.
2. **No explicit admission response:** pin the write/acceptance boundary and
   fail ambiguous delivery safely.
3. **Unsafe same-session waiter routing:** resolved server-side at v0.2.0
   (#201/§13.3: `in_reply_to`-aware routing); the adapter still enforces one
   in-flight operation per session as its own policy.
4. **Misleading resume-era naming:** resolved at v0.2.0 (#198) — the canonical
   `agent_start` key is `session_id` with `resume_session_id` a permanent
   alias; the adapter emits both keys and decodes canonical-wins.
5. **Session-destructive cancellation:** advertise degraded cancellation and
   distinguish intent from settlement.
6. **No lifecycle eviction:** resolved server-side at v0.2.0 (#202/§13.2.6:
   silent idle-TTL eviction, session-gone answers, never evicting in-flight
   runs); the adapter still makes no native lifecycle-ownership claim and
   bounds its own resources.
7. **Native sequence is not an ordering domain:** at v0.2.0 §13.1 formalizes
   the two-class discipline (allocated vs echo, validation errors at zero,
   legal gaps and interleaving); the adapter validates the zero rule, rejects
   duplicates by `message_id`, renumbers from receive order, and emits
   adapter-owned contiguous OAP run sequence.
8. **Frame identity is not transcript identity:** maintain typed registries.
9. **Run settlement escapes the run (two deviations):** the "multiple terminal
   channels" framing this entry carried through the v0.2.0 re-pin was already
   stale when the re-pin landed, and claimed less than the truth. §13.4.3 —
   present at `9f351fe…`, in the same spec section the re-pin gate's first item
   named — defines a single terminal arbiter: result XOR error, never both,
   children settled first, with the trailing `agent_end` an aggregate
   restatement rather than a second settlement. Makai has one arbiter. What
   survives is narrower and more consequential: the arbiter settles in two
   places OAP does not read a run terminal.
   (a) **A cancelled run produces no run-scoped settlement at all** (§13.4.4).
   `agent_stop` removes the session, the cancelled run's later publications are
   discarded, and makai has no run-scoped cancelled terminal; the correlated
   `agent_stopped` is the client's entire terminal observation, and it can
   itself go missing when its publication fails. OAP's `run.cancelled` is
   adapter-synthesized settlement with no native counterpart — see Terminal
   authority and Cancellation and teardown.
   (b) **Provider failures settle through the success shape** (§13.4.2).
   `agent_result` carrying `stop_reason: "error"` plus `error_message` is the
   settlement, with no `agent_error` envelope emitted; the same frame type
   settles successes and provider-originated failures, so classification is by
   payload, not by frame type. The spec names the resulting defect for an
   adapter that assumes otherwise.
   The adapter satisfies both — it emits `run.cancelled` from the correlated
   `agent_stopped`, and `finishRun` folds `result.StopReason` into the terminal
   decision — and the corpus now pins the second (`provider-error-result`).
10. **Server and SDK session models differ:** implement one explicit boundary.
11. **Tool bridge and tool observations overlap:** deduplicate one action.
12. **Unknown observations:** classify each as mapped, observed-only,
    required-unmapped/fatal, or unsupported request.

Per OAP issue #3, each mismatch must lead to an explicit OAP decision/revision
or a Makai fix. Safety compensation in an adapter remains visible in this
ledger and in fixture omissions; it is never silent convergence.

## Initial capabilities

- initialize and capability revision: `emulated`;
- session association: `native` normalization;
- session state: `degraded`, process-local observation;
- submission/admission: `emulated`;
- run identity/status/sequence: `emulated`;
- one foreground run per session: enforced;
- text streaming: `native` once exercised;
- tool lifecycle and progress: `degraded` until complete fixtures pass;
- configured model selection: native input, not a catalog;
- model catalog: unavailable until authority and failure behavior are exercised;
- cancellation: `degraded`, session-destructive;
- idle session stop: native normalization;
- load, resume, replay, cross-process recovery, concurrency, queue, steer, BTW,
  side runs, permission interactions, and exact replay: unavailable.

## Evidence corpus

Each case must include native JSONL, expected OAP envelopes, typed identity and
sequence maps, capability snapshot, provenance, mapping classifications, and an
omissions/deviations ledger. Materialized OAP traces pass both schema and
stateful semantic validation.

Representative positive fixtures:

- `initialize-minimal`, `session-start`, `message-admitted`
- `completed-text`, `result-before-agent-end`, `max-turns`, `failed-run`
- `multi-turn-tools`, `tool-completed`, `tool-progress`, `tool-failed`
- `tool-bridge-roundtrip`, `second-message-same-session`
- `session-status-ready-processing`, `session-stop-idle`
- `cancel-active`, `cancel-confirmed`, `sdk-terminal-teardown`, `process-exit`

Required degradation and fault fixtures include:

- no implied load/resume/replay from `session_id`;
- frame `message_id` distinct from transcript `message_id`;
- contiguous OAP sequence despite native zero/error sequence;
- model configuration distinct from model catalog;
- locally rejected unavailable operations;
- malformed/version-invalid envelope and nested event JSON;
- duplicate and zero native sequence;
- unmatched/foreign `in_reply_to` and duplicate frame IDs;
- overlapping starts/messages and issue #201 cross-consumption reproduction;
- stop before start, stop during processing, and stale stop;
- completion-winning and cancellation-winning races;
- result/end omission, contradiction, duplication, and error-plus-end;
- a provider-originated failure arriving through the success shape — an
  `agent_result` carrying `stop_reason: "error"` with no `agent_error` envelope
  anywhere in the trace (`provider-error-result`);
- tool update before start, duplicate terminal, unfinished child, and late result;
- EOF before admission certainty and during settlement;
- bounded frame/queue rejection and stdout contamination;
- idle-TTL eviction surfacing as a correlated session-gone run failure
  (`evicted-session-gone`);
- stale-registration trailing publication drained after a confirmed stop
  (`post-stop-stale-publication`).

Native-sequence fault coverage follows §13.1 at this pin: duplicate frames are
rejected by `message_id`, zero is reserved for correlated validation errors,
and allocated-sequence gaps/reordering are legal wire shapes (pinned by the
transport tests) rather than corpus faults.

## v1.1 deviations and re-pin gate

Makai's v1.1 proposal (PR #203) landed as spec §13 plus
`docs/oap-alignment.md`. At the current pin (`v0.2.0`):

| Concept | Classification |
|---|---|
| session correlation identity | aligned, without persistence implication |
| resume-era naming | renamed by #198; legacy key a permanent alias |
| endpoint/participant IDs | absent by design; adapter allocates |
| submission/run split | deviating: absent natively |
| envelope `message_id` | deviating: frame identity |
| `in_reply_to` | aligned (#201 routing landed) |
| sequence | deviating: two-class per-registration allocation vs echo; zero for validation errors |
| admission versus settlement | deviating: no explicit message admission response |
| one terminal arbiter | aligned (§13.4.3: result XOR error, children first, trailing `agent_end` a restatement) |
| cancelled-run settlement | deviating: no run-scoped terminal frame exists (§13.4.4); the adapter synthesizes `run.cancelled` from the correlated `agent_stopped` |
| provider-originated failure | deviating: settles through the success shape (§13.4.2: `agent_result` with `stop_reason: "error"`, no `agent_error`); classified by payload, not frame type |
| run-scoped cancellation | absent by design |
| `agent_stop` | aligned only as session teardown/intention |
| load/resume/reconciliation/replay | absent by design |
| TTL, eviction, connection ownership | implemented (#202; §13.2.6) |

The original re-pin gate — (1) the merged v1.1 lifecycle/frame-routing
specification and deviations ledger, (2) #198's honest session naming and
compatibility behavior, (3) #201's correlation-correct waiter routing and
overlap tests, (4) #202's normative eviction, cancellation, and ownership
behavior — is fully met at `v0.2.0`, and this re-pin exercised it: pins
recomputed, every mapped source diffed, all fixtures regenerated, capabilities
unchanged (no new evidence requires a different claim). Retain the old-pin
understanding as compatibility regression context.

### Re-pin obligation: re-walk every P0, not the gate

A re-pin must visit EVERY entry in the P0 mismatch list and record a verdict for
each — resolved, still deviating, or reframed — not only the items its own gate
names. A gate names what made the re-pin worth doing. It is not the scope of the
review, and treating it as one lets a mismatch go stale in place: the list still
reads as current, so nobody re-reads it, and the staleness compounds at the next
re-pin.

This rule exists because that happened here. P0 #9 claimed "multiple terminal
channels" and survived the v0.2.0 re-pin unexamined, even though §13.4.3 — a
single terminal arbiter, which contradicts the entry outright — landed inside
the very specification the gate's first item required. Meanwhile the two
deviations that actually survive at the pin (§13.4.4's cancelled run with no
run-scoped settlement, §13.4.2's provider failure settling through the success
shape) went unrecorded, so the ledger simultaneously overstated one mismatch and
omitted two sharper ones. Four gate items were walked; the other eight P0
entries were not. An external reader found the residue before we did.

A verdict is cheap and the walk is bounded — twelve entries. Record the verdict
even when it is "unchanged", because an unexamined entry and an entry confirmed
unchanged are indistinguishable in the file afterwards, and only one of them is
evidence.

## Deferred scope

This pin does not yet claim an implemented adapter, native OAP wire support,
durable identity storage, journaling, load/resume/reconciliation, cross-process
recovery, queue/steer/BTW/side runs, same-session concurrent runs, run-scoped
cancellation, lifecycle eviction, permission interactions, authoritative
catalogs, arbitrary multimodal projection, exact usage parity, or automatic
compatibility with newer Makai revisions.
