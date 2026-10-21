## Rust memory adapter consumer journey

The Rust adapter is a library too. Its `memory_trace` example opens a memory
session through the public `Adapter` and `Session` traits and prints the whole
exchange as an OAP trace. With `steer` it submits a run, steers it and resolves
both gates; with `compact` it asks an idle session for a focused compaction;
with `call` it opens the session providing a tool, then acknowledges and
resolves the call the script makes to it:

```sh
python3 .claude/skills/verify/scripts/rust-memory-trace.py --output-dir /tmp/oap-rust-memory-<unique-name>
```

Each trace must end in `run.completed` and pass the Rust, Go and Zig validators
(build `oapx` first). The steer trace must apply the steer right before the
input gate, and the compaction run must start and end its compaction in order.
With the steer exchange removed, all three must reject the trace with
`unmatched_steer`; with the compact exchange removed, all three must reject it
with `illegal_run_transition`. With the call's resolve exchanges removed, all three must reject it with
`illegal_tool_transition`.
