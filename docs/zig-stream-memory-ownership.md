# Zig stream/result memory ownership

This document is for consumers of the Makai Zig package: code that starts a
provider stream (`stream(...)` / `streamSimple(...)` or a registered provider's
`stream` function), polls the returned `AssistantMessageStream` for events, and
reads the final `AssistantMessage` result. Makai does not use a garbage
collector or arena-by-default; string fields inside events and results are
`[]const u8` slices whose ownership follows the rules below. Violating them
produces use-after-free / bus errors, not compile errors.

Module names below (`ai_types`, `event_stream`) refer to the Makai modules under
`zig/src/` — map them into your build the same way Makai's own `build.zig` does.

## TL;DR

1. **Event strings are usually borrowed.** Delta text, tool-call
   ids/names/arguments, and the `partial` message inside every
   `AssistantMessageEvent` point into producer-managed memory. Never free
   them yourself (unless the stream owns its events — see
   [Borrowed vs owned event streams](#borrowed-vs-owned-event-streams)), and
   never keep them past your poll loop without copying.
2. **`wait()` returning `null` is the completion signal.** Do not wait for a
   `done` event — several providers never push one.
3. **Take the result with `s.cloneResult(allocator)`.** The copy is fully
   owned (`is_owned = true`), survives `s.deinit()`, and is the only result
   value you should call `AssistantMessage.deinit()` on.
4. **Providers must hand `complete()` a fully heap-owned result.** If you
   implement your own provider (or a test mock), every content-block string
   must be allocated with the stream's allocator, because the stream frees
   them at `deinit()`.

## Who owns what

| Value | Strings allocated by | Freed by | Safe to keep? |
| --- | --- | --- | --- |
| Event from a **borrowed-event** stream (`stream.ownership == .borrowed`, the default) | producer (borrowed slices) | producer's buffers — **not** the stream, **not** you | only after copying |
| Event from an **owned-event** stream (`stream.ownership = .{ .owned = clone_fn }`, e.g. OpenAI Completions) | the stream (deep-copied on `push()`) | **you**, via `ai_types.deinitAssistantMessageEvent`, for each polled event; the stream frees only events still queued at `deinit()` | yes |
| `AssistantMessage` from `getResult()` | producer (borrowed view of the stream's internal copy) | `EventStream.deinit()` | no — read-only, do not deinit |
| `AssistantMessage` from `cloneResult()` | you (deep copy) | you, via `AssistantMessage.deinit()` | yes |
| `ToolCall` from `cloneToolCall()` | you (deep copy) | you, via `deinitToolCall()` | yes |

The completed result is transferred to the stream: `EventStream.deinit()`
frees it. Event handling depends on the stream's `ownership` setting — check
it before writing your poll loop.

## The reported working directory on a tool result

`AgentToolResult.working_directory` and `ToolResultMessage.working_directory`
are optional: a tool reporting nothing leaves the field **empty and borrowed**
and `working_directory_observed` **defaults to false**, so the absent case
allocates and frees nothing. `workingDirectory()` returns the value when there is
one; `observedWorkingDirectory()` returns `null` unless the flag is set.

**Transfer.** `finalizeToolExecution` copies the result out and empties it
before anything fallible, so the copy is sole owner from then on; the message
build takes that whole copy and leaves it empty, so no field has two owners and
none has none. The results list owns the message once appended, which is why the
guard releasing an unreached message is disarmed there — a failed
tool-execution-end publication, including the `StreamCompleted` a consumer's
teardown causes, must free nothing the list owns.

**Destruction and copies.** Both `deinit`s free the directory, so ownership moves
once. The three places rebuilding a result field by field — the message clone, the
agent's copy, the hand-off — carry the directory *and* the flag.

**No session-resume persistence.** The store does not carry this field, so a
session read back from disk has neither directory nor flag — deliberate, since a
stored absolute path is stale by construction. Any future resume change must
decide what to persist and re-validate it.

## When a run ends before its message is appended

A turn can end `.aborted`, in error, or part-way through a failure — and a consumer can
complete the stream mid-run, so the final publication itself can be rejected. The events
already published are **borrowed** and drained after the producer has returned, so three
backings must outlive the run: one by the result, released by `AgentLoopResult.deinit`,
and the other two by the stream's retention, declared by the result type.

- **Failed turn.** The assistant message is *parked* on `StreamRetention` and released
  with the stream; `LoopState.deinit` runs on the same error return. The `final_message`
  clone a `turn_end` borrows needs none of that: both such publications are followed only
  by a `break`, so the clone always reaches the result.
- **Publication rejected.** A rejected `agent_end` hands the whole result to that
  retention instead of releasing it, so the earlier events stay readable.

`AgentEventStream.deinit` reaches both, only after a consumer has drained; nothing on the
producing thread frees either. A turn that hands its message to the context marks the
transfer, so a later failure does not retain it twice. Parking never allocates, because the
hand-off runs *after* a borrowed publication, where an exhausted allocator has no
memory-safe answer but keeping the memory. `AgentEvent.deinit` frees nothing for
`message_end` or `turn_end`, so a drain alone proves nothing: each regression reads the
borrowed field it is about.

## The safe consumer pattern

One complete, leak-free flow (mirrored by the unit test
`AssistantMessageStream safe consumer flow` in `zig/src/event_stream.zig`):

```zig
const std = @import("std");
const ai_types = @import("ai_types"); // zig/src/ai_types.zig
const makai_stream = @import("stream"); // zig/src/stream.zig

const allocator = gpa.allocator();

// Start a stream through the registry facade (or a provider directly).
const s = try makai_stream.stream(registry, model, context, options, allocator);
defer {
    s.deinit(); // frees the stream's internal result copy
    allocator.destroy(s);
}

// State you keep across the stream must be copied out of the borrowed events.
var text = std.ArrayList(u8).empty;
defer text.deinit(allocator);

var tool_calls = std.ArrayList(ai_types.ToolCall).empty;
defer {
    for (tool_calls.items) |*tc| ai_types.deinitToolCall(allocator, tc);
    tool_calls.deinit(allocator);
}

// 1) Drain events until wait() returns null. That null — not a `done` event —
//    is the completion signal.
while (s.wait()) |event| {
    var ev = event;
    // Owned-event streams (.owned, e.g. OpenAI Completions) transfer ownership
    // of each polled event to you: free it per iteration. Borrowed-event
    // streams (the default) must NOT be freed here.
    defer if (s.ownership.isOwned()) ai_types.deinitAssistantMessageEvent(allocator, &ev);
    switch (ev) {
        // Delta strings are borrowed (or owned by the event, which the defer
        // frees): copy them as you consume them either way.
        .text_delta => |d| try text.appendSlice(allocator, d.delta),
        // tool_call strings share storage with the result: deep-copy to keep.
        // The errdefer releases the copy if the append itself fails.
        .toolcall_end => |tc| {
            var owned = try ai_types.cloneToolCall(allocator, tc.tool_call);
            errdefer ai_types.deinitToolCall(allocator, &owned);
            try tool_calls.append(allocator, owned);
        },
        else => {},
    }
}

// 2) An error-completed stream has no result; check getError() first.
if (s.getError() != null) return error.StreamFailed;

// 3) One call: take a caller-owned copy of the result. It stays valid after
//    s.deinit() (the deferred deinit above is fine).
var result = (try s.cloneResult(allocator)) orelse return error.NoResult;
defer result.deinit(allocator);

// Everything below is fully owned: `result`, `text`, `tool_calls`.
for (result.content) |block| {
    switch (block) {
        .text => |t| std.debug.print("{s}", .{t.text}),
        else => {},
    }
}
```

`poll()` / `pollBatch()` return the same borrowed events as `wait()`; the same
copy-on-keep rule applies. If you prefer non-blocking polling, loop until
`poll()` returns `null` **and** `isDone()` is true.

## Borrowed vs owned event streams

The sample above assumes the default: a **borrowed-event** stream
(`ownership == .borrowed`), where the stream stores pushed events as-is and never
frees their strings. You copy what you keep; you never free the event itself.

**Every provider stream is an owned-event stream** (`ownership = .{ .owned = ... }`):
`push()` deep-copies each event into stream-owned storage, and the producing thread
frees its own copy as soon as the push returns. There the obligations flip — after
processing each polled event you must free it with
`ai_types.deinitAssistantMessageEvent(allocator, &event)`. Events still queued when
the stream dies are freed by `EventStream.deinit()`.

```zig
if (s.ownership.isOwned()) {
    while (s.wait()) |event| {
        var ev = event;
        defer ai_types.deinitAssistantMessageEvent(allocator, &ev);
        // ... process ev (strings are owned by the event; still copy to keep
        // them past the defer) ...
    }
}
```

A consumer may lag the producer — UI buffering, slow sinks — and that is exactly
why provider streams clone. With borrowed events the producer must keep the
backing storage alive until you drain, which a producer thread cannot guarantee:
`wait()` reads the ring buffer before it checks `completed`, and `deinit()` drains
after joining the thread, so an event can reach you after the producing thread's
storage is gone (#192). The one-deep-copy-per-event cost is paid deliberately to
remove that race. `StreamOptions.requires_owned_stream_events` has been removed
rather than left optional, because a caller-chosen ownership flag is what let two
lifetime models coexist here.

Anthropic is built as an owned stream. Because it clones on push it frees each
parsed delta immediately, since the queued event holds a copy. The #192 window was
exactly the borrowed configuration, and an owned stream both removed it and stopped
the provider holding every delta string until the stream ends.

The TUI fixture provider (`zig/src/tui/fixture_provider.zig`, reachable through
`OAPX_TUI_FIXTURE`) is owned as well. It pushes a terminal *event* **and** calls
`stream.complete()`, and because those are two separately allocated messages the
event clone and the stream result are each released once. A consumer still
branches on `stream.ownership.isOwned()`, because the generic `EventStream`
default is still borrowed.

## Completion is `wait()` → `null` → result, not a `done` event

The `done` variant of `AssistantMessageEvent` exists, but providers are not
required to push it. The built-in Anthropic Messages and OpenAI Completions
providers deliberately complete their streams with `complete()` only, because a
`done` event's `message` would alias the very same `AssistantMessage` handed to
`complete()` — freeing both would double-free. Treat `done` as informational:
handle it if you receive it, never gate completion on it.

`markThreadDone()` (used with `wait_for_thread_on_deinit`) is likewise not a
result signal — some producers mark the thread done *before* publishing the
final result. Gate on `wait()` → `null` (blocking) or `isDone()` plus a drained
queue (polling), then read `getError()` / `cloneResult()`.

Nor is it uniformly a *cleanup* signal. Anthropic Messages, Ollama and Azure
OpenAI Responses defer the mark to thread exit, so `waitForThread()` returning
true there means every allocation the producer thread owned has been freed.
OpenAI Completions, OpenAI Responses, Google Generative and Google Vertex still
mark on each return path, ahead of the function's own `defer`s, so their threads
are still freeing buffers after the mark. A test that drives one of those four
with a leak-checking allocator can observe an allocation that is about to be
freed and report it as a leak; that is a race in the mark, not in the provider.
Anthropic was in that group until the tool-call leak tests needed a barrier that
meant what it said.

## A stream is never freed underneath a live producer thread

`wait_for_thread_on_deinit` makes `deinit()` join the producer before tearing
the stream down, but the join is bounded (`join_timeout_ms`, default
`DEINIT_THREAD_JOIN_TIMEOUT_MS` = 120 s). A producer parked in blocking I/O —
`compat.http` sets no socket timeouts, so a stalled upstream parks a provider
thread indefinitely — can outlast it, and the cancel token does not help there
because providers only test it between reads.

When the join fails, `deinit()` **abandons** the stream instead of finishing:
it does not free queued events, the result or the error message, and it does
not poison `self`. `wasAbandoned()` then reports true and the caller must not
`destroy()` the allocation — the producer still holds the pointer and will
dereference it the moment its I/O returns. Leaking a stream is the correct
outcome; freeing it is a use-after-free that surfaces as a SIGSEGV inside
`push`/`pushBlocking` on whatever thread the provider happens to be.

Use `deinitAndDestroy()` for heap-allocated streams: it joins, and on success
tears down and frees the allocation, returning `true`; on a failed join it
marks the stream abandoned and returns `false`, leaving both the stream and
anything the producer still references (its `CancelToken` flag in particular)
alive. `ProtocolServer` routes every free site through this policy, and signals
the cancel flag before joining so a cooperative provider unwinds promptly.

`markThreadDone()` publishes `thread_done` as its **last** touch of the stream:
the futex bump and wake happen first. A waiter that observes `thread_done` is
therefore guaranteed the producer will not dereference the stream again, which
is what makes freeing after a successful join safe. `waitForThread()` polls on
a bounded interval (`THREAD_DONE_POLL_INTERVAL_MS`) so that ordering costs no
latency.

## The three traps these rules prevent

1. **Bus error at exit with a hand-written mock provider.** The result passed
   to `complete()` has its content-block strings freed unconditionally by
   `AssistantMessage.deinit()` — the `is_owned` flag only guards
   `api`/`provider`/`model`. A result whose block strings are string literals
   (read-only memory) crashes when the stream deinits. If you write a provider
   or mock, allocate every block string with `allocator.dupe`, and dupe
   `api`/`provider`/`model` as well, setting `.is_owned = true` — the pattern
   every built-in provider uses on its main result path. (Passing borrowed
   `api`/`provider`/`model` with `is_owned = false` is only safe when the
   content blocks are empty or the borrowed strings outlive the stream.)
2. **`done` event / `getResult()` aliasing.** The message inside a `done`
   event and the value returned by `getResult()` share memory with the stream's
   internal result. Keeping either past `s.deinit()` dangles; freeing either
   double-frees when the stream deinits its copy. Use `cloneResult()` (or
   `ai_types.cloneAssistantMessage`) to own the data.
3. **`toolcall_end` aliasing.** On a borrowed-event stream, a `toolcall_end`
   event's `tool_call` strings (`id`, `name`, `arguments_json`,
   `thought_signature`) share storage with the completed result's `tool_call`
   blocks. Same rule: deep-copy with
   `ai_types.cloneToolCall(allocator, tc.tool_call)` when collecting calls, and
   free the copies with `ai_types.deinitToolCall`.

## Helper API reference

| Helper | Purpose |
| --- | --- |
| `EventStream.cloneResult(allocator)` | Deep copy of the completed result (`!?AssistantMessage`, `error{OutOfMemory}`). `null` if the stream has not completed or carries an error (`getError()` non-null — the error wins over a late result). Available on `AssistantMessageStream` (result type `AssistantMessage`). |
| `ai_types.cloneToolCall(allocator, tool_call)` | Deep copy of a `ToolCall` (owned by you, free with `deinitToolCall`). |
| `ai_types.cloneAssistantMessage(allocator, msg)` | Deep copy of any `AssistantMessage` (sets `is_owned = true`). |
| `ai_types.cloneAssistantMessageEvent(allocator, event)` | Deep copy of a whole event, including its `partial` message. Use when forwarding events across a lifetime boundary. |
| `ai_types.OwnedMessage` | A result the caller owns, from the provider protocol client's terminal query. Free with `deinit`, or hand the message on with `intoMessage`. |

## The allocator an owned value must be freed with

`OwnedSlice`, `OwnedMessage` and every other owned value here take the
allocator as an argument to `deinit` rather than storing one, which is what
lets a value be built in one allocator and released in another. The
obligation that follows is the one to be careful about: **`deinit` must be
given the allocator the value was built with, not whichever allocator is
nearest at the free site.**

For `OwnedMessage` that is concrete, because the value does not carry the
allocator with it. A `ProtocolClient` is built with one allocator and its
`waitResultFor` allocates the copy with that same one, so:

```zig
var client = ProtocolClient.init(client_allocator, .{});
// ...
var result = (try client.waitResultFor(stream_id, 1000)).?;
defer result.deinit(client_allocator); // the client's allocator, not `allocator`
```

Passing some other allocator is not caught at compile time. It frees memory
the value never allocated, which is a double free at best and a corruption at
worst. `OwnedSlice` has the same rule with the same reason: an
`OwnedSlice(u8)` built from a request's allocator is freed with that
request's allocator, and the field wrapper in a message is no different.

## Provider-side notes

If you implement the provider side (custom API registration or test mocks):

- `push()` stores the event as-is by default: the event's strings must outlive
  until the consumer polls them — the producer is responsible for keeping the
  backing storage alive until the queue is drained (this is the obligation the
  Anthropic direct path currently misses, #192). For producer threads that
  exit before the consumer drains (protocol forwarding, short-lived workers),
  construct the stream with `ownership = .{ .owned = clone_fn }` so
  `push()` deep-copies into stream-owned storage — and document the consumer's
  `deinitAssistantMessageEvent` obligation that comes with it. OpenAI
  Completions is the in-tree example.
- **An owned stream does the copying, so a producer does not.** `push()` on an
  owned stream deep-copies before the event reaches the ring, which is what
  makes the stream's own free safe: it can only ever free what it cloned. A
  producer therefore pushes directly and **frees its own event once the push
  returns** — the stream holds a copy, not the value it was handed. A producer
  that frees first and pushes after is a use-after-free, and one that pushes
  and never frees leaks; the copy is what makes the order observable.
- The `AssistantMessage` given to `complete()` is transferred to the stream:
  `EventStream.deinit()` calls `AssistantMessage.deinit()` on it, which frees
  every content-block string unconditionally and frees `api`/`provider`/`model`
  only when `is_owned` is true. Empty content (`&.{}`) and empty strings are
  always safe. Duplicate `api`/`provider`/`model` before freeing the model the
  strings came from — the published result outlives the producer thread.

## Spawned child processes

`process_runner` owns the same question one level down, for the children a tool
spawns rather than for the values it produces.

- A child is spawned into its **own POSIX process group**, and teardown signals
  the group, not just the child. Without that, anything the command backgrounded
  outlives the timeout or cancellation that was supposed to end it.
- **The group id is captured at spawn**, where it is still known. Waiting on the
  child clears `child.id`, and a command that exits on its own takes its id with
  it, so a teardown that consulted `child.id` would skip the group exactly when a
  backgrounded grandchild was still running.
- Teardown sends `SIGTERM` to the group, then `SIGKILL` after a short bounded
  grace if the group still has a member. This runs on every cleanup path, not
  only on timeout: a normal early exit gets the same treatment.
- **Windows is not covered by any of this.** The child id there is a native
  handle, so the group is null and the posix signals are not compiled; Windows
  keeps its existing child-only cleanup and gains nothing from this change.

Limits worth stating rather than implying: this is a process-group signal, so it
reaches descendants that stay in the group. It does not claim a detached session
or a new process group of its own, and it is not cross-platform group support.
