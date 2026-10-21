# Codex app-server 0.163.0 mapping ledger

Status: pinned implementation boundary, superseding
[`codex-app-server-0.162.1-mapping.md`](codex-app-server-0.162.1-mapping.md),
over the earlier ledgers it names and the full mapping in
[`codex-app-server-8d7cc24-mapping.md`](codex-app-server-8d7cc24-mapping.md).
This one records only what the move to 0.163.0 changed; everything it does not
mention carries over unchanged.

## Provenance

- Repository: `https://github.com/openai/codex`
- Release: `rust-v0.163.0` (latest stable on 2026-10-18, published
  2026-10-15T21:30:38Z), workspace version `0.163.0`
- Commit: `83086188ae0614bd882818ff1aa23f3194962012` (the annotated tag
  object is `129b8ec7ee6f4511557a4856279f529025c25af4`), tree
  `e91f335e7a03e1d14fa1e5fc7fc7620cdfc60842`, committed
  2026-10-15T18:57:07Z
- Generated schema tree SHA-256:
  `cc92f2c7bdbbf5738859fb11c0f13c344f51acfa2c86667924ae3c606303e160`, by the
  recipe of the previous ledgers over the tag's
  `codex-rs/app-server-protocol/schema`; 1067 files. The same recipe over
  `rust-v0.162.1` reproduces that ledger's digest
  (`075997d5cb497bd6518a973a6d7b85fef0eaa64c02428af866e46c725c55fad7`, 1062
  files), so the two digests differ only by the release.
- Release artifacts, digests from the release metadata and checked on
  download:
  - `codex-aarch64-apple-darwin.tar.gz`:
    `61c4d9a92521bdb42b0c1d9f87c673f6da628a06de5945cbdd9a0010632f6d10`
    (99551880 bytes), holding the binary `codex-aarch64-apple-darwin`:
    `8813367285524ac798dd96d1b071aea30d58708c7226fd236cd275cf1cfdee31`
    (249952688 bytes), which reports `codex-cli 0.163.0`
  - `codex-x86_64-unknown-linux-musl.tar.gz`:
    `beb339e8ecd9ba057a6bb2dc81ad8ca474812edfbf0fc5056ca818797648dde4`
    (112589966 bytes), holding `codex-x86_64-unknown-linux-musl`:
    `399ff69c431c066e40de318fba45c2b07f45a76b19939d247ee4828fc70c4215`
    (296865480 bytes); downloaded and hashed, not run

## What moved

232 commits separate the tags. Under `codex-rs/app-server-protocol`,
`src/rpc.rs` is byte-identical, so the transport is unchanged. The changes are
in `src/protocol/common.rs`, `v2/thread.rs`, `v2/turn.rs`, `v2/environment.rs`
and the generated schema:

| Change | Classification |
| --- | --- |
| New experimental methods `thread/readState/update` and `thread/items/read`, and the experimental notification `thread/readState/changed` | unused: experimental, and neither adapter sets `experimentalApi` at `initialize` |
| `thread/list` response gains experimental `readStates`; `thread/read` response gains experimental `readState` | unused: experimental and not served to a client that did not opt in; the native list reads only `data` |
| `ThreadListParams` gains optional `excludedThreadIds` | unused: the native search sends `cwd`, `limit` and `searchTerm` only |
| `ThreadForkParams` gains experimental `experimentalPredictionMode` | unused: neither adapter forks |
| `TurnToolOutput` gains `retain`, so `TurnStartParams` accepts it on a tool output | unused: neither adapter sends tool outputs on `turn/start` |
| `EnvironmentAddParams` gains optional `websocketRequestId` | unused: neither adapter adds environments |
| Response items gain optional `status`, `encrypted_content`, `annotations` and `logprobs` (seen in `rawResponseItem/completed` and the `history` of `ThreadResumeParams`) | unused: neither adapter subscribes to raw items nor sends resume history |
| The mock experimental method and its test are removed from `export.rs` and `common.rs` | not wire |

None of it reaches either adapter, and unknown members and methods are ignored
as before. The adapters do not change. The capability descriptor changes only
in the endpoint version it reports, which the revision names, so the revision
moves to `codex-appserver-0.163.0-oap-v1`, served by both trees (and later to
`-oap-v2`, below).

## Corpus

`fixtures/adapters/codex-appserver-0.163.0` carries every `native.jsonl`,
`mapping.json` and `omissions.json` of the 0.162.1 corpus forward unchanged;
none was re-recorded. Only `manifest.json` (the commit and the schema digest)
and the expectations' revision changed.

