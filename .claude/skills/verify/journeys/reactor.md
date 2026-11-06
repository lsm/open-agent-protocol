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
kernel wait for 150 ms and requires zero returned waits until an explicit wake.
Every test closes its descriptors and joins the waiter. This is the foundation
check. Stage 3's final acceptance also requires the complete reactor to remain
idle with zero wakeups.

The tests are part of `test-unit-core` and the complete Zig `test` step.
