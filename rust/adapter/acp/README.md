# oap-adapter-acp

The Rust port of `go/adapter/acp`: an OAP adapter over an Agent Client Protocol
v1 agent on stdio, pinned by `harnesses/acp.json` and judged by the corpus in
the `fixtures/adapters/` directory selected by that catalog.

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
The Session operations are described below. Full corpus replay remains a later
slice of #1439.

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
the same checks before opening the native session. Permission resolution uses the owned controller. Native cancellation uses the owned controller. Native reload and bounded canonical replay are described below. Text submission and
streaming require explicit degradation opt-in. The staged capability revision
appends `-rust-attachments` to the catalog revision because this descriptor has
fewer capabilities than the completed adapter. Full corpus replay remains a
later slice of #1439.

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
opt-in. It reports the latest model config-option snapshot, flattening
native option groups in order. `default` and `current` placeholders stay absent
from the current model. An absent model option returns an unsatisfiable control
error. Live snapshots replace the opening snapshot as described below.
The descriptor uses a `-rust-attachments`
revision to distinguish the staged capabilities.

`close().await` closes asynchronously, serializes concurrent closes, and makes
state/catalog access report closed. Repeated close succeeds. Native children
that remain alive after EOF are killed and reaped at the configured shutdown
timeout. Dropping an opening or closing future uses the owning cancellation
bridge. The permission resolver and native cancellation use the owned controller
described below. Replay remains unavailable.

After `session/new`, the session owns a `PromptBridge` that drains native
inbound messages in order. Idle notifications therefore cannot fill an
undrained queue while a model switch waits for its result. Unknown reverse
RPCs receive the native method-not-supported error. Explicit close, cancelled
close and dropping the final session owner release the child and dispatcher.
Normal permission handling and live configuration projection are described below.

Run `.claude/skills/verify/journeys/rust-acp-session.md` for real synthetic
stdio, public state/catalog output, cleanup and three-validator wire proof.
The additional `rust-acp-session-bridge.md` journey measures OS thread counts
before open and after public close/drop/cancellation while its consumer stays
alive, and requires all threads to return to the initial count.


## Confirmed model switches

`model_switcher().switch_model(request)` requires explicit
`session.model.switch` degradation opt-in, the matching OAP session id and a
model id in the latest catalog. It writes `session/set_config_option` using
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
Missing or unrecognized reasoning clears the previous level. Catalog values
follow the latest unsolicited config snapshot, while current/default flags
follow recorded state.

The feature remains degraded because model identity is the agent's word.
Unsolicited config-option projection is described below.
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
message. The terminal response contains the last message's own text. Cancellation without an accepted controller intent produces an explicit failure. OAP
`run.cancelled` requires accepted host cancellation evidence. Configuration-update
bodies are decoded structurally and malformed bodies fail the run. Session-owned
live config projection is described below; the standalone text engine does not
own the session model catalog.

Run `.claude/skills/verify/journeys/rust-acp-text.md` for native wire, Go/Rust
event comparison, bounded overflow, cleanup and three-validator event proof.


## Public staged text submission

`Session.submit` accepts user messages with string content and automatic delivery.
Both `session.message.submit` and `run.streaming` require explicit degradation
opt-in. The staged descriptor records these limits under its `-rust-attachments`
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
They expose no tool catalog or host execution API. Native cancellation is
described below; full byte corpus retains its later
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

`request`, `resolve` and `cancel` own native I/O workers. Ordinary `observe`
projection runs inline using only the run lock, so text can progress during a
blocked native reply. Parent completion/error waits for the bindings lock before
projecting under the run lock, without creating a local worker. This serializes
confirmation with an accepted cancel write and its intent commit. Settlement
also acquires bindings before the run lock. Pending native cancellation replies
use an owned I/O operation. Use these methods for a controller-managed run. Prepare and validate under the TextRun mutex, release
it for native I/O, then read the clock and commit. A successful selected write
and its commit belong to the same worker, so abandoning the future cannot
discard an accepted reply between them. Parent settlement commits local
cancellations even when the native transport has failed.

Native I/O operations begin work when constructed. Await joins their worker.
Dropping one aborts and reaps its native child and joins its worker, including
a native reply operation waiting for the controller lock. Ordinary notifications
run on the polling caller without waiting for native writes. The bridge owns the dispatcher,
and close joins it. Futures remain caller-owned through bridge shutdown.

Run `rust-acp-permission-controller.md` for the standalone native comparison,
source/duplicate/concurrent-answer checks and quiet-child ownership proof.
Normal Session integration is described below.

Malformed native asks receive their protocol error independently of whether a
local failure event can be emitted. Binding lookup uses the actual run object
and its interaction id. Completed entries expire with their native request
object, with pruning on the next native controller operation. Retained request objects
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
Live permission routes are removed on commit, parent settlement and close.
Up to 64 resolved interaction ids retain typed already-resolved refusals across
later admissions. An older id outside that cache returns InteractionNotFound
after its run is replaced. Normal Session runs share one id counter.