`fixtures/adapters/codex-appserver-0.163.0-writes/conversation.json` was
re-recorded with `OAP_UPDATE_CODEX_CONVERSATION=1`: every frame is
byte-identical to the 0.162.1 recording, and only `codex_commit`,
`capability_revision` and the descriptor's endpoint version changed.

## Token usage at this pin

No earlier ledger mapped the server's usage notification, so no Codex run
reported usage. This records it at 0.163.0; earlier pins were not checked.

- `src/protocol/common.rs:1979` registers
  `ThreadTokenUsageUpdated => "thread/tokenUsage/updated"`.
- `src/protocol/v2/thread.rs:1934` gives it `threadId`, `turnId` and
  `tokenUsage`; `:1974` makes `tokenUsage` a `ThreadTokenUsage` of `total`
  (the thread's running sum), `last` (the most recent model call) and
  `modelContextWindow`; `:1995` makes each a `TokenUsageBreakdown` of
  `totalTokens`, `inputTokens`, `cachedInputTokens`, `cacheWriteInputTokens`,
  `outputTokens` and `reasoningOutputTokens`. The tag's generated
  `schema/json/v2/ThreadTokenUsageUpdatedNotification.json` agrees.

- `codex-rs/app-server/src/bespoke_event_handling.rs:1595` sends it from
  core's `TokenCountEvent` whenever that event carries usage, and
  `src/request_processors/token_usage_replay.rs` replays the latest persisted
  one to a client that attaches to an existing thread, naming a turn of that
  thread's history.

| Native | OAP | Classification |
| --- | --- | --- |
| `thread/tokenUsage/updated`, `tokenUsage.total` | the run terminal's `usage`: the thread's `total` at the turn's latest notification less its `total` before the turn started, `inputTokens` to `input_tokens`, `outputTokens` to `output_tokens`, `totalTokens` to `total_tokens` | normalized: `total` is a running sum, so the difference is exact however many notifications a turn sends and whether one repeats; summing `last` would count a repeat twice |
| a notification naming a turn no run carries (the attach replay, or a turn before this session) | none, but its `total` becomes the baseline for the next run | normalized |
| `last`, `cachedInputTokens`, `cacheWriteInputTokens`, `reasoningOutputTokens`, `modelContextWindow` | none | omitted: OAP's `usage` has no member for them, and `input_tokens` already includes cached input |

A run whose turn sends no usage notification reports no `usage`, which OAP
reads as unknown. Corpus case `token-usage` carries two calls and a repeated
notification, and pins the result in both trees.

## Structured output at this pin

The descriptor said `run.structured_output` was `unavailable` because "this pin
exposes no per-turn output schema". No ledger recorded that, and it is wrong:

- `src/protocol/v2/turn.rs:276` gives `TurnStartParams` an optional
  `output_schema` ("Optional JSON Schema used to constrain the final assistant
  message for this turn"), serialized `outputSchema` and not marked
  experimental. The tag's generated `schema/json/v2/TurnStartParams.json`
  agrees.
- `src/protocol/v2/item.rs:252` makes an `agentMessage` item carry its whole
  `text`, which `item/completed` delivers.

| OAP | Native | Classification |
| --- | --- | --- |
| `output_schema` on a submit | `turn/start.outputSchema`, verbatim, after compiling it as Decision 0005 requires; a schema no object can satisfy is refused `unsatisfiable` and never sent | native |
| `run.completed.result` | the `text` of the turn's last completed `agentMessage` item, parsed as JSON and checked against the schema | normalized: the streamed deltas span every message of the turn, and only the final one is constrained |
| a final message that is not JSON or does not conform | `run.failed` with `structured_output_failed`, never a completion without its result | normalized |

`run.structured_output` is now `native` in both trees, so the revision moves
to `codex-appserver-0.163.0-oap-v2`. Corpus cases `structured-output` and
`structured-output-nonconforming` pin both outcomes.

## Real-process evidence

- **Go.** `TestPinnedCodexProcessAgainstResponsesMock` and
  `TestPinnedCodexProcessTakesALiveReasoningLevel`, with
  `OAP_CODEX_INTEGRATION=1`, the darwin-arm64 binary above,
  `OAP_CODEX_COMMIT=83086188…` and `OAP_CODEX_SHA256=88133672…`, pass 3x each
  against the loopback Responses mock, with a temporary `HOME`, no
  credentials, and `sandbox-exec` denying the keychain daemons and every
  non-loopback connection.

Not re-run: a turn under a real login, and approval, file-change, MCP and
user-input turns against the real process; the transport is unchanged and the
schema changes above touch none of those frames' members the adapters read.

## Model-provider settings at this pin

The 0.160.1 ledger's *Model-provider settings* section holds at 0.163.0,
checked the same way against the darwin-arm64 binary above (empty
`CODEX_HOME`, `--strict-config`, `app-server --listen stdio://` on a closed
stdin, in the same sandbox): the routed `-c` settings `oapx` writes for a codex
entry's `model_provider` load and exit 0, the same entry with `base_ulr` and no
`name` is refused because the provider's name must not be empty, and
`wire_api="chat"` is refused as no longer supported. `wire_api` still takes
only `responses`.

## Client-provided tools at this pin

Whether `action.tools.provide` ([Decision 0011](../decisions/0011-control-layer-provided-tools.md))
could be served natively, so that a host's own tools reach a Codex session.
Read from the source at `rust-v0.163.0`; not exercised against the binary.

**Possible, through an experimental surface.** `ThreadStartParams` carries
`dynamic_tools: Option<Vec<DynamicToolSpec>>`, marked
`#[experimental("thread/start.dynamicTools")]`
(`codex-rs/app-server-protocol/src/protocol/v2/thread.rs:145`). A spec is a
function (`name`, `description`, `inputSchema`, optional `deferLoading`) or a
namespace of functions (`codex-rs/protocol/src/dynamic_tools.rs`). When the
model calls one, the server sends the client the request `item/tool/call`
(`common.rs:1828`, "Execute a dynamic tool call on the client") with
`DynamicToolCallParams { threadId, turnId, callId, namespace?, tool, arguments }`.
The client answers `DynamicToolCallResponse { contentItems, success }`, each
item `inputText`, `inputImage` or `inputAudio` (`v2/item.rs:1663`). The turn
records the call as `ThreadItem::DynamicToolCall`, with status `inProgress`,
`completed` or `failed`. The specs are kept in the session's metadata, and a
compatibility deserializer exists "so new builds can resume sessions written
before explicit namespaces". That suggests a resumed thread keeps its tools,
though it has not been checked.

Experimental means the client must set `experimentalApi: true` in
`initialize` (`v1.rs:53`). Neither adapter sets it today. Setting it opts the
connection into every experimental member at once (this release's read-state
methods among them), so a native mapping takes the whole experimental surface,
and the risk that it changes between releases.

What `action.tools.provide` would map to:

| OAP | Codex |
|---|---|
| `tools` on `session.open.request` | `dynamicTools` on `thread/start`, one function spec per `ToolDefinition` (`name`, `description`, `input_schema` as `inputSchema`) |
| a provided tool at reopen | the specs the thread recorded, restored by `thread/resume` (to be checked) |
| `action.call.requested` | the server request `item/tool/call`; its `callId` is the `tool_call_id`, its `arguments` the input |
| `action.call.resolve.request` | the JSON-RPC response to that request: a result as `contentItems` with `inputText`, `success: true`; a failure as `success: false` with the error text |
| the five refusal reasons | the adapter's own, as for any provided tool; Codex sees only the one response it gets |
| limits | the schema states no count, name pattern or schema dialect; whatever Codex enforces beyond its spec parser was not read |

**Recommendation.** Feasible, and the call shape matches Decision 0011 one to
one. Whether to take on `experimentalApi` for it is the decision.

## Command process outcome on the normalized wire

A native `commandExecution` item with native status `completed` settles
`action.call.completed` under `name: "codex.command"`. Its result is:

```json
{"command":"git diff HEAD~1","cwd":"/workspace/repo","output":"...","exitCode":1}
```

`exitCode` retains the native spelling and integer value. Zero is evidence
that the command exited successfully; a nonzero value is the actual process
failure code. Native null or omission becomes explicit `exitCode: null`:
the exit status is unknown, never an inferred zero. Consumers must require a
numeric zero to classify a code read as successful; neither output text nor
`action.call.completed` alone proves a subprocess succeeded. Completion here
means the harness delivered the command's result, including nonzero outcomes
such as grep's no-match status. It does not turn a nonzero status into a sandbox
failure or fail the enclosing run.

The result also preserves the native command, cwd and aggregated output. If
a native command item additionally carries `result`, its whole JSON value is
retained under `nativeResult`; it cannot overwrite the process evidence. Other
item kinds retain their existing pass-through result shape. Native status
`failed` or an explicit native error still emits `action.call.failed` with
`native_action_failed` and no success result, even if an exit code is zero.

This adapter protocol note uses the existing free-JSON `result` arm defined by
Decision 0060 and the existing typed failure arm. It adds no envelope member,
schema rule or validator obligation. The credential-free
`.claude/skills/verify/scripts/codex-command-result.py` journey replays zero,
nonzero, negative, omitted, null, native-result and explicit-failure cases
through both real CLIs and validates both resulting traces in Go, Zig and Rust.
