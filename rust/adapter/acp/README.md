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
model switching and interactive controls remain unavailable. The staged capability revision
appends `-rust-open` to the catalog revision because this descriptor has
fewer capabilities than the completed adapter. Model switching and prompt/event mapping are the next slices of #1439.

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
slice. The descriptor discloses the opening snapshot and uses a `-rust-open`
revision to distinguish the staged capabilities.

`close().await` closes asynchronously, serializes concurrent closes, and makes
state/catalog access report closed. Repeated close succeeds. Native children
that remain alive after EOF are killed and reaped at the configured shutdown
timeout. Dropping an opening or closing future uses the owning cancellation
bridge. The session's remaining control methods return unavailable errors.

Run `.claude/skills/verify/journeys/rust-acp-session.md` for real synthetic
stdio, public state/catalog output, cleanup and three-validator wire proof.
