# Codex app-server mapping ledger

Status: pinned implementation boundary for the first production OAP adapter.

## Provenance

- Repository: `https://github.com/openai/codex`
- Commit: `8d7cc24a87f4aa66aa434eb4f25f4f4bafc0e0a9`
- Commit date: 2026-09-06
- Generated schema tree SHA-256: `d31125f254f93a9c6300e50c86ffbd3cc6ad388ef5b8833ecbd0a47371a344b6`
- Schema hash recipe:
  `find codex-rs/app-server-protocol/schema -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum`
- Transport evidence: `codex-rs/app-server-protocol/src/rpc.rs`
- Protocol definitions: `codex-rs/app-server-protocol/src/protocol/common.rs` and `protocol/v2/`
- Handshake evidence: `codex-rs/app-server/tests/common/test_app_server.rs`
- Process invocation: `codex-rs/app-server/README.md`

The checkout reports workspace version `0.0.0`; compatibility is pinned by full
commit and schema hash, not by package version.

## Wire boundary

`codex app-server --listen stdio://` speaks one JSON value per line. The wire is
JSON-RPC-shaped but deliberately neither emits nor requires
`"jsonrpc":"2.0"`. `RequestId` is an untagged union of string and signed
integer. The adapter therefore owns a strict private codec rather than using a
standard JSON-RPC implementation.

One reader must classify and dispatch responses, errors, server requests, and
notifications. One serialized write pump must send client requests,
notifications, and responses to reverse requests. Caller context cancellation
returns promptly; if a request may already have reached Codex, the transport is
closed so a late native admission cannot be mistaken for a different OAP run.
Reverse-request queue overflow also fails immediately without waiting on an
error write. Stderr is a separate diagnostic stream. EOF or process exit is
transport failure unless the authoritative run terminal was already observed.

The mandatory handshake is:

1. start `codex app-server --listen stdio://`;
2. request `initialize`;
3. receive its correlated response;
4. notify `initialized`;
5. only then issue thread or turn requests.

Codex initialization capabilities are directional declarations by its client;
they are not copied into an OAP capability descriptor.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| `Thread.id` | `session_id` | Maintain a typed bidirectional association. Never reuse an RPC ID. |
| `Turn.id` | `run_id` | One accepted foreground turn is one OAP run. Never use the thread ID. |
| agent-message item ID | `message_id` | Allocate/map within the message domain. |
| command/file/MCP item ID | `tool_call_id` | Allocate/map within the action-call domain. |
| reverse RPC `RequestId` | `interaction_id` | Generate an OAP ID and retain the private correlation until exactly one reply. |
| outbound RPC `RequestId` | none | Private transport correlation only. |

Native identifiers retained for diagnosis use namespaced extension metadata and
must not change portable identity semantics.

## Lifecycle mapping

Fidelity values are `native`, `normalized`, `synthesized`, `lossy`, and
`unsupported`. Fixture IDs are requirements for the adapter evidence corpus;
they do not claim that a capture already exists.

| Native observation | Pinned source | OAP meaning | Fidelity | Initial capability | Required fixture |
|---|---|---|---|---|---|
| `initialize` response then `initialized` | `common.rs`; `test_app_server.rs` | Adapter readiness prerequisite | native | `protocol.initialize`: native | `handshake` |
| `thread/start` response | `protocol/v2/thread.rs` | Open and associate an OAP session | normalized | `session.open`: native | `thread-start` |
| `thread/resume` response | `protocol/v2/thread.rs` | Explicit adapter configuration restores native conversation attachment; it does not replay OAP events | normalized | attachment: native; canonical replay: degraded process memory | `thread-resume` |
| `turn/start` response | `protocol/v2/turn.rs` | Successful run admission only | native | submit/admission: native | `turn-admitted` |
| `turn/started` | `protocol/v2/turn.rs` | `run.started`; first run sequence value | native | run start: native | `completed-text` |
| `item/agentMessage/delta` | `protocol/v2/item.rs`; `AgentMessageDeltaNotification.json` | `content.delta` | native | text streaming: native once exercised | `completed-text` |
| command execution item lifecycle | `protocol/v2/item.rs` | OAP action call lifecycle | normalized | tools: degraded until every mapped transition is exercised | `command-completed` |
| file change item lifecycle | `protocol/v2/item.rs` | OAP action call lifecycle | normalized | tools: degraded until exercised | `file-change-completed` |
| MCP tool item lifecycle | `protocol/v2/item.rs` | OAP action call lifecycle | normalized | tools: degraded until exercised | `mcp-completed` |
| `turn/completed`, status `completed` | `protocol/v2/turn.rs` | exactly one `run.completed` | normalized | terminal: native normalization | `completed-text` |
| `turn/completed`, status `failed` | `protocol/v2/turn.rs` | exactly one typed `run.failed` | normalized | terminal: native normalization | `failed-turn` |
| `turn/completed`, status `interrupted` | `protocol/v2/turn.rs` | `run.cancelled` only after confirmed interrupt | normalized | cancellation settlement: native normalization | `interrupted-turn` |
| successful `turn/interrupt` response | `protocol/v2/turn.rs` | cancellation-intent acknowledgement | native | targeted cancel: native | `cancellation-race` |
| `item/commandExecution/requestApproval` | `common.rs`; `CommandExecutionRequestApproval*.json` | permission interaction | normalized | simple decisions: native; rich amendments: unavailable | `command-approval` |
| `item/fileChange/requestApproval` | `common.rs`; `FileChangeRequestApproval*.json` | permission interaction | lossy (`grantRoot` is not projected) | simple decisions: native | `file-approval` |
| `item/permissions/requestApproval` | `common.rs`; `PermissionsRequestApproval*.json` | deterministic `-32601` reverse-response; no OAP interaction | unsupported | unavailable | `permissions-approval` |
| `item/tool/requestUserInput` | `common.rs`; `ToolRequestUserInput*.json` | ordinary user-input interaction | normalized/lossy by question kind | options and free text: degraded; secret input: unavailable | `user-input` |
| duplicate native terminal | reducer policy | diagnose and suppress | synthesized safeguard | terminal invariant | `duplicate-terminal` |
| EOF/process exit before terminal | `app-server` process boundary | one typed `run.failed` | synthesized failure projection | transport failure handling | `process-exit` |

