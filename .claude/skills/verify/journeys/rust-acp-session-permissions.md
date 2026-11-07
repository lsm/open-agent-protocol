# Normal Rust ACP Session permissions

Follow shared setup and the mutex in SKILL.md. Run from the repository root
with the ReleaseSafe validator built. Use fresh output directories.

```sh
python3 .claude/skills/verify/scripts/rust-acp-permission-projection.py --consumer session_permissions --output-dir /tmp/oap-session-permissions-native-<unique>
python3 .claude/skills/verify/scripts/rust-acp-session-permissions.py --output-dir /tmp/oap-session-permissions-ownership-<unique>
```

The first command opens the normal public Rust Adapter and Session, submits
synthetic text and resolves native permission asks through Session.resolve.
Fourteen cases compare public Go/Rust event JSON after normalizing wall-clock
fields and the declared staged capability revision. Parsed native host frames
must match exactly. Both raw directions, admissions and results are retained.
Actual Rust events with synthetic host exchanges pass all three validators.
Immediate state samples must reflect publication, and terminal state must have
the final cursor. This does not claim byte-for-byte corpus coverage.

The second command builds and runs the session_permission_ownership example
against real synthetic stdio peers. It covers dropping a blocked resolution,
close during resolution, pending-gate close and final Session-owner drop.
Resolution cases consume exactly 64 response bytes from a four-megabyte native
request id before the peer goes quiet. Poll must return pending promptly.
Each child must be reaped and OS threads return to their pre-open count while
the consumer remains alive. Abort close and drop retain the three-event prefix
without appended events. Native frames, PIDs and thread inventories are saved.

That script also runs the public permissions integration tests and retains
public-history.log. Require duplicate and late-answer refusals across later
admissions, wrong-responder rejection, default active-close refusal, and idle
permission cancellation or malformed-request errors. metadata-state.json must
show a public state read preserving the newer model-switch timestamp after
two permission runs.

Its library resource regressions retain resource-checks.log and resources.json.
Require zero Operation workers for 100 streamed notifications, local completion
and local prompt failure. A test-only counter records the actual Operation
spawn path while the native peer is held at a file barrier; all peers are reaped
before assertions. A peer that reads exactly 64 native reply bytes then stops
reading must still deliver its text chunk to the public stream while the
resolution future remains pending, without creating another Operation worker.
The script retains this blocked_reply metric alongside the other resources.
Native control and settlement writes retain bounded writer/inbound backpressure;
pure projection adds no deferred queue. Keep signed blocked-reply reproduction
6bfbdf6edf before the fix. Resolved and parent-settled runs must retain zero permission
routes. Eighty resolved runs keep at most 64 recent refusal ids. An evicted id
from an older run returns InteractionNotFound; the latest resolved id returns
InteractionResolved. Keep signed human-review regression 359b955247 before
the fixes.

Run rust-acp-permission-controller.md for standalone controller source binding,
accepted-native-write/local-commit failure, serialized replies and cancellation
ownership. Run all preceding ACP journeys, full baseline and Rust all-features
under the same mutex. Read the actual PTY fixture reply and /quit exit.
Accepted cancellation, reopen/config and corpus retain following slices. The
shared process helper's existing grandchild-held-pipe limitation applies.
