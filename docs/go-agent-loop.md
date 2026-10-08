# The Go agent loop

`zig/src/agent/` is a loop that runs turns, calls tools, checks permissions,
takes a cancellation and compacts. This note says which parts of it the Go
tree needs, in what order, and which one the first slice takes. It is the
companion to [`go-library.md`](go-library.md): that one lists the packages
`goap` has, this one covers the one it is about to grow.

Decision 0038 makes the Go tree native, and #370 is its loop. Until that lands,
`go/sdk`'s `Agent` runs a loop it does not own — it spawns `oapx serve
agent,provider --stdio` and projects the other tree's envelopes
(`go/sdk/oap.go:712`, `oapNext`). Every parity question about the Go loop is
therefore a question about `oapx` today, and the step that removes the process
is the one that makes the Go tree's answers its own. Nothing here claims a
parity result: it says what the Go tree needs and what the first slice is.

## What `oapx`'s loop actually is

The loop is `runLoop` in `zig/src/agent/agent_loop.zig:1137`, and it is
smaller than the surrounding files suggest. `agent.zig` (1903 lines) is the
queued, steerable, resumable `Agent` façade over it; `types.zig` is the event
and config vocabulary; `compaction.zig` is a summariser; the two provider
bridges are how the loop reaches a model. The loop itself is one outer while, one
inner while, and a `switch` on what a turn produced.

| part | where | what it is |
| --- | --- | --- |
| turn loop | `runLoop`, the `outer`/`inner` whiles | stream a reply, then decide: run its tool calls, or end the run |
| turn outcome | `turnOutcome` (`agent_loop.zig:1070`) | `failed` on `error`/`aborted`; `answered` on a reply with no tool call, on a `content_filter` reply whose calls are never run, or on a `length` reply once three cut-off turns have run; `called_tools` otherwise |
| cut-off calls | `max_cut_off_tool_turns` (`:1068`) | a `length` stop mid-tool-call is retried up to three times, then treated as answered |
| tool execution | `executeToolCalls` (`:647`) | per call: find the tool, gate it, run it, measure it, append a result message |
| permissions | `permission.PermissionEngine` (`zig/src/tools/permission.zig`) | `evaluate` first, a per-tool approval callback when the policy says prompt, persistence only where `canPersistDecision` allows it |
| cancellation | `cancel_token`, checked at the top of the outer loop | the run ends, and `agent_end.termination` is `cancelled` |
| compaction | `compaction.zig`, reached from the TUI and the overflow path | summarise, replace history, keep the transcripts on disk |
| telemetry | `estimateContextUsage`, `emitContextUsage` | three `prompt_segment_usage` events and one `context_usage` per turn, from a `len/4` token estimate |

Two facts about the loop are load-bearing and neither is obvious from the
signature. The first is that **a run ends with exactly one terminal event**,
and it is a type-level fact: `AgentEvent.isTerminal` names `agent_end` and
`run_failed`, and `Agent.runLoopThread` (`agent.zig:956`) emits exactly one of
the two from its `defer`, guarded by `terminal_sent` — `agent_end` sets the
flag on the way out (`agent.zig:1136`), and the `defer` emits `run_failed` only
if the flag is still clear (`agent.zig:968`). So every path out of the run ends
it: the paths that return before the run starts (`NoModelConfigured`,
`NothingToRun`), a failure inside it, and a failure *after* it has already
ended, which must not end it twice. The lower `runLoopThread`
(`agent_loop.zig:1412`) is not where that lives — its `catch` calls
`stream.completeWithError`, which records an error on the stream and emits no
event at all. The second fact is that **`AgentEndPayload.termination` is
evidence of nothing**: it encodes only `max_turns` or `cancelled`, and is null
on a clean finish *and* on a provider that refused the request. A provider
failure is a normal `agent_end` whose `final_message.stop_reason` is `error`.

The Go tree inherits both. The first is a rule about the Go loop's own
structure; the second is a rule about what the Go loop may claim.

## Where the Go tree stands

The Go provider runtime (#358) is part-way: `go/internal/provider` builds
request bodies for `openai-completions` and `anthropic-messages` and turns an
SSE chunk into `provider.Event`s — steps 2 and 3, both merged. Step 5 is in
progress in #528, which adds `OAPX_BASE_URL` and the per-provider base-URL
overrides to the Go tree plus `MockProvider`, a loopback `openai-completions`
endpoint that serves fixed frames and records what it was sent; the serving
layer is the next piece of it. Step 4, model discovery, waits on #352.

What the tree has none of is a loop. `provider.Message` is already the
user/assistant/tool-result triple (`go/internal/provider/types.go:98`), so the
slice reuses it rather than declaring a parallel one; what is missing is
everything a *loop* needs around it: an `AgentEvent` union, a `StopReason`
beyond `aborted`/`error` (`go/internal/provider/types.go:18`), a turn counter,
and the per-run event stream that carries a terminal.

The Go tree does have the half of the loop that is protocol rather than model:
`go/serve/serveendpoint` (`dispatch.go:294` `submit`, `:332` `pump`) admits a
run, stamps a per-run contiguous `sequence`, and streams a run's envelopes to
the host. The memory adapter (`go/adapter/memory.go`) is a full agent loop in
Go already, but a scripted one: it replays a fixed trace and its tool results
are `{"ok":true}`. The Go loop's first job is to be the memory adapter's
script-free counterpart, over the provider runtime.

## The first slice

**Text turns and client-executed tool calls, over `go/internal/provider`.**
One package, `go/internal/agent`, holding:

- the loop's own history over `provider.Message` and `provider.Context` — the
  types the provider runtime already takes, so a turn is a `provider.Context`
  and nothing translates between the loop and the wire;
- `TurnOutcome` with the same three cases and the same `max_cut_off_tool_turns`
  rule as `turnOutcome`, because a Go reply that ends a run on a different
  condition is a parity divergence the harness will report as an unexplained
  order difference;
- a `Run` whose `Events()` is a channel of a Go `Event` union whose terminal
  members are `AgentEnd` and `RunFailed`, with `IsTerminal` the same predicate
  `AgentEvent.isTerminal` is;
- a tool call executed **by the caller**, not in-process. This is the
  `action.call.*` lifecycle from Decision 0011, and it is the only tool
  execution the first slice carries: the loop emits `action.call.requested`,
  waits for `action.call.resolve.response`, and turns the answer into a tool
  result message.

It is `go/internal/agent` rather than `go/agent` while its API is spelled in
the provider runtime's types. A public package cannot take
`go/internal/provider`'s `Message` in its signatures, so a loop the first
slice builds that way is not importable from outside this module at all;
promoting it is a question for the public-set pass (#413) rather than one this
slice settles by naming a directory.

It lands as four PRs, each merged on its own from `main`, because the whole
slice at once is four concerns in one review. The order is the order of
dependency, and each is useful on its own:

| # | PR | what it carries |
| --- | --- | --- |
| 1 | the turn's outcome | `TurnOutcome` and the cut-off rule, over `provider.AssistantContent` and nothing else. No I/O, so it is the rule under test before anything streams |
| 2 | a turn, as a channel | a `Streamer` the loop depends on, `provider.EventSink.Drain` becoming readable outside its own package, and `provider.StopReason`'s six names so a reply's stop reason is typed rather than a string any caller can spell either way. This is the seam every later piece is written against |
| 3 | the loop | `Run`, one terminal per run, `max_iterations`, cancellation, and a turn that ends the run. A reply carrying a tool call fails the run here, because this loop has nowhere to send one; PR 4 is what makes that answerable |
| 4 | client-executed tool calls | the caller's round trip: ask, wait, answer, and the answer becomes a message |

The caller's round trip is three things, and each of them is a place a loop
can go quietly wrong. The loop **asks** by emitting `tool_call_requested` and
then waiting — so a caller has to be able to see the ask, and a consumer that
never drains cannot be answered. The loop **waits** for one specific call, and
`ResolveTool` refuses an id nothing is waiting on, by name, so a stale or
mistyped answer is an error rather than a result attributed to the wrong call.
The loop then **answers itself** for the calls no caller should be asked
about: a call whose arguments the output limit cut off, and a call the run was
cancelled while waiting for. Both are error results, and both say so in the
text, because a tool that silently did not run is the one failure a model
cannot recover from. The answer's `tool_call_id` is the loop's, not the
caller's — a caller's own id would not correlate with the call the model made.

Deliberately not in the first slice: permissions as a *policy engine*, steering
and follow-up queues, compaction, and token accounting. Each is a few lines in
the Zig loop and a real amount of policy in Go, and each is reachable only
after the first slice's traces match. `max_iterations` is the one that lands
with the loop rather than after it: it is one counter and one condition, and a
loop without it spins on a model that keeps calling tools.

### The contracts a consumer has to hold

**The channel closes; it does not report.** A `Turn` ends when its `Events`
channel closes, and a cancelled turn closes with no terminal event at all —
that is what a cancelled turn looks like from outside. So a consumer that
wants to know *why* the channel closed has to ask the run, not the turn: a
close the run itself caused is a cancellation, and a close nobody caused is a
provider that dropped its terminal and is a failed run. A loop that read every
close as cancelled would let a provider bug settle as a clean cancellation, and
the difference is invisible in the trace because both end the same way.

**Drain the channel, or cancel.** The channel is buffered, and a turn whose
consumer stops reading blocks on the next event until its context is
cancelled. There is no abandoned-turn case to recover from, so a consumer that
might stop reading early has to cancel the context rather than walk away.

**Calls are asked for one at a time.** A reply carrying several tool calls has
them asked in the order the model wrote them, and the loop waits for the
answer to the first before asking for the second. A client therefore cannot
run two of a turn's calls in parallel, which `oapx` can — it opens every call
in a reply and settles them in the order the run settles. That is a
difference in what a client *may* do rather than in what the wire carries, so
it shows up in a trace only as one call's `action.call.requested` where two
would be open at once. It is deliberate for the first slice: asking in order
makes the cancellation rule simple to state, since only one call is ever
outstanding. Parallel calls are the change to make when a client needs them,
and `pi-two-open-calls` is the fixture that would catch it going the other
way.

**A run that hits its turn limit settles as `max_turns`,** not as the
model's own stop reason. The model that wanted another turn said `tool_use`;
the reason the run ended is that the loop stopped asking, and a consumer
reading `tool_use` off a `run.completed` would conclude the turn finished on
the model's terms when it did not. `oapx` reports the same at
`bridge.zig:1010`, where an `agent_end` carrying `stop_reason: max_turns`
becomes a `run.completed` with `stop_reason: max_turns` and whatever partial
text the last turn produced. The partial text is the other half: a run cut off
by its own limit has nothing else to show for, and the cut-off turn's text is
what the caller has to work with.

**A settle the answer derived names the request it answers.** A call's
`action.call.completed` and `action.call.failed` come from a
`action.call.resolve.request` the control layer accepted, and the validator
reads them against it: the terminal must name that request in `request_id`,
and a call whose accepted resolution is the error arm may only be failed. The
loop cannot know any of that — it sees an answer, not a request — so the
mapping asks the consumer for it (`looptrace.Trace.Accepted`) and cites what it
is given. A consumer that accepts a resolution and forgets to record it gets a
terminal citing no request, which the validator reports as
`unmatched_interaction`. That is the intended failure: the alternative is a
mapping inventing a request id, which settles a call against an answer nobody
gave. It is also why the mapping and the session cannot be separate concerns —
the contract is only holdable by the layer that receives the resolution.

A run's terminal is the exception, and it is the exception by construction
rather than by luck: the event buffer's last slot is reserved for it, so a
non-terminal send never takes it and a cancelled run's terminal lands even
when the buffer is full and nobody is reading. A `select` over a send and a
cancelled context cannot do that — both cases are ready, and Go picks at
random, so a run that raced would settle as cancelled and deliver no terminal,
which is the one trace a consumer cannot interpret. After a cancel, a
non-terminal is dropped rather than waited for, so a cancelled run's events
after the cancellation are whatever arrived before it. A tool call's closing
event is dropped under the same rule, which is a trade worth naming: waiting
for room instead would put the run's terminal behind a wait the cancelling
consumer has already stopped serving, and the terminal is the one event the
buffer reserves a slot for. So a cancel that lands with the buffer already
full can leave a call open on the wire, which the validator reads as
`pending_tool_at_terminal`. It takes a consumer that is behind by the whole
buffer to get there, and the alternative loses the terminal, which is a worse
trace than one missing a close.

## What the loop must be, whatever the slice

Three rules carry over from the Zig loop and are worth stating here because
each is easy to violate while writing Go.

**A run ends with exactly one terminal event.** Zig spells it as a flag on the
run's state, set when `agent_end` goes out and tested by the thread's `defer`.
Go's structure differs — a `Run` is a goroutine writing into a channel, and a
`select` on `ctx.Done()` gives the cancellation path somewhere to happen — so
the natural spelling is a `settled` flag on the run under its mutex, but the
requirement is the flag's: the terminal comes from one place every exit path
reaches, and a run that ends twice is a defect rather than a duplicate. Those
paths are normal finish, provider failure, cancellation, a run that never
started, and a turn whose channel closed with no terminal.
`go/serve/serveendpoint` already knows the difference between "the run ended"
and "the stream stopped reaching the host" (`reportLostStream`,
`dispatch.go:354`); the loop's terminal is the former, and `run.failed` is a
settlement while a lost stream is a control frame.

**A provider failure is a normal `agent_end`, and the *endpoint* decides what
that settles as.** These are two layers, and conflating them is the mistake.
At the loop, `provider.Stream` reports a provider that refuses through
`Event{Kind: EventError}` rather than a Go `error`, so the loop makes a final
message with `StopReason: StopError` and an `agent_end` — the run got far
enough to end, and `termination` is null, which is the CLAUDE.md rule written
about. Above the loop, the OAP server turns that into the wire's settlement:
`Bridge.settleFromEvidence` (`bridge.zig:285`) reads the stop reason, and
`"error"` becomes `settleFailed` — a `run.failed` carrying
`err.code: provider_error` (`server.zig:1515`). So `oapx` emits a `run.failed`
on the wire for a provider refusal, and a Go loop that returned a Go `error`
all the way to the endpoint would reach the same settlement by a different
route, or — worse — a `run.failed` where `oapx` completes. The rule for the Go
loop is the narrower one: **the terminal is the loop's own event, and what it
settles as is the endpoint's decision, not the loop's.**

**A tool call the loop cannot resolve is a tool result, not a run failure.**
`executeToolCalls` produces an error result and carries on
(`agent_loop.zig:700` onwards: a denied call, an unknown tool, an
unparseable argument — each a result, each followed by the rest). The first
slice's analogue is the resolve round trip: no answer, a refusal, and a
malformed argument are three results, not three failures.

## Where each loop concern lands

| concern | first slice | after parity holds | note |
| --- | --- | --- | --- |
| turn loop, turn outcome, one terminal | yes | — | the shape of the loop; everything else hangs off it |
| text turns over `provider.Stream` | yes | — | including `EventError` → `stop_reason: error` |
| client-executed tool calls | yes | — | `action.call.*`, the Decision 0011 lifecycle |
| `max_iterations` | yes | — | one counter; without it a tool-calling model spins |
| streaming, per-run `sequence` | via `serveendpoint` | — | the endpoint already owns sequence and pump |
| permissions as policy | — | yes | the Zig order is `evaluate`, then approval callback, then persistence where `canPersistDecision` allows; Go's version is OAP's `action.permission.requested`/`resolved` pair, and it is a *protocol* exchange, so it wants the endpoint in the loop rather than beside it |
| steering, follow-up | — | yes | `get_steering_messages_fn` is a callback polled between turns and after each tool. A steering message re-enters the `inner` loop and a follow-up re-enters the `outer` one, which buys a fresh cancel-token check before the next turn; `state.iterations` is not reset on either path, so `max_iterations` keeps binding across follow-ups |
| compaction | — | yes | `compaction.zig` plus the overflow detection in `zig/src/utils/overflow.zig`; it needs transcript files, so it is storage work as much as loop work |
| token accounting, `context_usage` | — | yes | a `len/4` estimate; not worth carrying until something reads it |

## Served as `goap serve agent` with no backend

`oapx serve agent` without `--backend` builds a `StdioProtocolLoop`
(`zig/src/tools/makai.zig:8886`) and runs its own loop; with `--backend` it
runs an adapter. The Go `serve agent` always runs an adapter — the flag
defaults to `memory` (`go/cmd/goap/endpoint.go:39`), so there is no
no-backend case to fill. The third step gives it one: `goap serve agent` with
no `--backend` serves the Go tree's own loop, so the parity harness can drive
`goap serve agent` and `oapx serve agent` over the same scenario with no
adapter in either, and compare the two loops' traces.

That is the comparison worth making. The eight harness fixtures compare
adapters, and the `memory` fixture compares two scripted loops; neither reaches
a loop that decides. Driving two real loops over one scenario is the first check
that can say the Go loop's *answers* are its own.

The comparison is over the trace, so both trees have to be driven by the same
deterministic model — and #528 is what makes that possible. It honours
`OAPX_BASE_URL` in the Go tree and adds `MockProvider`: a loopback
`openai-completions` endpoint serving fixed frames and recording the path,
headers and body it was sent, with `TextTurn`, `ToolTurn`, `UsageTurn` and
`ErrorTurn` building the frames a turn is made of. The parity scenario points
both trees at that provider, and the recorded requests are a second diff
alongside the traces — where `exchangeWithChild` compares what an endpoint
wrote to its *child*, a native loop's equivalent is what it wrote to the
provider, and only a second tree shows that.

## And then the SDK

`go/sdk`'s `Agent` keeps its OAP path (`oapBegin`, `oapStream`) as the shape a
caller sees, and the fourth step swaps the delegation for the native loop: no
`oapx` process, the loop in-process, the same `AgentService.Run` and
`AgentService.Stream` signatures. `docs/go-library.md` says what
`go/sdk` spawns today, so that page changes with it.

The step is last because it is the one the parity claim rests on. Until the Go
loop's traces match `oapx`'s, swapping the delegation would change what a Go
program observes and nothing in the tree would say why.

## What is not settled here

- **Cancellation scope.** The Zig loop's `cancel_token` ends the run and sets
  `termination: cancelled`; `oapx`'s former OAP server advertised `run.cancel` as
  `degraded` because its cancellation was a session-scoped teardown; the `oapx`
  adapter that replaced it advertises `run.cancel` with the session left open. Go's loop should be run-scoped and
  advertise `native` — but that is a difference from `oapx` the parity harness
  will report, so it needs the draft it touches updated in the same PR, not
  left to a later reader.
- **Whether the parity fixture is its own loop or a shared provider.** #528
  answers what the model is, and the scenario above says the fixture drives
  both trees at one loopback provider. What is still open is the ordering:
  whether that provider is started per fixture like `startFakeOpenCode` is for
  `opencode`, or is started once and shared. It is a fixture question with no
  protocol content, and it is the first thing to settle when the second step
  lands.
