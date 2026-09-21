# ACP v1 mapping ledger

Status: pinned evidence boundary for the second production OAP adapter.

## Provenance and version boundary

- Repository: `https://github.com/agentclientprotocol/agent-client-protocol`
- Stable release: `v1.7.0`
- Schema release: `schema-v1.21.0`
- Commit: `272bf799f35a258c6a4107a0410ed361e83683d3`
- Commit date: 2026-08-20
- Stable specification tree SHA-256:
  `a23fba5f3ec62d4aeddec67f183a781e68acc89bc87dac8ffb98a440d20e1995`
- Stable generated-schema tree SHA-256:
  `135854daf9d2b934c9498771d8084d3bf3174c6cf1db29b9d1a3ad1ec8a8dff3`
- Specification hash recipe:
  `find schema/v1 agent-client-protocol-schema/src/v1 docs/protocol/v1 -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum`
- Schema hash recipe:
  `find schema/v1 -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum`
- Wire registry: `schema/v1/meta.json`
- Wire schema: `schema/v1/schema.json`
- Reduced source types: `agent-client-protocol-schema/src/v1/`
- Stable prose: `docs/protocol/v1/`

The production target is stable **ACP wire version 1**. The repository also
contains ACP v2 alpha material. ACP v2 was announced as a draft on 2026-07-20
and changes prompt admission, completion, replay, tools, and reverse client
operations. It is not part of this mapping and must not be blended into a v1
connection. A future v2 binding requires separate negotiation, types, fixtures,
and a new ledger.

The release commit is preferred over a moving `main` snapshot. The inspected
2026-09-07 snapshots `4e9052fa90b1f72821a88c6079ac74d45a62407d`
and `367bcb1b2120b5a871f7b03a41fa7cabb3cf7e24` add later repository and draft
work. Between the release and `4e9052fa`, changes within the hashed boundary are
limited to an unstable schema, draft documentation, and generated source for
feature-gated additions. The stable release remains the reproducible production
contract.

## Devin Desktop evidence boundary

Official Cognition documentation inspected on 2026-09-07:

- `https://docs.devin.ai/desktop/acp`
  - sitemap `lastmod`: 2026-07-30T01:21:33.753Z
- `https://docs.devin.ai/desktop/acp-custom`
  - sitemap `lastmod`: 2026-06-01T04:37:17.705Z

Those pages establish that Devin Desktop:

- is the ACP client and launches an installed agent as a local subprocess;
- exchanges JSON-RPC over the subprocess stdin/stdout;
- requires a custom agent to implement `initialize`, `session/new`,
  `session/prompt`, and `session/cancel`;
- consumes assistant-message, plan, tool-call, and tool-call-update session
  updates and supports `session/request_permission`;
- passes the selected workspace and configured MCP servers to `session/new`;
- does **not** advertise ACP terminal capabilities;
- does **not** expose legacy ACP Session Modes in its UI and recommends session
  configuration options with category `mode`;
- discovers local entries at `~/.windsurf/acp/registry.json` and Desktop Next
  entries at `~/.windsurf-next/acp/registry.json`;
- currently expects binaries to be installed rather than downloading registry
  archives.

The official ACP registry was inspected at
`aafb17a3b5dd1bef54cfcc606cf2c0e303202fe9` (tag
`v2026.09.07-aafb17a`, 2026-09-07). Its `devin/agent.json` entry identifies
Devin CLI `3000.6.14` and launches it as `devin acp`. This is evidence for the
registry format and Devin CLI as an ACP **agent**, not source code or a wire
trace of Devin Desktop as the ACP **client**.

Devin Desktop is proprietary, and the Devin CLI it launches (`devin acp`) is
separately licensed `"license": "proprietary"` in the official registry; see
"Process-gate status" below for why that rules it out as gate evidence. Its
exact initialize payload, filesystem
capabilities, authentication behavior, process reuse, timeouts, stderr policy,
load/resume support, permission persistence, and model-option UI are unknown.
Third-party reverse engineering and probes are useful compatibility candidates,
but cannot upgrade those unknowns into official claims. The initial adapter
must branch on negotiated capabilities and target only the official minimum.

## Wire and process boundary

Stable ACP stdio is UTF-8, newline-delimited JSON-RPC 2.0:

1. the client launches the agent;
2. client requests and notifications go to agent stdin;
3. agent requests, notifications, and responses go to agent stdout;
4. each complete JSON value is followed by one newline;
5. a frame contains no literal embedded newline;
6. stdout and stdin contain no non-ACP data;
7. diagnostics may use stderr;
8. shutdown closes stdin and terminates the process.

The adapter needs one incremental bounded reader, one serialized write pump, a
pending outbound-request table, concurrent reverse-request handling, separately
drained and redacted stderr, and deterministic process settlement. Numeric and
string JSON-RPC IDs are correlation values only. EOF or process exit before an
admitted run settles is failure, never success.

