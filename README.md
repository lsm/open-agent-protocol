# Open Agent Protocol

Draft public-domain semantic protocol for agent software layers, agent loops,
model IO, tools, resources, and adapters for existing agent SDKs.

The first conformance target is an agent-control core covering the boundary
between a control layer and an agent loop: capabilities, session state, message
submission, run lifecycle, streamed content, cancellation, and state recovery
without binding to a specific transport.

OAP layers are logical software boundaries, not deployment sides. A
presentation layer, control layer, agent loop, model provider, tool executor,
resource provider, and binding can live in one process or across multiple
transports while preserving the same protocol semantics. Adapters for existing
SDKs or protocols are implementation shims, not a separate semantic layer.

Current drafts:

- [Conformance Draft](drafts/conformance.md)
- [Presentation Control Profile](drafts/presentation-control-profile.md)
- [Agent Control Core](drafts/agent-control-core.md)
- [Agent Control Profile](drafts/agent-control-profile.md)
- [Layered Agent Protocol](drafts/layered-agent-protocol.md)
- [Staged Units Graduation Plan](drafts/staged-units-graduation.md)

Protocol artifacts:

- [Illustrative protocol envelopes](examples/README.md)
- `fixtures/`: normative executable conformance traces
- `fixtures/packs/`: extension packs, loadable with `oap validate -pack`
- `schema/v0.1/`: JSON Schema bundle for the agent-control core

Executable core (Go 1.27 or later):

```sh
go run ./cmd/oap check
```

The command validates positive and negative fixtures and drives the deterministic
in-memory reference adapter. The reference adapter proves the public adapter
boundary and bounded process-memory recovery; it is not a production harness or
a durable persistence implementation.

### Validating a trace (`oap validate`)

```sh
go run ./cmd/oap validate [--format=human|json] [-mode strict|tolerant] [-pack <dir>]... <trace.json>...
```

`-mode strict` (the default) compiles the bundle exactly as published: an
unknown member, enum value, or envelope type fails. `-mode tolerant` compiles
it under the layered draft's extension rules, so a later revision's additive
field or an extension's envelope type is accepted on the common fields alone.

`-pack` loads an extension pack — a directory holding a `pack.json` descriptor
and the schemas it contributes — and is repeatable. With the pack loaded its
envelope types are validated against its own branches and its capability keys
are gated exactly as core keys are; without it the same envelopes take the
tolerant unknown path. Both flags are the caller's stated choice and neither is
inferred from the input: a live envelope saved to a file is indistinguishable
from a fixture. `fixtures/packs/storage` is a worked pack, and
[Decision 0004](decisions/0004-extension-packs.md) specifies the format.

### Run controls (`+run-controls`)

A submission may carry four per-run controls — `model_id`, `instructions`,
`tool_choice`, and `output_schema` — and each is gated on its own capability
key. The discipline is fail-closed: a control an endpoint has not
affirmatively advertised is refused *before* admission with
`unsupported_feature` naming the key, a `degraded` one needs the caller's
`allow_degraded_features` opt-in, and one whose value cannot be honoured is
refused as `unsatisfiable`. Nothing is accepted and ignored, and a refusal
always says what to change. A request failing more than one of these gets one
refusal, ranked capability → degradation → unsatisfiability → state.

Execution is claimed per control, only for the keys an endpoint advertises
above `unavailable`, so an endpoint that supports none of the four still
claims the unit by refusing all four correctly.
[Decision 0005](decisions/0005-run-controls.md) graduates the discipline and
`model_id`: Codex and Makai apply it natively per run
(`run.model_selection`, mode `per_run`), which means the model binds that run
alone and `current_model_id` — the model the next control-free submission
would use — does not move. The reference adapter executes all four.

### Tool sources (`+tool-sources`)

