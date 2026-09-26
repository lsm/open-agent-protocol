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
| `turn/start.model` | `protocol/v2/turn.rs` | Per-run `model_id` control: the requested model binds exactly the run it was requested for | native | `run.model_selection`: native, mode `per_run` | `model-per-turn` |
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
| `item/tool/requestUserInput` | `common.rs`; `ToolRequestUserInput*.json` | ordinary user-input interaction | normalized/lossy by question kind | options native; `isOther` custom text unrepresentable (the OAP answer `oneOf` is options-or-text, never both), so no synthetic "other" option is advertised; secret input: unavailable | `user-input` |
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
- per-run model selection through `turn/start.model`, advertised
  `run.model_selection` at `native` with mode `per_run`: the requested model
  is applied to that turn and the thread's configured model remains the
  session default (`current_model_id`). The other three per-submit controls
  are advertised `unavailable` and refused before admission under their own
  keys. `turn/start` does carry a `developerInstructions` parameter at this
  pin, but no fixture exercises it and this ledger does not pin its
  semantics, so `run.instructions` stays unavailable until it does;
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

### Conformance note (decision 0002, 2026-09-10)

The submit response originally reported `admission=started` with
`status=queued` — a mixed claim predating the admission-shape vocabulary.
Decision 0002 made the shapes canonical and the validator now rejects the
mix; the adapter reports `status=running` for its started admissions. The
run's internal state still promotes at `run.started` exactly as before; only
the response's status claim changed.

## Zig port (2026-09-24)

`zig/src/adapter/codex/` is a second implementation of this ledger, a peer of
the Go adapter under Decision 0032: `rpc.zig` (line codec and a Go-compatible
frame encoder), `native.zig` (pinned method names, the decode shape of every
native struct the adapter reads, and the parameters it writes), `session.zig`
(the reducer) and `corpus.zig` (the evidence driver). `oapx serve agent
--backend codex` serves it (see the section below); the reducer has the shape
of the other Zig ports, with two
additions this adapter needs because it writes to Codex: `writes` holds every
frame the adapter would send, and `settled` holds the outcome of each call once
its response is observed.

### What the corpus proves

All thirteen cases are claimed, and the driver's case list must equal the
manifest's in order. Each case replays through the production codec and the
reducer, and the Zig trace, encoded the way Go encodes it, must equal
`expected-oap.json` with its whitespace removed, byte for byte. Each trace
then passes the Zig schema check and `validation/semantic.zig`, built the way
`adaptertest` builds it: a capabilities exchange carrying the adapter's
descriptor, the submit exchange, and for `interrupted-turn` the cancel
exchange at the same cut. The driver also ports Go's
`assertClassifications`, reads events with Go's `Next` semantics (an
`await_events` count must already have been emitted), refuses an
`observed-only` line that emits anything, and requires exactly one native
response per reverse request, equal to `expected_result` or `expected_error`.

Three properties of the Go test harness, not of the adapter, are reproduced so
the expectations keep reproducing:

- `process-exit` and `interrupted-turn` open with a `turn/started` the Go test
  sends itself; it is not in either `native.jsonl`. `interrupted-turn` then
  skips its first native line, which that prefix replaces.
- The fake client answers `thread/start` with `native-thread` and `turn/start`
  with `native-turn`; the Zig driver answers the requests the reducer wrote with
  the same results, and answers `turn/interrupt` with `{}`.
- Ids are `%s-%02d` from one counter (`Options.id_width = 2`) and the clock
  ticks once per read, as `fakeIDs` and `fakeClock` do.

### What the adapter writes

The corpus carries no host-to-server lines, so the writes are pinned
separately. `fixtures/adapters/codex-appserver-writes/conversation.json` is a
conversation between the Go adapter, through its real `rpc.Start` process
path, and a scripted app-server: the handshake, `thread/start` with every
optional member, `turn/start` with a per-turn model and text Go escapes for
HTML, an approval answer, a user-input answer, the `-32601` and `-32602`
replies, `turn/interrupt`, and the `-32800` that closes an interaction at the
terminal. It also records the capability descriptor. Go's
`TestCodexProcessWritesTheRecordedConversation` fails if the Go adapter stops
writing those bytes (`OAP_UPDATE_CODEX_CONVERSATION=1` rerecords), and the Zig
driver replays the same server frames and requires the same bytes and the same
descriptor. `thread/resume` and the `-32602` variants other than scope are
pinned only by Zig unit tests against hand-written bytes.