The ACP specification does not define discovery, command arguments, launch cwd,
environment inheritance, frame-size limits, retries, restart policy, timeouts,
backpressure, signals, or process-tree cleanup. Those remain binding policy.
Session `cwd` is an absolute protocol value and remains the base for relative
session paths regardless of process launch cwd.

## Handshake and capabilities

`initialize` must precede session creation. The client sends its newest
supported integer major `protocolVersion`, `clientCapabilities`, and preferably
`clientInfo`. The agent returns the selected version, `agentCapabilities`,
optional `authMethods`, and preferably `agentInfo`.

If the requested major is supported, the agent returns it. Otherwise it returns
its newest supported major; a client unable to support that response should
close. Omitted capability fields mean unsupported. A method or content type
whose capability is absent must not be used.

ACP capabilities are directional structural declarations, not OAP effective
support records. They have no revision, support level, model/session scope, or
stale-revision fencing. The adapter therefore synthesizes an OAP descriptor from
both negotiated ACP sides plus adapter policy. Reinitialization or a material
configuration change creates a new OAP capability revision.

Implementation names are diagnostics, not endpoint, participant, or
authorization identities.

## Stable method surface

Client to agent requests:

- `initialize`
- `authenticate`
- `session/new`
- `session/load` (capability-gated)
- `session/set_mode`
- `session/set_config_option`
- `session/prompt`
- `session/list` (capability-gated)
- `session/delete` (capability-gated)
- `session/resume` (capability-gated)
- `session/close` (capability-gated)
- `logout` (capability-gated)

Client to agent notifications:

- `session/cancel`

Agent to client requests:

- `session/request_permission`
- `fs/read_text_file` and `fs/write_text_file` (independently gated)
- `terminal/create`, `terminal/output`, `terminal/wait_for_exit`,
  `terminal/kill`, and `terminal/release` (group-gated)
- capability-gated elicitation methods

Agent to client notifications:

- `session/update`
- capability-gated elicitation completion

Protocol-level notification:

- `$/cancel_request`

Custom methods start with `_`. Unknown custom notifications are ignored;
unknown custom requests receive `-32601`. Custom fields belong under `_meta`;
unknown metadata must not affect correctness.

## Identity domains

| ACP identity | OAP identity | Rule |
|---|---|---|
| JSON-RPC request `id` | none | Private request/response correlation; never a run or interaction identity. |
| ACP connection | `endpoint_id` and participants | Adapter allocates stable typed identities for its lifetime/persistence boundary. |
| `sessionId` | `session_id` | Maintain a typed mapping; namespace by endpoint. |
| outstanding `session/prompt` | `submission_id`, `run_id` | Allocate one pair per admitted prompt. Enforce one active prompt per ACP session. |
| optional `messageId` | `message_id` | Preserve through a typed mapping; synthesize when absent. |
| `(sessionId, toolCallId)` | `tool_call_id` | ACP tool IDs are unique only within a session. |
| permission request `id` | `interaction_id` | Generate a portable ID; retain native request ID privately until one response. |
| permission `optionId` | choice ID | Preserve opaque value and separately retain kind/name. |
| `terminalId` | private client resource | Do not confuse with an OAP run terminal. |
| list cursor | private pagination token | Opaque; never parse or use as recovery cursor. |

ACP supplies no endpoint ID, participant principal, submission/run/turn ID,
event ID, sequence, idempotency key, or replay cursor. It does not correlate a
`session/update` with a specific prompt. The minimum safe adapter therefore
allows only one outstanding prompt per ACP session and allocates contiguous OAP
run sequence numbers in native receive order.

## Lifecycle mapping

Fidelity values are `native`, `normalized`, `synthesized`, `lossy`, and
`unsupported`. Fixture names are requirements, not claims that captures already
exist.

| ACP observation | OAP meaning | Fidelity | Initial claim | Required fixture |
|---|---|---|---|---|
| successful initialize exchange | readiness and effective descriptor | normalized | capabilities: emulated | `initialize-minimal` |
| `session/new` response | `session.open.response` | normalized | new session: native | `new-prompt-completed` |
| `session/new` `mcpServers` entry | `session.open.request.tool_sources` attachment, published back as a `ToolSourceDescriptor` | native | `action.tool_sources.attach`: native, `mode: session_open`, `limits.transports: ["process"]` | `session-new-tool-sources` |
| `session/load` replay then response | transcript reconstruction, never event replay | lossy | capability-dependent/degraded | `load-history-not-replay` |
| `session/resume` response | native attachment without replay | normalized | capability-dependent/degraded | `resume-no-replay` |
| prompt accepted by adapter and written | submit admission | synthesized | admission: emulated | `prompt-admitted` |
| prompt execution begins | `run.started` | synthesized | run start: emulated | `new-prompt-completed` |
| `agent_message_chunk` | `content.delta` | normalized | text stream: native once exercised | `new-prompt-completed` |
| `agent_thought_chunk` | reasoning delta only when policy/profile permits | lossy/optional | unavailable by default | `thought-degraded` |
| `tool_call` | `action.call.requested`, possibly started if status proves it | normalized | tools: degraded until exercised | `tool-completed` |
| `tool_call_update` | action start/progress/terminal patch | normalized | tools: degraded until exercised | `tool-completed` |
| permission reverse request | permission interaction | normalized | permissions: native semantics, synthesized ownership | `permission-allow-deny` |
| `plan` | namespaced complete plan snapshot | lossy/extension | no core claim | `plan-replacement` |
| `usage_update` | retained usage, normally folded into terminal/state | normalized | no live core claim | `usage-fold` |
| `config_option_update` | complete session config replacement | normalized | optional session metadata | `config-replacement` |
| prompt result `end_turn` | exactly one `run.completed` | normalized | terminal normalization | `new-prompt-completed` |
| result `max_tokens` | `run.completed`, preserving truncation reason | normalized | terminal normalization | `max-tokens` |
| result `max_turn_requests` | `run.completed`, preserving reason | normalized | terminal normalization | `max-turn-requests` |
| result `refusal` | `run.failed` under the existing OAP v0.1 refusal convention | normalized/policy | terminal normalization | `refusal` |
| result `cancelled` | `run.cancelled` after children settle | normalized | cancellation: degraded | `cancel-confirmed` |
| prompt JSON-RPC error | `run.failed`, except confirmed cancellation handling | normalized | typed failure | `prompt-error` |
| EOF/process exit before result | one typed `run.failed` | synthesized | transport failure handling | `process-exit` |