A tool catalog says where each of its tools comes from. `action.tools.list.response`
carries `sources` — `{ id, kind, display_name?, protocol?, endpoint? }` — and
each `ToolDefinition` carries the `source` id it belongs to, so a consumer
attributes a call to an MCP server without parsing a namespaced name, and
`action.call.*` may carry the same id. A source `id` is unique across a
session's catalog and a tool `name` is unique whatever its source. The catalog
is gated on `action.tools.list` and nothing else: `action.tools` means
lifecycle observation, and several adapters observe tool calls while publishing
no portable catalog at all.

A session may attach sources for its lifetime:
`session.open.request.tool_sources` carries `ToolSourceAttachment` values —
the descriptor's members plus, for a `process` source, `command`, `args`, and
`environment`. The attachment shape is deliberately not the catalog shape,
because an attachment's environment can hold a credential: a published source
carries none of those three members, and an endpoint that reflected one back
into its catalog is a schema rejection rather than a convention. The endpoint
discloses what it accepts in `action.tool_sources.attach` — `modes` (always
`session_open`, plus `remote` where offered) and `limits` (`max_sources`, the
`transports` it takes) — so a refusal is checkable in both directions. The
modes are a set, following `run.tool_selection`, so disclosing `remote` never
erases the session-open one.

**The daemon does not take a command from the wire.** "Loopback, single-user"
describes the transport, not the origin of a request on it: a page in the
user's browser can reach `127.0.0.1` with a valid envelope. On
`POST /adapters/{name}/sessions` a `process` attachment therefore names an
operator-configured source by `id` only — from the registry document's
`tool_sources` map — and the daemon fills `command`, `args`, and `environment`
from its own entry. A wire-supplied command, argument list, or literal
`NAME=value` environment value is refused before the open is forwarded; the
bare-`NAME` allowlist form is the only `environment` a wire caller may write.
Beside that, every route refuses a request bearing an `Origin` header, and the
routes that read a request body also require `Content-Type: application/json`.
The origin refusal wraps the whole mux rather than living in the routes that
parse an envelope, so it covers the ones that read no body — `close` — and the
ones not yet written.

[Decision 0008](decisions/0008-tool-sources.md) graduates the unit: Claude Code
serves the catalog at `degraded` from its per-turn `system/init` frame, and ACP
attaches at `native` onto `session/new`'s `mcpServers`. Control-layer-provided
tools are the separate `+control-tools` unit and are not graduated by it.

### Local daemon (`oap serve`)

`oap serve` exposes the adapter registry over HTTP + Server-Sent Events so any
client — not only Go hosts — can drive any OAP adapter. The daemon is a thin
HTTP+SSE codec (`serve/servehttp`) over the embeddable `serve` package; its
wire behavior is the contract the `client` package proves:

```sh
oap serve [--config examples/oap-serve.json] [--addr 127.0.0.1:6270]
```

Without `--config` the daemon serves the built-in memory reference adapter
only. The registry document maps names to in-repo adapter configurations
(`examples/oap-serve.json` shows every entry type); constructor requirements
(absolute working directories, provider settings) surface at startup. The
`environment` list of an entry is an explicit allowlist: a bare `NAME`
forwards the value the daemon itself carries (unset names are omitted) and
`NAME=value` passes through literally — ambient credentials are never
inherited by a child process unless their variable was listed.

The document's `tool_sources` map is the same allowlist idea for the MCP
sources a client may attach at session open: each entry names a `kind`, the
descriptor members the catalog publishes, and the `command`, `args`, and
`environment` the daemon supplies on the client's behalf. Its `environment`
takes the same form with one rule of its own: a bare `NAME` the daemon does not
carry fails at startup, naming the source and the variable, rather than being
omitted the way an adapter's is. An adapter's list forwards whatever of a
harness's variables the daemon happens to have; a tool source's names the
credentials of one executable the daemon itself launches, and dropping one
starts that MCP server without its token to fail later as though the server
were broken. Write `NAME=` if a name is meant to be optional. (The example
config lists `MCP_TOKEN` for its filesystem source, so export it or drop the
entry before starting with that document.) A client attaches one by `id` and
nothing else — see "Tool sources" above for why the wire form is refused on
that route.

