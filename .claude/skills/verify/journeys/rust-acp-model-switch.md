# Rust ACP confirmed model switches

After the baseline builds ReleaseSafe `oapx`, run from the repository root with
Python selected as specified in shared setup:

```sh
python3 .claude/skills/verify/scripts/rust-acp-model-switch.py --output-dir /tmp/oap-acp-model-switch-<unique>
python3 .claude/skills/verify/scripts/rust-acp-session.py --output-dir /tmp/oap-acp-session-<unique>
python3 .claude/skills/verify/scripts/rust-acp-foundation.py --output-dir /tmp/oap-acp-foundation-<unique>
(cd rust && cargo test -p oap-adapter-acp)
```

The first script runs standalone public Go and Rust consumers against identical
synthetic ACP agents with empty environments. Both must emit the same parsed
native initialize/new/set-config-option frames, preserve native id translation,
and return the same state, catalog, switch response and structured error data.
The comparison removes only wall-clock `updated_at_ms`; each raw result remains
in its own artifact. Every child must be reaped.

Twelve cases cover confirmation, current-value reasoning priority, Unicode
label aliases, unknown reasoning, stale/duplicate/wrong-kind confirmation,
native refusal, missing degradation opt-in, unknown model, wrong scope and an
absent model option. Local refusals must stop before the native setting RPC.
The idle-inbound case sends 256 notifications and an unknown reverse RPC before
the model response. Both consumers must drain them, return the same native
method-not-supported response and still record the confirmed model/reasoning.
The Go consumer uses the existing public adapter and establishes the pinned
mapping, including dotted-capital-I lowercase behavior.

The script retains actual pre-switch state, returned response, post-switch state
and a wrapped OAP exchange. A host's required `session.state.updated` is built
from the returned snapshot, with session sequence 1. Go, Rust and Zig must accept
that exchange. This validates the library's response/state handoff; served
adapter parity remains the later C8 hand-off. An inconclusive or rejected trace
must fail the journey.

Focused Rust tests also observe serialization of concurrent switches, unchanged
state on refusals, closing during a quiet native switch, and reaping after shared
future cancellation with another process holder alive. Shared cancellation
terminates the native process and joins its worker. The documented shared-helper
grandchild-held-pipe limits remain applicable. No real harness, providers,
credentials or network calls are used.

Run the complete baseline, full Rust all-features suite, format/clippy/comment
checks and inspect the baseline's isolated PTY reply/quit evidence. Normal permissions are covered by
`rust-acp-session-permissions.md`. Live config updates are covered by
`rust-acp-live-config.md`. Reload is covered by `rust-acp-reload.md`. Opening configuration is covered by `rust-acp-opening-config.md`; ACP
corpus replay is covered by `rust-acp-corpus.md`.
