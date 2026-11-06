# Reactor primitives

Stage 3 of the adopted event-driven loop plan adds these primitives for later
integration. Current runtime entry points do not import them.

`Poller` owns its kernel queue and wakeup descriptor. Descriptor watches borrow
the caller's descriptor. Register, replace, remove and wait on the owner thread.
Only `wake` may run concurrently, and its caller must finish before `deinit`.
A watch selects read or write interest, replacing previous interest for that
same descriptor. Remove the watch before closing a borrowed descriptor.

Tokens identify registrations in returned readiness records. The maximum
`usize` token is reserved for wakeups. A caller must keep tokens valid until
it has processed all readiness records already returned by `wait`.

Readiness is level-triggered. A caller consumes available input, handles EOF
and failures, or removes the watch before waiting again. Explicit wakeups may
coalesce. Waiting blocks in the kernel with an infinite timeout, retrying
interrupted system calls. Neither backend uses a periodic timer.

Run `zig build --build-file zig/build.zig test-unit-reactor --summary all`.
The same suite belongs to `test` and CI's `test-unit-core` group. Tests use
anonymous pipes and an explicit wake to release an idle waiter.

For sockets, `failed` reports kqueue's EOF socket error or epoll's error flag.
The caller retrieves the socket error and reads buffered data before treating
EOF as fully consumed. Registration and wait syscall failures return an error.
