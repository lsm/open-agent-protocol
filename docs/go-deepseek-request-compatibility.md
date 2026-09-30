# Go DeepSeek request compatibility

What the Go provider package does when a model resolves to DeepSeek, and which
parts of that are settled, borrowed, or still open.

Scope: `go/internal/provider`. This records the Go implementation only. The Zig
provider core is a separate implementation and is **not** covered by anything
here — see [Zig handoff](#zig-handoff) before reading this as cross-SDK truth.

## Identity

DeepSeek is recognised in two independent ways, and the recognition is
**additive** — a match on either is enough, and neither can cancel the other:

| Signal | Source | Meaning |
| --- | --- | --- |
| `model.Provider == "deepseek"` | explicit configuration | the operator named DeepSeek |
| `deepseek.com` host or subdomain | base URL | the request is addressed to DeepSeek |

`IsDeepSeekModel` is the single place both are combined. Before this change only
the host test existed, and it gated two things: the reasoning-effort capability
and the effort mapping. A DeepSeek model reached through a configured proxy lost
both, and its request then carried **no `reasoning_effort` at all** — the
capability was already false before the mapping gate was ever reached, so fixing
the mapping alone would have changed nothing observable.

That is why identity also carries the capability now: a model with an explicit
`deepseek` provider that is a reasoning model supports `reasoning_effort` even
when its host says nothing.

## Effort mapping

DeepSeek documents a requested/actual effort table. The Go package maps each
public level to the literal it puts on the wire:

| Requested | Sent | Note |
| --- | --- | --- |
| `minimal` | `low` | |
| `low` | `low` | |
| `medium` | `high` | |
| `high` | `high` | |
| `xhigh` | `high` | previously sent `max` |
| `max` | `max` | |
| `ultra` | `max` | previously fell through to `high`; there was no case for it |

An unrecognised level, and any level not in this table, maps to `high`.

An **absent** effort is different from a level: the field is omitted entirely.
Omission and an empty value are kept distinguishable, and the table above applies
only when a level was actually requested.

The table is also gated twice before anything is written: the model must be a
reasoning model, and the merged capabilities must support the effort. A
non-reasoning DeepSeek model therefore writes no `reasoning_effort` at all, which
is why a proxy that lost the capability wrote nothing rather than a wrong value.

## Host fallback and the residual ambiguity

The host test is unchanged and unconditional, so a model is treated as DeepSeek
whenever its base URL is a `deepseek.com` host or subdomain **even if its
provider label names something else**. This is deliberate and load-bearing: a
`Provider` value in this tree is frequently a routing label rather than a vendor
identity — `local`, `ollama` and `openrouter` are all in use — so giving a
provider label precedence over a host would strip DeepSeek identity from models
served through a gateway, which is the same regression the identity fix exists to
remove.

**Open, and deliberately not decided here:** whether an explicit non-DeepSeek
provider label may override a DeepSeek host. It currently may not. This record
does not propose a rule, and the tests here pin current behaviour rather than
argue for it.

A related gap, also open: a DeepSeek model reached through a gateway that is
neither a `deepseek.com` host nor labelled `deepseek` is recognised by no rule
at all. Gateway-hosted models are detected elsewhere in this package by model-ID
prefix — `IsOpenRouterAnthropic` matches an `anthropic/` prefix — but no
`deepseek/`-prefixed model id exists in the tree today, so that convention has
no DeepSeek instance. Adding one would widen the set of configurations the
package recognises, which is a contract decision, not a repair.

## `none` and explicit off: pending

DeepSeek has no effort literal meaning "off". Its documented way to disable
reasoning is `thinking.type = "disabled"`, and **omitting the effort does not
disable reasoning** — thinking is enabled at high by default.

The Go package has no off value, and the catch-all currently folds an explicit
`none` into `high`. That is almost certainly wrong for a caller who asked for no
reasoning, but the correct substitution is unresolved:

- omitting the field — does not disable, per the vendor's own documentation;
- a DeepSeek-specific off value — no documented effort literal exists for it;
- keeping `high` — preserves today's behaviour and the caller's request is not
  honoured.

**No option has been implemented and no contract has been accepted.** Nothing in
this branch pre-empts the decision, and the mapping table above is written so
that whichever option is chosen is a change to one place.

## Verification

The mapping and the identity rules are pinned by independent tables over the same
functions, and each mutation is checked to fail them:

Each mutation below was applied to the real source and the whole
`go/internal/provider` package run. Every one fails, so none of the rules can be
satisfied by the identity check alone:

| Mutation | Failing assertions |
| --- | --- |
| `xhigh` mapped with `max` | 6 |
| `ultra` case dropped | 3 |
| host fallback removed from `IsDeepSeekModel` | 13 |
| proxy capability carry dropped | 9 |
| `IsDeepSeekModel` reverted to host-only | 11 |

The three effort tables — the documented one, the pre-existing request test, and
the public-options test — are independent of each other, so the `xhigh` and
`ultra` mutations are each caught more than once.

## Zig handoff

The Zig provider core has its own DeepSeek detection and effort mapping. This
document does **not** describe it, and no parity between the two is claimed. The
Zig owner should integrate from this record by explicit section handoff rather
than by parallel edits:

| Section | Claim it would have to verify independently |
| --- | --- |
| Effort mapping | whether `xhigh` and `ultra` match the documented table |
| Identity | whether an explicit provider survives a non-vendor proxy |
| Host fallback | whether the host still wins over a provider label |
| `none` / off | **blocked on the owner decision above** — not portable until made |

Until that integration lands, this is a record of the Go implementation and
nothing wider. Do not read it as evidence that SDK behaviour is aligned.

## Related

- [Custom OpenAI- and Anthropic-compatible endpoints](custom-endpoints.md) —
  how `base_url` and provider ids are configured, which is what both the identity
  signals above are derived from.