The daemon binds `127.0.0.1` by default and has no authentication: v0 is a
single-user local service, and pointing it at an external interface is
explicitly unsupported. On a loopback bind the daemon serves only requests
whose `Host` header names a loopback host, which closes the browser-borne
cross-origin and DNS-rebinding vectors against an unauthenticated local
service; binding a non-loopback `--addr` deliberately opts out of the
single-user trust model. Restarts kill every session — run child processes
are per-session and no adapter here survives a daemon restart — and no session
state persists across restarts. Session entries accumulate for the daemon's
lifetime (closed sessions stay listed with their final state); there is no
eviction in v0.

OAP operations exchange verbatim schema/v0.1 envelopes (rejected input gets a
correlated `error.response`; `GET /capabilities` responses cite a
daemon-minted correlation id a client can pair with its own request envelope):

| Endpoint | Operation |
| --- | --- |
| `POST /adapters/{name}/sessions` | `session.open.request` → `session.open.response` |
| `GET /adapters/{name}/capabilities` | `capabilities.response` (probed descriptor) |
| `POST /sessions/{id}/submit` | `session.message.submit.request` → admission response |
| `GET /sessions/{id}/events` | SSE stream of run-event envelopes |
| `POST /sessions/{id}/resolve` | `action.permission.resolve.request` or `user.input.resolve.request` → response |
| `POST /sessions/{id}/cancel` | `run.cancel.request` → `run.cancel.response` |
| `GET /sessions/{id}/state` | `session.state.response` |
| `POST /sessions/{id}/close` | Close (v0.1 defines no close envelope; returns 204) |
| `GET /adapters`, `GET /sessions` | daemon-management listings, plain JSON |

The daemon acts as participant `user`: interactive gates opened over a
session resolve with `responded_by: "user"`.

`GET /sessions/{id}/events` streams envelopes with `data:` carrying the
envelope JSON and `id:` carrying the envelope sequence, so an SSE
Last-Event-ID reconnect (or an explicit `?after=` cursor) maps directly onto
`Resume.AfterSequence` for the session's current run: the daemon drives the
adapter `Resume` and streams the replayed suffix, then live events, and ends
the stream at the run's terminal event. Two terminal signals are transport
framing, not envelopes: `event: oap-overflow` reports that the connection's
bounded buffer fell behind (`last_sequence` names the last sequence this
connection delivered; reconnect with a cursor after it), and
`event: oap-replay-gap` reports `adapter.ReplayGap` — the requested cursor is
no longer retained (`oldest_available`/`latest_available` bound what is;
reconnect with a cursor at or after `oldest_available - 1`). A stream also
ends when the client closes the connection; a stream that is open when the
session closes receives the events already in flight and then ends, and a
connection made to an already-closed session is refused with
`409 session_closed` rather than parking. Sequence numbers are per-run: a
connection that happens to span an immediate resubmit (a second run admitted
inside the settle window of the first) continues into the new run, and
clients keying on the envelope `run_id` see each run's own sequence space.

On SIGINT/SIGTERM the daemon stops accepting, terminates in-flight streams,
and closes every session inside a bounded window — active runs that refuse
Close are cancelled first — so child agent processes are settled rather than
orphaned.

### Embedding the registry (`serve`)

The `serve` package is the transport-neutral core the daemon is built on: a
Go host that wants the full registry semantics in process — several adapters,
many concurrent sessions, per-session subscriptions with cursor replay —
embeds it directly with no HTTP hop. `serve/servehttp` (the daemon) is one
codec over this same hub.

