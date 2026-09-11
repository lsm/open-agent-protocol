# Makai agent-protocol mapping ledger

Status: pinned evidence boundary for the third production OAP adapter. This is
an implementation input, not an interoperability claim.

## Provenance

- Repository: `https://github.com/lsm/makai`
- Commit: `67ad51420c3f4d7918218573366fde7db8c35b9c`
- Commit tree: `27d32e64ff5efed88c1302de0e25c1acdb9373b2`
- `zig/src/protocol/agent/` tree:
  `0a21997a9bc6d4358a8ca549bb2f31c623f4583b`
- `zig/src/tools/makai.zig` blob:
  `feccd54dde57fa2a5eafec97dd880bf8c63121c0`
- `typescript/src/execution_client.ts` blob:
  `d3a1e9d6c28372f271c33501d33a9290c55a3a91`
- OAP convergence contract:
  `https://github.com/lsm/open-agent-protocol/issues/3`

Reproduce the pin with:

```sh
git clone https://github.com/lsm/makai.git
git -C makai checkout 67ad51420c3f4d7918218573366fde7db8c35b9c
git -C makai rev-parse HEAD 'HEAD^{tree}' \
  HEAD:zig/src/protocol/agent \
  HEAD:zig/src/tools/makai.zig \
  HEAD:typescript/src/execution_client.ts
```

Normative inspected sources are:

- `zig/src/protocol/agent/types.zig`
- `zig/src/protocol/agent/envelope.zig`
- `zig/src/protocol/agent/server.zig`
- `zig/src/protocol/agent/runtime.zig`
- `zig/src/agent/types.zig`
- `zig/src/tools/makai.zig`
- `typescript/src/execution_client.ts`

Moving `main`, the open v1.1 proposal, and issue descriptions are design
signals only. They do not establish behavior at this pin.

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

Normal outgoing session frames are sequenced, but the pinned server error path
can emit sequence zero. Native sequence is therefore not a valid substitute for
OAP's positive contiguous per-run sequence.

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
| context/prompt usage | fold into bounded state or terminal metadata | normalized/optional | no streaming claim | `usage-fold` |
| `tool_execution_start` | action requested then started | normalized | degraded | `tool-completed` |
| `tool_execution_update` | action progress | normalized | degraded | `tool-progress` |
| successful `tool_execution_end` | action completed | normalized | degraded | `tool-completed` |
| failed `tool_execution_end` | action failed | normalized | degraded | `tool-failed` |
| `tool_execute` / `tool_result` | private client-hosted execution bridge for the same action | normalized | degraded | `tool-bridge-roundtrip` |
| successful `agent_result` | result evidence; settlement still awaits terminal arbitration | normalized | pending | `result-before-agent-end` |
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

The host deliberately publishes `agent_result` before a trailing `agent_end`.
`agent_error` provides another possible terminal signal. A single reducer must:

1. retain result evidence without independently creating a second terminal;
2. settle every open action before the parent terminal;
3. choose exactly one completed, cancelled, or failed terminal;
4. accept compatible later evidence only as corroboration; and
5. diagnose and suppress duplicate or contradictory evidence.

A non-streaming TypeScript return becoming available at `agent_result` does not
weaken the OAP terminal rule.

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
cancellation. Therefore:

- natural completion or failure observed before the stop response may win the
  race;
- a correlated `agent_stopped` acknowledges destructive teardown;
- at this pin, stop removes the session before the detached execution publishes
  its final `agent_end`, so that later publish is rejected and discarded;
- the adapter must therefore settle cancellation from correlated
  `agent_stopped` when no earlier terminal evidence won;
- stale cancellation must never target a replacement run; and
- a successful run cannot be reported as cancelled merely because stop was
  requested.

Initial OAP cancellation support is degraded and session-destructive. The
`agent_stopped` fallback is a pinned implementation impedance mismatch, not a
claim that the native frame is generally run-targeted terminal authority.