The v1 `session/prompt` request remains pending for the whole turn. Its response
is the authoritative semantic terminal boundary. It is not merely admission.
All session updates belonging to that turn precede the response. The adapter
separately synthesizes OAP admission and `run.started` after it has reserved the
session and safely written the request.

One terminal arbiter owns parent terminal emission. Exposed tools and
interactions settle before it. Duplicate responses, updates after the prompt
response, and late transport failures cannot create another terminal.

## Session state, load, resume, and replay

`session/new` requires absolute `cwd` and an MCP server array. ACP agents must
support stdio MCP descriptors; HTTP/SSE descriptors are capability-gated.
Additional directories are optional, absolute, and supplied as the complete
intended set on each load/resume.

`session/load` is gated by top-level `loadSession`. It restores an existing
session and replays the entire conversation through `session/update` before its
response. `session/resume` is separately gated and restores without replay.
`session/close`, list, and delete have distinct optional semantics.

Neither operation supplies OAP canonical event replay:

- load is transcript reconstruction without original run IDs, sequence numbers,
  full child lifecycles, or replay-gap proof;
- resume is attachment with no history;
- list cursors paginate sessions and are not event cursors.

OAP state reconciliation is adapter-owned. OAP event replay is unavailable
unless the adapter journals its canonical output. Process-memory journaling is
then degraded and must declare gaps; cross-process replay cannot be claimed
without durable adapter state.

## Cancellation and races

ACP v1 has two distinct cancellation mechanisms.

`session/cancel` is a session-scoped notification. It has no response. The
client must cancel pending permission requests, should mark unfinished tools
cancelled in presentation state, and continues accepting final updates. The
agent should stop work and must eventually return the original prompt with
`stopReason: cancelled`.

`$/cancel_request` is optional, request-ID-scoped generic cancellation. A
supporting peer still sends exactly one response to the original request: a
valid result or error `-32800`.

OAP policy:

1. one active prompt per ACP session makes run-targeted cancellation unambiguous;
2. OAP cancel acceptance means the adapter accepted and forwarded intent, not
   settlement;
3. settlement waits for the prompt response;
4. final updates remain valid before that response;
5. natural completion or failure wins if it settles first;
6. a confirmed post-cancel `-32800` may normalize to cancelled, but arbitrary
   errors do not;
7. pending permission interactions and actions settle before `run.cancelled`;
8. stale cancellation never targets a later prompt;
9. EOF, process exit, or timeout does not prove cancellation or success.

ACP has no stable `cancelled` tool status. When parent cancellation is confirmed,
the adapter must synthesize OAP child cancellation for any exposed unfinished
action before terminalizing the run.

## Tool and permission mapping

`tool_call` creates a presentation entity with required session-unique ID and
title. Status defaults to pending; stable statuses are `pending`, `in_progress`,
`completed`, and `failed`. `tool_call_update` is a sparse patch. Present
collections replace previous collections; they do not append.

| ACP state | OAP action state |
|---|---|
| first tool call | requested |
| proven `in_progress` | started |
| intermediate content/raw output/location changes | progress |
| `completed` | completed |
| `failed` | failed |
| unfinished when prompt cancellation is confirmed | adapter-synthesized cancelled |

Raw input becomes `arguments_json` only when its shape is usable. Raw output,
content blocks, diffs, locations, and terminal references require bounded,
namespaced projection where OAP has no exact core field. ACP tool reporting does
not prove a discoverable tool catalog or execution owner, so `action.tools.list`
is unavailable unless separate evidence exists.

`session/request_permission` carries a tool update and ordered options. Each
option has opaque `optionId`, display name, and one of `allow_once`,
`allow_always`, `reject_once`, or `reject_always`. The response is selected with
an exact option ID or cancelled.

