# Go DeepSeek on the Anthropic endpoint

How the Go provider package writes thinking for a DeepSeek model on the Anthropic
Messages wire, and what remains unresolved.

Scope: `go/internal/provider`, the Anthropic request writer. This records what
the Go code **emits**. It is a serialization record: it establishes nothing about
vendor acceptance, because no request was sent. The Zig provider core is a
separate implementation and **no parity is claimed** — see [Zig](#zig).

## The vendor contract

DeepSeek's Anthropic-compatible endpoint documents two things that together
decide this code path:

- `budget_tokens` is **ignored**.
- `output_config.effort` **is** supported.

<https://api-docs.deepseek.com/guides/anthropic_api/>

Its thinking-mode guide gives the effort table, as requested versus actual:

| Requested | Actual |
| --- | --- |
| `minimal`, `low` | `low` |
| `medium`, `high`, `xhigh` | `high` |
| `max`, `ultra` | `max` |

<https://api-docs.deepseek.com/guides/thinking_mode/>

## What was wrong

The writer had one vendor-shaped decision and applied it to every model: if the
model was adaptive-thinking, write `output_config.effort`; otherwise write
`budget_tokens`.

A DeepSeek model is not adaptive — that check is a substring test for an Opus 4.6
model id. So **every** DeepSeek model on this wire took the `budget_tokens`
branch, which this vendor ignores. The effort was therefore never sent as
`output_config.effort` for any level, including `low`, `high`, `xhigh` and `max`.

Worse, the `budget_tokens` branch was additionally gated on the request's
`max_tokens` exceeding 1024. A DeepSeek model with thinking enabled and a
`max_tokens` of 1024 or less received **no `thinking` member at all** — the
vendor saw no thinking, despite thinking being requested and the model being a
reasoning model.

## What this cut changes

A DeepSeek model now takes its own branch, ahead of both Claude paths:

- `thinking: {type: "enabled"}` is written whenever thinking is enabled on a
  reasoning model.
- `output_config: {effort}` is written whenever a level was requested, mapped
  through the documented table above.

This is **independent of the Claude 1024 budget rule**. The fix does not raise,
default or synthesise the caller's `max_tokens`; the DeepSeek branch simply does
not consult it. A DeepSeek request with a small `max_tokens` now carries thinking
and its effort exactly as one with a large budget does.

The Claude paths are untouched. An Opus 4.6 model still writes
`thinking: {type: "adaptive"}` plus `output_config.effort`; any other reasoning
model still writes `thinking: {type: "enabled", budget_tokens}` when
`max_tokens` exceeds 1024 and nothing when it does not.

## Why the requested level had to be carried

The writer previously held only `ThinkingEffort`, which is the **Claude**-mapped
effort produced by `AnthropicThinkingForLevel`. Mapping that value again for
DeepSeek does not work, because Claude's vocabulary loses the information:

| Requested | Claude effort | DeepSeek effort applied to that | Documented |
| --- | --- | --- | --- |
| `minimal` | `low` | `low` | `low` |
| `medium` | `medium` | `high` | `high` |
| `high` | `high` | `high` | `high` |
| `xhigh` | `max` | `max` | **`high`** |
| `max` | `low` | `low` | **`max`** |
| `ultra` | `low` | `low` | **`max`** |

Several levels have already collapsed by the time they reach the writer: `off`,
`minimal` and `low` all become `low`, and `max`, `ultra` and any unrecognised
level all become `low` as well. `max` and `ultra` are therefore indistinguishable
from each other and from an unrecognised level, and the documented table cannot
recover them.

The requested level is therefore carried on `AnthropicOptions.ThinkingLevel`,
which `AnthropicThinkingForLevel` populates. That field is additive on an
exported struct and changes no existing field's meaning.

`deepSeekAnthropicEffort` implements the documented table directly.

## Known overlap with another cut

`deepSeekAnthropicEffort` duplicates a table that a separate identity-and-mapping
cut corrects in `deepSeekEffort` in `request.go`, which on `main` still maps
`xhigh` to `max` and has no `ultra` case.

This cut does **not** depend on that cut and does not edit `request.go`. But the
two tables are the same table, so they should be collapsed to one function when
both land. Sequencing them is a maintainer decision, not one taken here.

## Identity scope, stated precisely

The branch is keyed on the **provider label** `deepseek` alone. It does not
inspect the base-URL host, and it adds no URL policy and no new precedence rule.

Consequences, stated so they are not over-read:

- A model configured with the `deepseek` provider takes this branch **whatever
  its base URL is**, including a non-vendor proxy. That is the positive direction,
  and it works.
- A model on a `deepseek.com` host whose provider label is something else does
  **not** take this branch. It takes the Claude budget path and its
  `budget_tokens` will be ignored.

Whether a provider label may override a host remains an **open** question, and
this cut does not decide it. The behaviour above is a consequence of keying on
the label, recorded rather than defended.

This scope matches the Zig writer, which gates the same branch on
`model.reasoning and model.provider == "deepseek"` and likewise inspects no host.
The reasoning condition is applied by the enclosing guard here, so the effective
gate is the same. The match is a statement of present parity of *scope*, not of
behaviour: the two writers still differ in the table they apply and in how they
express off, and nothing here claims otherwise.

## Two public ways to build `AnthropicOptions`, and how effort is chosen

`AnthropicOptions` is exported, so it has two construction paths, and they supply
different things:

| Path | `ThinkingEffort` | `ThinkingLevel` |
| --- | --- | --- |
| `AnthropicThinkingForLevel(level, budgets)` | set, Claude-mapped | set, the requested level |
| a manual `AnthropicOptions{...}` literal | the caller's value, already a wire effort | usually absent, so empty |

`AnthropicThinkingForLevel` **always** populates `ThinkingLevel` when it enables
thinking at all: it returns empty options for an empty level and for `off`, and
otherwise fills both fields. So the requested level is available on that path
without exception, and the requested-level mapping is what decides the effort
there.

Which means the two paths are decided by different things, on purpose:

| Source of effort | `ThinkingLevel` set | Effort sent |
| --- | --- | --- |
| requested level, mapped | yes | `deepSeekAnthropicEffort(level)` |
| caller's own wire value | no | `ThinkingEffort`, carried verbatim |
| nothing usable | no | no `output_config` member |

A caller who hand-builds the options has already written a wire effort — `low`,
`high` or `max` — and that value is carried unchanged. They are not required to
also set the new field in order to express an effort this vendor understands, and
`ThinkingEffort` is not retired: on a hand-built value it is the primary source,
and on a helper-built one the Claude path still reads it exactly as before.

The two paths deliberately disagree about `xhigh` and `max`, and that is not an
inconsistency. A helper caller asking for `xhigh` supplies the **level** `xhigh`,
which the documented table maps to `high`. A manual caller who wrote `max` into
`ThinkingEffort` supplied the **wire value** `max`, and it is sent as `max`. The
first is a request to be mapped; the second is a value already chosen. Since
`ThinkingLevel` is always present on the helper path, the two can never be
confused for one request, and the raw level always wins when both are present.

### Values this does not decide

Only `low`, `high` and `max` are carried from a manual value, because those are
the efforts this vendor documents. A manual `ThinkingEffort` outside that set —
and an explicit `none` or `off` — produces no `output_config` member at all.

**No fallback policy is invented for the remaining values.** What an
unrecognised manual effort should do is an open question, as is the `none`/off
contract below, and both are left unanswered here rather than resolved by
defaulting.

A control is owed that builds options manually with `ThinkingEnabled` and
`ThinkingEffort` set and `ThinkingLevel` empty, and asserts that each documented
wire effort survives verbatim, alongside the helper path at the same levels, so
the two construction paths are pinned separately rather than one standing in for
the other.

## Off and `none`, still open

On this wire "off" is expressed by **omitting** `thinking`; there is no off value
to send. An empty level and the literal `off` both leave thinking disabled, and
that is unchanged.

An explicit **`none`** is not handled. It is neither empty nor `off`, so thinking
is enabled and the level falls to the table's catch-all, sending effort `high`.

**This is not implemented here.** What `none` should map to is an open contract
question. Omission is the option that follows from what this writer already does
for off, but it is still a decision, and it is not taken. Until it is made, an
explicit `none` on this wire enables thinking at a high effort, which is very
likely not what the caller asked for. That is a recorded gap, not endorsed
behaviour.

## Controls owed

None exist yet. This cut was authored under a resource hold that deferred builds
and test processes, so **`go build`, `go test`, `go vet` and every mutation check
were not run**. The only check performed was `gofmt`, which confirms the file
parses and says nothing about behaviour. **No gate has cleared and none is
claimed.**

Controls to add, on the public writer payload, once testing is permitted:

- a non-adaptive DeepSeek reasoning model with thinking enabled, asserting
  `thinking.type` is `enabled` and `output_config.effort` is present, at `low`,
  `high` and `xhigh`;
- the low-`max_tokens` case — `max_tokens` at or below 1024 — asserting thinking
  and effort are still written for DeepSeek, and that the caller's `max_tokens`
  is **not** altered;
- `max` and `ultra` mapping to `max`, and `minimal` to `low`;
- the Claude adaptive and non-adaptive paths, asserting both are byte-identical
  to their behaviour before this cut;
- the explicit-`deepseek`-provider-on-a-proxy case, and the deepseek-host-with-
  another-label case, asserted to the scope stated above rather than to a
  preference;
- `off` and empty level omitting `thinking`, and `none` pinned to current
  behaviour as a recorded gap;
- a **manually built** `AnthropicOptions` with `ThinkingEnabled` set,
  `ThinkingLevel` empty, and `ThinkingEffort` at each of `low`, `high` and
  `max`, asserting each is carried **verbatim** rather than mapped — a wire `max`
  must stay `max`;
- a manual `ThinkingEffort` outside that documented set, asserting **no**
  `output_config` member is written, so the omission is pinned rather than
  drifting into an invented default;
- a request carrying **both** fields, asserting the raw `ThinkingLevel` wins and
  the manual value is ignored, so the priority is pinned;
- the adaptive Claude path reading `ThinkingEffort` unchanged in both of those
  constructions, so the other vendor's behaviour is shown not to have moved;
- the shared `AnthropicThinkingForLevel` path at the same levels, so the two
  construction paths are distinguished rather than one standing in for the other.

Each needs a mutation showing the table fails without the change — in particular
one that restores the Claude budget branch for a DeepSeek model.

## Zig

The Zig provider core writes this wire independently, and its DeepSeek branch
gates on the reasoning flag and the `deepseek` provider label, emitting
`thinking` and `output_config.effort` without consulting a token limit. **No
parity is claimed** and nothing above describes it.

Two differences are known and unaddressed here: the Zig effort table is applied
to an already-mapped effort rather than to the requested level, and Zig can write
`thinking.type` as `disabled` where this writer can only omit the member.
Whoever reconciles them should derive the correct behaviour from the vendor
documentation above rather than from either implementation, and should treat the
open `none` question as blocking parity on the off path.
