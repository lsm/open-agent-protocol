# ACP clock and identity sources

On the designated test host, run the public synthetic session regression:

```sh
cd rust
cargo test -p oxap-adapter-acp --test clock_identity -- --nocapture
```

Require exit 0 and three passing tests. The first compares the complete recorded
start envelope, including distinct payload and envelope times. The second
replays the pinned native frames through a real child and compares every event's
clock, ID, payload and position directly with the unchanged corpus. It also
checks native RPC IDs, generated submission/message IDs, final session time and
child cleanup. The adapter's capability revision is checked separately;
this is not the full byte corpus graduation gate.

The third opens two sessions per adapter with default and injected sources to
check that their generated IDs share one sequence. Children receive an empty
environment and each test owns its temporary directory.