The adapter generates the OAP interaction identity and participant ownership,
preserves the chosen native ID, and derives `granted` only from the selected
option kind. It must not infer a durable authorization scope from “always”; ACP
does not define its lifetime. Permission display is not proof of sandboxing or
enforcement by Devin Desktop.

## Plans, modes, models, and configuration

Stable `plan` updates are complete snapshots. Entries have content, priority,
and status but no stable entry ID. OAP v0.1 has no core plan event. Preserve the
snapshot only under a negotiated extension or bounded diagnostics; do not turn
entries into tools or fabricate identity by matching text or array position.

Legacy ACP modes remain in v1 through `session/set_mode` and
`current_mode_update`, but config options supersede them. Config-aware clients
prefer config options when both exist. Devin Desktop officially says its UI does
not support the legacy Session Modes surface.

Session config options are complete ordered state. Select is baseline; boolean
options require a client capability. Categories such as `mode`, `model`,
`model_config`, and `thought_level` are UI hints and cannot be required for
correctness. `session/set_config_option` and `config_option_update` carry the
complete resulting list because options may depend on one another.

ACP model selection is session configuration, not a model catalog or an atomic
per-prompt field. Mapping OAP `model_id` requires serializing and confirming a
session config mutation before prompt admission. It is degraded/emulated, not
native. Concurrent prompts or concurrent model changes make attribution unsafe.
A `model` option does not prove OAP `models.list`, provider metadata, context
limits, pricing, or per-run selection.

## Reverse filesystem and terminal calls

ACP filesystem and terminal calls run from agent to client, opposite the normal
client-to-agent lifecycle. They do not automatically become OAP tool entries.
Their execution ownership must remain explicit and one high-level ACP tool must
not be double-counted with its lower-level client callback.

Filesystem calls are independently capability-gated. Reads can reflect unsaved
editor state; writes replace full text and create absent files. Paths are
absolute and line numbers are one-based. A future implementation needs explicit
workspace/path and write authorization policy.

Terminal support gates all terminal calls. Create returns an ID while execution
continues; output is polled and may be prefix-truncated at valid character
boundaries; kill retains the terminal; release kills if necessary and
invalidates it. ACP defines no push output notification, PTY contract, shell
parsing, stdin interaction, environment policy, or authorization model.

The initial OAP-to-ACP client binding advertises neither filesystem nor terminal
callbacks. This is also the only honest documented Devin Desktop profile for
terminal support: Desktop explicitly advertises none. Adding either surface
requires separate fixtures, security policy, and execution ownership.

## Error and unknown-observation policy

Stable predefined ACP error codes are:

- `-32700` parse error;
- `-32600` invalid request;
- `-32601` method not found;
- `-32602` invalid params;
- `-32603` internal error;
- `-32800` request cancelled;
- `-32000` authentication required;
- `-32002` resource not found.

Other integer codes remain representable. Prompt stop reasons and process exit
statuses are separate domains from JSON-RPC errors.

Every native frame is classified as one of:

1. mapped into portable state;
2. observed-only bounded diagnostic;
3. required-unmapped and therefore fatal to the affected run/connection;
4. unsupported reverse request receiving a deterministic native error;
5. ignorable unknown custom notification under ACP extension rules.

Lifecycle-significant unknowns are never silently discarded or guessed onto an
active run. Native codes/messages may be retained in redacted, namespaced error
metadata; secrets, raw private content, and absolute paths are not copied into
portable diagnostics.

## P0 decisions before implementation

1. **Target v1 only.** Draft v2 needs a distinct binding and reducer.
2. **Enforce one active prompt per ACP session.** Updates have no prompt/run ID.
3. **Separate admission from settlement.** Writing a prompt supports synthesized
   OAP admission; only its response settles the run.
4. **Make ambiguous post-write cancellation transport-fatal where necessary.**
   Never release a reservation while an uncorrelated native prompt may survive.
5. **Generate typed OAP identities and sequence.** JSON-RPC IDs remain private.
6. **Use one terminal arbiter.** EOF is failure, duplicate terminals are
   suppressed, and children settle first.
7. **Treat cancel response and settlement separately.** ACP cancel is an
   unacknowledged session notification; the prompt result is authoritative.
8. **Keep load, resume, reconciliation, and replay distinct.** ACP load history
   is not event replay.
9. **Synthesize capability revisions conservatively.** Omitted ACP features are
   unavailable; config changes may revise effective OAP behavior.
10. **Do not claim a tool or model catalog.** ACP reports calls and session
    options, not authoritative catalogs.
11. **Keep reverse client facilities private initially.** No filesystem,
    terminal, or elicitation claim without execution policy and fixtures.
12. **Target documented Devin behavior only.** No terminal, no reliance on
    legacy modes, and no assumption of load, filesystem, or model UI.
13. **Fail strict transport contamination and overflow.** Serialized writes,
    bounded frames/queues, drained stderr, and explicit stream overflow are
    mandatory.