### Where the port differs

1. **Invalid UTF-8.** Go's decoder accepts a frame carrying it; the Zig codec
   refuses the frame. Codex writes JSON with serde, which cannot emit it.
2. **Pass-through members** (`item.arguments`, `item.result`, a file change's
   `kind`) are re-encoded from the parsed value. Number literals survive
   verbatim in both, but Go's `json.RawMessage` also keeps every string escape
   as Codex spelled it (`A`, `\/`, `é`, a lone `\ud800`), escaping
   only `<`, `>`, `&` and U+2028/2029, while Zig writes the decoded character
   in Go's canonical form (a lone surrogate as U+FFFD). No corpus frame
   carries an escape in a pass-through member.
3. **Order at a terminal.** Go closes open interactions and actions by ranging
   over maps, so with two of either open the order of their `-32800` replies and
   envelopes is unspecified; Zig uses the order they opened in. Neither the
   corpus nor the recorded conversation has two open at once.
4. **Case-variant duplicate members** are resolved with the shared `gojson`
   fold helpers. A pointer member that a later null resets and a still later
   member reassigns is merged across the reset in Zig, where Go starts again.
5. **No journal.** Like the other Zig ports the reducer keeps no journal and
   has no `Resume`. The descriptor the corpus pins is byte-identical to Go's,
   so it advertises `run.resume` and `run.replay` as `degraded`, and the served
   endpoint does too under the same revision: the `oapx` endpoint journals 256
   events per session and answers the replay control from them.
6. **Endpoint checks.** Unadvertised controls, the session id, delivery,
   degraded opt-in and metadata are refused before a submission reaches the
   reducer, as are tools and tool sources at open. Call failures come back as a
   structured refusal (method, remote code, message) rather than Go's composed
   error text.

One finding outside the adapter: `std.json.Stringify` keeps a fixed nesting
stack and panics past 256 levels in safe builds, while Go admits 10 000. A
pass-through member can reach that depth, so the codex encoder walks values
iteratively; anything that stringifies another port's envelopes with
`std.json.Stringify` inherits the panic.

## Served by `oapx serve agent --backend codex`

`zig/src/adapter/codex/adapter.zig` implements the adapter contract in
`zig/src/adapter/contract.zig` around the reducer, and
`zig/src/adapter/endpoint.zig` serves it over the endpoint stdio binding. It
spawns `<executable> <args> app-server --listen stdio://`, writes the pinned
`initialize` (id 0) and `initialized` frames, then drives the reducer: its
`writes` go to the child, each response settles the waiting request through
`settled`, and its envelopes are drained as run events. With the model,
approval policy and sandbox of the recorded conversation, the three frames an
open writes equal the conversation's first three client frames byte for byte.
Against a scripted child, `goap conformance` passes every check but the two
model-switch checks, and `goap serve agent --backend codex` fails the same two
against the same child.

What differs from the Go adapter:

| Area | oapx | Go adapter | Why |
| --- | --- | --- | --- |
| Request bound | `initialize`, `thread/start`, `turn/start` and `turn/interrupt` are awaited at most 60 s; past that the request is answered `internal` and the session is abandoned, its child stopped | context-bound | The endpoint serves one request at a time, so a request cannot wait unbounded. A late answer would otherwise settle the next request. |
| Session memory | Once a settled session has grown 256 KiB past its last compaction, its arena is rebuilt from what later runs consult: the thread, the session state, each run's id, turn, status and sequence, and a stub per item and interaction so a reused item id and a late answer are handled as goap handles them | garbage-collected | Bounded process memory. |
| Configuration | Without `--config`, `codex` from `PATH` with only `HOME` and `PATH`, stating no approval policy or sandbox, so Codex applies its own defaults | `goap serve` needs a `--config` entry | Member names are exact; Go's decoder matches them case-insensitively. |
| Transport failure text | `codex app-server rpc: <error>` or the child's departure | Go's error text | The run still fails `native_transport_closed`, inferred. |
| Cancel answer | `cancelling` and a `run.status.updated`, then `run.cancelled` | The same, except that when `turn/completed` directly follows the `turn/interrupt` answer, usually `cancelled` and no status update | Go settles the run on its reader goroutine while the cancel waits for the lock; the endpoint answers before reading the next frame. `go/cmd/goap/testdata/parity/codex` delays the completion so both answer alike. |
