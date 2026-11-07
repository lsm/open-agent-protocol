# Rust ACP session bridge ownership

Follow shared setup in SKILL.md and run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-session-bridge.py --output-dir /tmp/oap-session-bridge-<unique>
python3 .claude/skills/verify/scripts/rust-acp-model-switch.py --output-dir /tmp/oap-session-bridge-models-<unique>
python3 .claude/skills/verify/scripts/rust-acp-session.py --output-dir /tmp/oap-session-bridge-open-<unique>
python3 .claude/skills/verify/scripts/rust-acp-prompt.py --output-dir /tmp/oap-session-bridge-native-<unique>
(cd rust && cargo test -p oap-adapter-acp)
```

The `session_owner` public consumer opens an OAP session through the normal
adapter contract, using a credential-free synthetic ACP process with native
queue capacity one. Its real child emits 256 idle notifications and an unknown
reverse request. The recorded native response must be method-not-supported.
The opening model snapshot stays observable throughout this staged layer.

Five lifecycle cases exercise explicit close, dropping the session, cancelling
a quiet model switch, cancelling quiet close and closing while a quiet model
switch is pending. Quiet agents ignore EOF. The verifier observes the actual
native model request before cancellation, refuses blocked polling, checks
closed-state/idempotent-close results and confirms the native PID is absent.

While the consumer stays alive, the verifier measures its OS threads before
open, while open and after cleanup. Counts must return to the initial value
before allowing consumer exit. On macOS it retains `ps -M` output for every
phase. On Linux it retains `/proc/<pid>/task` membership. This checks resource
release through the public session path in addition to the native bridge's
existing joining tests. Every failure cleans up its isolated process group.

The model-switch journey is a Go/Rust differential regression. Its idle-inbound
case must record the same native frames and successful model/reasoning update
on both implementations. The previously failing case and its log establish the
queue-overflow regression before this integration. Its returned-snapshot OAP
exchange must still pass Go, Rust and Zig validators.

Run the full baseline and Rust all-features suite too, and inspect the baseline
PTY fixture reply and `/quit` exit. No ports, credentials or live provider calls
are used. Normal text submission is covered by `rust-acp-submit.md`; permission handling
and live config updates remain later slices. Shared grandchild-held-pipe limitations remain applicable.
