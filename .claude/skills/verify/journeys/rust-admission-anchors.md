# Admission anchors and their eventual responses

From the repository root, run:

```sh
python3 .claude/skills/verify/scripts/rust-admission-anchors.py --output-dir /tmp/oap-admission-anchors-<unique>
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test admission_anchors
```

The helper rebuilds and invokes the Go, Rust and Zig public validator CLIs on
33 shared and mutated traces. Session and entry anchors must name submit
requests or message-carrying opens on the snapshot's session. Their admissions
must match the session and entry run. A pending session-level claim must account
for the run through active_runs, as_of.settled or the scalar active pointer.
Refused and unanswered claims fail on the original snapshot, including claims
answered only after the snapshot. A message-carrying open can assign its session
in its response. Accepted steer requests also serve as admission anchors.

The controls include admissions before and after the snapshot, allocated and
preassigned sessions, multiple anchors, reversed steer responses and valid
repairs. An admission on another session preserves scope_mismatch and adds
session_state_mismatch on the snapshot in both response orders. The shared
fixtures cite existing captured-at rows 28–34; this does not change the wire.

With no concurrent Go, Rust or Zig edits or builds in the worktree, run:

```sh
python3 .claude/skills/verify/scripts/rust-admission-anchors.py --mutation-check --output-dir /tmp/oap-admission-mutations-<unique>
```

Fourteen source mutations must compile and change a public CLI judgment. They
weaken request kind/session checks, admitted session/run checks, accounting,
refusal and closure checks, steer lookup, message-open recognition and allocated
session binding. The Go and Zig mutations remove only the immediate session
check: the snapshot diagnostic disappears while scope_mismatch remains.
Sources are restored in finally, all validators are rebuilt and every case is
replayed on all three CLIs. Compiler errors do not count as caught mutations.
The helper retains traces, raw outputs and exit statuses in its output folder.

Failing public-consumer tests and the coordinated Go/Zig repro were committed
before their fixes. Run the shared baseline and full Go/Rust suites before
merge, including Go validation, the Rust manifest and Zig test-unit-validation.
Compare the new replay with zero unexpected disagreements. The existing
rust-deferred-captures and rust-snapshot-listing journeys preserve Decision
0057's advertisement controls, settled rows and duplicate/disjoint checks.

Pending-admission lead classification, arrangement and per-run claim attachment,
row 35, remaining active-state/future-terminal listings and model captures stay
outside this slice. An arrangement fixture gaining its expected code through
an invalid anchor does not prove its arrangement rule is ported. Live providers
and persistent endpoints are not exercised.