14. **Preserve evidence boundaries.** Official spec, official Devin docs,
    registry probes, third-party observations, and adapter policy remain
    separately labelled.

## Initial advertised OAP surface

- `protocol.initialize`: adapter-native/emulated boundary;
- capabilities: emulated synthetic revision;
- new session: native normalization;
- session state: emulated from adapter-owned state;
- submission/admission: emulated;
- requested `auto`, effective `start`;
- one foreground run per session;
- live text streaming: native once exercised;
- run status and contiguous sequence: emulated;
- cancellation: degraded because native intent is session-scoped and
  unacknowledged;
- tool execution/progress: degraded until mapped lifecycle fixtures pass;
- permission choices: native semantics with synthesized identities/ownership;
- session load/transcript: capability-dependent and degraded;
- event replay: unavailable unless adapter journaling is implemented, then
  degraded according to its persistence boundary;
- queue, steer, BTW, concurrent same-session runs, tool catalog, models catalog,
  native per-run model selection, client filesystem, client terminal, exact
  replay, and cross-process recovery: unavailable.

## Required executable fixture inventory

Each case contains a native ACP JSONL transcript, expected OAP envelopes,
identity map, omissions ledger, advertised capabilities, and provenance. The
materialized OAP trace must pass the existing schema and semantic validator.

Minimum positive cases:

- `initialize-minimal` and `initialize-full`;
- `session-new-minimal`;
- `session-new-tool-sources` — an open carrying `tool_sources`, the
  `mcpServers` entry the adapter wrote for it (a bare `NAME` resolved against
  the operator's own allowlist, a name the operator never exposed dropped, a
  `NAME=value` literal passed through), and the sanitized descriptor the
  session publishes back;
- `new-prompt-completed`;
- message chunks with shared, changed, and absent message IDs;
- `max-tokens`, `max-turn-requests`, and refusal;
- confirmed cancellation and completion-winning cancellation race;
- post-cancel updates before settlement;
- generic request cancellation kept distinct from prompt cancellation;
- tool requested, started, progress, completed, and failed;
- permission allow, reject, and cancelled;
- parent cancellation with open tool and permission settlement;
- full plan replacement;
- complete config replacement and dependent-option change;
- load replay classified as transcript, resume classified as no replay;
- process exit producing failure.

Minimum capability/degradation cases:

- omitted capability is unavailable;
- same native message/tool ID in different sessions remains distinct;
- adapter-generated contiguous sequence;
- model option is not a model catalog;
- effective config change revises OAP capability/config state;
- filesystem/terminal call rejected when unadvertised;
- unknown custom notification ignored;
- v2/draft or feature-gated unstable shape not accepted as stable v1.

Minimum malformed/race cases:

- non-JSON stdout and malformed JSON-RPC;
- embedded physical newline and oversized frame;
- duplicate request ID and unmatched response;
- reverse-request queue overflow;
- write interleaving prevention;
- duplicate prompt response;
- update after prompt response;
- second same-session prompt rejected before side effects;
- load response followed by replay update;
- resume that unexpectedly replays history;
- tool patch before creation or same-session ID reuse;
- unfinished tool or interaction at parent terminal;
- invalid permission option ID;
- arbitrary error after cancellation not misclassified as clean cancellation;
- EOF during prompt or cancellation;
- settlement timeout;
- stale cancel targeting a later run;
- relative path and unadvertised reverse-call violations.

## Deferred scope

This pin does not claim ACP v2, Streamable HTTP, Desktop-specific filesystem,
Desktop persistence/load, terminal callbacks, terminal authentication,
elicitation, MCP HTTP/SSE, additional directories, session list/delete/close,
durable permission policy, full arbitrary content projection, plan core
semantics, provider/model catalogs, per-prompt model atomicity, concurrent
same-session prompts, durable identity storage, cross-process journals, exact
replay, or automatic compatibility with newer ACP/Devin releases.

Each deferred feature requires new pinned evidence, explicit capability claims,
and executable native-to-OAP fixtures before implementation.

## Process-gate status (2026-09-11)

The adapter previously had **no real-process gate**: Devin Desktop is
proprietary and the adapter is an ACP *client*, so there was no
redistributable pinned ACP *agent* to spawn. That gap is now closed with an
independent open-source peer.

### Pinned agent: docker/cagent

- Repository `https://github.com/docker/cagent`, module
  `github.com/docker/docker-agent`, license Apache-2.0.
- Tag `v1.138.0`, commit `c06bb46bd00815c795f8bca4e162c4632fb7825d`, tree
  `07a063266984d5158b6ee3b8e49aa4236d6e13b7` (2026-09-10).
- Native ACP server, not a bridge: `docker-agent serve acp <agent.yaml>` over
  stdio, implemented in `pkg/acp/` on the official `coder/acp-go-sdk v0.13.5`
  — the same SDK surface the adapter targets. `acp.Run` wires
  `NewAgentSideConnection(agent, stdout, stdin)`.
- Selected because it is open source (buildable from source, unlike Devin
  CLI), Go-native, and provider-neutral via a `base_url`/`token_key` provider
  block, which makes a hermetic loopback gate possible.
