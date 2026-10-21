# A late event cannot move a run's terminal capture boundary

From the repository root, run:

```sh
python3 .claude/skills/verify/scripts/terminal-capture.py --output-dir /tmp/oap-terminal-capture-check
```

The consumer builds and invokes the Go, Rust and Zig validator CLIs. A snapshot
may retain a run at a position before its terminal when the read predates that
terminal. At or past the terminal position it must draw `session_state_mismatch`,
even when an invalid later event has advanced the stream cursor. Every late
event must still draw `event_after_terminal`.

The shared negative fixture is
`fixtures/semantic-invalid/queue-state-terminal-capture-after-late-event.json`.
Mutations move its capture before, at and past the terminal, add another late
event, move the late event past the snapshot and remove it. A duplicate
terminal must not replace the first terminal's sequence. Repairing both the
event stream and capture restores a valid trace. All three validators must
agree on exact code sets and exit statuses; Go and Rust also identify the
capture pointer. Inputs and raw stdout/stderr, exit codes and comparisons are
retained in the output directory. Run the Go validation suite, Rust manifest
test and Zig `test-unit-validation` too, with no unexpected disagreements.

A settled claim must name the observed terminal's sequence, rather than a late
stream cursor. The two shared settled-claim fixtures pin correct sequence 2 and
incorrect late-event sequence 3. The consumer removes and duplicates the late
event and changes the incorrect claim to the latest cursor again. A correct
claim must not gain a capture mismatch; an incorrect claim must keep one.
Rust checks only terminals already observed at the snapshot; future/deferred
claims remain outside this slice.
