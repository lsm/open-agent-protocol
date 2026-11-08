# Rust ACP observed tool lifecycle

Follow shared prerequisites and the verification mutex in SKILL.md. Run from
this checkout's root with the ReleaseSafe validator built.

```sh
python3 .claude/skills/verify/scripts/rust-acp-results.py --suite tools --output-dir /tmp/oap-tools-<unique>
python3 .claude/skills/verify/scripts/rust-acp-text.py --output-dir /tmp/oap-tools-engine-<unique>
(cd rust && cargo test -p oap-adapter-acp)
```

The tools suite uses the same standalone public Rust and Go Adapter/Session
consumers as the results journey. Real credential-free synthetic stdio agents
run at native queue capacity one. It records both native directions, actual
admission/state/events and native PIDs. Every child must be reaped. Parsed JSON
comparison normalizes only wall clocks and the staged descriptor revision.
The standalone engine journey compares fixed-clock/id event JSON exactly.

Cases exercise sparse progress, presentation titles and native string names,
invalid name patches, direct completion/failure with synthesized start,
explicit null arguments/results, repeated call observations, malformed calls
and patches, missing identity, unknown status, patches before creation,
post-terminal observations and cross-prompt native id reuse. Pending children
settle in creation order before the run. Normal completion synthesizes start
and fails incomplete tools. Refusal, prompt errors and invalid observations
cancel pending tools before failing the run. Sparse progress omits arguments,
results and errors.

Each normal public trace contains actual Rust admission and events with a
synthetic host request prefix. Go, Rust and Zig must accept it. Independently
inspect event kinds, OAP tool identity/scope, retained raw values and final
session state. Retain the signed failing public stdio regression before the
implementation commit.

The staged revision is `-rust-attachments`. Tool feature rows are degraded observed
lifecycle claims with no catalog. Host execution APIs remain
unavailable. Normal permissions use the owned Session integration. Session close/drop retains its existing prefix/cleanup behavior.
Native cancellation is covered by rust-acp-cancel.md. Live config is covered by `rust-acp-live-config.md`. Reload is covered by `rust-acp-reload.md`. Opening config is covered by `rust-acp-opening-config.md`; full byte corpus and C8 served
parity retain their approved later slices. Usage semantics retain the ambiguity
recorded in the results journey. Shared process-helper grandchild-held-pipe
limits remain applicable.

Run all existing ACP journeys, the full baseline and Rust all-features under
the same mutex. Read the actual PTY reply and require `/quit` exit 0. Start no
live provider or simulator and stop everything started by the proof.

The standalone typed permission projection is covered by
`rust-acp-permission-projection.md`. Its native controller is covered by `rust-acp-permission-controller.md` and
normal Session integration by `rust-acp-session-permissions.md`.
