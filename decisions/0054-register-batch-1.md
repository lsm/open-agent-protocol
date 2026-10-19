# Decision 0054: Register Batch 1 — Pending Sets Without a Position, and Provider Attachment

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0007](0007-queue-delivery.md) and
[Decision 0028](0028-live-model-and-provider-control.md)

## Context

The validator rule register being assembled under `drafts/validator-rules/`
holds every behaviour a validator judges to the decision text that states it.
Where the text was silent, or the trees disagreed, the behaviour was recorded
as an open item, numbered as in the headings below, instead of being copied
from whichever tree had it. This decision settles the first batch of those items, and states each
answer in the decision it belongs to, because a rule that lives only in a
register row is one reader's interpretation rather than the protocol's.

## Decisions

### An entry's pending set is judged whether or not the queue is advertised (OPEN-0007-1)

Decision 0007 reads an `active_runs` entry that states no position "as the
trace stands". Go and Zig applied that to the entry's `pending_interactions`
only where the descriptor advertised `session.message.delivery.queue`. The
condition belongs to a different rule: whether `active_runs` must be present at
all where a run's recovery needs an id only an entry holds, which 0007 ties to
"an endpoint that puts entries there at all". An entry that exists has already
answered that question. An entry naming pending interactions must state the
position it caught them at, so an entry stating no position names none, and is
judged as the trace stands: whatever the descriptor advertises, it is wrong if
its run has an unresolved interaction. The omission rule keeps its condition.

### A refused attachment is refused like every other gated control (OPEN-0028-1, OPEN-0028-3)

Decision 0028 refuses an unsupported attachment with `unsupported_feature`
naming `action.providers.attach`, "with the normal degraded opt-in rule when
applicable". The normal rule is Decision 0005's gate, and both of its halves
apply: a refusal of an attachment the endpoint does not advertise carries
`details.reason: "unadvertised"`, and an attachment sent to a `degraded`
disclosure without the caller's opt-in is refused `capability_degraded` naming
the feature, and that refusal is judged.

### An acceptance needs the `session_live` mode (OPEN-0028-2)

Decision 0028 has an endpoint advertise `action.providers.attach` "with the
`session_live` mode to accept" an attachment. The mode is therefore a
precondition of each acceptance, not only a disclosure the descriptor owes: a
descriptor that advertises the feature without the mode is
`undisclosed_attach_modes`, and an attachment accepted under it is
`unavailable_capability` as well.

### Only a catalog that binds owes the attached providers (OPEN-0028-4)

After an accepted attachment, the next catalog of that session lists the
attached provider and its models. Decision 0006 makes a catalog bind only when
it cites the active revision with its descriptor in hand — not one citing
another revision, and not one served in the window where a revision is
announced but its descriptor not yet obtained — so any other catalog is not
held to the attachment: whether it lists one depends on session state that a listing
captured under an earlier descriptor may predate. Such a catalog is still held
to every rule about the listing alone.

## Consequences

- 0007's capture rules and 0028's attachment rules state these answers in
  their own text, each marked as amended by this decision.
- Go stops conditioning the no-position pending check on the queue, judges a
  degraded attachment's refusal, requires the unadvertised reason, and moves
  its attached-provider check behind the catalog's binding check, where Zig
  already has it; Zig stops conditioning the pending check on the queue and
  judges an attachment accepted without the mode. Each port cites its open
  item.
- `drafts/conformance.md` states the attachment refusal with its reason, and
  the queue unit's capture-position bullet says what an entry without a
  position claims.
