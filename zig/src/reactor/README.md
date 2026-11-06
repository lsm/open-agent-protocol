# Reactor primitives

Stage 3 of the adopted event-driven loop plan adds these primitives for later
integration. Current runtime entry points do not import them.

`Poller` owns its kernel queue and wakeup descriptor. Descriptor watches borrow
the caller's descriptor. Register, replace, remove and wait on the owner thread.
Only `wake` may run concurrently, and its caller must finish before `deinit`.
A watch selects read, write or both interests, replacing previous interest for
that same descriptor. A failed kqueue registration clears its descriptor watches. Remove the watch before closing a borrowed descriptor.

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

`Sources` owns one-shot timer and child-exit registrations on a borrowed
poller. It uses kqueue timers and process filters on macOS, and monotonic
`timerfd` and `pidfd` descriptors on Linux. Its operations run on the poller
owner thread. Each active source needs a unique token, distinct from descriptor
watch tokens. Process already-returned readiness before reusing a token.

Call `take(token)` for each returned token before interpreting descriptor flags.
A non-null result identifies a source and releases its registration. Source
consumers branch on that returned kind. Raw source flags vary by backend:
kqueue timers have no interest flags and child exits may carry EOF, while
Linux timerfd and pidfd readiness carries the read flag. Descriptor flags apply
when `take` returns null. `cancel(token)` is idempotent, and destruction
cancels all remaining watches. A zero timer delay schedules one millisecond.
Timers fire once. Rearm explicitly after consumption or cancellation.

A child that has disappeared before registration returns `exited` immediately.
A registered child produces readiness even if it exits immediately after the
watch is installed. The caller owns the child and reaps it separately. A process
can have only one active child watch in a `Sources` instance. Destroy sources
before their poller. Platforms must provide the named kernel facilities.

`Pool.create(allocator, workers, capacity)` starts a fixed number of workers
with a FIFO queue bounded by `capacity`. `post` returns `Full` immediately when
the queue is full and `Closed` after shutdown starts. A successful post transfers
execution of the borrowed job context to a worker exactly once. Its context
must remain valid until execution completes. Callbacks run outside the queue lock.

Idle workers sleep on a condition variable. `shutdown` rejects new jobs, drains
accepted jobs and joins all workers. It may race with `post`. One external owner
calls `shutdown` and `destroy`, after stopping other submitters. A callback must
return and must never join or destroy its own pool. The supplied allocator is
used by the owner to allocate and free pool storage. Worker argument storage
uses the standard thread allocator.

`Reactor.create` starts one owner thread with a bounded command queue and an
explicit per-tick command budget. Other threads call `post`. Commands and event
handlers run on the owner. Use its `poll` and `sources` only from those callbacks,
and keep callbacks short and nonblocking. Use the shared bounded pool for
blocking work and post its completions back to the owner. Job and command
contexts must outlive all callbacks using them.

Each tick processes at most the command budget, then observes kernel readiness.
Pending commands schedule another explicit wake so I/O and timers still get
service. Source readiness is classified for the entire returned batch before
handlers run. Returned events may outlive cancellation, so callers validate
active registration tokens and postpone token reuse until the batch is consumed.
I/O callers use nonblocking descriptors and consume buffered data before EOF.

`requestStop` may run in a callback. One external owner uses `stop` to reject new
commands, drain accepted commands and join the thread. Stop other submitters
before `destroy`, which stops the reactor and releases its owned resources.
Borrowed descriptors and children remain the caller's responsibility. A kernel
wait failure closes admission, drains queued commands and is returned by `stop`.
If a wake fails before joining, `destroy` returns the error with the reactor
still allocated for the caller to retry.

`Poller.waitReturns()` counts every completed kernel wait syscall, including
interruptions. The idle probe first confirms the OS reports the owner asleep,
then requires this counter to stay zero through its hold. An explicit stop wake
ends that hold with exactly one returned wait.

Duplex I/O may arrive as two kqueue records or a combined epoll record. A handler
acts on whichever readiness flags each record supplies. The same token may occur
more than once in a returned batch, so retain its registration through the batch.
