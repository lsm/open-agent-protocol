# Typed Rust ACP permission projection

Follow shared prerequisites and the verification mutex in SKILL.md. Run from
the repository root with the ReleaseSafe validator built.

```sh
python3 .claude/skills/verify/scripts/rust-acp-permission-projection.py --output-dir /tmp/oap-permission-projection-<unique>
(cd rust && cargo test -p oap-adapter-acp --test permission_projection)
```

The standalone public consumer combines TextRun, typed permission requests and
the existing owned PromptBridge. It publishes requests, prepares immutable
native replies, writes them outside the TextRun mutex and commits their
correlated OAP resolutions. Parent completion/failure prepares cancellation
replies, which settle permissions before tool and parent terminals.

The consumer and public Go adapter/session reference drive identical synthetic
stdio agents with empty environments and native queue capacity one. Fixed
clocks and ids make projected event JSON compare exactly. Native host frames
also compare as parsed JSON. Both raw directions, public results and native
PIDs are retained, and every child must be reaped. The agent records a finished
marker after observing all host replies. The Go reference waits for this real
wire boundary before closing, including when its public run failed earlier.

Cases cover allow/deny, multiple gates in creation order, parent completion,
refusal and RPC error, filtered and duplicate options, foreign sessions,
missing/malformed tool identity/title, invalid options and terminal-tool reuse.
Actual Rust events use a synthetic host prefix with the reference descriptor
and admission. All three validators must accept them. This is standalone
projection proof. Normal Rust Session permission support remains unavailable.

Focused public tests exercise responder/scope/choice/grant validation, repeated
resolution, opaque replies from another projection with coincident generated
ids, first pending terminal usage, failure metadata, and close without stream
revival. Keep the signed API red and the separate ownership/usage red tests
before the fix. The initial API red is a missing-method compile failure.

Native request binding, serialized async reply ownership and quiet-child
permission cleanup belong to the following approved controller layer. Normal
Session publication/resolve/admission/close follows it. Accepted run
cancellation, reopen/config, full byte corpus and C8 served parity retain their
later slices. The shared process helper's grandchild-held-pipe limits apply.
Run all existing ACP journeys, full baseline and Rust all-features under the
same mutex, and read the real PTY reply and `/quit` exit.