- Requires Go 1.27; the repository toolchain was moved to Go 1.27 to match.

Devin CLI advertises ACP too, but the official registry entry
(`devin/agent.json`, version 3000.10.21, `args: ["acp"]`) declares
`"license": "proprietary"` with `license_url` pointing at Cognition's platform
terms, and distributes prebuilt archives only. It therefore cannot be pinned,
built, or redistributed as gate evidence.

### Findings from the first live process gate

Driving the real server through the production adapter exposed two defects that
the hermetic corpus could not, because both are wire-shape mismatches against
an independent implementation rather than reducer logic:

1. **`session/new` sent `mcpServers: null`.** ACP v1 types the field as a
   required array; the official client SDK validates `mcpServers is required`
   and the real server answered `-32602 Invalid params`. Cause: a nil Go slice
   with no `omitempty`. Fixed by always emitting the empty array; pinned by
   `TestSessionNewSendsRequiredMCPServersArray` (fails before the fix).
2. **Defined-but-non-lifecycle session updates were fatal.** The tolerated set
   enumerated 8 of the 13 stable `sessionUpdate` variants. cagent emits
   `available_commands_update` immediately after `session/prompt` is written,
   which the adapter classified as an unknown stable update and turned into
   `run.failed`. Fixed: the full defined non-lifecycle set
   (`user_message_chunk`, `agent_thought_chunk`, `plan`, `plan_update`,
   `plan_removed`, `available_commands_update`, `current_mode_update`,
   `config_option_update`, `session_info_update`, `usage_update`) is
   observed-only, while a discriminator outside the set stays fatal so a stale
   pin still fails loudly. Pinned by
   `TestDefinedNonLifecycleUpdatesAreObservedOnly` (fails before the fix).

Neither finding changes OAP core or schema; both are adapter-internal
conformance fixes to the already-pinned ACP v1 surface.

### Gate variables

Both gates are skip-by-default and never enabled by credential presence.

- `OAP_ACP_SMOKE=1` with absolute `OAP_ACP_BIN`: credential-free
  `initialize` / `session/new` / teardown against the pinned server.
- `OAP_ACP_INTEGRATION=1` with absolute `OAP_ACP_BIN`: one prompt through the
  production adapter against an in-process loopback chat-completions mock,
  asserting streamed deltas, a single `run.completed` terminal, the exact
  final response, and exactly one authorized `/v1/chat/completions` request.
- `OAP_ACP_SHA256` optionally binds the artifact to a 64-hex digest. The
  locally built artifact at the pinned commit was
  `a933e662cb257babe1c110cd4210ac483101e988d50e3f22819e131796d271b6`.

The agent file is generated per run against the loopback endpoint; the
checked-in real-provider examples are never reused, and the child environment
is fully replaced (isolated `HOME`, `TELEMETRY_ENABLED=false`, dead-loopback
proxies with loopback-only `NO_PROXY`, and only a fixed non-secret placeholder
token).

## Second-runtime port (Zig)

`zig/src/adapter/acp/` carries a second implementation of this mapping:
`rpc.zig` (line codec and JSON-RPC frame table), `session.zig` (the reducer,
with its unit tests), `corpus.zig` (the evidence driver). It exists so the
ledger can be falsified by something other than the implementation it
describes. All eleven ACP corpus cases are claimed: each case's `native.jsonl`
is replayed through the Zig reducer and compared envelope for envelope with the
Go expectation.

### What the two corpora cover, and where they differ

The Go corpus test decodes every script line into `rpc.NotificationMessage` /
`rpc.IncomingRequest` values and additionally asserts each frame's
classification against `mapping.json` and `omissions.json`. The Zig driver
reads only `native.jsonl` and `expected-oap.json`; `case.json`, `mapping.json`
and `omissions.json` are the Go side's obligation and the Zig side does not
duplicate it.

Within the frames it does read, the Zig driver decodes **every** line through
the production `rpc.parseMessage`, including the handshake responses and the
settlement frames that never reach the reducer, so a corpus line that stops
being a well-formed JSON-RPC message fails the case rather than being skipped.
What that does *not* cover is byte-level framing: the driver re-serializes the
script's `raw` object, so duplicate keys, CRLF, invalid UTF-8 and the frame
limit are exercised by `rpc.zig`'s own tests and not here. Only `session/update`
and `session/request_permission` reach the reducer; `complete`, `cancel`,
`prompt-error` and `process-exit` are control calls in both runtimes and
exercise no codec on either side.

The shared harness gained one field for this adapter. `corpus.Step` now carries
`script`, the whole parsed script line, because an ACP `permission` line states
its `choice_id` and `granted` as siblings of `action` rather than inside `raw`,
and the resolution is part of the case. This does not widen what a driver may
read on a boundary that was previously narrow: `raw` was already handed over
whole and unvalidated, and existing drivers already dig fields out of it.

### Properties the expected traces pin that no type holds