Default close refuses an active gate with RunActive. Opt-in abort close and
owner drop finish the retained prefix, stop the native child and join the
dispatcher and prompt workers. A successful native response consumes its
request binding before local commit, including a projection closed during the
write. Parent settlement consumes the binding before committing cancellation.

The descriptor advertises native action.permissions and interactive gates,
under the staged -rust-attachments revision. Run
`rust-acp-session-permissions.md` for normal Session event/wire comparisons,
immediate resolution, idle replies, across-run history, state and cleanup.
Native cancellation, reload and opening settings are described below. Full
corpus retains its later slice.

## Run-targeted cancellation

`Session.cancel(run_id)` targets the tracked run. Unknown or expired prior ids
return RunNotFound, the retained completed/failed run returns RunAlreadyTerminal,
and cancelling/cancelled retries are accepted without another native notification.
Retained terminal metadata still answers after the native agent exits; an
explicitly closed Session refuses controls with SessionClosed.
No stale control can target a subsequent run. Canonical replay of prior known
runs is separate from the tracked cancellation target and is described below.

A written native session/cancel accepts intent and emits run.status.updated
with cancelling. Ordinary text/tool events preserve that status, and Session
state stays running until authoritative settlement. A native cancelled stop or
remote -32800 confirms cancellation; another result/error may win. Pending asks
receive cancelled and correlated permission resolutions with run_cancelling.
A later valid ask projects its observed tool but receives native cancellation
without publishing another permission prompt.

`Permissions.cancel(run)` exposes the same owned bridge operation for a managed
TextRun. Native write and intent commit share one worker. Parent results wait
for that transaction; ordinary notifications keep progressing. Dropping an
unfinished control, including one queued behind a blocked permission reply,
reaps its child and joins its worker. Abort close also joins the dispatcher and
prompt workers while control futures remain caller-owned. No cancel settlement
is invented when the native notification was never written.

Run rust-acp-cancel.md for public Go/Rust event and parsed-wire comparisons,
three-validator traces, controlled confirmation ordering and quiet-child/thread
ownership. The staged revision is -rust-attachments and run.cancel is native. Submit
and streaming retain their degradation opt-ins. Run replay/resume is covered
by the bounded journal below; full byte corpus and C8 served parity remain later slices.

## Live config state and model catalog

The ordered session dispatcher applies a valid native `config_option_update`
inline, whether idle or running. It replaces the model option and catalog and
derives recorded model/reasoning from the full list, using the same mapping as
Go. Later model switches validate against and write that replacement option.
Missing model options remove the catalog and make switches unsatisfiable.
Empty/default/current model values retain the previous model; missing or
unreadable thought levels clear recorded reasoning. Only changes to recorded
model or reasoning advance the state timestamp.

Config-only updates emit no stream event. Wrong-session and malformed idle
updates leave state unchanged; malformed active updates retain the existing
`acp_invalid_update` failure. No native I/O worker is started for this local
projection. Capability fidelity stays staged under `-rust-attachments`, as no
optional feature's support level changes.

Run `.claude/skills/verify/journeys/rust-acp-live-config.md` for actual native
wire, public Go/Rust snapshots, streams and three-validator proof. Native
reload and confirmed opening config/reasoning are described below.

## Native conversational reload

A reopen needs an existing native session id. After initialize, the adapter
uses session/load when loadSession is true, otherwise session/resume when its
capability is an object. Missing bindings or unsupported agents refuse with
an unsatisfiable session.open.reopen control. The descriptor advertises native
reopen under the -rust-attachments revision. Reopening creates a fresh journal;
pre-close OAP run ids and events are not recovered from native history.

The public Process.call_draining_async operation consumes ordered inbound
messages while its scoped RPC worker waits. Historical notifications are
discarded and reverse requests receive -32601. The fixed-size RPC queue remains
the bound; no history buffer or detached worker is added. A 60-second reload
deadline closes the owned process. Dropping the pending future kills/reaps
the child and joins both operation and RPC workers, including a reverse reply
blocked mid-write. The initiating RPC deadline is preserved when its shutdown
also makes the reverse reply fail with a broken pipe.

A successful reload preserves the bound id when the response omits it or sends
null, refuses a changed/empty id, and reports recovery plus returned model and
reasoning through the existing config projection. A subsequent prompt starts a
fresh OAP run at sequence 1. Reopened cumulative usage stays in ACP extensions;
it is not attributed to that run's core usage. Opening config/reasoning is
covered below; the full byte corpus remains later.

Run `.claude/skills/verify/journeys/rust-acp-reload.md` for normal public Go/Rust
state/catalog/event and parsed-wire comparisons, three-validator exchanges,
partial native bytes, child absence and OS-thread cleanup before consumer exit.

## Confirmed opening configuration and reasoning

Config.session_config owns a private map of native option ids to string values.
New and reloaded sessions set them in sorted key order, only when a unique
select option advertises the value. Each reply must confirm it, and the full
required set is rechecked before and after a requested reasoning level. A later
setting cannot silently reset an earlier required value. Refusals close the
owned process before any Session or prompt is published.

