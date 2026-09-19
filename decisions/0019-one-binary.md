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

### Byte equality is not the whole gate

The corpus adjudicates **outputs**, and a port of this size is not mainly a
logic problem. The documented top defect class in the implementation doing the
porting is an `errdefer` left armed after ownership transfers — six instances on
the `model-provider-core` branch alone. A reducer with a double free or a leak
produces byte-identical `expected-oap.json` right up until it does not, and
every output gate in this record is blind to it.

So each stage gate carries a second clause: **allocation-failure testing over
every allocating function, and the corpus run under a leak-checking allocator**,
not byte equality alone. Cheap now, very expensive to retrofit across eight
adapters.

### Every stage is gated on data that already exists

The port does not need the Go tests. It needs the Go *fixtures*, which are
language-neutral:

- **546 manifest fixtures**, each with the exact `(valid, phase, codes)` the
  validator must produce.
- **108 adapter corpus cases** — `native.jsonl` through the production reducer,
  compared against `expected-oap.json`.

A Zig reducer either reproduces those bytes or it does not. That answers the
question a rewrite usually cannot answer — how do you know the new one is
faithful — for everything observable in a frame, and nothing else. What it does
not answer is the clause above.

**The seam differential execution needs already exists.** `oap validate
[--provider] --format=json <trace>` emits `(phase, code, pointer, expected,
actual)` per diagnostic as data, on arbitrary input rather than only on the
corpus. Comparing two implementations costs a diff of two JSON documents, so the
groundwork stage other ports need is free here.

### The schema is embedded and interpreted, not compiled into code

The Go validator compiles `schema/v0.1/*.json` with a JSON Schema 2020-12
library. Zig has no mature one, and this is the port's only genuine unknown.

**The schema bytes are embedded in the binary and interpreted at runtime** by a
bounded 2020-12 subset interpreter covering what these twelve files use.

An earlier version of this record chose code generation, and the argument
against it is that it buys nothing. A generator has to understand 2020-12
exactly as much as an interpreter does — it moves where that understanding runs
and adds two artifacts, the emitted validator and the gate that proves it still
matches its source. The interpreter is the one thing; codegen is that same thing
plus a generator plus generated code in review.

The deciding property is stronger than the arithmetic. **An embedded schema is
normative by construction**: the bytes in the binary *are* the schema. Under
codegen the binary's behaviour is a derivative and "the schema is still the
truth" becomes a claim some job checks — which is the same inversion this record
refuses one rung up.

The cost is a parse at process start and dynamic dispatch instead of
straight-line code: microseconds against milliseconds of I/O, amortised per
process when a test harness validates repeatedly. Codegen is reserved for a
profile that shows it matters.

Hand-writing the checks remains refused. It makes the Zig source the truth and
the schema a description of it.

### Order: validator first, adapters after

1. Types and strict decode. Makai has most of this already, and one piece of it
   wrong in a load-bearing place: its agent-control decoder reads the error code
   through a required enum, so a code outside its own eleven is a *decode
   failure* rather than an unknown value — at two call sites, one of which is
   `error.response`, the frame this record just chose as the profile-independent
   answer. Built on unchanged, the Zig validator rejects this repository's own
   agent-control fixtures before any semantic check runs. The field is an owned
   string; an enum survives only as a mapping for what an implementation itself
   emits.
2. Schema validation by codegen. Gate: all 546 fixtures match phase and codes.
3. The semantic state machine. Same gate, now including every diagnostic code.
4. Adapters, one at a time. Gate: the corpus reproduces `expected-oap.json`,
   under a leak-checking allocator.
5. The oracle is retired, or kept in CI.

**Differential fuzzing is not a stage.** It starts the moment stage 2 compiles
and runs continuously from there. A stage can slip; a gate that runs on every
change cannot. The argument for it is the one this record already makes against
the corpus — fixtures prove what someone wrote a fixture for — and that argument
applies to stages 2 and 3 as much as to the end of the port.

**The order is not arbitrary.** `adapter/adaptertest` runs the real validator
in-process on every trace an adapter emits, and that loop is the reason adapter
bugs surface at the unit-test boundary rather than in a trace weeks later.
Porting adapters first gives that up for the length of the port. Porting the
validator first means every adapter lands with the check already under it.

Adapters port by demand rather than by list. An unported adapter is unavailable
rather than broken, and `--backend hermes` says so.

### The oracle is not retired while any adapter is unported

Differential execution falling silent is the condition for retiring the
**validator** oracle. It says nothing about adapters.

