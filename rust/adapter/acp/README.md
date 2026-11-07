# oap-adapter-acp

The Rust port of `go/adapter/acp`: an OAP adapter over an Agent Client Protocol
v1 agent on stdio, pinned by `harnesses/acp.json` and judged by the corpus in
`fixtures/adapters/acp-v1.12.0`.

`native` holds the pinned upstream wire types and nothing else. Because the
corpus records what the Go adapter accepted, they keep four of Go's
`encoding/json` decoding rules: a `null` member, or a `null` element of a string
list, is its zero value; a struct member refuses an array; and a raw member
keeps an explicit `null`. `native::decode` applies them to a whole message.

Three Go behaviours are not reproduced here:

- Go falls back to a case-insensitive key match, so `{"sessionid":"s"}` sets
  `SessionID`; these types match keys exactly. The pinned corpus spells every
  key in its exact case, so nothing observes the difference yet.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029. Serializing a
  type here does not, so the process layer writes
  every frame through `oap_adapter::gojson::go_marshal` (#1445).
- Go decodes an unpaired surrogate escape such as `\ud800` as U+FFFD; serde_json
  refuses the document. The shared rpc layer passes each frame through
  `oap_adapter::gojson::replace_lone_surrogates` before decoding it.
  Invalid UTF-8 never reaches these types: the frame reader refuses it, as Go's
  does.

A member written twice never reaches these types. Go's encoding/json would keep
the last copy, but the rpc layer refuses the whole frame first with
`oap_adapter::gojson::reject_duplicate_keys`, as Go's `ParseMessage` refuses it
with `jsonwalk`, so serde's refusal of a repeated field is never the one that
decides.

## Process and initialization

`process::Process::start(&ProcessConfig, deadline)` launches a configured agent
and completes ACP version-1 initialization through the shared JSON-RPC client.
The returned `client` handles requests and ordered inbound messages. Client
capabilities default to `{}` and client info is optional. The agent must return
version 1 and a non-null `agentCapabilities` object. Initialization failures
abort and reap the child, with redacted bounded stderr in the error.

`close()` releases stdin, waits up to `shutdown_timeout` (five seconds by
default), then kills and reaps a child that remains alive. Its result is cached,
and dropping the process also closes it. This reuses the shared child-process
helper and its documented pipe/grandchild limitations in `rust/adapter/README.md`.
The handshake types share the native types' exact-key decoding limitation.
Session operations and corpus replay remain subsequent slices of #1439.

Run the synthetic public-consumer journey in
`.claude/skills/verify/journeys/rust-acp-process.md` for captured wire evidence.

## Adapter foundation

`Adapter::new(Config)` checks an executable path, absolute process working
directory and uniquely named configured MCP servers. `pin()` reads the current
ACP version, capability revision and corpus from the embedded harness catalog.
`Adapter::describe()` and the shared `Adapter::probe()` trait return its endpoint
and configured source descriptors.

The shared `Admitter::admit()` validates participant identity and refuses
unadvertised controls without starting a process. `Adapter::open()` applies
the same checks before opening the native session. Permission resolution uses the owned controller. Reopen, accepted cancellation and replay have approved follow-on slices. Text submission and
streaming require explicit degradation opt-in. The staged capability revision
appends `-rust-permissions` to the catalog revision because this descriptor has
fewer capabilities than the completed adapter. Remaining event mappings and live configuration updates are later slices of #1439.

Run `.claude/skills/verify/journeys/rust-acp-foundation.md` for the public
consumer, no-spawn proof and three-validator capability exchange.

## Cancellable process operations

`Process::start_async(config, deadline)` returns an owning `Operation<Process>`
future. `process.call_async(method, params, deadline)` transfers process
ownership to one opening RPC and returns an `Operation<(Process, RawValue)>`.
On success, the caller gets the process back. An RPC error closes it.

Operations start a worker at construction and wake their consumer through a
oneshot response. Polling a quiet operation returns pending. Dropping an
unfinished operation kills its owned native child, closes the RPC client and
joins the worker. Cancellation also covers the race before startup registers
its process. Completed operations join before handing their process to the
caller. Cleanup runs during drop, with the shared process helper's documented
grandchild-held pipe limitations.

This is the process/open-operation bridge for the next session slice. The
session-opening path uses this bridge. Run the public consumer in
`.claude/skills/verify/journeys/rust-acp-operation.md` for captured quiet-agent
cancellation, early-drop races, successful handoff and deadline evidence.


## Session opening and model catalog

A valid open initializes the configured process, sends `session/new` with the
absolute working directory and configured MCP servers, and returns idle OAP
state plus the opaque native session id. An omitted OAP session id is generated
per adapter. Missing or malformed native session ids fail and close the process.

`model_lister().models(request)` requires explicit `models.list` degradation
opt-in. It reports the opening response's model config-option snapshot, flattening
native option groups in order. `default` and `current` placeholders stay absent
from the current model. An absent model option returns an unsatisfiable control
error. Native configuration-update handling belongs to the later event/config
slice. The descriptor discloses the opening snapshot and uses a `-rust-permissions`
revision to distinguish the staged capabilities.

`close().await` closes asynchronously, serializes concurrent closes, and makes
state/catalog access report closed. Repeated close succeeds. Native children
that remain alive after EOF are killed and reaped at the configured shutdown
timeout. Dropping an opening or closing future uses the owning cancellation
bridge. The permission resolver uses the owned controller described below. Cancellation and replay expose their staged unavailable results.

After `session/new`, the session owns a `PromptBridge` that drains native
inbound messages in order. Idle notifications therefore cannot fill an
undrained queue while a model switch waits for its result. Unknown reverse
RPCs receive the native method-not-supported error. Explicit close, cancelled
close and dropping the final session owner release the child and dispatcher.
Normal permission handling is described below. Live configuration projection belongs to a later slice.

Run `.claude/skills/verify/journeys/rust-acp-session.md` for real synthetic
stdio, public state/catalog output, cleanup and three-validator wire proof.
The additional `rust-acp-session-bridge.md` journey measures OS thread counts
before open and after public close/drop/cancellation while its consumer stays
alive, and requires all threads to return to the initial count.


## Confirmed model switches

`model_switcher().switch_model(request)` requires explicit
`session.model.switch` degradation opt-in, the matching OAP session id and a
model id in the opening catalog. It writes `session/set_config_option` using
the native session/option ids. The returned list must contain exactly one
matching option with an allowed kind and the requested current value before
recorded state changes. Refused or unconfirmed switches preserve the previous
state. Successful results include both the response and updated state for a
host to publish as `session.state.updated`.

Switches serialize with each other. Concurrent close releases a pending switch;
dropping its shared RPC future cancels and reaps the process for all holders.
The shared operation helpers retain their process in an `Arc`, and join their
workers. Returned reasoning metadata follows the agent's confirmed thought-level
value or label, including Go's simple lowercase handling of dotted capital I.
Missing or unrecognized reasoning clears the previous level. Catalog values stay
the opening snapshot while their current/default flags follow recorded state.

The feature remains degraded because model identity is the agent's word.
Unsolicited config-option projection retains its later slice.
Run `.claude/skills/verify/journeys/rust-acp-model-switch.md` for public Go/Rust
consumer comparisons, captured native frames and wire validation.

## Native prompt bridge

`PromptBridge::new(process, handler)` takes a successfully initialized process
and owns an ordered inbound dispatcher. The handler receives native notifications
and reverse requests and may answer each reverse request through its responder.
Handlers finish promptly and avoid reentrant blocking calls or bridge shutdown.
The bridge acknowledges internal RPC barriers after earlier handlers return.
A prompt's native result therefore follows all earlier inbound observations.
A handler panic closes the RPC client and is reported during shutdown.

Use `bridge.call(method, params, deadline)` for native session setup or other
RPCs. `bridge.prompt(params, deadline)` returns an owning `PromptCall` future.
Await `call.admitted()` once to observe serialized writer completion, then await
the call for the typed native result. Successful admission establishes the write
boundary. The agent's response establishes completion. Both futures wake their
consumer while a quiet agent leaves polling pending.

Dropping an unfinished prompt cancels the shared process, waits for its child
exit and joins the call worker. `Arc::new(bridge).close().await` closes the child
and joins the inbound dispatcher before returning. Dropping the last bridge
owner also performs shutdown and joining. A child ignoring EOF is killed and
reaped after the configured timeout. The shared helper's documented
grandchild-held-pipe limitations remain applicable.

The normal session integration below uses this native layer for OAP submission.
Its public synthetic stdio proof is in
`.claude/skills/verify/journeys/rust-acp-prompt.md`. OAP run/event mapping follows
in the next approved #1439 slice.

## Bounded native text-run engine

`TextRun` projects one admitted ACP text run into OAP envelopes through the
shared bounded `Subscriber`. Its context supplies valid session/run/message
identities, the opaque native session id and an optional capability revision.
The host supplies the event-id generator and observed timestamps. The engine
serializes publication through its mutable methods.

After the prompt's native writer admission, `start(now_ms)` publishes
`run.started` and returns its event stream. Feed ordered RPC notifications to
`observe(notification, now_ms)`, and finish with `complete(result, now_ms)` or
`fail(code, message, inferred, now_ms)`. The host orders admission before
notifications. A bridge handler can wait on admission and the RPC client's done
receiver so shutdown can release it. The verification consumer exercises that
ownership boundary over real synthetic stdio.

The engine emits contiguous sequences, text deltas, a complete assistant
message, observed completion/refusal errors and explicitly inferred transport
failures. Once settled, late updates and repeated settlement preserve the first
terminal. A slow consumer receives its bounded prefix and an explicit overflow.
the native projection can still settle. `close()` or dropping the engine
finishes the retained prefix when the owner abandons a run.

Native message identities map to separate OAP message ids and accumulated text.
Repeated native ids reuse their OAP ids, and an omitted id continues the current
message. The terminal response contains the last message's own text. Cancellation
requiring host evidence produces an explicit staged failure. OAP
`run.cancelled` requires accepted host cancellation evidence. Configuration-update
bodies are decoded structurally and malformed bodies fail the run. Live config
projection retains its approved later slice.

Run `.claude/skills/verify/journeys/rust-acp-text.md` for native wire, Go/Rust
event comparison, bounded overflow, cleanup and three-validator event proof.


## Public staged text submission

`Session.submit` accepts user messages with string content and automatic delivery.
Both `session.message.submit` and `run.streaming` require explicit degradation
opt-in. The staged descriptor records these limits under its `-rust-permissions`
revision. Native writer admission precedes the accepted response and the first
run event. The ordered dispatcher waits for admission before projecting native
notifications. A shared session snapshot tracks the run alongside model switches.

The submit future owns the prompt through native write admission. After admission,
the session owns the completion worker. Default close immediately refuses an active run, including blocked write admission.
`Config.abort_active_on_close` enables closing during a run or blocked admission.
Close and final session drop release the native child, dispatcher and completion
workers. A submission interrupted by close reports session closed. Cancelled close joins the same owned workers. An EOF-ignoring child can
report a shutdown timeout after being killed and reaped. Abandonment closes the
subscriber's retained prefix. Later mapping slices extend the staged engine.

Run `.claude/skills/verify/journeys/rust-acp-submit.md` for public API traces,
bounded overflow, child/thread ownership and three-validator proof.


## Reported usage and context

Each fresh native session owns one `UsageScope`. `TextRun::new_in_scope` lets
native bridge consumers share an `Arc<Mutex<UsageScope>>` across their runs.
`UsageScope::observe` consumes usage observations during idle periods too. Forward
unconsumed notifications to the active run. `TextRun::new` creates a fresh scope.
Use `prompt_error` for native RPC errors, including failed write admission.
Before start, it closes that attempt at sequence zero.
A late answer after an observation already failed the run still consumes that
session's first-call scope while preserving the published terminal.

The first completed native prompt call of a new session can report core input,
output and total counts. A failed native write consumes that scope even though
its run never starts, matching Go's accounting transition. Later prompts retain
the agent's turn counts in
`com.agentclientprotocol.turn_usage`. Thought and cache counts stay in that
extension. The ACP v1.12.0 mapping ledger records their unresolved accounting
semantics, and core input/output meanings remain lossy and unverified against
Decision 0063. Counts are preserved as reported. Required malformed counts omit
the turn report, while bad optional counts omit only themselves. Zeros and the
full unsigned 64-bit range stay exact. Fields use Go's case folding and last
matching duplicate value.

Valid `usage_update` observations replace the session's context snapshot, including
between runs. Bad required counts preserve the preceding snapshot, and bad cost
drops only the cost. Terminal envelopes carry the latest applicable context in
`com.agentclientprotocol.context`. Cost is retained as the agent's reported
session cost, with Go-compatible floating-point JSON spelling. No price is derived
from token counts. Native RPC errors carry context without invented turn counts.

Run `.claude/skills/verify/journeys/rust-acp-results.md` for the public multi-turn
Go/Rust replay, captured native frames and three-validator traces.

## Observed native tool calls

The normal session projects ACP tool creation and sparse patches into scoped
requested, started, progress and terminal events. Native tool ids receive OAP
ids, repeated observations retain them, and reuse across prompts is refused.
Native string names override presentation titles. Invalid name patches retain
the preceding name, and empty title patches retain the preceding title.

A terminal without progress first emits start. Missing requested arguments and
completed results are explicit null. Sparse progress patches omit arguments,
results and errors. Native raw inputs, outputs, content and locations retain
their JSON values. Unfinished children settle in creation order before the run.
Normal completion starts pending tools and fails them as incomplete. Run failure
cancels outstanding tools. Abandonment and session close still finish the
retained prefix under the documented ownership contract.

`action.tools` and `action.tools.execute` are degraded observed-lifecycle claims.
They expose no tool catalog or host execution API. Accepted host
cancellation, reopen/config projection and the full byte corpus retain later
slices. Run the synthetic `rust-acp-tools.md` verification journey.

## Owned native outbound operations

`PromptBridge.notify(method, params, deadline)` forwards a notification through
an owning `Operation<()>`. `respond(request, result)` and
`respond_error(request, code, message, data)` own reverse-request response writes.
The request must originate from this bridge. Identity is checked by the actual
request object, and a matching wire id from another bridge is refused. Retained
requests can be answered once through either response spelling.

Operations begin work when constructed. Polling is nonblocking. Await joins the
operation worker. Dropping an unfinished operation cancels the native process,
kills/reaps its child and joins the worker, including a response whose native
write is blocked. Bridge close/drop settles its native transport while pending
operation futures remain caller-owned. A notification deadline returns its RPC
error, after which the owner closes the bridge for complete process cleanup.

All writes use the shared bounded FIFO writer and its queue-full result. Request
registration retains weak references and prunes expired entries when new
requests arrive. Run `rust-acp-outbound.md` for successful frames, quiet-child
cleanup, deadlines, bounded admission and thread inventories. Normal Session permission publication/resolution is described below. Run cancellation retains its approved following layer.

## Typed permission projection

`TextRun.permission_request(request, participant, now_ms)` takes a decoded
native permission request and the admitted control participant. It reuses the
observed tool identity/input, filters native choices, publishes the permission
and returns its OAP interaction id. Duplicate native option ids retain their
last grant meaning and their presented choices.

`permission_reply(&resolution)` validates the control answer and returns an
opaque `PermissionReply`. Its `interaction_id()` and immutable `response()`
allow the caller to locate the actual incoming request and send the native
response through the owned bridge. Write outside the TextRun mutex, then call
`commit_permission(reply, now_ms)` to publish the correlated OAP resolution.
A reply belongs to its actual gate object. Another projection's coincident ids
cannot authorize a commit, and a repeated resolution is refused.

A pending permission defers the first selected parent terminal. Call
`permission_settlements()` after completing/failing the projection, write its
native cancellation replies in order and commit them. The last commit settles
unfinished tools and the parent. Parent settlement cancels local gates even
when the native cancellation write fails. Selected replies require successful
native writes before commit. Callers serialize native reply attempts and keep
projection locks out of native I/O. The owned Permissions controller below handles that
coordination. Pending terminal usage/context stays at its first snapshot while
later native answers still advance session usage scope.

Closing the projection finishes its retained prefix and refuses a prepared
commit. The typed methods require an active TextRun and an admitted participant.
The standalone `rust-acp-permission-projection.md` journey proves native replies
and three-validator traces. Normal `Session.resolve` uses this projection through the owned controller and Session integration below.

A pending terminal still accepts native usage observations into the shared
session scope. They leave the chosen terminal snapshot intact and reach the
next run. The pending-usage native barrier proof covers that hand-off.

## Owned permission controller

`Permissions::new(bridge, participant, clock)` binds one native bridge and the
admitted participant. `request(run, incoming)` accepts only actual requests
registered by that bridge. Reusing a request object is refused. The controller
retains the source binding through native reply and correlated commit.

`resolve`, `observe`, `complete` and `prompt_error` serialize native writes and
projection transitions through an owned worker. Use these methods for a
controller-managed run. Prepare and validate under the TextRun mutex, release
it for native I/O, then read the clock and commit. A successful selected write
and its commit belong to the same worker, so abandoning the future cannot
discard an accepted reply between them. Parent settlement commits local
cancellations even when the native transport has failed.

The returned futures begin work when constructed. Await joins the worker.
Dropping one aborts and reaps its native child and joins the worker, including
an operation waiting for the controller lock. The bridge owns the dispatcher,
and close joins it. Futures remain caller-owned through bridge shutdown.

Run `rust-acp-permission-controller.md` for the standalone native comparison,
source/duplicate/concurrent-answer checks and quiet-child ownership proof.
Normal Session integration is described below.

Malformed native asks receive their protocol error independently of whether a
local failure event can be emitted. Binding lookup uses the actual run object
and its interaction id. Completed entries expire with their native request
object, with pruning after each controller operation. Retained request objects
keep weak duplicate guards until release. Per-run id generators can restart
without routing the next run's reply through a preceding binding.

## Normal Session permissions

The public Session uses Permissions for native publication, control answers and
parent transitions. It waits for prompt admission before publishing run events.
Incoming sources are registered before answers can acquire the controller lock,
so a control consumer can resolve immediately after receiving the request.
Malformed idle asks receive -32602 and valid idle asks receive cancelled.

State reads refresh from the active projection when its cursor advances,
including during publication and settlement. This preserves newer model-switch
timestamps when the run stays settled. Late completion workers refresh the current active projection.
Settled interaction ids retain typed already-resolved refusals across later
admissions, with one shared id counter across normal Session runs.

Default close refuses an active gate with RunActive. Opt-in abort close and
owner drop finish the retained prefix, stop the native child and join the
dispatcher and prompt workers. A successful native response consumes its
request binding before local commit, including a projection closed during the
write. Parent settlement consumes the binding before committing cancellation.

The descriptor advertises native action.permissions and interactive gates,
under the staged -rust-permissions revision. Run
`rust-acp-session-permissions.md` for normal Session event/wire comparisons,
immediate resolution, idle replies, across-run history, state and cleanup.
Accepted run cancellation, reopen/config and full corpus retain later slices.