```go
registry, _ := serve.DefaultRegistry()           // or serve.LoadRegistry(path, os.LookupEnv)
hub := serve.New(registry, serve.Options{})

session, _, _ := hub.Open(ctx, "memory", adapter.OpenRequest{SessionID: "demo"})
sub, _ := hub.Subscribe(ctx, session.ID())        // subscribe before submitting
session.Submit(ctx, protocol.MessageSubmitRequest{
	SessionID: session.ID(), Delivery: protocol.DeliveryAuto,
	Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}},
})
for {
	envelope, err := sub.Next()
	if err == io.EOF {
		break // the run reached its terminal event
	}
	if err != nil {
		break // fell behind: resume per the error's cursor
	}
	// resolve gates and collect content as events arrive
}
```

Subscriptions are the daemon's SSE connections without the socket: any number
per session, bounded per-subscription buffers, and the same typed terminal
signals — `*serve.OverflowError` carries the resume cursor (re-subscribe with
`serve.After(err.RunID, err.LastSequence)`), and an expired cursor fails `Subscribe` with
`*adapter.ReplayGap` naming what is still retained. `hub.Sessions(ctx)` lists
every session with its adapter and lifecycle status (closed entries stay
listed with their final state), and `hub.CloseSessions(ctx)` settles every
session for shutdown — in-process hosts own their lifetime, since the
restart-kills-sessions property of the daemon is a process boundary, not a
library one. The hub adds no protocol semantics of its own and never
validates envelopes; hosts forwarding untrusted input validate at their own
boundary, exactly as `servehttp` does against the bundled schema.

Pick the tier that fits: `adapter.Session` directly for one embedded session
(a single run stream with a single consumer, replay through `Resume`);
`serve` for multi-adapter, multi-session hosts in process; `oap serve` plus
`client` for out-of-process or non-Go consumers over HTTP + SSE.

### Go client (`client`)

The `client` package is the far-side conformance proof for that wire: a public
Go client that drives `oap serve` over HTTP + SSE, and the template later
clients (TypeScript) copy. It speaks verbatim schema/v0.1 envelopes, consumes
the event stream through a real `text/event-stream` parser, and resolves
interactive gates as participant `user` by default:

```go
c := client.New("127.0.0.1:6270")

session, _ := c.Open(ctx, "memory", "demo")
stream := session.Events(ctx)          // subscribe before submitting
session.Submit(ctx, protocol.MessageSubmitRequest{
	Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}},
	Delivery: protocol.DeliveryAuto,
})
for {
	envelope, err := stream.Next()
	if err == io.EOF {
		break // the run reached its terminal event
	}
	// resolve action.permission.requested / user.input.requested as they
	// arrive, or collect content.delta / run.completed envelopes
}
```

`Session.Events` resumes invisibly: when the SSE connection drops, the client
reconnects with the last observed sequence as the `Last-Event-ID` / `?after=`
cursor, the daemon replays the suffix, and the stream continues with no
duplicates — a repeated or skipped sequence within a run is surfaced as an
error, never silently accepted. `WithStrictResume` turns resume off and reports
the drop as a `DisconnectError` carrying the cursor. The daemon's terminal
signal events
surface as typed errors: `OverflowError` (reconnect with
`Session.EventsAfter(RunID, LastSequence)`) and `ReplayGapError` (the requested cursor
expired; `OldestAvailable`/`LatestAvailable` bound what is retained). Daemon
refusals arrive as `ServerError` with the correlated `error.response` code.

`WithHTTPClient` injects the underlying transport, `WithParticipant` overrides
the responder identity, and `WithEnvelopeValidation` turns on dev-mode
validation of every inbound envelope against the bundled schema (off by
default). The client logs nothing. See `example_test.go` for the canonical
lifecycle and the package documentation for the full surface.

### TypeScript client (`clients/ts`)

The TypeScript client mirrors the Go one — same wire surface, same typed
errors, same invisible resume — for non-Go hosts, with a zero-dependency
runtime on platform fetch and streams (Node 18+ baseline):

```ts
const client = dial('127.0.0.1:6270');
const session = await client.open('memory', { sessionId: 'demo' });
const events = session.events();          // subscribe before submitting
await events.ready;
await session.submit({ messages: [{ role: 'user', content: 'hi' }], delivery: 'auto' });
for await (const envelope of events) {
  // resolve gates, collect deltas, end at run.completed
}
```

