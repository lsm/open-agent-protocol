# Native session readiness hook

Use the toolchain and isolated build setup in `SKILL.md`, then run from the
repository root.

```sh
zig build --build-file zig/build.zig test-unit-tui test-unit-adapter -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
python3 .claude/skills/verify/scripts/oapx-session-restart.py --output-dir /tmp/oap-session-readiness-<unique-name>
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

This slice exposes the observer for the Hub ready queue. Whole-stage resource
acceptance uses the working and idle measurements in the Hub journey after the
Hub consumes these notifications.
