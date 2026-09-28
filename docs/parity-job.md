# What the parity job is for

Two trees implement OAP: `oapx`, the Zig runtime, and this repository's Go
tree. Neither is the oracle — when they disagree, a decision, a draft or a
corpus decides (Decision 0032). The parity job is the **last check before
that rule has to be used by hand**: it drives the same requests through both
trees and fails when the bytes that cross the pipe differ.

`goap check` already covers the fixtures, the schemas and the reference
adapter, and the per-harness corpora already cover each adapter's native wire
against recorded frames. So the job is not "does each tree work". It is the
narrower question those cannot answer: **given the same request, do the two
trees emit the same answer, in the same order, with the same refusals?**

## What it catches, and what covers it instead

| divergence | caught by | not by |
| --- | --- | --- |
| a payload the two trees decode into different values | the content diff, per envelope | the ordered diff, which sees order |
| an answer one tree refuses and the other admits | the content diff | — |
| **the order of a run's events** | `runOrderDifference` within `TestBackendsMatchOapx`, since #475 | anything in the corpus or the unit tests |
| a refusal's code, reason or detail that differs | the content diff | — |
| a **settlement order** — which of two open interactions ends the run, and in what order the calls close | the ordered comparison, given a fixture that opens more than one. `memory` is the only fixture that opens two interactions, and it is not a harness | the corpora, which record one interaction at a time |

Everything else a divergence can be is covered cheaper elsewhere, and the
parity job is deliberately not where it is duplicated: the fixtures are the
oracle for the wire, the corpora are the oracle for each harness, and the
per-adapter tests are the oracle for each adapter's own error handling. If a
divergence is reproducible without a second tree, it belongs in one of those
and the fix is cheaper there.

## What each fixture is for

| fixture | the divergence it is the last check on |
| --- | --- |
| `acp`, `claude`, `codex`, `deepseek`, `hermes`, `opencode`, `pi` | that tree's adapter translates this harness's frames the same way the other tree's does |
| `memory` | that a run's whole event stream — including both of its interactions and its terminal — comes out identical and in the same order |

The seven harness fixtures share one shape: a fixed `scenario.jsonl` and a
`registry.json` naming the adapter. Six drive a `child.sh` that answers
deterministically; `opencode` has none and answers the in-test fake HTTP server
the harness starts for it when its registry carries `@URL@`. None of them
subscribes, so none of them can stream a run — which is why the ordered
comparison had nothing to walk until `memory` landed. `memory` is the one that
can: its script is in-process, so it is identical in both trees by
construction, it answers both of its interactions, and its twelve envelopes
are what the ordered comparison reads.

**Two tests, and only one of them looks at order.** `TestBackendsMatchOapx`
runs the eight fixtures with a content diff **and** the ordered comparison.
`TestMemoryBackendMatchesOapx` compares the memory backend's output as a
*sorted* set, so it is order-blind by construction: it answers "do the two
trees emit the same envelopes", not "in what order". The order claim belongs to
the fixture, not to the backend test.

## What it is not for

- **It is not conformance.** A tree can agree with the other tree and both be
  wrong; that is what `goap validate` and the conformance corpus are for.
- **It is not the harness's coverage.** A harness whose adapter agrees with
  the other adapter may still be driven wrongly; the corpora record the real
  wire and the per-adapter tests drive the adapter.
- **It is not a race detector.** The exchange is one deterministic script per
  fixture. A divergence that only appears under concurrency belongs in a test
  that can schedule it, and #475's ordering work found those by reading the
  diff, not by running this job.

## Running it

```sh
zig build --build-file zig/build.zig install --prefix /tmp/oapx
OAP_OAPX_BIN=/tmp/oapx/bin/oapx go test ./go/cmd/goap/ \
  -run 'TestBackendsMatchOapx|TestMemoryBackendMatchesOapx'
```

Both tests, or you have run one of the two halves. Without `OAP_OAPX_BIN` they
skip, so an ordinary `go test ./...` stays fast and the jobs are the only thing
that pays for it. CI builds `oapx` and runs them on every change, in two jobs:
`backend-parity` for the fixtures and `memory-conformance` for the memory
backend.

## When it fails

The job reports a difference; it does not say which tree is right. The order
is: the corpus for the harness if there is one, then the decision or draft
that covers the case, and only then a judgement about the code. Fixing the
side the record disagrees with is the whole point of the rule in Decision
0032, and a divergence nobody can settle with a record is a decision that has
not been written yet.