See `clients/ts/README.md` for the full surface. npm publishing is
intentionally out of scope; the package is consumed from a checkout.

Research:

- [Harness interoperability study](research/harness-interoperability.md)
- [P0 protocol gaps from harness interoperability](research/p0-protocol-gaps.md)
- [Pinned Codex app-server mapping](research/codex-app-server-8d7cc24-mapping.md)
- [Pinned ACP v1 and Devin Desktop mapping](research/acp-v1.7.0-mapping.md)
- [Pinned Makai agent-protocol mapping](research/makai-agent-67ad514-mapping.md)
- [Pinned Pi coding-agent mapping](research/pi-v0.85.1-mapping.md)
- [Pinned DeepSeek Harness mapping](research/deepseek-harness-47f9438-mapping.md)
- [Pinned Hermes agent mapping](research/hermes-v2026.8.31-mapping.md)
- [Pinned Claude Code CLI and Agent SDK mapping](research/claude-code-agent-sdk-2.1.263-mapping.md)
- [Z.ai China Coding Plan evidence matrix](research/zai-china-coding-plan-evidence.md)
- [Protocol feedback from eight adapter tranches](research/protocol-feedback-2026-09.md)

Decisions:

- [0001 — agent-control v0.1 executable core](decisions/0001-agent-control-v0.1-executable-core.md)
- [0002 — admission before started](decisions/0002-admission-before-start.md)
- [0003 — graduating staged control units](decisions/0003-staged-unit-graduation.md) (proposed)
- [0005 — run controls](decisions/0005-run-controls.md) (proposed)
- [0008 — tool sources](decisions/0008-tool-sources.md) (proposed)

Provider compatibility is tested independently from harness conformance. Inspect
the credential-free China Coding Plan presets with:

```sh
go run ./cmd/oap providers zai-cn
```

Ordinary tests use fake credentials and loopback provider servers. Credentialed
provider evidence is separately and explicitly gated as documented in the matrix;
credential presence alone never enables network traffic.

Pi real-process checks are also explicitly opt-in and skipped by ordinary CI.
Provide an absolute Pi v0.85.1 executable in `OAP_PI_BIN`, then set
`OAP_PI_SMOKE=1` for the credential-free readiness check or
`OAP_PI_INTEGRATION=1` for the loopback-provider path. The executable's reported
semver is runtime-version evidence only; it does not prove the source commit.
Set `OAP_PI_SHA256` to the expected 64-character artifact digest when exact
artifact provenance is required. The gate never downloads an executable and
passes no ambient credentials to it.

DeepSeek Harness real-process checks follow the same opt-in gate. Provide an
absolute runtime built from the pinned source commit in
`OAP_DEEPSEEK_HARNESS_BIN` — the build emits
`deepseek-harness-sdk-runtime-linux-x64` from the pinned release — then set
`OAP_DEEPSEEK_HARNESS_SMOKE=1` for the credential-free initialize/shutdown
check or `OAP_DEEPSEEK_HARNESS_INTEGRATION=1` for the loopback-provider path.
The gates never download a runtime and pass no ambient credentials. The
runtime boots the shipped `sdk` profile (`--profile sdk`) against an isolated
`DSH_HOME`; the loopback gate redirects the stock deepseek provider with
`DEEPSEEK_BASE_URL`. The wire `serverInfo` version and any release text are
runtime-version evidence only; the pinned source commit and tree in the
mapping ledger remain the provenance. Set `OAP_DEEPSEEK_HARNESS_SHA256` to the
expected 64-character artifact digest when exact artifact provenance is
required.

