# Rust session-list response pages

From the repository root, run:

```sh
python3 .claude/skills/verify/scripts/rust-session-list.py --output-dir /tmp/oap-session-list-<unique-name>
```

The helper builds Go's `goap validate`, Rust's public `validate` example and
Zig's `oapx validate`, then directly replays the six former Rust gap fixtures,
the paged/tie-order positive fixtures and repaired/boundary controls. Inspect
`results.json` and the retained input, stdout and stderr files: all three
consumers must agree on exact diagnostic code sets, semantic phase and exit
status, with zero disagreements. Rebuilding and invoking `oapx` avoids relying
on Zig's cached fixture tests.

Oversized pages must draw `session_list_over_limit`, including 51 entries with
no requested limit; exactly 50 is accepted. Pages must be newest first, with
session IDs ascending at equal timestamps, or draw `session_list_order`.
Repeated session IDs, adjacent or separated, must draw
`duplicate_session_entry`. Empty pages, repaired pages and the same session
listed on a separate page remain valid. Two outstanding requests whose replies
arrive out of order must each retain their own limit.

Run the repeatable source mutations with no other build or edit in this worktree:

```sh
python3 .claude/skills/verify/scripts/rust-session-list.py --mutation-check --output-dir /tmp/oap-session-list-mutations-<unique-name>
```

Each weakened Rust check must flip the selected public CLI verdict; compiler
errors do not count. The helper restores the source in `finally`, rebuilds the
Rust consumer and replays every case through all three CLIs after restoration.
If Python is forcibly killed, check source restoration before continuing.

Before merging, integrate main locally, run Go validation, the Rust manifest
test and Zig `test-unit-validation`, then rerun this journey with zero
disagreements. Run the shared Verify baseline and inspect its isolated PTY
reply/quit evidence. Register rows are
`0046#a-list-is-paged-on-an-opaque-cursor-newest-first/1–2` and
`0046#the-list-is-the-hosts-bindings-and-only-those/1`.

This journey checks response-page contents. Capability/degradation refusal
gates, endpoint persistence, live session discovery and live providers remain
outside its scope; snapshot deferred claims are unchanged.
