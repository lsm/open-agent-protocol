# Go OpenAI-completions reasoning replay

How the Go provider package serialises a prior assistant reply when it is played
back into a later request, and why the reasoning-only case is shaped the way it
is.

Scope: `go/internal/provider`, the OpenAI-completions request builder. This
records what the Go code **emits**. It is a serialization record: it does not
establish that any vendor accepts these payloads, because no request was sent.
See [Acceptance](#acceptance) and [Zig handoff](#zig-handoff).

## Two separate questions

Deciding where a reply's reasoning goes conflates two independent questions, and
`#752` conflated them:

1. **Preservation** — does the reasoning survive the round trip at all?
2. **Relocation** — is the reasoning written somewhere other than
   `reasoning_content`?

A reply holding only reasoning needs *some* valid `content`, because a message
with neither content nor tool calls is rejected on the wire this targets. That is
a **shape** problem. `#752` solved it by moving the reasoning into `content`,
which also **discarded** it: the reply came back with no `reasoning_content` at
all, on every request, for every provider.

The repair separates them. Relocation is now gated on a **declared** capability
only, and the shape problem is solved without spending the reasoning.

## Shapes emitted

For a prior assistant reply, keyed on what the reply contains:

| Reply contains | `content` | `reasoning_content` |
| --- | --- | --- |
| text | array of text parts | the reasoning, if any |
| text and tool calls | array of text parts, plus `tool_calls` | the reasoning, if any |
| reasoning only | **empty string** | **the reasoning** |
| tool calls only | `null` | absent |
| nothing visible | `null` | absent |

The reasoning-only row is the one that changed. It was previously a one-part
content array holding the reasoning, with no `reasoning_content`.

Note the deliberate asymmetry: a reply with **tool calls** and no reasoning still
gets a null content, because tool calls already satisfy the wire. Only the
reasoning-only case needs the empty string.

## Declared `RequiresThinkingAsText`

Relocation now happens only when the model **declares** it takes its thinking as
text. For such a model the reasoning is written inside the content array — first,
ahead of any answer text — and no `reasoning_content` member is emitted at all,
even on a request carrying tools.

That declaration is the only thing that triggers relocation. A reply's own shape
never does. This is the behavioural difference from `#752`, which relocated on
shape for every provider.

## Why the reasoning must survive a tool-carrying request

DeepSeek's thinking-mode documentation requires that **all prior reasoning** be
passed back on a request that carries tools — including prior turns that contained
no tool call of their own. A conversation that reasons, then calls a tool, then
reasons again therefore has reasoning-only turns in its history that must still
be present when the tool-carrying request goes out.

`#752` dropped exactly those. The turn's reasoning was relocated into `content`,
so the vendor received no `reasoning_content` for it, and the required reasoning
was lost on precisely the requests the documentation is about.

This is also why the repair does not condition on whether the request carries
tools. Preserving the reasoning unconditionally satisfies the documented
requirement without needing the request builder to know anything about the tools
in the surrounding context.

## Provider scope

This behaviour is **not** DeepSeek-specific and carries no vendor branch. It is
the OpenAI-completions builder's handling of reasoning replay, applied to whichever
model is configured. A model declaring `RequiresThinkingAsText` opts out of
`reasoning_content`; everything else gets it.

DeepSeek is named above because its documentation is what makes the dropped
reasoning observable, not because the code detects it.

Note that DeepSeek is now served on the Anthropic endpoint by default, but an
explicit `base_url` override still reaches the OpenAI-completions builder, so
this path remains reachable and the repair remains necessary.

## Controls

All four shapes above are pinned by local controls, with no network access. Each
was mutation-checked against the real source:

| Mutation | Result |
| --- | --- |
| restore the `#752` shape condition | 2 assertions fail, both reasoning-only controls |
| drop the empty-string content repair | 2 assertions fail, the content assertions |
| relocate regardless of the declaration | 2 assertions fail, both thinking-as-text controls |
| suppress `reasoning_content` entirely | 7 assertions fail |

The controls cover a reasoning-only reply with no tools, the same reply on a
request carrying tools, a reply with reasoning beside text and a tool call, and a
model that declares it takes its thinking as text.

## Acceptance

These controls assert the **bytes this package writes**. They do not assert that
a vendor accepts them, and no live request was made to check.

One point of scope, recorded rather than resolved: the official chat request
schema treats assistant `content` as **nullable**, so a null content is not
inherently invalid. The narrower refusal that motivated the empty string — a
message with neither content nor tool calls being rejected — is a report about
this wire, not a property of the schema, and it is recorded here as the reason
the shape exists. Confirming its exact boundary would need a request against the
vendor, which is out of scope for this cut.

The empty string is therefore a conservative shape that satisfies both the schema
and the reported refusal, chosen so that the reasoning is never spent to obtain a
valid content.

## Zig handoff

The Zig provider core is a separate implementation. **No parity is claimed**, and
nothing above describes it.

As reported to this cut, Zig at `b50395` currently emits, for a reasoning-only
reply: null content when the request carries tools, and text with no reasoning
when it does not. That is reported state, not something verified here, and it
differs from the Go shape in both directions. Whoever reconciles the two should
establish which behaviour is correct against the vendor documentation rather than
against either implementation, and should re-derive the tool-carrying requirement
independently rather than taking the Go reasoning as settled.

## Related

Identity and effort mapping on this same builder are recorded separately, in a
document that does not exist on `main` yet. No link is given here on purpose: this
file stands alone and neither document depends on the other.