A `turn/completed` notification is authoritative, but its embedded status selects
the OAP terminal. `inProgress` at completion is a native-protocol failure, never
success. One terminal arbiter owns all terminal emission. Child actions and
interactions close before it, and a later EOF cannot create a second terminal.

## Interaction policy

Reverse requests are requests, not notifications. The adapter validates their
thread and turn scope, creates a stable OAP `interaction_id`, records the native
request ID privately, emits one portable request, accepts only the declared
responder once, writes the native response using the original ID, and emits one
portable resolution. Unsupported reverse requests receive a deterministic
native error; silently ignoring them can deadlock Codex.

Only choices proven to round-trip are advertised. Rich approval scopes, edited
commands, and UI-specific question data remain degraded or unavailable rather
than being discarded silently. `deny` and `cancel` remain distinct when Codex
provides distinct semantics.

## Cancellation and recovery

A successful `turn/interrupt` response acknowledges intent. Settlement waits for
`turn/completed`: `interrupted` cancels, while natural completion or failure may
win. Duplicate intent is idempotent; stale cancellation cannot target a later
turn.

`Config.ResumeThreadID` selects `thread/resume` during `Open` and restores native
conversation attachment. It is not OAP event replay and does not reconstruct
historical or active runs. `Session.State` provides reconciliation for state that
this adapter has observed. `Session.Resume` separately replays the bounded
process-memory journal of canonical events emitted by this adapter, with explicit
gaps; exact native and cross-process replay are unavailable. A slow live consumer
is explicitly detached with `adapter.ErrEventStreamOverflow` after a contiguous
prefix and must resume from its last sequence; events and terminals are never
silently dropped into an apparently normal stream close.

## Unknown and omitted observations

Every observed native method is classified as one of:

1. **mapped** — drives a canonical OAP transition;
2. **observed-only** — bounded diagnostic, no portable state effect;
3. **required-unmapped** — fail the active run because safe reduction is not
   possible;
4. **unsupported request** — reply with a deterministic native error.

Telemetry, product UI hints, and redundant snapshots may be observed-only.
Unknown content fragments, unresolved reverse requests, and lifecycle-significant
unknowns are never silently ignored. Unscoped observations are not guessed onto
the active run. Every native fixture must prove that each frame is mapped or
listed in its omissions ledger.

## Initial advertised surface

- one nonterminal foreground run per OAP session;
- requested `auto`, effective `start`;
- explicit admission, start, text delta, and terminal mapping;
- run-targeted cancellation intent with authoritative later settlement;
- reconciliation from adapter state;
- bounded process-memory replay only if implemented and then marked degraded;
- queue, steer, BTW, side runs, exact replay, cross-process durability, dynamic
  client tools, MCP elicitation, authentication management, WebSocket transport,
  and product configuration are unavailable;
- command/file approvals and structured input become affirmative only after
  complete correlation and settlement fixtures pass.

## Provider boundary

Harness conformance and provider compatibility are independent. Official China
Coding Plan documentation gives Responses base
`https://open.bigmodel.cn/api/v1`, and the official Codex configuration sets
`wire_api = "responses"`; no Responses-to-Chat proxy is planned. The composed
`/api/v1/responses` route is inferred from that base plus Codex's wire API rather
than printed as a full URL in that guide.

The official Codex example uses `glm-5.3`. Separate documentation establishes
that `glm-5.3-flash` is available in China Coding Plan, but does not explicitly
establish the exact Codex + Flash combination. Live evidence therefore uses
`glm-5.3` as the documented control and reports `glm-5.3-flash` as a distinct
compatibility candidate. Backend results cannot alter hermetic adapter
conformance claims.

A real-process loopback integration is available but skipped by default because
provisioning the pinned Rust binary is external to Go CI. Build commit
`8d7cc24a87f4aa66aa434eb4f25f4f4bafc0e0a9` from `codex-rs` with:

```sh
cargo build --locked --release -p codex-cli --bin codex
OAP_CODEX_INTEGRATION=1 \
OAP_CODEX_COMMIT=8d7cc24a87f4aa66aa434eb4f25f4f4bafc0e0a9 \
OAP_CODEX_BIN=/absolute/path/to/codex \
go test ./adapter/codex/appserver -run '^TestPinnedCodexProcessAgainstResponsesMock$' -count=1 -v
```

The test creates isolated `CODEX_HOME` and workspace directories, supplies only a
small sanitized child environment, disables provider retries, targets the local
Responses mock with a fake bearer token, and drives the process through the
public OAP adapter. Explicit opt-in without the exact commit assertion or binary
is a failure, not a skip.

## Deferred scope

This pin does not cover `turn/steer`, queued/side runs, concurrent runs, full
command output presentation, complete diff UI, plan/checklist UI, MCP/dynamic
inventory, multimodal input, account/login/model/config APIs, durable admission,
durable identity storage, cross-process journals, exact replay, continuity
leases, a generalized Responses proxy, Chat-Completions-to-Responses conversion,
or automatic support for newer Codex revisions. Each requires new pinned
evidence, capability units, and fixtures.
