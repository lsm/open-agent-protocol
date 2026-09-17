# Decision 0016: The Model Provider Profile

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: introduces `open-agent-protocol.model-provider-core`
Amends: nothing in `agent-control-core`. It defines a second profile at a
different boundary; no envelope, rule or capability of the agent-control
profile changes, and an endpoint implementing only that profile is unaffected
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

OAP has two boundaries and defines one.

```
  presentation layer  (UI, CLI, TUI)
        |  agent-control-core          <- defined
  agent loop
        |  ???                          <- this decision
  model provider  (OpenAI-compatible, Anthropic-compatible, others)
```

The README has named the model provider as an OAP layer since the beginning,
beside the control layer, the agent loop and the tool executor. A layer with no
protocol is a diagram.

The lower boundary is real however the agent loop is built, and it is built
both ways in practice. A loop can wrap a vendor SDK and expose
`agent-control-core` above it, which is what most of the eight pinned harnesses
do. Or it can speak to inference endpoints directly and expose the same
profile above, which is what Makai does. The upper boundary is identical in
both; the lower one is where they differ, and it is where every implementation
re-solves the same problem: OpenAI and Anthropic disagree about request shape,
streaming deltas, tool-call encoding, usage accounting and stop reasons, and
somebody has to normalize them.

Every agent loop that supports more than one vendor has written that
normalization, none of them share it, and OAP has been silent about the one
boundary where the duplication is total.

## Decisions

### A second profile, not a unit of the first

`open-agent-protocol.model-provider-core` is a peer of
`open-agent-protocol.agent-control-core`, not a conformance unit inside it.

The two describe different participants at different boundaries. Agent control
is a control layer talking to an agent loop about sessions, runs and tools.
The provider profile is an agent loop talking to a model about one inference
call. Nothing in it has a session, a run, a run sequence or an interaction,
and the concepts that carry agent control — admission, terminal arbitration,
per-run ordering — have no referent below the loop.

Making it a unit would put envelopes with no `session_id` inside a profile
whose every rule assumes one. Making it a profile costs a `profiles:` value
and keeps both sets of rules coherent.

This is also why the agent-control draft's statement that direct inference is
not carried on its wire stays exactly true and is not weakened. That sentence
is about `agent-control-core`. It was never a statement that OAP has no
opinion about the provider boundary, and this decision is the opinion.

### What the profile normalizes

One inference call, in both directions:

- **Request**: model, messages, tool definitions, tool choice, sampling
  controls, max output, structured output, streaming on or off.
- **Response and stream**: content deltas, tool-call deltas, reasoning or
  thinking where a provider exposes it, stop reason, usage, and the provider's
  own error shape mapped to a typed error.

The three wires already named in this repository's `provider` package are the
evidence base and the compatibility target: `openai-responses`,
`anthropic-messages`, `openai-chat-completions`. A conformant provider-profile
implementation presents one vocabulary over any of them.

### It shares vocabulary with agent control where the meaning is the same

`ContentPart`, `ToolDefinition`, `Usage`, `ProtocolError` and the tool-call
identity domain mean the same thing on both boundaries, and an agent loop
sitting between them should not translate a content part into a different
content part. Shared definitions are reused; nothing that mentions a session,
a run or an interaction crosses down.

Where the two profiles would otherwise diverge, the provider profile yields:
this boundary is younger and has no implementers to protect.

### It ships in this repository

Same schema directory, same validator, same conformance runner, a second
`profiles:` value. A separate repository would duplicate the bundle loader, the
diagnostic vocabulary, the manifest format and the harness, to keep apart two
profiles that share half their payload types.

## Evidence

**Makai** is the worked example and the reason this is writable now rather
than speculative. Its provider protocol is exactly this boundary, in
production, normalizing OpenAI-flavour and Anthropic-flavour endpoints behind
one internal vocabulary, with an agent loop above it exposing agent control.
That is the design this profile generalizes.

**This repository already models the hard part.** `provider/` carries the wire
enum, builds real requests for all three shapes, and parses each one's SSE. It
was written as a compatibility prober — it sends one fixed prompt and checks
the stream shape — but the disagreements it had to encode are the
disagreements the profile has to normalize, and `EvidenceClass` is already an
attempt at saying how well a given endpoint honours a wire.

**The duplication is visible across the pinned adapters.** Several wrap a
vendor SDK, one talks to endpoints directly, and their ledgers record
provider-shaped concerns — DeepSeek's `initialize { provider, model }`,
Hermes's `session.create { model?, provider? }`, OpenCode's `provider/model`
id shape — with no shared vocabulary between them.

**What is not yet evidence, stated plainly.** No implementation speaks this
profile, because it does not exist. Under
[Decision 0015](0015-evidence-from-implementations-we-do-not-control.md) a unit
graduates on an implementation this project does not control, and the same
standard applies here: the profile is proposed, and it becomes executable when
something outside this repository speaks it. Makai doing so is the expected
first case and is not sufficient on its own if Makai becomes first-party.

## Consequences

An agent loop gains a defined lower boundary, so wrapping a vendor SDK and
talking to endpoints directly become two implementations of one protocol
rather than two unrelated engineering problems.

`oap serve` can reach an inference endpoint without bridging an SDK or a CLI,
because there is a specified thing to reach it with. That does not oblige this
repository to ship such a loop, and the decision on whether it should is
separate and not taken here.

`provider/` acquires a purpose beyond the evidence matrix: it becomes the
compatibility layer under the profile, and its `EvidenceClass` becomes the
answer to how well a given endpoint honours a wire it claims.

The conformance story doubles. `oap conformance` drives an agent-control
endpoint; a provider-profile implementation needs its own harness, and the
question of how to pin a vendor API that has no commit hash — raised and
unanswered for adapters — has to be answered here rather than deferred.

## What this decision does not admit

That `agent-control-core` gains an inference envelope. It does not, and the
boundary section of its draft is unchanged.

That the profile's envelope set is settled. This defines the boundary, the
scope and the vocabulary-sharing rule. The envelopes, their schemas and the
validator rules are the next decision, and it should be written against at
least two wires implemented rather than from the shapes alone.

That every provider feature is in scope. Batching, embeddings, fine-tuning,
files, and vendor-specific server-side tools are not an agent loop's lower
boundary; they are vendor APIs an implementation may use directly.

That this repository will ship an agent loop. The profile is what an agent
loop speaks downward, not a commitment to write one.
