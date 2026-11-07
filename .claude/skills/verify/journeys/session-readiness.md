# Native session readiness hook

Use the toolchain and isolated build setup in `SKILL.md`, then run from the
repository root.

```sh
zig build --build-file zig/build.zig test-unit-tui test-unit-adapter -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig test-unit-hub -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
python3 .claude/skills/verify/scripts/oapx-session-restart.py --output-dir /tmp/oap-session-readiness-<unique-name>
python3 .claude/skills/verify/scripts/oapx-session-restart.py --auto-park --output-dir /tmp/oap-session-ready-park-<unique-name>
```

The runtime observer receives an initial signal, a visible text delta and a
terminal event. After detaching, another event remains readable and the former
observer stays idle. The concurrent test holds one callback and requires detach
to finish after that callback returns. The adapter test runs two synthetic
turns through the session contract, observes readiness on the first, detaches,
and observes a completed second turn. Both turns' envelopes are validated.

The real stdio journey saves a synthetic prompt and reply, exits, restores
them in a fresh Hub process, and includes them in the next provider request.
Require exit 0 and `PASS` and retain the JSONL and provider-request artifacts
described in `oapx-session-restart.md`.

The auto-park invocation starts the real Hub with a one-second idle threshold,
lets a completed session park, and submits another input in the same process.
Require `recovery.recovered` in `auto-park-state.json` and a provider request
containing the old prompt, reply and new input. It then checks a fresh process
restart as above.

After changing Hub queue ownership, run `test-unit-hub` too. The public Hub test
polls one shared handle, coalesces a producer thread's repeated notifications,
closes queued head and middle sessions, and reads the surviving run's events.
The park test observes a pending cancellation before reopening the session.
The failed-open allocation sweep requires every refused open to leave its
session absent, including an observed session with live background work.

The idle regression opens 150 notifying sessions, consumes their initial
notifications, and requires a subsequent idle pump to leave no unfinished pass.
The mixed-adapter test keeps unnotified live-work output readable while closing
neighboring fallback sessions. It then removes that session's live-work
obligation, parks it, and observes a new run after reopening. Require both
sessions' subscription events to carry their admitted run IDs.

This slice exposes the observer for the Hub ready queue. Whole-stage resource
acceptance uses the working and idle measurements in the Hub journey after the
Hub consumes these notifications.