Hermes agent real-process checks follow the same opt-in gate. Provide an
absolute python interpreter in `OAP_HERMES_BIN` (able to import the pinned
checkout's dependencies) and the pinned hermes-agent v2026.8.31 checkout in
`OAP_HERMES_ROOT` (the gateway's cwd), then set `OAP_HERMES_SMOKE=1` for the
credential-free ready/session.create/EOF-teardown check or
`OAP_HERMES_INTEGRATION=1` for the loopback-provider path (streaming OpenAI
chat completions against an in-process mock, test-owned key only). The gates
never download anything and pass no ambient credentials; teardown evidence is
stdin EOF, matching the pinned gateway, which has no shutdown RPC. Set
`OAP_HERMES_SHA256` to the expected 64-character interpreter digest when exact
artifact provenance is required.

Claude Code real-process checks follow the same opt-in gate. Provide an
absolute path to the pinned claude 2.1.263 binary in `OAP_CLAUDE_BIN`, then
set `OAP_CLAUDE_SMOKE=1` for the credential-free spawn/initialize/EOF-teardown
check or `OAP_CLAUDE_INTEGRATION=1` for the loopback-provider path (streaming
Anthropic Messages against an in-process mock, test-owned key only;
structural request assertions only, per the mapping pin). The gates never
download anything and pass no ambient credentials; readiness is the
initialize control exchange, and teardown evidence is stdin EOF. Set
`OAP_CLAUDE_SHA256` to the expected 64-character binary digest when exact
artifact provenance is required.

ACP real-process checks follow the same opt-in gate, driven against an
independent open-source ACP agent rather than a Devin product. Provide an
absolute `docker-agent` binary built from the pinned docker/cagent release
(Apache-2.0, tag `v1.138.0`) in `OAP_ACP_BIN`, then set `OAP_ACP_SMOKE=1` for
the credential-free `initialize`/`session/new`/teardown check or
`OAP_ACP_INTEGRATION=1` for one prompt through the production adapter against
an in-process loopback chat-completions mock. The generated agent file points
`base_url` at the loopback endpoint; the checked-in real-provider examples are
never reused. Building cagent needs Go 1.27. Set `OAP_ACP_SHA256` to the
expected 64-character artifact digest when exact artifact provenance is
required. Devin CLI also speaks ACP (`devin acp`) but is proprietary and
prebuilt-only, so it cannot be pinned as evidence.

### Real-process gate coverage

CI runs `gofmt`, `go vet`, the full and race suites, and `oap check` on every
push and pull request. The real-process gates above are **not** run in CI:
they need pinned third-party binaries, are skip-by-default, and require an
explicit opt-in variable plus an absolute binary path (and optionally a
64-hex digest). Credential presence alone never enables them, and they never
download anything.

| Adapter | Gate variables | CI |
| --- | --- | --- |
| Codex app-server | `OAP_CODEX_INTEGRATION`, `_BIN`, `_COMMIT` | skipped |
| Makai | `OAP_MAKAI_INTEGRATION`, `_BIN`, `_COMMIT`, `_SHA256` | skipped |
| OpenCode | `OAP_OPENCODE_INTEGRATION`, `_BIN` | skipped |
| pi | `OAP_PI_SMOKE` / `OAP_PI_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| DeepSeek Harness | `OAP_DEEPSEEK_HARNESS_SMOKE` / `_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| Hermes | `OAP_HERMES_SMOKE` / `OAP_HERMES_INTEGRATION`, `_BIN`, `_ROOT`, `_SHA256` | skipped |
| Claude Code | `OAP_CLAUDE_SMOKE` / `OAP_CLAUDE_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| ACP (docker/cagent) | `OAP_ACP_SMOKE` / `OAP_ACP_INTEGRATION`, `_BIN`, `_SHA256` | skipped |

Each adapter additionally has a hermetic corpus that runs in CI: it decodes
sanitized native frames through the production codec and drives the
production reducer, so codec and reducer regressions are caught without any
external process.

The repository is dedicated under CC0-1.0 so any presentation layer, control
layer, agent loop, model provider, tool executor, resource provider, tool
source, or SDK adapter can implement the protocol without project-specific
licensing friction.
