# DeepSeek identity and the thinking-level mapping

Where the two DeepSeek facts live, who may change them, and what is
still not decided. Written with the change that moves both facts into one
place.

## Alignment with the Go record

This cut is the Zig side of the handoff the Go record
(`docs/go-deepseek-request-compatibility.md`, section **Zig handoff**) prescribes,
integrated by section rather than by parallel edits. Each section it names:

- **Effort mapping** — the table in this record matches that record's `minimal/low →
  low`, `medium/high/xhigh → high`, `max/ultra → max`; the Go table is its [Effort
  mapping] section.
- **Identity** — `usesDeepSeekWire` combines the same two signals the Go
  `IsDeepSeekModel` does, additively, and the Go record's [Identity] section states
  the same rule.
- **Host fallback** — this record keeps the host test unconditional, as the Go
  record's [Host fallback and the residual ambiguity] section does.
- **`none`/off** — not decided here and not decided in the Go record; both leave it
  to the owner.

## One table, one identity predicate

`zig/src/utils/provider_caps.zig` now holds both:

- `usesDeepSeekWire(vendor_id, base_url)` — true when the vendor id is
  literally `deepseek`, or when the host is DeepSeek's. The host check is
  the one that already existed; the vendor check is additive, so
  recognising it is not a precedence change between the two.
- `deepSeekEffort(effort)` — the level table.

Both provider files used to carry their own private `deepSeekEffort`.
Those copies were identical to each other and both were wrong in the same
way, which is exactly how a table drifts. There is now one copy, in the
module that already owns provider capability facts, and the two writers
call it. Adding a third caller cannot introduce a fourth table.

`anthropic_messages_api` did not import `provider_caps`, so this cut adds
the one line it needs, in the same commit:

    zig/build.zig, inside anthropic_messages_api_mod (line 797), .imports:
    .{ .name = "provider_caps", .module = provider_caps_mod },

`provider_caps_mod` is declared at line 707, above that use, so nothing
needs reordering. This is the whole of the build change, and it is here
so the cut builds on its own rather than depending on an unpublished
commit. Shared-file edits to `zig/build.zig` are coordinated; this one is
the lane's own and was not made while other lanes were mid-change.

## The mapping

| internal level | sent as |
|---|---|
| `minimal`, `low` | `low` |
| `medium`, `high`, `xhigh` | `high` |
| `max`, `ultra` | `max` |
| anything else | `high` (unchanged fallback; not decided here) |

`xhigh` was previously sent as `max`. It is sent as `high`: the published
table puts `xhigh` with `medium` and `high`, and reserves `max` for
`max` and `ultra`. `ultra` was previously unhandled and fell into a
catch-all; it is now `max` rather than a guess.

An unrecognised string keeps the **pre-existing** fallback, `high`. This
cut does not decide that case. An earlier draft of this record claimed
the unknown arm maps to nothing and that the caller would then omit the
field; that was wrong, and implementing it would have written an empty
string into `output_config.effort` and `reasoning_effort` at the two call
sites, which is an invalid value rather than an absent one. The table is
therefore scoped to the seven known non-off levels, and the unknown arm is
left exactly as it was.

What that leaves open, and is not decided here: what `off`, `none` and an
unrecognised string should mean on a vendor whose default is to think.
Both writers send the field unconditionally, so an omitted field is not
currently reachable without changing the request shape, and changing that
shape is a separate decision. It is owner-pending.

## Identity scope, and its remaining gap

The vendor check is a literal string match on the provider id. It does not
read the catalog and adds no catalog schema, so it cannot disagree with
whatever the catalog later decides. It is deliberately literal rather than
fuzzy: a prefix or substring match would let a vendor named
`deepseek-eu` inherit DeepSeek's thinking behaviour on the strength of
its name.

The gap that remains: a DeepSeek model whose vendor id is spelled with a case
difference is not recognised by the vendor test. A model that names some *other*
vendor but is served by DeepSeek **is** recognised on both wires — on Anthropic by
`thinksWhenAsked` (`anthropic_messages_api.zig:110`), which calls `usesDeepSeekWire`,
and on completions by the capability merge — because both include the host test. That is a vendor-label
question and it is separate from, and does not wait on, the catalog
question about which models a vendor is allowed to claim. The label gap
is closed by a label change; the catalog question is a schema decision
and is not made here.

## Not decided here

The explicit `none`/`off` toggle is **unresolved**. The table has no `off`
arm. `mapThinkingLevelToEffort` in the Anthropic file still maps its
`.off` arm to `"low"`, and that is untouched: deciding what `off` means
is an owner question, and neither adding an `off` mapping nor ratifying a
vendor fallback is done in this change.

The Anthropic file's own `mapThinkingLevelToEffort` is also untouched, so
it still collapses `xhigh` and `max` before the shared table sees them.
That collapsing is a separate concern, belongs to the level-mapping work,
and is left where it is.

## What these controls establish

The three controls in `provider_caps` pin the identity predicate, the
seven-level mapping, and the unchanged fallback for an unmapped level. They are
source-level controls in the module that owns the facts. They are not
evidence that any endpoint accepts these values, and nothing here has been
sent to a live endpoint.
