## Rust reopen consumer journey

After changing reopen state judgment, replay the shared idle, restored-queue,
unrecovered and running-run fixtures through the Rust public validator example,
Go CLI and rebuilt oapx:

```sh
python3 .claude/skills/verify/scripts/rust-reopen.py --output-dir /tmp/oap-rust-reopen-<unique-name>
```

Idle recovered sessions and recovered queued runs must pass. Missing recovery
and a running run must produce semantic `session_state_mismatch`, exit 1, in
all three. The baseline runner includes this journey and retains the JSON.
The focused Rust tests also cover the legacy active-run field, ordinary opens
and the carried-message exception. Mutation-check those tests in this worktree
with no concurrent Rust builds:

```sh
python3 .claude/skills/verify/scripts/rust-reopen.py --mutation-check --output-dir /tmp/oap-rust-reopen-mutations-<unique-name>
(cd rust && cargo test -p oap-validation --test reopen)
```

The optional mutation mode temporarily weakens six conditions in
`state/admissions.rs`, requires a test assertion failure for each (a compile
failure is not a kill), and restores the original source in `finally`.

The same example judges a `model-provider-core` trace with `--provider` first,
as `goap validate --provider` does; `oapx validate` routes a provider trace by
the profile it declares. Replay every provider fixture through all three:

```sh
python3 .claude/skills/verify/scripts/rust-provider-parity.py --output-dir /tmp/oap-rust-provider-<unique-name>
```

Rust and Go must report the same phase, code, envelope and pointer for every
diagnostic, and both they and Zig must draw exactly the codes the manifest
declares, exiting 0 only when it declares none. Build `oapx` first.
