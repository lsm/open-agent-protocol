# Normal Rust ACP Session permissions

Follow shared setup and the machine mutex in SKILL.md. Run from the repository
root with the ReleaseSafe validator built.

```sh
python3 .claude/skills/verify/scripts/rust-acp-permission-projection.py --consumer session_permissions --output-dir /tmp/oap-session-permissions-native-<unique>
python3 .claude/skills/verify/scripts/rust-acp-session-permissions.py --output-dir /tmp/oap-session-permissions-ownership-<unique>
(cd rust && cargo test -p oap-adapter-acp --test permissions)
(cd rust && cargo test -p oap-adapter-acp --test permission_controller an_accepted_reply_releases_its_native_request_when_local_commit_fails)
```

The first command uses normal Adapter.open, Session.submit, Session.resolve,
state and close. Fourteen cases compare Go/Rust public events while normalizing
wall-clock timestamps and the declared staged revision. Parsed native host
frames compare exactly. Retain both raw directions and public results. Actual
Rust traces use the actual Rust descriptor and admission, with a synthetic host
request carrying the required degradation opt-ins. All three validators must
accept. The native child must be absent in every case.

Immediate resolution also reads state at publication, and terminal state must
carry the last event's cursor. Focused public tests cover wrong responders,
duplicate answers, late answers across subsequent admissions, default active
close refusal and idle native cancellation/malformed replies. Retain signed
public failing reproduction 9a3a873cbe before the integration fix.

The ownership command uses normal Session publication before pending close,
owner drop, dropping a blocked resolution and closing with a blocked resolution.
Large native string ids make response writes block. The agent consumes 64 bytes
before going quiet for resolver cases. Poll must return promptly. The consumer
stays alive after cleanup while native PID absence and the original OS thread
inventory are required. Abort close/drop must finish the retained prefix with
no appended events. Resolver cancellation retains its actual failure settlement.
All native peers have empty environments and no provider calls or listeners.

The separate accepted-write race pauses a real native response after 64 bytes,
closes the projection, then lets the peer receive the complete selected reply.
Local commit must fail closed, the external weak request handle must expire,
and the retained stream must have only its original three events. The native
request guard already refuses a second wire response. Clearing the controller
binding before commit also releases the accepted source after failed commit.
Retain this signed failing ownership regression before the fix.

Run all preceding ACP journeys, full baseline and Rust all-features under the
same mutex, and inspect the real PTY reply and /quit exit. The descriptor now
advertises native action.permissions and interactive gates. Accepted run cancel,
reopen/config, byte corpus and C8 served parity retain their subsequent slices.
The shared process helper's grandchild-held-pipe limitation applies.
