# Validator divergences

Behaviours a validator judges that no decision states plainly, or where the Go,
Zig and Rust validators disagree. Each item stays OPEN until a decision or the
owner settles it. Rule tables in `drafts/validator-rules/` link here instead of
drafting a rule.

## OPEN-0008-1

**What differs.** After the open, Go holds every source a snapshot reports to
one description: the open response's adopted description for an attached
source, and the descriptor's for a declared one
(`go/validation/toolsources.go:checkPublishedUnion`). Zig compares only attached
sources and skips a declared source's members
(`zig/src/validation/semantic.zig:checkPublishedUnion`, `stated orelse continue`).
So a snapshot that redescribes a declared source fails in Go and passes in Zig.

**Decision text.** 0008 § Attachment is for the session's lifetime: the
snapshot's `sources` are "the union of the open's attachments and the
descriptor's declared sources, compared by `id` and by each descriptor's
published members."

**Options.** (a) Zig judges declared sources too, as Go does. (b) Narrow the
rule to attached sources.

**Recommendation.** (a). The decision text names both halves of the union, so
this is a Zig port gap rather than an open protocol question. It needs a
negative fixture, since none isolates a redescribed declared source.

**Status.** Settled by 0008's existing text. What remains is the Zig port fix
and its fixture.

## OPEN-0008-2

**What differs.** Three scope checks exist only in Go:

- A session-scoped `action.tools.list.response` must name its session on the
  envelope (`toolsListResponse`). Zig's `checkScope` fires only when both
  sides are present and differ.
- `action.call.resolve.request` and `.response` must carry the same
  `tool_call_id` on the envelope and in the payload (`controlResolveRequest`,
  `controlResolveResponse`).
- A resolution must name the tool call its interaction is bound to
  (`controlResolveRequest`).

Zig has none of the three.

**Decision text.** 0008 § A list request is a request of its own requires the
response to repeat the session "on its envelope and in its payload". 0011 gives
the resolve pair its members but states no scope check for them.

**Options.** (a) Port all three to Zig and Rust, citing 0008 for the first and
0001's scope agreement for the other two. (b) Port the first and record the
other two as drafted rules.

**Recommendation.** (a). The first has decision text. The other two follow
from the pair being session- and run-scoped, and are drafted in
`drafts/validator-rules/0008-tool-sources.md` as such.

## OPEN-0008-3

**What differs.** A `permission.resolved` event that names a `tool_call_id`
(on the envelope or in the payload) other than the one its request was bound
to is `scope_mismatch` in Go (`state.go:interactionResolved`) and Rust
(`state/interactions.rs:interaction_resolved`). Zig judges neither.

**Decision text.** 0001 § Correlated reverse interactions correlates a
resolution with its request, but no decision says the tool binding must carry
over. Decision 0055's scope rule covers responses and the tool-call resolve
pair, not permission resolution events.

**Options.** (a) Extend 0055's binding rule to every resolution that carries
`tool_call_id`, and port it to Zig. (b) Leave the binding unjudged, and drop
the Go and Rust checks.

**Recommendation.** (a). A resolution that moves its tool binding resolves a
call it was never asked about, the defect 0055 already rules out for the
resolve pair.

## OPEN-0011-1

**What differs.** Go refuses a `tools` array naming a tool outside a disclosed
`name_pattern` as a limit violation (`controltools.go:provideLimitViolation`).
Zig reads `max_tools` and `schema_dialect` but not `name_pattern`
(`semantic.zig:provideLimitViolation`).

**Decision text.** 0011 § The capability discloses its limits lists
`name_pattern` among the limits a refusal may rely on. 0019 § A rule the port
cannot reach is recorded, not approximated, already records this as a
deliberate Zig omission. Zig has no regex engine, a hand-written subset would
diverge from RE2, and no fixture discloses `name_pattern`.

**Options.** (a) Vendor an RE2-compatible engine in Zig, which 0019 calls its
own decision. (b) Pin the pattern dialect to a subset both trees can decide,
and amend 0011.

**Recommendation.** Keep 0019's record and leave the item open until an engine
is chosen. The rule row cites 0011, and the Zig column says it does not judge
the pattern.