## Session state and recovery

At this pin, `session_id` names an in-memory correlation/container. An unknown
ID creates fresh state and a live ID may be busy. It does not imply transcript
load, resume, reconciliation, event replay, or cross-process recovery. The
server can reuse a successful session, while the TypeScript execution client
tears down each attempt. The adapter implements and documents the server model;
it must not blend these two behaviors.

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

## P0 mismatches

1. **No submission/run split:** allocate distinct typed IDs in the adapter.
2. **No explicit admission response:** pin the write/acceptance boundary and
   fail ambiguous delivery safely.
3. **Unsafe same-session waiter routing:** enforce one in-flight operation until
   issue #201 is resolved and exercised.
4. **Misleading resume-era naming:** treat `session_id` only as correlation;
   issue #198 governs native naming cleanup.
5. **Session-destructive cancellation:** advertise degraded cancellation and
   distinguish intent from settlement.
6. **No lifecycle eviction:** bound adapter resources and make no native
   lifecycle-ownership claim; issue #202 governs server behavior.
7. **Native sequence includes exceptional zero:** validate separately and emit
   adapter-owned OAP sequence.
8. **Frame identity is not transcript identity:** maintain typed registries.
9. **Multiple terminal channels:** use one terminal arbiter.
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
- duplicate, zero, regressed, and gapped native sequence;
- unmatched/foreign `in_reply_to` and duplicate frame IDs;
- overlapping starts/messages and issue #201 cross-consumption reproduction;
- stop before start, stop during processing, and stale stop;
- completion-winning and cancellation-winning races;
- result/end omission, contradiction, duplication, and error-plus-end;
- tool update before start, duplicate terminal, unfinished child, and late result;
- EOF before admission certainty and during settlement;
- bounded frame/queue rejection and stdout contamination.

## v1.1 deviations and re-pin gate

Makai PR #203 is a docs-only v1.1 proposal at the time of this pin. Its
`aligned`, `renamed`, `deviating: reason`, and `absent by design` vocabulary is
useful for the cross-repository ledger, but its prose is not runtime evidence.
At `67ad514...`:

| Concept | Classification |
|---|---|
| session correlation identity | aligned, without persistence implication |
| old resume-oriented naming | renamed pending #198 |
| endpoint/participant IDs | absent by design; adapter allocates |
| submission/run split | deviating: absent natively |
| envelope `message_id` | deviating: frame identity |
| `in_reply_to` | structurally aligned; waiter routing deviates pending #201 |
| sequence | deviating: native session/frame ordering and exceptional zero |
| admission versus settlement | deviating: no explicit message admission response |
| one terminal arbiter | deviating: result/end/error require normalization |
| run-scoped cancellation | absent by design |
| `agent_stop` | aligned only as session teardown/intention |
| load/resume/reconciliation/replay | absent by design |
| TTL, eviction, connection ownership | deviating/unimplemented pending #202 |

Remain pinned until one newer commit contains all of:

1. the merged v1.1 lifecycle/frame-routing specification and deviations ledger;
2. issue #198's honest session naming and compatibility behavior;
3. issue #201's correlation-correct waiter routing and overlap tests; and
4. issue #202's normative eviction, cancellation, and ownership behavior.

PR #203 alone is not a re-pin event. Once all four land, pin the first complete
commit, recompute hashes, diff every mapped source, rerun all positive and
fault fixtures, and change capabilities only where new evidence passes. Retain
old-pin fixtures as compatibility regression evidence.

## Deferred scope

This pin does not yet claim an implemented adapter, native OAP wire support,
durable identity storage, journaling, load/resume/reconciliation, cross-process
recovery, queue/steer/BTW/side runs, same-session concurrent runs, run-scoped
cancellation, lifecycle eviction, permission interactions, authoritative
catalogs, arbitrary multimodal projection, exact usage parity, or automatic
compatibility with newer Makai revisions.
