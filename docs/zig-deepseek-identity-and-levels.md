# DeepSeek identity and the thinking-level mapping

Where the two DeepSeek facts live, who may change them, and what is
still not decided. Written with the change that moves both facts into one
place.

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

`anthropic_messages_api` does not import `provider_caps`, so this cut's
source will not compile until `zig/build.zig` gains one line for that
module. **That line is not in this cut.** `zig/build.zig` is shared and
serialised, and a named integrator applies changes to it, so the edit is
handed over rather than made here:

- file: `zig/build.zig`
- inside `anthropic_messages_api_mod` (defined at line 797), in its
  `.imports` list, add
  `.{ .name = "provider_caps", .module = provider_caps_mod },`
- `provider_caps_mod` is declared at line 707, before that use, so no
  reordering is needed.

This is a one-line addition and nothing else. Until it lands, treat the
source change as not yet buildable, and do not assume it compiles.

## The mapping

| internal level | sent as |
|---|---|
| `minimal`, `low` | `low` |
| `medium`, `high`, `xhigh` | `high` |
| `max`, `ultra` | `max` |
| anything else | nothing |

`xhigh` was previously sent as `max`. It is sent as `high`: the published
table puts `xhigh` with `medium` and `high`, and reserves `max` for
`max` and `ultra`. `ultra` was previously unhandled and fell into a
catch-all; it is now `max` rather than a guess.

An unrecognised string maps to **nothing**, not to a default level. The
previous catch-all returned `high` for every unknown input, which meant a
typo and a deliberate request produced the same bytes. The caller omits
the field when the mapping is empty, so an unmapped level is visible as
absent rather than silently upgraded.

## Identity scope, and its remaining gap

The vendor check is a literal string match on the provider id. It does not
read the catalog and adds no catalog schema, so it cannot disagree with
whatever the catalog later decides. It is deliberately literal rather than
fuzzy: a prefix or substring match would let a vendor named
`deepseek-eu` inherit DeepSeek's thinking behaviour on the strength of
its name.

The gap that remains: a model that names some *other* vendor but is
served by DeepSeek is not recognised, and neither is a DeepSeek model
whose vendor id is spelled with a case difference. Those need a catalog
answer, which is the owner's, and the schema choice is not made here.

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
seven-level mapping, and the empty result for an unmapped level. They are
source-level controls in the module that owns the facts. They are not
evidence that any endpoint accepts these values, and nothing here has been
sent to a live endpoint.
