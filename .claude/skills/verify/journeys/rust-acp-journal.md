# Rust ACP bounded journal, resume and live-follow

Use shared prerequisites and the mkdir mutex in SKILL.md. From the repository
root, after the baseline builds ReleaseSafe oapx, run:

```sh
python3 .claude/skills/verify/scripts/rust-acp-journal.py --output-dir "$EVIDENCE/journal"
(cd rust && cargo test -p oap-adapter-acp --test journal)
```

The normal public Adapter/Session consumer and Go reference drive the same
synthetic native frames with empty environments and queue capacity one. Nine
cases cover default/explicit capacity, completed suffixes, active live-follow,
current and prior-run eviction, missing/future/end cursors, a blocked native
close, slow-follower overflow, a 102-event snapshot closed before consumption,
and fresh native reopen isolation. No live provider, MCP server or port is used.

Compare actual recovery state/ranges, original and replayed envelopes and both
native wire directions. Normalize only clocks and the staged revision. Raw
shutdown errors stay retained; the quiet native child ignores EOF, so close
must retain its bounded timeout, reap it and release followers. All recorded
native PIDs, including both sides of reopen, must be absent after completion.
Replay must preserve exact original envelope identity and sequence; its live
queue has 64 slots beyond the initial retained suffix. The slow case delivers
66 contiguous events and an explicit overflow, matching Go.

Seven complete original traces must pass Go, Rust and Zig validators. The close
and overflow prefixes have no confirmed terminal; all three must reject those
with missing_run_terminal. Retain those expected refusals and never append a
fabricated terminal. This is public API replay proof, not the later complete
byte corpus or served-parity C8 hand-off. The current stage is -rust-attachments;
Per-open MCP attachments are covered by rust-acp-attachments.md. Deterministic
clocks/ids with the full byte corpus remain the approved next slice.

Run all prior ACP journeys, the complete baseline and Rust all-features under
the same mutex. Read the actual baseline PTY reply and /quit exit. Keep the
signed runtime refusal before the fix. Shared process grandchild-pipe limits
and ordinary Session close policy still apply. No additional journal worker
or native I/O is introduced; the session owner performs existing cleanup.
