# Decision 0057: An Endpoint That Puts Entries There Is One Advertising the Queue

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `queue` (claim term `+queue`)
Amends: [Decision 0007](0007-queue-delivery.md)

## Context

Decision 0007 requires `session.state.active_runs` wherever `active_run_id`
cannot carry the answer, and names three such cases: a reservation, an
overlap, and, "on an endpoint that puts entries there at all", a run whose
recovery path needs an id only an entry holds, the id of an unresolved
interaction being the one a validator can see. The text does not say how a
reader tells an endpoint that puts entries there from one that does not. Go and
Zig both read it as an endpoint advertising `session.message.delivery.queue`,
and judged the third case only there; Rust does not judge it. The validator
rule register recorded the question as OPEN-0007-2 rather than copying the
trees' reading into a rule.

The other reading was an endpoint that has sent an `active_runs` entry anywhere
in the trace. It makes the obligation depend on what the endpoint happened to
send before, so the same snapshot would be complete in one trace and
incomplete in another, and an endpoint could avoid it by never listing a run.

## Decision

### The interaction case binds an endpoint advertising the queue (OPEN-0007-2)

`active_runs` is required for a run blocked on an unresolved interaction whose
id only an entry holds only where the descriptor advertises
`session.message.delivery.queue`. The advertisement is the commitment to report
runs as entries, so it is what makes an absent list read as an empty one. Where
the queue is not advertised, `active_run_id` and the interaction events carry
the run, and an absent `active_runs` claims nothing. The reservation and
overlap cases are unchanged: they need the list on any endpoint.

## Consequences

- Decision 0007's `session.state` clause states the condition in its own text.
- Go and Zig already judge it this way; Rust owes the port, with fixtures for
  a snapshot omitting the list where the queue is advertised and one where it
  is not.