**Submission, message, run, tool-call, interaction and event ids come from one
counter.** `run-b`, `session/message-c`, `event-d` in a single turn is a
statement about allocation order across six kinds, not six sequences;
reordering two allocations rewrites every later id in the trace. Two orderings
are load-bearing and neither is visible to an assertion that reads only the
last envelope:

- `submit` mints the prompt's message ids, then the run, then the run's own
  message, ticks the clock once, ticks again for `started_at_ms`, emits
  `run.started`, and burns a submission id afterwards. That submission id never
  reaches the wire; it exists only to advance the counter, so dropping it
  shifts every later event letter by one and nothing else.
- a permission request mints its interaction id **before** it filters the
  options, so a request whose options all lack an `optionId` burns a letter
  before `acp_invalid_permission` is raised.

Both carry named tests in `zig/src/adapter/acp/session.zig`. No corpus case can
express the second: the only case with a gate has usable options.

### Where the run boundary lives

Go has no `terminal` flag to consult from the notification path. `emitRecorded`
sets `s.active = nil` at the terminal, and `handleNotification` and
`handleRequest` both read `s.active` and return when it is nil, so a frame
arriving after settlement is dropped before it can reach a reducer function.
The Zig port keeps its run slot populated after settlement — `submit` needs it
to refuse a second prompt, and `settlePrompt` needs it to be idempotent — so it
must reproduce that boundary explicitly. `active()` returns the run only while
it is nonterminal, and `observe` and `handleRequest` gate on it.

Getting this wrong is not a suppressed event, which is why it is worth naming.
The first Zig port suppressed the *emit* and let the frame through, so a late
`tool_call` still minted a tool-call id, burned an event letter and wrote the
session's tool table; the next prompt then refused that native id with
`acp_tool_id_reuse` for a call it had never legitimately seen. A trace-level
comparison cannot see any of that: the emitted envelopes were identical. The
tests therefore assert the id counter and the tool and gate tables, not the
envelope count alone.

### Guards present in Go and deliberately absent in Zig

Three Go guards have no reachable mutation in the Zig reducer. An unkillable
guard is worse than none, because nothing fails when it rots, so each was
removed rather than left in place unexercised.

1. `handleNotification` guards text accumulation with `if !run.terminal`. In
   Zig a settled run is not active, so the chunk path is unreachable after
   settlement, and nothing reads the text buffer afterwards in any case — a
   second settlement returns early and the next `submit` mints a fresh message
   id and clears the bindings. The run boundary holds the rule alone.
2. `toolPayload` re-tests `t.status == "in_progress"` before attaching progress
   and `t.status == "failed"` before attaching the error object. Every caller
   has already decided the status: `started && !terminal` implies
   `in_progress`, and the failed branch sets `failed` itself. Zig decides once,
   at the call site.
3. `Resolve` compares the gate's run against the session's active run. A gate
   whose run is not the active one has already been resolved by
   `settleChildren`, so that comparison cannot fire. Zig replaces it with the
   reachable check: a resolution names the run it answers for, which is what
   `PermissionResolveRequest.run_id` carries on the wire, and one naming another
   run is refused.

The third is a divergence toward the oracle, not away from it: Go's `Resolve`
validates `res.RunID` too, and the Zig seam now takes the same argument.

A fourth guard was removed for the same reason after the fact. The Zig emitter
began with its own `if (run.terminal)` early return, which was killable only
while the run boundary was missing; once `observe` and `handleRequest` gated on
`active()`, every remaining path to the emitter already refused a settled run
and no mutation of the emitter's check could fail a test. It came out. The
lesson is worth the sentence: a guard can be load-bearing on Monday and dead on
Tuesday because a different defect was fixed, so the mutation set has to be
re-run after a fix and not only after a feature.

### Id allocation is a code point, not a byte

The Go test oracle mints its letter with `string(rune('a' + n - 1))`. That is a
code point, and it leaves ASCII sooner than it looks: the 27th id is `{`, the
**32nd** is U+0080, which Go encodes as `c2 80`, and the 160th is `Ā`. A port
that reads the expected traces as ASCII letters and casts to a byte agrees with
every corpus case — none reaches even `z` — and then, from the 32nd id, emits a
bare `0x80`..`0xFF`, which is not valid UTF-8 and so not a trace the shared
validator can read at all. Casting to `u8` additionally traps at 160. Both
thresholds sit inside an ordinary long answer of a couple of hundred streamed
chunks.

Naming the wrong threshold is easy and was done twice, here and independently
on the deepseek port, both times by deriving it rather than running it. The
boundaries are therefore pinned as emitted bytes against the output of a Go
program, not as characters worked out by hand, at every value Go's conversion
treats specially: the ASCII edge, the two-byte edge, both ends of the surrogate
block, the first code point above it, U+10FFFF, and the two ranges Go replaces
with U+FFFD. Zig's `{u}` makes the same two substitutions Go's rune conversion
makes, so the port needs no guard for them and carries none; what it does need
is the range cast, which is one `std.math.cast`.

No corpus case reaches any of this, so it is those twelve byte sequences and a
155-chunk run or it is nothing.

