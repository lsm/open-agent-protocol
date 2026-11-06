# Reactor primitives

Stage 3 adds standalone reactor primitives before runtime integration. Run the
focused suite on macOS and require the PR's Linux `Unit Tests - core` job to
pass on the same head.

```sh
zig build --build-file zig/build.zig test-unit-reactor --summary all
```

The suite observes real kernel readiness through anonymous pipes. Check read
and write readiness, registration replacement, removal, peer EOF, coalesced
wakeups and invalid arguments. The idle test leaves a thread blocked in a
kernel wait, confirms the OS reports the waiter asleep, then observes it for 150 ms and requires zero returned waits until an explicit wake.
Every test closes its descriptors and joins the waiter. This is the foundation
check. Stage 3's final acceptance also requires the complete reactor to remain
idle with zero wakeups.

The tests are part of `test-unit-core` and the complete Zig `test` step.

The same suite also exercises one-shot timers, cancellation and token reuse,
child exit through an anonymous-pipe gate, an already-reaped child, duplicate
registrations and allocation failures. The child test releases and reaps its
child, with kill-and-reap cleanup on every parent failure path. It verifies that
a child-exit notice is distinct from an I/O error.