The corpus carries a graduated unit's evidence across the port only for an
adapter that has been ported and reproduces it. For one still in Go, the
artifact its graduation rests on **is** the Go adapter. Retiring the tree on a
validator-shaped condition alone would delete the evidence base for every
unported adapter while its unit still claims graduation.

So retirement takes a second clause: every adapter whose unit claims graduation
is ported and reproduces its corpus, or that unit's graduation is explicitly
recorded as lapsed. There is no third option where the evidence quietly stops
existing.

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

**The comment policy already survives the merge.** The implementation tree
carries a checker over every tracked `.zig` and `.ts`, with self-tests and its
allowlist ratchet retired. The merged tree inherits enforcement and needs the
checker extended to `.go`, which is the cheap direction. `tools/nocomment`
retires with the Go tree.

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

**What `oap serve agent provider` routes on, and what answers a frame with no
profile.** This was filed as untested and is worse than that: it contradicts
what is built. Each of the implementation's two endpoints refuses the other's
profile **at decode**, and the refusal names which profile that endpoint serves.

Serving both therefore needs a router that reads the envelope's `profile` before
decode and hands the frame to the endpoint that owns it. The hard case is a
frame whose `profile` is absent or unknown.

**The answer this record proposes: `error.response` carrying
`invalid_request`, correlated where an id can be recovered, connection kept
open.** For its own decision to confirm.

`error.response` is in the base envelope schema rather than in either profile,
so answering with it needs no vocabulary the frame failed to declare. Two
earlier answers were wrong and are recorded here because the reasons are worth
more than the conclusion.

The first was that the profiles carry *disjoint* error sets, so no code could
answer for both. That was inherited from an implementation's own notes and
repeated without checking. What the specifications actually say is stranger:
**`agent-control-core` enumerates no error codes at all.** `ProtocolError.code`
is an open string in `common.schema.json`, and 47 distinct codes appear across
this repository's own agent-control fixtures, including harness-specific ones
like `claude_api_429`. Only the provider profile closed its set, in `#82`, three
days before this record. So there is no intersection to appeal to and no missing
vocabulary either — there is one closed set and one open string.

The second was to close the connection. That is the wrong severity and it breaks
a rule this profile argues for elsewhere. A provider connection carries accepted
inferences, each owed exactly one terminal; closing on an unattributable frame
strands every one of them, which is the failure the profile refuses when it
declines to hand back an `inference_id` with a refusal. It is also not one
severity: on `--addr` it costs a connection, and on `--stdio` there is one
connection per process, so it means exit. Closing is for framing desync, where
the next frame boundary genuinely cannot be located. A frame that framed,
parsed, and had an envelope shape is a content error on a healthy stream, and a
healthy stream can carry its own answer.

Correlation is solved and has precedent: an implementation that cannot decode a
bad frame already sniffs the `id` out of the raw line so the refusal goes back
correlated. An unattributable frame takes the same treatment.

**What `agent-control-core` does about its error codes.** The provider profile
closed its set, and the reason applies here unchanged: a code table in prose
over an open string makes the action a caller derives a convention rather than a
contract. But 47 distinct codes appear in this repository's agent-control
fixtures, with `claude_api_429` and `com.example.storage.object_not_found` among
them, which says the field is carrying two things — what a caller should **do**,
and where the error came **from**. Three shapes are available and this record
decides none of them.

*Close the set and send origin detail in `extensions`.* This is the resolution
the provider profile already made in prose — "an implementation that wants them
as distinct codes is free to say so in `extensions`" — so it keeps one
`ProtocolError` shape across both profiles. It is also the largest migration:
every harness-specific code in every adapter moves.

*Keep `code` open and add a required closed member for the action class.* The
contract becomes the action, a harness keeps its own identifier, and nobody
enumerates a 48th string. Note what it costs before choosing it: the provider
profile **refuses** an `action` member today, deliberately and with a schema
test, because for a closed set the action is derivable from the code and a wire
member would be a second source of truth that can disagree. For an open set that
argument inverts, which is the case for this shape — but it leaves the two
profiles with different `ProtocolError` shapes, and `common.schema.json` is
`additionalProperties: false`, so the member has to be admitted for both.

*Leave it open.* Status quo. The action stays a convention, and `retriable` —
which the base schema already carries and the provider profile treats as derived
rather than primary — stays the only machine-readable hint.

The port has to model one of the three, so this needs deciding before stage 1
finishes rather than after.

**How much larger the Zig tree is.** Thirty-eight thousand lines of Go is not
thirty-eight thousand lines of Zig. Explicit allocators and `errdefer` inflate
it. This changes the estimate and not the decision.
