# Decision 0001: Agent-Control v0.1 Executable Core

Status: accepted (admission clause amended by
[Decision 0002](0002-admission-before-start.md); the one-run-per-invocation
clause amended by [Decision 0010](0010-terminal-provenance.md))
Date: 2026-09-06
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`

## Context

Source-level adapter studies of Pi, ACP, Claude Code, Codex, Makai, DeepSeek
Harness, Hermes, and OpenCode found recurring incompatibilities in identity,
run settlement, stream reduction, reverse interactions, cancellation, recovery,
and capability disclosure. Leaving those subjects implicit would let adapters
produce structurally similar but behaviorally incompatible streams.

This decision freezes the smallest subset needed to make the agent-control core
executable. It does not adopt every proposal in
[`research/p0-protocol-gaps.md`](../research/p0-protocol-gaps.md).

## Decisions

### Typed identity domains

OAP identifiers are opaque strings in distinct semantic domains. Envelope `id`
and `in_reply_to`, `participant_id`, `endpoint_id`, `session_id`,
`submission_id`, `run_id`, `message_id`, `tool_call_id`, and `interaction_id`
are not interchangeable, even when their string values happen to match.

`in_reply_to` references an envelope request ID only. Envelope and payload
copies of a scoped ID must agree. Native identifiers may be retained in a
namespaced `extensions` value, but they do not become portable OAP identities.

The executable reference adapter uses process-local identifiers. Durable
native-to-OAP mappings and caller idempotency keys are deferred.

### One foreground invocation is one run

A successful `session.message.submit.response` admits one foreground run. The
executable v0.1 subset allows at most one nonterminal run per session and
supports only `auto` delivery resolving to `start`. `queue`, `steer`, and `btw`
remain optional and unavailable in this subset.

Every accepted run emits exactly one terminal event:

- `run.completed`
- `run.failed`
- `run.cancelled`

A refusal after admission is a typed `run.failed`; a refusal before admission is
an `error.response`. `run.orphaned` is reserved for a later negotiated
revision. A source that cannot eventually settle an accepted run into one of
the three v0.1 terminals cannot claim this executable conformance target.

### Deterministic run event order

Every run-scoped event carries a positive, contiguous `sequence` in one ordering
domain for that `run_id`. The sequence covers lifecycle, content, tool,
interaction, and terminal events. Request and response envelopes do not consume
that sequence.

Events are append-only facts. Deltas append; snapshots replace the identified
state; terminal values are final. A terminal or ending snapshot is authoritative
for reconciliation. Duplicate native observations must not produce duplicate
portable terminals.

### Correlated reverse interactions

Initialization identifies logical participants independently of transport
layout. Permission and user-input requests retain distinct payloads but share a
stable interaction contract:

- `interaction_id`
- `requested_by`
- `responded_by`
- `session_id`
- `run_id`

Only the declared responder may resolve an interaction, and every interaction
has at most one resolution. Tool execution ownership is explicit. Secure
participant reassociation, deadlines, and retained cross-process interactions
are deferred.

### Cancellation intent is not settlement

`run.cancel.response` acknowledges whether cancellation was accepted for
processing; it is not a run terminal. The adapter waits for authoritative
settlement. Natural completion or failure may win a race with cancellation, and
only confirmed interruption emits `run.cancelled`.

Duplicate cancellation of a cancelling or cancelled run is idempotent. A stale
cancellation must not affect a later run. Cancellation of a completed or failed
run returns a typed `run_already_terminal` error. A timeout alone does not prove
cancellation.

### Resume, reconciliation, and replay are separate

- **Resume** restores an attachment to adapter-owned execution or conversation
  state.
- **Reconciliation** returns authoritative current state.
- **Replay** returns historical canonical OAP events from a cursor.

The reference adapter offers a bounded process-memory journal. A retained cursor
returns a contiguous suffix. An expired cursor returns an explicit replay gap
and authoritative state; it never pretends continuity. No cross-process
persistence, exact native-event replay, or durable admission is claimed.

### Capabilities disclose effective fidelity

Capabilities describe effective OAP behavior rather than an ideal native
harness. The support levels remain:

- `native`
- `emulated`
- `degraded`
- `unavailable`

Descriptors additionally disclose semantic properties where needed, including
submission receipts, streaming level, cancellation scope, resume/reconciliation/
replay level, approval scopes, maximum active runs per session, and unknown
event handling. A selected adapter surface and capability revision remain fixed
for a run. Unsupported required behavior fails before side effects.

## Canonical v0.1 vocabulary

The flat core envelope is normative. Agent-control output uses `content.delta`.
`model.content.delta`, `model.stream.*`, provider tool fragments, nested
`scope`/`trace` substitutes, and model-IO vocabulary do not satisfy this core
profile.

The three run terminal events above remain the complete v0.1 terminal set.

## Deferred work

The following remain research or later optional/versioned units:

- durable idempotent admission and cross-replica identity mapping;
- contextual and provisional capability composition;
- queue, steer, side runs, and concurrent foreground runs;
- durable/exact event replay and continuity leases;
- authorization-view transition fencing;
- secure retained-interaction reassociation;
- versioned orphan run/action/interaction terminals;
- first-class subagent and background-task lifecycles.

## Consequences

The executable schemas and validator can reject lifecycle ambiguity before real
adapters are added. Adapters must own a reducer and terminal arbiter rather than
renaming native events mechanically. Native transcript restoration cannot be
advertised as event replay, and session-scoped cancellation forces a concurrency
limit of one unless a stronger capability is negotiated.

The first real adapter should target Codex app-server because its explicit turn
admission, item identity, targeted interruption, reverse approvals, and
single authoritative `turn/completed` event form a strong semantic oracle. Makai
then stress-tests normalization of duplicate terminal and error channels.
