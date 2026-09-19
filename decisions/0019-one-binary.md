# Decision 0019: One Binary, And The Rewrite That Gets There

Status: proposed
Date: 2026-09-18
Protocol: `open-agent-protocol` version `0.1`
Profiles: `open-agent-protocol.agent-control-core` and
`open-agent-protocol.model-provider-core`
Amends: nothing. It decides what ships, what the Go tree becomes, and the order
and gates of the port that gets there
Gated by: [Decision 0018](0018-makai-becomes-first-party.md)

## Context

[Decision 0018](0018-makai-becomes-first-party.md) makes Makai the OAP SDK. It
now implements both profiles natively, and the work that record named is
finished: strict payload decoding, the tier-1 credential channel end to end,
tools and reasoning forwarded so a caller can ask for every part kind, the carry
round trip, and specimen emission.

That leaves two trees in two languages on opposite sides of one boundary. This
repository is the specification, the validator, a corpus, and eight adapters
that translate third-party harnesses **into** OAP. Makai is an implementation
that speaks OAP natively.

A user should install one thing and type one command. An earlier version of this
record proposed reaching that with two executables and declined a rewrite,
arguing from effort. That argument was weak, and this record replaces it.

## Decisions

### One binary ships, and it is `oap`

```mermaid
flowchart TB
  you["you<br/>TUI, CLI"]
  apps["other apps<br/>SDKs: ts, python, go"]
  callers["inference callers<br/>one vocabulary, no agent"]

  subgraph oap["oap — the one binary that ships"]
    loop["own agent loop<br/>TUI and CLI"]
    agent["agent profile<br/>agent-control-core"]
    provider["provider profile<br/>model-provider-core"]
    adapters["harness adapters<br/>ported one at a time"]
    validator["validator<br/>in process, always available"]
  end

  vendors["inference providers<br/>anthropic, openai, ollama"]
  third["Claude Code, Codex, ACP, …"]

  you --> loop
  apps --> agent
  callers --> provider
  agent -- "--backend" --> adapters
  adapters --> third
  provider --> vendors
```

The two profiles are independent doors and neither is downstream of the other.
`oap serve provider` serves a caller that wants one vocabulary over many
inference vendors and no agent loop at all.

**The validator ships inside it**, which is a capability rather than a
convenience. `oap validate` needs no second install, an endpoint can check its
own emitted trace at runtime, and the split between a tool that validates and a
binary that serves disappears.

```
oap                                  the TUI
oap run "fix the failing test"       one shot, same loop, no server
oap serve agent                      agent-control-core
oap serve provider                   model-provider-core
oap serve agent provider             both profiles, one process
oap serve agent --backend claude     a harness behind the same door
oap validate <trace.json>            the validator, either profile
oap check                            schemas, fixtures, reference adapter
oap conformance --command "..."      drive an endpoint, assemble, validate
oap specimens                        one of every envelope it emits

  serve flags:  --stdio | --addr host:port   --backend <name>   --config <path>
```

A role is a noun, so it is an argument rather than a flag. `--backend` carries a
value, and **its absence means the binary's own loop** — the property that makes
the name work without explanation.

Rejected: `oap serve --agent` as a boolean, an option pretending to be a noun.
`oap agent` at top level, which collides with `oap run` in the reader's head.
`oap serve endpoint` for the provider role, because `endpoint` already names the
*agent*-role binary in this repository, the destination member on a descriptor,
and the implementation serving a profile — three senses before a fourth.

### The Go tree becomes the oracle, not the product

It is not deleted when the port starts and it is not shipped. It is the
implementation the Zig one is diffed against.

A corpus proves what someone wrote a fixture for. Differential execution proves
what nobody thought of: run both implementations over the same input and compare
diagnostics. That has a natural end — when the Zig validator matches on all 546
fixtures **and** on fuzzed input until the fuzzer stops finding disagreements,
the Go implementation has no information left to give. Deleting it before that
turns a verifiable port into a hopeful one.

### Every stage is gated on data that already exists

The port does not need the Go tests. It needs the Go *fixtures*, which are
language-neutral:

- **546 manifest fixtures**, each with the exact `(valid, phase, codes)` the
  validator must produce.
- **108 adapter corpus cases** — `native.jsonl` through the production reducer,
  compared against `expected-oap.json`.

A Zig reducer either reproduces those bytes or it does not. This is why the
rewrite is mechanical rather than risky, and it is the answer to the question a
rewrite usually cannot answer: how do you know the new one is faithful.

### Schema validation is generated from the schemas

The Go validator compiles `schema/v0.1/*.json` with a JSON Schema 2020-12
library. Zig has no mature one, and this is the port's only genuine unknown.

