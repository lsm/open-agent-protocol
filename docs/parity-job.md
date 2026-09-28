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
| **what each tree writes to the harness** | the child diff (`childLineDifference`) and, for `opencode`, the fake server's transcript | nothing — a tree can serve identical envelopes while driving its child differently, and only a second tree shows that |
| a **contested settlement** — several gates open at once, and the order the tree closes them in | the ordered comparison, *given* such a fixture, and no fixture is one | nothing today. The `memory` run has two interactions but opens them one after the other, and a terminal event sweeping several open gates — what `sweepRun` does in the claude adapter, and what #475 was written for — has no fixture |

One row in that table cannot be placed by the rule below it, and the note says so rather than bending the rule: comparing what a tree writes to its harness needs two trees, so there is nowhere cheaper for it to live. Everything else a divergence can be is covered cheaper elsewhere, and the
parity job is deliberately not where it is duplicated: the fixtures are the
oracle for the wire, the corpora are the oracle for each harness, and the
per-adapter tests are the oracle for each adapter's own error handling. If a
divergence is reproducible without a second tree, it belongs in one of those
and the fix is cheaper there.

## What each fixture is for

| fixture | the divergence it is the last check on |
| --- | --- |
| `acp`, `claude`, `codex`, `deepseek`, `hermes`, `opencode`, `pi` | that tree's adapter translates this harness's frames the same way the other tree's does |
| `memory` | that a whole run's event stream — from the permission gate through the tool call and the input gate to the terminal, both interactions in sequence — comes out identical and in the same order |

The seven harness fixtures share one shape: a fixed `scenario.jsonl` and a
`registry.json` naming the adapter. Six drive a `child.sh` that answers
deterministically; `opencode` has none and answers the in-test fake HTTP server
the harness starts for it when its registry carries `@URL@`.

**Every fixture that submits streams that run's envelopes**, so the ordered
comparison has always had runs to walk — #475 caught the `pi` fixture announcing
`run.status.updated{status:cancelling}` and then answering, where oapx answers
and then announces. What subscribes is the submit handler
(`go/serve/serveendpoint`), not the session-open `subscribe` member: `memory` is
the only fixture that asks for it, and asking is not what makes the run
visible.

What `memory` adds is the only run long enough to be worth reading as a stream,
and the only one with **two** interactions in it — `permission-2` and
`input-3`, both resolved against `run-1`, where every other fixture resolves at
most one per run. They open one after the other, not together:
`resolvePermission` ends in `requestInput`, and both adapters hold a single
pending interaction, so the second cannot open before the first closes. What the
ordered comparison reads there is therefore the sequence, not a contested
settlement. Its script is in-process, with no child process mediating the
exchange. It is not, however, identical in both trees by
construction: `go/adapter/memory.go` and `zig/src/adapter/memory/adapter.zig` are
separate implementations, which is why the comparison scrubs `id` and every
`*_ms` member before it compares anything.

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
skip, so an ordinary `go test ./go/...` stays fast and the jobs are the only
things that pay for it. CI builds `oapx` and runs them on every change in three
jobs: `backend-parity` for the fixtures, `memory-conformance` for the memory
backend, and `pi-parity-repeat`, which runs the `pi` fixture twenty times
because that scenario must not depend on goroutine scheduling.

## When it fails

The job reports a difference; it does not say which tree is right. The order
is: the corpus for the harness if there is one, then the decision or draft
that covers the case, and only then a judgement about the code. Fixing the
side the record disagrees with is the whole point of the rule in Decision
0032, and a divergence nobody can settle with a record is a decision that has
not been written yet.
