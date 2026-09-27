# Decision 0032: Go and Zig Are Peers, and the Specification Decides

Status: accepted (its Go binary is an internal tool, and its libraries stay first-class
and permanent, under [Decision 0038](0038-one-released-binary-and-a-library-for-every-language.md))
Date: 2026-09-23
Protocol: `open-agent-protocol` version `0.1`
Profiles: neither; this record governs the two implementations and their
binaries, not the wire
Supersedes: the parts of [Decision 0019](0019-one-binary.md) that make the Go
tree the oracle and schedule its retirement — "The Go tree becomes the oracle,
not the product", stage 5 of the port ("The oracle is retired, or kept in CI"),
the retirement conditions in "The oracle is not retired while any adapter is
unported", the layout line `go/ the oracle — deleted when differential goes
silent`, "`go/` means the oracle and nothing else", and "the Go binary keeps
`oap` until it retires". The rest of 0019 is unchanged and keeps its own status.

## Context

Decision 0019 made `oapx` the product and the Go tree its oracle: the Zig
implementation is diffed against Go, and Go is deleted once the diff falls
silent. The project owner did not choose that ranking, and it no longer
describes the work:

- **No Go code serves or consumes the provider profile.** The
  `model-provider-core` endpoints over stdio and HTTP, the remote provider
  client and composed stdio (Decisions 0027 and 0030) exist only in Zig. Go
  carries the profile's validator, not a runtime, so "probe Go and match it"
  has no behaviour to probe for the newest and fastest-moving part of the
  protocol.
- **Go keeps a job after the port.** It is where Go users get the adapters
  natively, as libraries and as a binary. A tree with users is not scaffolding
  waiting for a deletion condition.
- **The ranking leaks Go runtime behaviour into the corpus.** Expectations quote
  text the Go codec produced, so the Zig ports reproduce Go's `%q` quoting,
  `encoding/json` error messages and case-folded key matching byte for byte,
  in `zig/src/adapter/goquote.zig`, `goquote_table.zig` and `gojson.zig`. Porting
  convenience has become de facto protocol behaviour that no decision specifies.
- **It sets the order of work.** Every plan written under it put Go first and
  Zig after, including for runtime capability the product binary is meant to
  carry.

## Decisions

### Go and Zig are peer implementations

Neither tree is the reference for the other.

- **`oapx` is the product.** It is the binary every SDK spawns, and new runtime
  capability lands in it first, or in both trees within the same milestone.
- **The Go tree is first-class and permanent.** Its libraries (`go/adapter`,
  `go/validation`, `go/serve`, `go/client`, `go/conformance`) and its binary are
  maintained for Go users. Nothing schedules its deletion.
- **A capability may ship in one tree first.** Where the other tree does not
  carry it yet, the gap is visible — an unported backend or verb answers
  `unavailable` — and never approximated.

### The specification decides

When the two trees disagree, neither wins by default. The arbiter, in order:

1. accepted decision records;
2. the drafts under `drafts/`;
3. `schema/v0.1` and the fixtures `fixtures/manifest.json` names;
4. the adapter corpora and the pinned upstream wire their ledgers record.

The tree that is wrong is fixed. When it cannot be fixed yet, the divergence is
recorded where divergences are already recorded — in the harness's ledger for an
adapter, as the ACP, Claude and DeepSeek ledgers do today — and the record only
shrinks.

A disagreement the arbiter does not settle is a gap in the specification. It is
closed by specifying it, not by adopting whichever implementation was written
first.

### A Go runtime quirk is not protocol behaviour

Error text quoted with `%q`, `encoding/json` messages, case-insensitive member
matching and similar behaviour of the Go runtime are not protocol rules because
the Go adapters happen to produce them. Each one is either specified by a later
decision, or normalized out of the expectations.

Normalizing rewrites the corpus, so Decision 0019's rule stands: it happens
entirely before a port or entirely after it, never during. Until then the Zig
ports keep reproducing the text, so that the corpora keep reproducing.

### Differential execution runs both ways

Running both implementations over the same input stays the strongest evidence
either has, and it keeps running. A disagreement is a finding against whichever
side the specification says is wrong, not against Zig by default. Its falling
silent retires nothing.

### The binaries are `oapx` and `goap`

- **`oapx`** is the Zig product binary, unchanged.
- **The Go binary is renamed from `oap` to `goap`.** The two install side by
  side, which keeps the reason 0019 gave for not sharing a name: differential
  execution needs both on one `PATH`. This answers the question the CHANGELOG
  left open, that the Go CLI "keeps the name `oap` for now".
- **Where both binaries carry a verb, it means the same thing in both.** The
  verb set is specified in a CLI draft that follows this record, including where
  the Go multi-session daemon, today `oap serve`, sits in it.

### `go/` is a peer tree

`go/` holds the Go implementation, not an oracle. 0019's layout otherwise stands:
the specification at the root belongs to neither tree.

## Consequences

- `CLAUDE.md` stops calling the Go tree the oracle, in the same change as this
  record.
- The rename to `goap` is its own change: `go/cmd/oap`, the CI steps and
  `clients/ts` that build it, and the docs that invoke it.
- Zig test names that say "the oracle" name the Go codec's behaviour the corpora
  quote. They are renamed when that behaviour is adjudicated under the rule
  above, not before, so that no name claims a rule that is still undecided.
- Records written before this one — ledgers, adjudications, CHANGELOG entries —
  keep their wording. They record what was true when written.

## What this decision does not admit

- Deleting either tree.
- "Match Go" as a rule for Zig, or "match Zig" as a rule for Go.
- Holding a capability out of `oapx` until Go has it.
- Reading a quirk of either runtime as protocol behaviour without a decision.