Three ways out, and only one keeps the schema normative. Porting a
schema-validation subset is bounded work and a new thing to maintain.
Hand-writing the checks makes the Zig source the truth and the schema a
description of it, which is the failure this project has spent its whole history
avoiding. **So the checks are generated from the schemas at build time**, and CI
fails when generated code and schema disagree.

### Order: validator first, adapters after

1. Types and strict decode. Makai has most of this already.
2. Schema validation by codegen. Gate: all 546 fixtures match phase and codes.
3. The semantic state machine. Same gate, now including every diagnostic code.
4. Differential fuzzing against the Go oracle until it stops finding anything.
5. Adapters, one at a time. Gate: the corpus reproduces `expected-oap.json`.
6. The oracle is retired, or kept in CI.

**The order is not arbitrary.** `adapter/adaptertest` runs the real validator
in-process on every trace an adapter emits, and that loop is the reason adapter
bugs surface at the unit-test boundary rather than in a trace weeks later.
Porting adapters first gives that up for the length of the port. Porting the
validator first means every adapter lands with the check already under it.

Adapters port by demand rather than by list. An unported adapter is unavailable
rather than broken, and `--backend hermes` says so.

### The repositories merge with history

`git subtree` or a merge of unrelated histories, never a copy.

Both trees carry zero comments by policy, so every rationale lives in a commit
message. A flat copy destroys the only record of why the implementation is
shaped the way it is — which is the precise failure the comment policy's own
merge rule already anticipates for code arriving from another unit.

### A rewritten wrapper does not weaken the evidence

What makes the eight adapters third-party evidence under
[Decision 0015](0015-evidence-from-implementations-we-do-not-control.md) is that
the *harness* is outside this project and does not bend to it. The language of
our wrapper is not part of that test.

So a graduated unit keeps its standing across the port, on one condition already
built: the ported adapter reproduces its corpus. The corpus is what carries the
evidence across the rewrite, which is a second job it was not designed for and
does anyway.

### Who builds and who checks stay different people

Through the port, the implementation and the expectations are not written by the
same side. Makai writes Zig; this repository holds the fixtures, the oracle and
the review.

This is not process for its own sake. Three days before this record, the corpus
in this repository shipped two positive fixtures encoding a sequence gap as
valid, because they were run before their expectations were written and the
validator that ran them encoded the same misreading. One artifact checking
itself agrees with itself. The separation is what caught it, and a rewrite is
the moment it matters most.

## Evidence

**The corpus has caught defects in both trees within a day of existing.** Run
against a real implementation it surfaced a coherence rule in that
implementation's decoder that admitted a grant response granting and refusing at
once. Reviewed against the draft it surfaced two fixtures of this repository's
own that blessed a sequence gap. Neither tree found its own defect.

**The adapters are the best-shaped part of the port**, not the worst: eight
independent units of 2,670 to 3,540 lines each, each with a hermetic corpus and
a mapping ledger in `research/` that does not move. They are two thirds of the
work and the only part that can be done in any order.

**The measured shape.** About 48,000 lines of Go source, of which roughly 38,000
port: 24,545 in adapters, 9,151 in validation, 3,959 in the adapter interfaces
and reference adapter. The hub in `serve/` (5,564) is a separate question this
record does not answer. `client/`, `conformance/`, `provider/` and `tools/` are
development artifacts that do not ship.

## Consequences

A user installs one binary and types one command, and the validator is part of
the product rather than a second download.

The rewrite re-derives every decision against a second implementation. That is
the argument for doing it beyond distribution: this project's entire defect
history is things found by building rather than by reading, and a port is that
exercise at full scale.

`adapter/makai/` is retired as Decision 0018 already anticipated — not needed
once Makai speaks OAP natively, not third-party evidence once Makai is
first-party, and frozen as the historical record of what `+control-tools`
graduated on.

`clients/ts` is unaffected. It is a client of the wire and its conformance claim
becomes "passes the corpus", which is now an artifact rather than a promise.

## What this decision does not admit

Deleting the Go tree when the port begins. It is the oracle until differential
execution stops producing disagreements, and its retirement is a separate call
with a stated condition.

Hand-written schema checks in Zig. The schema stays normative and the code is
derived from it.

A merge by copying. History is the rationale record in a tree with no comments.

The same side writing both the Zig implementation and the fixtures it is judged
against.

## Open questions

**Whether `serve/` survives.** The hub is twelve ops, an adapter dimension,
cursor replay and multiplexed subscriptions — a different layer from `oap serve
agent`, which exposes one loop. Whether it ports, moves, or is dropped is not
decided here.

**Whether both profiles can be served concurrently by one process today.** The
diagram shows two doors on one binary. That follows from the architecture and
has not been run.

**What replaces `tools/nocomment` for the Zig tree.** The zero-comment policy is
enforced by a Go tool over Go files. The policy is not Go-specific; the
enforcement is.