Opening reasoning is emulated in session_open mode under -rust-attachments. The
agent's thought_level option supplies a case-insensitive value/name mapping;
its reply must confirm the selected id/value. Requested reasoning remains
explicit in state. With configured options or reload, other model/reasoning
metadata comes from the final full snapshot. A fresh unconfigured session
preserves unspecified defaults. Compaction and live reasoning control remain
unavailable. This does not expose session_config in the served registry yet.

Every opening setter uses the owned draining process operation. Native
notifications and reverse calls drain before normal Session dispatch starts.
Dropping a quiet or partially written setting reaps the native child and joins
its workers. The opening-config journey compares normal public Go/Rust
results and native wire, validates Rust exchanges with all three validators,
and retains partial bytes and OS-thread inventories. Large snapshot CPU
preparation is separately measured; waiting-poll latency is checked only
after actual native bytes/requests are observed. Full byte corpus and C8
served parity remain their separate gates.


## Bounded session journal and canonical replay

Config.journal_capacity bounds retained event records per Session; zero selects
256, matching Go. The descriptor reports session/process_memory scope and
degraded run.resume/run.replay, with emulated reconciliation. There is no disk
persistence, native history import or recovery of pre-close OAP runs.

Session.resume returns the exact retained suffix after its cursor and follows
that run through its terminal. Snapshot installation is serialized with event
emission; replay preserves event ids, sequence, payload, timestamps and revision.
A future cursor is refused, a missing run is distinct from an evicted suffix,
and Error::ReplayGap carries the requested, oldest and latest sequence. Prior
completed run cursor metadata remains addressable even after all of its event
records are evicted, as in Go. Only retained event records have the configured
capacity bound; this is not a claim of durable history or bounded run metadata.

The shared journal stores canonical events. Each follower retains its initial
suffix plus a 64-event live queue. A slow consumer receives a contiguous prefix
and an explicit overflow instead of skipped events. Close preserves queued
prefixes, closes followers and joins the existing native workers; it invents no
terminal for an unconfirmed run. Reopening starts an empty journal. Cancellation
continues to address the tracked run under the previously disclosed stage.

Run rust-acp-journal.md for public Go/Rust recovery and event comparisons,
parsed native wire, close/overflow and missing/future/evicted cursor outcomes.
Complete original traces must pass all three validators. Aborted/overflowed
prefixes must be retained as incomplete and produce missing_run_terminal,
not be padded with a synthetic terminal to make validation pass. The stage is
-rust-attachments. Per-open MCP attachments are described below. Deterministic
clocks/ids with the full corpus retain their next approved slice; this is not C8 served parity.


## Per-open stdio MCP attachments

OpenRequest.tool_sources accepts process descriptors in session_open mode.
The attachment capability is native with the process transport limit. The
adapter sends configured servers first, followed by that open's attachments,
in session/new, session/load or session/resume. The agent owns MCP execution;
the adapter does not start an attached command itself. Protocol/display/endpoint
members remain descriptor metadata, matching Go's process-transport mapping.

Ids must be nonempty and unique across configured and attached servers.
Non-process transports and duplicate environment names produce typed
unsatisfiable controls. A missing command returns InvalidResolution; inline
tool definitions remain unadvertised. Admission performs these same checks
before starting a native process.

Literal NAME=value entries, including empty values, are forwarded in order.
Bare NAME entries resolve only against explicit Config.process.env pairs;
missing names are dropped and the ambient process environment is not consulted
for this allowlist. The last explicit configured value wins. Per-open arguments,
commands and environment values appear only in the native parameters. Public
Session state publishes configured plus attached descriptor projections, with
no command, args or environment fields, and later opens do not inherit them.

Run rust-acp-attachments.md for captured-array matching, normal new/load/resume
wire, validation priority, environment behavior, independent opens and actual
quiet-opening cancellation cleanup. The stage is -rust-attachments. The full
byte corpus with deterministic clocks/ids remains the next C1 prerequisite;
no C8 served-parity hand-off is claimed.

## Clock and identity sources

`Config::runtime` accepts a cloneable `Runtime::new(clock, ids)` whose sources
are shared by the adapter's sessions and events. Omission selects wall time in
milliseconds and one sequential ID source per adapter, as before. Native RPC
request IDs remain owned by the JSON-RPC client.

Each emitted event samples its envelope time separately. Payload times such as
`run.started.started_at_ms` and cancellation's `updated_at_ms` are sampled when
that payload is constructed. Notifications that emit nothing consume no clock
read. Session state changes sample the same source. Session-owned text runs
therefore do not pre-sample a fallback time at each native input boundary. The
permission controller retains a real fallback clock and samples it only for
standalone runs without an installed runtime.

Standalone `TextRun` callers keep their explicit timestamp and ID callback
contract. `TextRun::with_runtime` opts into the shared sources for both IDs and
timestamps. This supports the pinned corpus's distinct start-payload and
envelope timestamps without adding an offset to caller-supplied times.

The clock/identity regression replays the pinned `new-prompt-completed` native
frames through a real synthetic child with an empty environment. It compares
all event clocks, IDs, payloads and positions directly with the recorded values,
and separately verifies the Rust adapter's staged capability revision. The
complete byte corpus, descriptor graduation and other adapter cases remain a
later slice of #1439; this check does not claim their completion.
