# Decision 0019: One Command, Two Executables

Status: proposed
Date: 2026-09-18
Protocol: `open-agent-protocol` version `0.1`
Profiles: `open-agent-protocol.agent-control-core` and
`open-agent-protocol.model-provider-core`
Amends: nothing. It records a distribution shape and a command surface, and
decides what happens to `adapter/` if the two repositories merge
Gated by: [Decision 0018](0018-makai-becomes-first-party.md)

## Context

[Decision 0018](0018-makai-becomes-first-party.md) makes Makai the OAP SDK: it
implements both profiles natively and its own provider wire is deleted rather
than bridged. That leaves two codebases on opposite sides of one boundary. This
repository is the specification, the validator, the corpus, and eight adapters
that translate third-party harnesses **into** OAP. Makai is an implementation
that speaks OAP natively on both sides.

The question this record answers is what a user installs and what they type,
and whether unifying the two trees into one language is worth doing to get
there. The motivating goal is the right one: a user should install one thing and
learn one command.

## Decisions

### One command, two executables

`oap` is the Zig binary. It is what a user installs, and it carries every mode a
user runs.

The Go tree stays a second executable — the adapter host and the implementer
tooling — and `oap` spawns it only for the one mode that needs it. Most
installations never have it on disk.

```
      you                                          other apps
       │ TUI, CLI                                   │ SDKs
       ▼                                            ▼
  ┌─ oap ─ one binary ───────────────────────────────────────────┐
  │                                                              │
  │    agent profile  ──▶  own agent loop  ──▶  provider profile │
  │                                                              │
  └──────────────────────────────────────────────────────────────┘
                    │                                │
                    │ endpoint-stdio,                │
                    │ only under --backend           │
                    ▼                                ▼
      ┌─ oap-adapters ─ Go, optional ──┐     inference providers
      │  oap endpoint                  │     anthropic, openai,
      │  eight harness adapters        │     ollama, …
      └────────────────────────────────┘
                    │
                    ▼
        Claude Code, Codex, ACP, …
```

`agent profile` is `agent-control-core` and `provider profile` is
`model-provider-core`. The two doors and the loop between them are one process.

### The adapters are reached over the protocol, not rewritten into it

The obvious unification is to rewrite the eight adapters in Zig so there is one
language and one executable. This record declines that, and the reason is that
the protocol already specifies the boundary the rewrite would be removing.

[The endpoint binding](../drafts/endpoint-stdio.md) exists, ships, and is
tested: `oap endpoint --adapter claude` exposes one agent loop as raw OAP
envelopes, one per line, with the framing, ordering and exit contract written
down. `oap` spawns it and speaks OAP to it. An implementation that rewrites
eight adapters to avoid that process boundary is declining to use its own
protocol at the one place it most obviously applies, and the first question from
an outside implementer is why.

**The cost of the rewrite is not the code.** Each adapter is the executable form
of a mapping ledger in `research/`, carrying provenance hashes, every impedance
mismatch and its classification, and a hermetic corpus pinned to an upstream
commit. `research/protocol-feedback-2026-09.md` is the adjudication record for
every mismatch found across eight harnesses. The fixtures are language-neutral
and would carry over; the reducers and the adjudications would have to be
re-established against new code, and three graduated units rest on them.

**The adapters are also off the default path.** A user who never fronts Claude
Code or Codex never runs that binary. Unifying the language of a component most
users never execute is the worst available ratio of effort to reach.

### The command surface is a verb and a role

A role is a noun, so it is an argument rather than a flag. `serve` is the verb
that marks the boundary between "the agent runs here" and "something else drives
it".

```
oap                                   the TUI — the binary's own loop
oap run "fix the failing test"        one shot, same loop, no server
oap serve agent                       agent-control-core
oap serve provider                    model-provider-core
oap serve agent provider              both profiles, one process
oap serve agent --backend claude      a wrapped harness behind the same door
oap check | validate | conformance    the corpus and the validator
oap specimens                         one of every envelope it emits
```

`--backend` carries a value, which is what a flag is for. **Its absence means
the binary's own loop**, which is the property that makes the name work without
explanation. `--stdio` stays a boolean because it selects a mode whose
alternative already carries a value (`--addr host:port`).

