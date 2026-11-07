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
the same checks before opening the native session. Reopen, prompt streaming,
interactive controls remain unavailable. The staged capability revision
appends `-rust-switch` to the catalog revision because this descriptor has
fewer capabilities than the completed adapter. Prompt/event mapping and live configuration updates are later slices of #1439.

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
slice. The descriptor discloses the opening snapshot and uses a `-rust-switch`
revision to distinguish the staged capabilities.

`close().await` closes asynchronously, serializes concurrent closes, and makes
state/catalog access report closed. Repeated close succeeds. Native children
that remain alive after EOF are killed and reaped at the configured shutdown
timeout. Dropping an opening or closing future uses the owning cancellation
bridge. The session's remaining control methods return unavailable errors.

After `session/new`, the session owns a `PromptBridge` that drains native
inbound messages in order. Idle notifications therefore cannot fill an
undrained queue while a model switch waits for its result. Unknown reverse
RPCs receive the native method-not-supported error. Explicit close, cancelled
close and dropping the final session owner release the child and dispatcher.
Permission handling and live configuration projection belong to later slices.

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
Unsolicited config-option updates and prompt/event mapping are later slices.
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

This low-level native layer leaves OAP `session.message.submit` unavailable.
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

This slice handles one native message identity. Multiple identities, tool
updates and cancellation requiring host evidence produce explicit unavailable
failures. OAP `run.cancelled` needs accepted host cancellation evidence.
Configuration-update bodies are decoded structurally and malformed bodies fail
the run. Usage and live configuration projection belong to their later mapping
layers. The public consumer covers these staged limits. OAP submit remains unavailable
until the approved session-integration slice.

Run `.claude/skills/verify/journeys/rust-acp-text.md` for native wire, Go/Rust
event comparison, bounded overflow, cleanup and three-validator event proof.
