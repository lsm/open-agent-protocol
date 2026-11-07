# Owned Rust ACP permission controller

Follow shared setup and the mutex in SKILL.md. Run from the repository root
with the ReleaseSafe validator built.

```sh
python3 .claude/skills/verify/scripts/rust-acp-permission-projection.py --consumer permission_controller --output-dir /tmp/oap-permission-controller-native-<unique>
python3 .claude/skills/verify/scripts/rust-acp-permission-controller.py --output-dir /tmp/oap-permission-controller-ownership-<unique>
(cd rust && cargo test -p oap-adapter-acp --test permission_controller)
```

The first command uses the controller through its normal standalone native
consumer. Fourteen cases compare exact fixed-clock/id projected event JSON
and parsed host frames with the public Go adapter. Actual Rust events with a
synthetic host prefix pass all three validators. It also retains the existing
pending-window usage proof. Neither comparison claims byte-for-byte corpus
coverage or normal Rust Session permission integration.

The ownership command sends a native permission request with a four-megabyte
JSON-RPC string id. The agent consumes exactly 64 response bytes before going
quiet, proving the reverse write started. Poll must return pending promptly.
Dropping selected/settlement futures, dropping a queued controller operation,
and closing the bridge must reap the native child and join workers. The live
consumer's OS thread inventory must return to its pre-open count. One local
permission resolution must precede tool and parent settlement after failed
writes, and all artifacts, PIDs and partial native frames are retained.

The focused tests reject a forged source object with a matching native id,
refuse duplicate publication and serialize concurrent answers to one native
reply and one correlated resolution. The initial signed API red precedes the
implementation. Use the controller for all transitions of a managed TextRun,
and keep native I/O outside the projection mutex. The clock is read at commit.

Run all existing ACP journeys, full baseline and Rust all-features under the
same mutex. Read the actual PTY reply and `/quit` exit. Normal Session permission
publication/resolve/admission/close remains the next approved slice, followed
by cancel, reopen/config and corpus. The shared process helper's existing
grandchild-held-pipe limitation applies.