### Where the port and the oracle disagree on purpose

Three inputs make the Go adapter emit a payload the shared validator refuses,
and a fourth makes it misreport one. All four are raised as #142 so the two
implementations settle on one answer; the port takes the valid answer now,
which is what it did for the negative `duration_ms` in #136.

| Input | Go | Zig |
| --- | --- | --- |
| `tool_call_update` carrying `title: ""` | assigns it, and `omitempty` then drops the required `name` | retains the admitted title; a patch renames a call but cannot un-name it |
| a permission option with an empty `name` | publishes `label: ""`, which `permissionChoice` refuses with minLength | does not offer the option |
| a permission option whose `kind` is outside ACP v1 | classifies it as rejecting, so a `granted:false` resolution is accepted and reported as a denial | does not offer the option; a request with no usable option raises the existing empty-options refusal |

The third is a decision rather than a patch, which is why it is in the issue
and not only in the port. Dropping an unusable option is loud at a pinned
version boundary, matching what the adapter already does with a session-update
discriminator outside the defined set; refusing the whole request would be
louder. Either beats reporting an unclassifiable option as a denial.

None of the three is reachable from the eleven corpus cases.

### Where the oracle's Go runtime shows through

Go semantics leak into the wire in places the ACP specification never
mentions. Each is a trace difference rather than an internal one, and the
recurring rule underneath four of them is that **a present `null` is not an
absent member**: decoding null into a Go value is a no-op, so the field keeps
its zero value and the frame stays valid, while an absent member may instead
fail the decode outright.

| Input | Settles | Because |
| --- | --- | --- |
| `update` absent | `acp_invalid_update` | a nil `json.RawMessage` fails to unmarshal |
| `update: null` | `acp_unknown_update` | the four bytes unmarshal as a no-op, leaving an empty discriminator |
| `update: "x"`, `[]`, `7` | `acp_invalid_update` | the typed decode fails |
| `sessionUpdate` absent, `null`, `""` | `acp_unknown_update` | the switch reaches its default |
| `sessionUpdate: 7`, `true`, `{}` | `acp_invalid_update` | the typed decode fails |
| `options: [null, …]` | the remaining choices are published | null decodes to a zero-valued option the empty-`optionId` filter drops |
| `options: [7]` | `acp_invalid_permission` | the typed decode fails |

Ordering is observable too, so the port reproduces it: a wrongly typed update
refuses before the active run is consulted, a null one after, which is why a
null update arriving before any submit is dropped rather than refused.

Diagnostics carry Go's formatting verbs. `RequestID.String()` is
`strconv.Quote`, so a prompt error naming a string id reads
`for request "req-7"` with quotes and escapes; an integer id is bare decimal
and an unset one is `<unset>`. The unsupported-stop-reason message uses `%q`
on the same helper. That helper is `zig/src/adapter/goquote.zig` — a module
rather than a copy per codec, because Claude's diagnostics need it for the
same reason — and it is `strconv.Quote` rather than an approximation of it:
`strconv.IsPrint` is a Unicode table, not a predicate, so `go/tools/goprintable`
walks the scalar range through it and generates the 741 printable spans the
helper binary-searches, recording the toolchain that produced them. An
approximation is what made the helper wrong in the first place, and it was
wrong in a direction no ASCII test could see — U+200B, U+2028, U+FEFF, the
private-use area and every unassigned code point all quote as escapes.

Every one of these was settled by driving the Go adapter and reading its
output, never by reading its source. Twice the source would have given the
wrong answer, both times on a null.

### Mutation evidence

84 mutants over the reducer, every one compiling, every one killed by a named
test. Six survived the first pass. Two were unkillable guards and were removed;
one exposed the missing run-id argument on `resolve`; three were gaps in
assertion rather than in rule — a grant of kind `allow_always`, a prompt error
carrying a code other than -32800 on a run that had already requested
cancellation, and a repeated `in_progress` whose payload was never checked for
an error object it must not carry. A seventh was reported as surviving and was
not: the mutation moved a declaration without moving it past the check it was
meant to escape, which asserts nothing.

Twenty-eight of them cover the thirteen defects five rounds of Codex review
found on the reducer unit, and **not one of the thirteen is reachable from the
eleven corpus cases**. That is the single most useful thing this port
established, so it is worth saying why rather than only that.

Four were reachable only past the end of a case or on a frame no case carries:
the byte-wide id letter, the missing run boundary, the session-global message
bindings and the tolerated wrong-typed chunk member. Three were the
schema-validity class above — inputs the Go adapter turns into an envelope the
shared validator refuses, on a frame its own codec accepts. Six were the
Go-runtime class: quoting, printability, and the null-versus-absent rule in
four places.

A corpus is a record of what a harness was observed to send, so by
construction it cannot contain the input that makes its own reducer misbehave.
Finding these takes reading the schema's constraints against what the reducer
fills — the sweep that produced #136 — or a second implementer asking why a
value is never checked. The corpus proves the mapping; it does not bound the
reducer, and a green corpus is not evidence that one is correct.
