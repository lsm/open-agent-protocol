# Harness-Initiated Session State

Status: proposal, for an owner decision
Date: 2026-10-18
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Prompt: the last clause of
[issue #1177](https://github.com/lsm/open-agent-protocol/issues/1177), split
out of [#1179](https://github.com/lsm/open-agent-protocol/pull/1179)

When a harness changes a session's canonical state on its own, with no OAP
request behind the change, the endpoint should publish the change. Today the
endpoint records it and publishes nothing. This document records that gap, the
three places it lives, and a proposal for each. It graduates nothing; a
decision does.

## What the draft already says

[The core draft](agent-control-core.md) lists `session.state.updated` as the
event to "emit when canonical session state changes", and says it "broadcasts
the same shape when canonical state changes". Its sequence is in the session's
own domain, never compared with any run's. Nothing there limits the event to a
change an OAP request caused. So publishing a harness-initiated change is not
new protocol: it is the draft's rule, which no implementation honours outside
a switch or a settings update.

## The case that found it

An ACP agent that changes its own options, through a mode change or its UI,
sends `config_option_update`. Since #1179 both adapters take it into the
session's models list, `current_model_id` and `reasoning_level`. A later
`session.state.request`, `models.request` or switch reports the change, but
no `session.state.updated` announces it. A host that renders the current model
from the event stream shows the old one until it asks.

It is not the only harness event of this kind: OpenCode publishes
`session.model.selected`, which both OpenCode adapters list among the events
they pass over. Whether other harnesses move a session's settings unasked has
not been surveyed.

## Where the gap is

1. **No path from adapter to endpoint.** An adapter hands the endpoint events
   only inside a run's stream, or as the answer to a request.
   `go/adapter/adapter.go` and `zig/src/adapter/contract.zig` carry nothing
   else. The endpoint emits `session.state.updated` itself, and only after
   answering a switch or a settings update: Go at
   `go/serve/serveendpoint/dispatch.go` (the switch and settings branches,
   sequenced by `nextStateSequence` in `endpoint.go`), Zig at
   `zig/src/adapter/endpoint.zig` (the same two).
2. **No fan-out in the hub.** [The hub draft](hub.md) records that the
   `session.state.updated` an endpoint publishes after a settings response
   "has nowhere to go, because the hub's streams are a run's". A host behind
   the hub reads `state`.
3. **A validator rule that assumes requests move the model.** The semantic
   validator's model-control tracking (`go/validation/modelcontrol.go`, and
   `applyModelControl` in `state.go`) treats a change of `current_model_id`
   while a run carries a per-run model selection as `unapplied_control`: "a
   per_run model selection moved the session default". A harness that moves
   its own default mid-run would trip that rule. The rule cannot tell a
   harness's own move from an endpoint that misapplied a per-run selection.

## Proposal

1. **Adapter to endpoint.** Do not add a push channel. After each pump, the
   endpoint compares the adapter's `State` with the last state it published
   for that session, and emits `session.state.updated` when
   `current_model_id`, `reasoning_level` or `compaction_policy` differ. It
   compares only those members, not `updated_at_ms`, so it never publishes
   noise. This needs no adapter change, and covers every harness that already
   records its own changes, as both ACP adapters now do. A later push hook
   could replace the comparison without changing the wire.
2. **Sequencing.** The endpoint numbers the event from the counter that
   already sequences its switch and settings events, so one session has one
   state domain whatever caused the change.
3. **Hub.** Deliver a session's `session.state.updated` to each subscription
   on that session, as an `envelope` line between runs and at its position
   within one, carrying its session-domain `sequence`. A subscription whose
   run has not started yet still receives it. A resumed subscription's
   `after` cursor stays a run cursor, since session events are not replayed.
   A host that reconnects reads `state`, which the draft already makes
   authoritative.
4. **Validator.** Keep `unapplied_control` for an endpoint whose own default
   moved while a per-run selection was active, and exempt a state update the
   endpoint marks as the harness's own. Two ways to mark it: a
   `reason: "harness"` member on the payload, or an extension the validator
   reads. The first is a schema change to `session.schema.json#/$defs/stateUpdated`,
   `protocol/`, a fixture and `clients/ts`; the second keeps the schema but
   makes the validator read an extension to judge a core rule. The proposal is
   the member.

## Questions for the owner

- Is a harness-initiated change canonical state the endpoint must publish, as
  the core draft reads, or is the endpoint free to publish it only on request?
- If it must publish, is comparing after each pump acceptable, or should
  adapters push?
- Should the hub fan session events out to subscriptions now, or only once a
  host needs them?
- Is the `reason` member the right way for the validator to tell a harness's
  move from a misapplied per-run selection?

Until these are settled, both ACP adapters record the change and publish
nothing, which is what #1179 does.

## A harness that ends the session

The DeepSeek harness runtime starts a turn of its own when a background job
settles after a run
([ledger](../research/deepseek-harness-dsh-v0.2.1-rc.2-mapping.md#the-runtime-starts-a-turn-of-its-own-when-a-job-settles)).
Both DeepSeek adapters treat any notification outside a reserved run as
external activity and mark the session unusable. From then on `state` and
`session.message.submit` answer `session_closed`, but nothing announces the
change: a host learns the session ended only when it next asks. A model that
backgrounds a long command therefore ends its session a few seconds after the
run completes.

This is the same gap with `status` as the member that moved, and it changes
two answers above:

- **The comparison needs a pump, and this change has none.** The activity
  arrives between runs, when the endpoint is reading no stream, so comparing
  after each pump never sees it. Publishing it needs the push channel the
  first proposal leaves out: an adapter hook that tells the endpoint a
  session's state changed, which the endpoint answers by reading `State` and
  emitting `session.state.updated` with `status: "closed"` (or `"error"`) in
  the session's state domain.
- **The hub must fan it out between runs.** The third proposal already
  delivers session events to a subscription whose run has not started; this
  case needs it for a subscription with no run at all.

The alternative is to project the runtime's own turn as a run the host did
not submit. OAP admits a run only through `submit` (Decision 0002), so that
is a new admission path, a `run.started` with no request behind it and no
`delivery`, and the validator, both hubs and every SDK would have to accept
it. The proposal is the closure, since it is a state the core already has.

Question for the owner:

- Should an endpoint publish `status: "closed"` when its harness ends a
  session between runs, through an adapter push, or should a harness's own
  turns become runs?
