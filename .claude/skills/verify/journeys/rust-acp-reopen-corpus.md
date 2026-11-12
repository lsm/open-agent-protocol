# Rust ACP reopen corpus

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-acp --test corpus_reopen -- --nocapture)
```

The test spawns `tests/support/reopen_agent.py`, which replays the pinned
session-reopen case on its stdio. Each request the host sends must equal the
recorded one; the agent writes a `mismatch` file and exits on the first that
differs. A request after the recording ends fails it the same way, so the
test reads the marker only after closing the session, and closing fails on
the agent's non-zero exit too. A second test feeds the agent the recorded
requests plus one more and requires that exit. The one substitution is `session/load`'s `cwd`: the recording names
the recorder's temporary workspace, so the agent accepts the configured
directory in its place and compares every other byte.

After the load, the session state must equal the expected file except
`updated_at_ms`, which Go's driver does not compare either. The binding keeps
the recorded native session id, the descriptor matches the case's advertised
levels, and resuming a pre-close run refuses with `RunNotFound`.

To see the wire check fail, change the agent's cwd substitution to any other
value: the test must fail, and pass again once restored.