Rejected: `oap serve --agent` as a boolean, because it is an option pretending
to be a noun. `oap agent` at top level, because it collides in the reader's head
with `oap run` — both sound like "do agent things" and only one opens a socket.
`--harness` and `--adapter` as the backend flag, because the first is jargon and
the second names our implementation rather than the user's choice.

Short role names are the CLI's, not the protocol's. Help text spells
`agent-control-core` and `model-provider-core` so a reader can connect the two
without guessing.

### Two names have to move

`oap` is today the Go binary, so it is renamed — `oap-adapters` is the working
name. And `oap serve` today means the hub in `serve/`: twelve ops, an adapter
dimension, cursor replay, multiplexed subscriptions. That is a different layer
from `oap serve agent`, which exposes one loop. If both survive they need
different verbs, or one of them quietly becomes wrong in the documentation.

### If the repositories merge, the firewall is mechanical

A merge is reasonable and this record does not refuse it. What it refuses is a
merge whose only protection is intent.

Today a disagreement between the draft and the implementation costs two pull
requests in two repositories, and that friction is what makes someone notice
they are bending the protocol to fit the implementation. In one repository it is
one commit. Decision 0018 predicted this: *the discipline that produced four
corrections to the provider draft in one day stops being structural and becomes
a habit someone has to keep.*

So a merged repository carries a CI rule: **a change touching `schema/`,
`drafts/` or `decisions/` may not touch the implementation tree in the same
pull request, and the reverse.** That restores the two-commit friction as
something that fails rather than something someone remembers.

### The corpus belongs to the protocol side

`fixtures/provider/` and the validator stay owned by the specification, not by
the implementation, whether or not the trees merge.

That is not symbolism. The corpus is the only artifact in this system that has
caught defects in both trees, and it works precisely because it is written on
one side of a boundary and run on the other.

## Evidence

**The endpoint binding is not hypothetical.** `oap endpoint` and `oap
conformance` ship in this repository, and `conformance` already spawns an
endpoint binary, drives a scripted session, assembles the exchange and runs it
through the real validator. The mechanism this record depends on is the one the
repository already uses to test itself.

**The corpus found defects on both sides within a day of existing.** Running the
first version against a real implementation surfaced a coherence rule in that
implementation's decoder that admitted a grant response which granted and
refused at once. Review of the corpus itself surfaced two positive fixtures
encoding a sequence gap as valid, which would have made a *correct*
implementation fail a normative corpus. Neither tree found its own defect.

**Substitutability is demonstrable rather than asserted.** The same
`agent-control-core` door serves the binary's own loop and a wrapped
third-party harness, and a caller cannot tell which is behind it. That is the
property the profile exists to provide, and it is the strongest thing to show an
outside implementer because it can be run rather than described.

## Consequences

A user installs one thing and types one command. The language count is invisible
to them, as it is to anyone installing a tool whose parts are written in more
than one language.

`adapter/makai/` loses its purpose in both directions, as Decision 0018 already
records: not needed once Makai speaks OAP natively, and not third-party evidence
once Makai is first-party. Freezing it preserves the evidence for the
graduation that already happened. This record forces that disposition rather
than deferring it.

The eight harness adapters keep their standing. They wrap genuinely external
projects, and nothing here changes what their corpora prove.

A second implementer reads a command surface where the two profiles are the two
visible things the product does, rather than two values of a configuration flag.

## What this decision does not admit

A rewrite of the adapter layer as a precondition for merging. It is months of
work, produces no new capability, and puts the evidence three graduated units
rest on through a re-derivation.

A merged repository without the firewall rule. The rule is the merge's price,
and a merge that declines it has traded the protocol's independence for
convenience without saying so.

The corpus moving to the implementation tree. A corpus an implementation owns
checks that implementation against itself.

## Open questions

**Whether both profiles can be served concurrently by one process today.** The
diagram above shows `oap serve agent provider` as one process with two doors.
That follows from the architecture and has not been run. If the modes turn out
to be exclusive, the surface is unchanged and the line is removed.

**Where the implementer tooling lives once `oap` is the Zig binary.** `check`,
`validate` and `conformance` are listed above under one command, and they are
implemented in the Go tree. Either `oap` relays them to the second executable,
or they move, or they are documented as a separate tool. This record does not
decide it.
