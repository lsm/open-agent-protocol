# Rust ACP native cancellation

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root with the ReleaseSafe validator built and fresh output paths.

```sh
python3 .claude/skills/verify/scripts/rust-acp-cancel.py --output-dir /tmp/oap-cancel-public-<unique>
python3 .claude/skills/verify/scripts/rust-acp-cancel-ownership.py --output-dir /tmp/oap-cancel-ownership-<unique>
```

The first command uses normal public Go/Rust Adapter and Session consumers
against the same credential-free synthetic native fixture. Compare admission,
accepted intent, duplicate/retry results, active/final state and all events,
normalizing only clock fields and the staged revision. Parsed host frames must
match exactly; only one native session/cancel may be sent. Both native wire
directions and raw consumer results are retained.

Require cancellation to remain nonterminal until the native cancelled stop or
remote -32800 confirms it. Completion and another remote error win their races.
Text after intent must preserve cancelling, while Session state stays running.
Pending and late permission asks receive native cancelled outcomes; only the
initial ask is published, and its resolution links to the request with
run_cancelling. Actual Rust events use the real descriptor/admission/accepted
response with synthetic host requests. Go, Rust and Zig must accept each trace.
This is a normalized public comparison, not byte-for-byte full corpus proof.

The script also runs native integration/control-order tests and retains
integration.log and order.json. Unknown/completed/closed controls must refuse
with typed errors, and a stale id must not cancel a later run. A cancelled retry
must also succeed after the confirmed native agent exits, while an explicitly
closed Session refuses controls. Keep signed native exit regression 6cabc6761a
before the transport/metadata precedence fix. The public
controller clock pauses after a native notification write but before intent
commit; an already received native confirmation must remain pending until
commit is released. The trace ends started, cancelling, cancelled and the
native child is reaped. Keep signed unavailable-control regressions 2039168bee
and 7235de88f4 before the implementation.

The ownership command first holds a selected permission reply after its peer
reads exactly 64 bytes from a four-megabyte reply id. A normal Session cancel
then waits behind that native write. A second pair writes actual cancel intent,
then holds its pending gate's cancellation reply at the same 64-byte boundary.
In both phases, polling must be pending and nonblocking. Dropping the control
or abort-closing the Session must reap the peer, join workers and return OS
threads to their initial count while the consumer remains alive. Explicit close
retains three events before intent and four after it. Before intent there is no
cancelling event; neither phase may invent cancelled without confirmation.
Retain actual cancel frames, partial bytes, PIDs, timings and thread inventories.

Run all preceding ACP journeys, the complete baseline and Rust all-features
under the same mutex. In particular, Session permission resource proof must
still deliver text during a blocked native reply with zero per-chunk workers.
Read the real PTY fixture reply and /quit exit. Only the tracked run is
addressable while journal/resume is unavailable. Live config is covered by
`rust-acp-live-config.md`. Reload is covered by `rust-acp-reload.md`. Opening config, full ACP corpus and C8 served parity retain their following slices. Shared process
grandchild-held-pipe limits remain applicable. Start no live provider.
