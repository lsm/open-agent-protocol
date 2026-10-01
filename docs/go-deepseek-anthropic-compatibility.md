# Go DeepSeek on the Anthropic endpoint

How the Go provider package writes thinking for a DeepSeek model on the Anthropic
Messages wire, and what remains unresolved.

Scope: `go/internal/provider`, the Anthropic request writer. This records what the
Go code **emits**. It is a serialization record: it establishes nothing about
vendor acceptance, because no request was sent. The Zig provider core is a
separate implementation and **no parity is claimed** — see [Zig](#zig).

## The vendor contract

DeepSeek's Anthropic-compatible endpoint documents that `budget_tokens` is
**ignored** and that `output_config.effort` **is** supported
(<https://api-docs.deepseek.com/guides/anthropic_api/>). Its thinking-mode guide
gives the effort table, requested versus actual
(<https://api-docs.deepseek.com/guides/thinking_mode/>):

| Requested | Actual |
| --- | --- |
| `minimal`, `low` | `low` |
| `medium`, `high`, `xhigh` | `high` |
| `max`, `ultra` | `max` |

## What was wrong

The writer had one vendor-shaped decision for every model: if the model was
adaptive-thinking, write `output_config.effort`; otherwise write `budget_tokens`.
Adaptive is a substring test for an Opus 4.6 model id, so **every** DeepSeek model
took the `budget_tokens` branch, which this vendor ignores. The effort was
therefore never sent for any level — not `low`, `high`, `xhigh`, nor `max`.

That branch was additionally gated on the computed `max_tokens` exceeding 1024, so
a DeepSeek model with thinking enabled and a `max_tokens` of 1024 or less
received **no `thinking` member at all**: the vendor saw no thinking despite
thinking being requested on a reasoning model.

## What this cut changes

A DeepSeek model takes its own branch, ahead of both Claude paths:
`thinking: {type: "enabled"}` whenever thinking is enabled on a reasoning model,
and `output_config: {effort}` whenever an effort is available.

The branch does **not** consult the token limit, so this is not a change that
raises, defaults or synthesises the caller's `max_tokens`; a small `max_tokens`
now carries thinking and effort the same way a large one does. The Claude paths
are untouched: an Opus 4.6 model still writes `thinking: {type: "adaptive"}` plus
`output_config.effort`, and any other reasoning model still writes
`thinking: {type: "enabled", budget_tokens}` when `max_tokens` exceeds 1024 and
nothing when it does not.

## Two public ways to build `AnthropicOptions`

`AnthropicOptions` is exported, so it has two construction paths, and they supply
different things:

| Path | `ThinkingEffort` | `ThinkingLevel` |
| --- | --- | --- |
| `AnthropicThinkingForLevel(level, budgets)` | set, Claude-mapped | set, the requested level |
| a manual `AnthropicOptions{...}` literal | the caller's value, already a wire effort | usually absent, so empty |

`AnthropicThinkingForLevel` **always** populates `ThinkingLevel` when it enables
thinking at all: it returns empty options for an empty level and for `off`, and
otherwise fills both fields. The requested level is therefore available on that
path without exception, which is what decides the effort there.

| Source of effort | `ThinkingLevel` | Effort sent |
| --- | --- | --- |
| requested level | set | `deepSeekAnthropicEffort(level)` — mapped |
| caller's own wire value | empty | `ThinkingEffort`, carried verbatim |
| nothing usable | empty | no `output_config` member |

A caller who hand-builds the options has already written a wire effort, and that
value is carried unchanged. They are not required to also set the new field in
order to express an effort this vendor understands. `ThinkingEffort` is **not**
retired: on a hand-built value it is the primary source, and on a helper-built one
the Claude path still reads it exactly as before.

The two paths can therefore report different efforts for what a reader may see as
the same request, and that is not an inconsistency. A helper caller asking for
`xhigh` supplies the **level** `xhigh`, which the documented table maps to `high`.
A manual caller who wrote `max` into `ThinkingEffort` supplied the **wire value**
`max`, and it is sent as `max`. The first asks to be mapped; the second has already
chosen. Since `ThinkingLevel` is always present on the helper path the two can
never be confused for one request, and the raw level wins when both are present.

One honest limit on that: a manual value is carried **verbatim**, but for the
three documented efforts the mapping is the identity — `low`, `high` and `max` are
each their own image — so carrying and mapping cannot be told apart by any
request. The distinction is real in the code and unobservable on the wire, so no
control claims to prove it. What *is* observable, and is controlled, is the
level-versus-wire-value difference above.

### Why the level had to be carried

The writer previously held only `ThinkingEffort`, the Claude-mapped value, where
`off`, `minimal` and `low` have already collapsed to `low` and `max` and `ultra`
have collapsed to `low` as well. `max` and `ultra` are therefore indistinguishable
from each other and from an unrecognised level, and the documented table cannot
recover them. `AnthropicOptions.ThinkingLevel` is additive and changes no existing
field's meaning.

### Values this does not decide

Only `low`, `high` and `max` are carried from a manual value, because those are
the efforts this vendor documents. A manual `ThinkingEffort` outside that set — and
an explicit `none` or `off` — produces no `output_config` member at all. **No
fallback policy is invented for the remaining values.** What an unrecognised
manual effort should do is an open question, as is the `none`/off contract below;
both are left unanswered rather than resolved by defaulting.

## Identity scope, stated precisely

This **Go** writer's branch is keyed on the **provider label** `deepseek` alone:
`anthropic_request.go` gates on `model.Provider == "deepseek"` and inspects no
host. The Zig writer widened its equivalent branch to
`reasoning and usesDeepSeekWire(provider, base_url)`, so the two writers **diverge
in scope**: Zig takes the branch for a model on a `deepseek.com` host under another
label, Go does not. The divergence is recorded rather than aligned; whether the Go
gate should widen is a separate question.

Consequences, stated so they are not over-read:

- a model configured with the `deepseek` provider takes this branch **whatever its
  base URL is**, including a non-vendor proxy;
- a model on a `deepseek.com` host whose provider label is something else does
  **not** take it in Go. It takes the Claude budget path, and its `budget_tokens`
  will be ignored. (The Zig writer, widened, does take it.)

Whether a provider label may override a host remains an **open** question and this
cut does not decide it. The above is a consequence of keying on the label, recorded
rather than defended.

The Zig writer gates the same branch on
`model.reasoning and usesDeepSeekWire(model.provider, model.base_url)`, so the two
writers differ in **scope** here (Go label-only, Zig additive) as well as in the
table they apply and in how they express off.

## Off and `none`, still open

On this wire "off" is expressed by **omitting** `thinking`; there is no off value
to send. An empty level and the literal `off` both leave thinking disabled, and
that is unchanged.

An explicit **`none`** is not handled. It is neither empty nor `off`, so thinking
is enabled and the level falls to the table's catch-all, sending effort `high`.
**This is not implemented here.** What `none` should map to is an open contract
question; omission is the option following from what this writer already does for
off, but it is still a decision and it is not taken. Until it is made, an explicit
`none` enables thinking at a high effort, which is very likely not what the caller
asked for. A recorded gap, not endorsed behaviour.

## Known overlap with another cut

`deepSeekAnthropicEffort` duplicates a table that a separate identity-and-mapping
cut corrects in `deepSeekEffort` in `request.go`, which on `main` still maps
`xhigh` to `max` and has no `ultra` case. This cut does **not** depend on that cut
and does not edit `request.go`, but the two tables are the same table and should be
collapsed to one function when both land. Sequencing them is a maintainer decision,
not one taken here.

## Verification

Measured in `go/internal/provider` on this branch: `go build ./go/...` clean, `go
vet` clean, `gofmt` clean, the full package green, and the package green under
`-race`.

Controls are on the public writer payload: the helper path at `minimal`, `low`,
`medium`, `high`, `xhigh`, `max` and `ultra`; the low-`max_tokens` case asserting
thinking and effort are still written, that the caller's `max_tokens` is unaltered,
and that the Claude path at the same limit writes nothing, so the two are compared
like for like; manual options at each documented wire value; a manual value outside
that set; both fields present; the Claude adaptive and non-adaptive paths,
including that adaptive grew no `budget_tokens`; the proxy and mismatched-label
cases; and `off`, empty and `none`.

Every mutation below was applied to the real source and the whole package run:

| Mutation | Failing assertions |
| --- | --- |
| remove the DeepSeek branch, restoring the Claude budget path | 14 |
| the completions-wire table instead of the documented one | 3 |
| gate the DeepSeek branch on the Claude 1024 rule | 1 |
| let a hand-built value outrank the raw level | 6 |
| carry undocumented manual values too | 1 |
| map a hand-built value instead of carrying it | **0 — equivalent** |

The last row is a real result, not a gap: as noted above, the mapping is the
identity on the three documented efforts, so that mutation changes no observable
output. It is reported rather than hidden, and no coverage is claimed for a
distinction the wire cannot express.

## Zig

The Zig provider core writes this wire independently. **No parity is claimed** and
nothing above describes it. Two differences are known and unaddressed here: the Zig
effort table is applied to an already-mapped effort rather than to the requested
level, and Zig can write `thinking.type` as `disabled` where this writer can only
omit the member. Whoever reconciles them should derive the correct behaviour from
the vendor documentation above rather than from either implementation, and should
treat the open `none` question as blocking parity on the off path.
