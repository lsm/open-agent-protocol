# The Go agent loop

`zig/src/agent/` is a loop that runs turns, calls tools, checks permissions,
takes a cancellation and compacts. This note says which parts of it the Go
tree needs, in what order, and which one the first slice takes. It is the
companion to [`go-library.md`](go-library.md): that one lists the packages
`goap` has, this one covers the one it is about to grow.

Decision 0038 makes the Go tree native, and #370 is its loop. Until that lands,
`go/sdk`'s `Agent` runs a loop it does not own — it spawns `oapx serve
agent,provider --stdio` and projects the other tree's envelopes
(`go/sdk/oap.go:704`, `oapNext`). Every parity question about the Go loop is
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
| turn outcome | `turnOutcome` (`agent_loop.zig:1070`) | `failed` on `error`/`aborted`, `answered` on a reply with no tool call, `called_tools` otherwise |
| cut-off calls | `max_cut_off_tool_turns` (`:1068`) | a `length` stop mid-tool-call is retried up to three times, then treated as answered |
| tool execution | `executeToolCalls` (`:647`) | per call: find the tool, gate it, run it, measure it, append a result message |
| permissions | `permission.PermissionEngine` (`zig/src/tools/permission.zig`) | `evaluate` first, a per-tool approval callback when the policy says prompt, persistence only where `canPersistDecision` allows it |
| cancellation | `cancel_token`, checked at the top of the outer loop | the run ends, and `agent_end.termination` is `cancelled` |
| compaction | `compaction.zig`, reached from the TUI and the overflow path | summarise, replace history, keep the transcripts on disk |
| telemetry | `estimateContextUsage`, `emitContextUsage` | three `prompt_segment_usage` events and one `context_usage` per turn, from a `len/4` token estimate |

Two facts about the loop are load-bearing and neither is obvious from the
signature. The first is that **a run ends with exactly one terminal event**,
and it is a type-level fact: `AgentEvent.isTerminal` names `agent_end` and
`run_failed`, and `runLoopThread` (`:1412`) emits exactly one of the two from
its `catch`, so every path out of the run ends it — including a path that
returns before the run starts, and a failure that arrives after the run has
already ended. The second is that **`AgentEndPayload.termination` is evidence
of nothing**: it encodes only `max_turns` or `cancelled`, and is null on a
clean finish *and* on a provider that refused the request. A provider failure
is a normal `agent_end` whose `final_message.stop_reason` is `error`.

The Go tree inherits both. The first is a rule about the Go loop's own
structure; the second is a rule about what the Go loop may claim.

## Where the Go tree stands

The Go provider runtime is done (#358): `go/internal/provider` builds request
bodies for `openai-completions` and `anthropic-messages` and turns an SSE chunk
into `provider.Event`s. What it does not have is a loop. There is no
equivalent of `ai_types.Message`'s user/assistant/tool-result triple, no
`AgentEvent` union, no `StopReason` beyond `aborted`/`error`
(`go/internal/provider/types.go:18`), and no turn counter.

The Go tree does have the half of the loop that is protocol rather than model:
`go/serve/serveendpoint` (`dispatch.go:294` `submit`, `:332` `pump`) admits a
run, stamps a per-run contiguous `sequence`, and streams a run's envelopes to
the host. The memory adapter (`go/adapter/memory.go`) is a full agent loop in
Go already, but a scripted one: it replays a fixed trace and its tool results
are `{"ok":true}`. The Go loop's first job is to be the memory adapter's
script-free counterpart, over the provider runtime.

## The first slice

**Text turns and client-executed tool calls, over `go/internal/provider`.**
One package, `go/agent`, holding:

- a `Message` union over user, assistant and tool result, in the shape
  `provider.Context` already takes, so a turn is a `provider.Context` and
  nothing translates between them;
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

Deliberately not in the first slice: permissions as a *policy engine*, steering
and follow-up queues, compaction, token accounting, `max_iterations`. Each is a
few lines in the Zig loop and a real amount of policy in Go, and each is
reachable only after the first slice's traces match. `max_iterations` is the
exception — it is one counter and one condition, and a loop without it can spin
on a model that keeps calling tools, so it lands with the loop.

## What the loop must be, whatever the slice

Three rules carry over from the Zig loop and are worth stating here because
each is easy to violate while writing Go.

**A run ends with exactly one terminal event.** In Go this is a
`sync.Once`-guarded terminator on the run rather than a `defer` in a thread
function, because Go's structure is different: a `Run` is a goroutine writing
into a channel, and a `select` on `ctx.Done()` gives the cancellation path
somewhere to happen. The terminal is emitted from one place that every exit
path — normal finish, provider failure, cancellation, and a run that never
started — goes through. `go/serve/serveendpoint` already knows the difference
between "the run ended" and "the stream stopped reaching the host"
(`reportLostStream`, `dispatch.go:354`); the loop's terminal is the former, and
`run.failed` is a settlement while a lost stream is a control frame.

**A provider failure is a normal `agent_end`.** `provider.Stream` reports a
provider that refuses through `Event{Kind: EventError}`, not through a Go
error, so the loop turns that into a final message with
`StopReason: StopError` and an `agent_end`. Returning it as a Go `error` would
make a `run.failed` where `oapx` emits `run.completed` with
`stop_reason: error` — a settlement the parity harness will call a difference,
and one the CLAUDE.md rule about "a provider that refuses is a run that got
far enough to end" is written about.

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
| steering, follow-up | — | yes | `get_steering_messages_fn` is a callback polled between turns and after each tool; the `inner`/`outer` `continue` distinction is what makes a follow-up restart the turn counter |
| compaction | — | yes | `compaction.zig` plus the overflow detection in `zig/src/utils/overflow.zig`; it needs transcript files, so it is storage work as much as loop work |
| token accounting, `context_usage` | — | yes | a `len/4` estimate; not worth carrying until something reads it |

## Served as `goap serve agent` with no backend

`oapx serve agent` without `--backend` builds a `StdioProtocolLoop`
(`zig/src/tools/makai.zig:8886`) and runs its own loop; with `--backend` it
runs an adapter. The Go `serve agent` currently requires `--backend` and
defaults it to `memory` (`go/cmd/goap/endpoint.go:38`). The third step makes
the Go tree's own loop the no-backend case, so the parity harness can drive
`goap serve agent` and `oapx serve agent` over the same scenario with no
adapter in either, and compare the two loops' traces.

That is the comparison worth making. The eight harness fixtures compare
adapters, and the `memory` fixture compares two scripted loops; neither reaches
a loop that decides. Driving two real loops over one scenario is the first check
that can say the Go loop's *answers* are its own.

The harness needs one thing it does not have today: a provider both trees can
answer from. `exchangeWithChild` compares an endpoint's envelopes and, where
one exists, what the endpoint wrote to its child. A native loop has no child,
so the comparison is over the trace, and both trees must be driven by the same
deterministic model. The shape that fits: a local HTTP provider speaking the
`anthropic-messages` wire, which `go/internal/provider` already speaks, and a
scenario that gives it a scripted tool call to make.

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
  `termination: cancelled`; `oapx`'s OAP server advertises `run.cancel` as
  `degraded` because its cancellation is a session-scoped teardown
  (`zig/src/protocol/oap/server.zig:34`). Go's loop should be run-scoped and
  advertise `native` — but that is a difference from `oapx` the parity harness
  will report, so it needs the draft it touches updated in the same PR, not
  left to a later reader.
- **Which model a parity run uses.** The scenario above needs a provider
  neither tree can be given a credential for. That is a fixture question with
  an owner answer attached, and it is the first thing to settle when the
  second step lands.
