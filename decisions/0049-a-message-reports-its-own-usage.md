# Decision 0049: A Message Reports Its Own Usage

Status: accepted 2026-10-10 (the owner chose a new run event over a `usage`
member on `content.delta` and over a per-message list on the run's terminal;
what `usage` counts defined by
[Decision 0063](0063-usage-counts-cache-and-reasoning-tokens.md))
Date: 2026-10-10
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `core`

## Context

Agent control reports usage once per run, on `run.completed`, `run.failed` and
`run.cancelled`. A run that calls a tool and then answers is at least two model
calls, and the run's figure is their sum. A consumer measuring output speed per
message, which the `oapx` TUI does for its status bar, can attribute the run's
figure only when the run produced one assistant message. For every other run it
falls back to estimating each message's tokens from its text, so an agentic
turn shows an estimate where the endpoint knew the exact count.

`content.delta` streams a message's parts but nothing marks the message
finished, and the provider reports a call's usage only when that call ends.

Three shapes were weighed. A `usage` member on `content.delta` needs no new
type, but the producer would have to hold back each message's last delta until
the call's usage arrived, and the stream would still not say a message ended. A
per-message list on the run's terminal is the smallest schema change and adds
no ordering rule, but it arrives only when the run ends, so a consumer corrects
its rates after the fact. A run event closing the message gives both the exact
figure and the boundary, at the moment the call ends.

## Decisions

1. **`content.completed` is a run event** carrying `session_id`, `run_id`,
   `message_id` and an optional `usage`, the same `usage` shape the run's
   terminal carries. It consumes the run's next `sequence`, is subject to every
   rule a run event is (after `run.started`, before and never after the
   terminal), and needs no capability: an endpoint may emit it or not.
2. **It closes the message it names, once.** A second `content.completed` for
   the same `message_id` in a run is `duplicate_message_completion`, and a
   `content.delta` naming a completed `message_id` is
   `content_after_completion`. Both are decidable from the trace.
3. **`message_id` is required on it**, though it stays optional on
   `content.delta`: an event that closes a message must say which one.
4. **A message need not have streamed to be completed.** A message carrying only
   a tool call has no `content.delta`, and still made a model call worth
   reporting.
5. **`usage` reports one call, and absence is unknown.** It is the provider's
   figure for the call that produced this message, not a running total. An
   absent `usage` says nothing about cost, as Decision 0035's absence rule says
   for model facts.

## Consequences

- Schema `run.schema.json#/$defs/contentCompleted`, the envelope union, `go/protocol`, both validators and `clients/ts/src/protocol.ts` carry the type. Fixtures: `content-completed-per-message-usage` (valid, including a message completed without streaming and one completed without `usage`), `content-completed-twice`, `content-after-completion` and `content-completed-without-message-id`.
- A consumer that ignores the event loses nothing it had: the run's `usage` is unchanged.

## What this decision does not admit

- It does not judge that the messages' `usage` sums to the run's. A run may make
  calls that produce no message, such as a compaction, so the sum is an
  obligation the endpoint keeps where it can, not a rule.
- It does not oblige an endpoint to emit the event, nor a consumer to wait for it.
