# Codex app-server 0.157.0 mapping ledger

Status: pinned implementation boundary, superseding
[`codex-app-server-8d7cc24-mapping.md`](codex-app-server-8d7cc24-mapping.md).
That ledger remains the full mapping; this one records only what the move to
the release changed, and everything it does not mention carries over
unchanged.

## Provenance

- Repository: `https://github.com/openai/codex`
- Release: `rust-v0.157.0` (latest stable on 2026-09-24), workspace version
  `0.157.0`
- Commit: `00c972ed5d6ff6499317fd41b7f23605b8e6850d`, tree
  `6fa1a3767a92ba5d579fcaf724fdce3049c2070b`, committed 2026-09-25T01:33:35Z
- Generated schema tree SHA-256:
  `8ab8212ea05ccb2326439bffebde7ece2f64eab0534c5de531e449e31a8bef5d`, by the
  recipe of the previous ledger (`find codex-rs/app-server-protocol/schema -type f -print0 | sort -z | xargs -0 shasum -a 256 | shasum -a 256`)
  over the tag's source archive; 1056 files.
- Release artifacts, digests from the release metadata and checked on
  download:
  - `codex-aarch64-apple-darwin.tar.gz`:
    `0f1522362bf8c8bbb58bf2fa8a3c600a0b723f1d4e3405ab831afeda32438909`
    (94750529 bytes), holding the binary `codex-aarch64-apple-darwin`:
    `ad0be20d04e2ba6146ecdb51d7f8b7b0fe15420a15dc9b0057518d858f1f3714`
    (238289440 bytes), which reports `codex-cli 0.157.0`
  - `codex-x86_64-unknown-linux-musl.tar.gz`:
    `db3fe3adaa35c50edfb68a988a117782fe3492960fb63d7003eb6748ccc0657b`
    (107842066 bytes), from release metadata only; not downloaded or run

The same recipe over the source archive of `8d7cc24` gives
`29bc71c05cb155fe877eb4b97b416ee23df60cd705dc10e901f30bd7bace3bcd` (1018
files), not the `d31125f…` that ledger records. The earlier digest cannot be
reproduced from the archive; its manifest is retired with it, and this ledger's
digest is the one reproduced here.

## Wire boundary

Unchanged. `codex-rs/app-server-protocol/src/rpc.rs` is byte-identical at both
pins; `stdio://` is still the default `--listen` URL
(`codex-rs/app-server-transport/src/transport/mod.rs`). The app-server README
shrank from 3039 to 456 lines and no longer documents the transport; the source
is the evidence now.

## Schema diff

Of the JSON schema files, 40 differ, 11 are new and 2 are gone. Method sets:

| Set | 8d7cc24 | 0.157.0 | Added | Removed |
|---|---|---|---|---|
| `ClientRequest` | 99 | 104 | `account/gatewayOAuth/{login,read,cancel}`, `thread/attachment/{add,list,remove}` | `thread/rollback` |
| `ServerNotification` | 81 | 83 | `account/gatewayOAuth/changed`, `thread/attachment/updated` | none |
| `ServerRequest` | 10 | 10 | none | none |
| `ClientNotification` | 1 | 1 | none | none |

Per payload the adapters read or write, with `description` and `title`
ignored:

| Payload | Change | Effect on the adapters |
|---|---|---|
| `InitializeParams` | optional `capabilities.explicitGatewayOauth` | none; the adapters send no capabilities |
| `InitializeResponse` | none | none |
| `ThreadStartParams` | none | none |
| `ThreadStartResponse` | optional `disabledPluginIds`; `ThreadItem` and `UserInput` as below | none; the new member is not read |
| `ThreadResumeParams` | history `ContentItem` image variant becomes `image_url` or `file_id` | none; only `threadId` is sent |
| `ThreadResumeResponse` | optional `collaborationMode`, `disabledPluginIds` | none; the new members are not read |
| `TurnStartParams` | optional `disabledPluginIds`; `UserInput` image variant becomes `url` or `fileId` | none; only `text` input is sent |
| `TurnStartResponse`, `TurnStartedNotification`, `TurnCompletedNotification`, `ItemStartedNotification`, `ItemCompletedNotification` | `ThreadItem.mcpToolCall` gains optional `mcpAppUi` | none; unknown members are ignored and `mcpAppUi` is not mapped |
| `TurnInterruptParams`/`Response`, `AgentMessageDeltaNotification`, `ThreadStatusChangedNotification` | none | none |
| every `ServerRequest` params and response (`item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/tool/requestUserInput`, `item/permissions/requestApproval`) | none | none |
| `JSONRPCMessage`, `JSONRPCRequest`, `JSONRPCError`, `RequestId` | none | none |

`Turn`, `TurnError`, `TurnStatus` and every other `ThreadItem` variant are
unchanged. `thread/rollback` was never called. The adapter code does not change,
and `internal/native` in Go and its Zig counterpart are unchanged.

The capability descriptor is unchanged except for the endpoint version it
reports, which a revision names, so the revision moves to
`codex-appserver-0.157.0-oap-v1`. There is one revision: `oapx` serves the Go
adapter's descriptor under it, and the catalog no longer carries a separate
`oapx_capability_revision`.

## Corpus

`fixtures/adapters/codex-appserver-0.157.0` carries every `native.jsonl`,
`mapping.json` and `omissions.json` of the 8d7cc24 corpus forward unchanged;
none was re-recorded, and the 8d7cc24 corpus itself was never a recording of a
live process. Each of the 41 wire frames across the thirteen cases was
validated as its `ServerNotification` or `ServerRequest` against both schema
trees: every frame draws the identical error set at both pins. Five are
schema-valid at both; the other 36 abbreviate a `Turn` or `ThreadItem`
(no `items`, no `commandActions`, and so on) and fail both identically, as they
did at 8d7cc24. `process-exit` has no wire frame. Only the expectations
changed, and only in the revision they carry.

`fixtures/adapters/codex-appserver-0.157.0-writes/conversation.json` was
re-recorded with `OAP_UPDATE_CODEX_CONVERSATION=1`: every frame is byte-identical
to the 8d7cc24 recording, and only `codex_commit`, `capability_revision` and
the descriptor's endpoint version changed.

## Real-process evidence

All runs used the darwin-arm64 binary above, a temporary `HOME` and
`CODEX_HOME` and no credentials; the Go and Zig runs answered model requests from a loopback Responses mock.

- **Handshake probe.** `initialize` with the adapters' exact frame answers
  `userAgent`, `codexHome`, `platformFamily`, `platformOs`; `thread/start`
  with `model`, `approvalPolicy`, `sandbox` answers a `thread` whose `id` is a
  UUID and emits `thread/started`. Before the response it emits
  `remoteControl/status/changed`, with the top-level `emittedAtMs` the
  8d7cc24 schema already declares; both adapters ignore it. `thread/rollback`
  is refused `-32600` as an unknown variant. `turn/interrupt` with a thread id
  that is not a UUID is refused `-32600`.
- **Go.** `TestPinnedCodexProcessAgainstResponsesMock` with
  `OAP_CODEX_INTEGRATION=1`, `OAP_CODEX_COMMIT=00c972ed…` and
  `OAP_CODEX_SHA256=ad0be20d…` passes: one run, completed, one Responses
  request.
- **Zig.** `goap conformance --command "oapx serve agent --backend codex"`,
  with `codex` on `PATH` linked to the binary, passes every check but the two
  model-switch checks, as at 8d7cc24; the run settles `run.completed` and the
  trace names `codex-appserver-0.157.0-oap-v1` and endpoint version
  `00c972ed…`.

Not re-run: approval, file-change, MCP and user-input turns against the real
process, which need a mock that scripts tool calls. The schema shows those
payloads unchanged.

## Model-provider settings at this pin

Codex names a model provider in `~/.codex/config.toml` under
`[model_providers.<id>]`, and selects it with the top-level `model_provider`
(default `openai`); `model` names the model. A layer is `$CODEX_HOME/<name>.config.toml`
under `-p/--profile`, and `-c key=value` overrides any key for one run with a
dotted path, so a child can be pointed at a provider without a config file at all.

| Key | What it sets |
| --- | --- |
| `model_providers.<id>.base_url` | the provider's API base URL |
| `model_providers.<id>.env_key` | the environment variable holding the API key |
| `model_providers.<id>.name` | display name |
| `model_providers.<id>.wire_api` | the protocol; `responses` is the only value, and the default |
| `model_providers.<id>.query_params`, `.http_headers`, `.env_http_headers` | extra query parameters and request headers |
| `model`, `model_provider` | the model id and which `model_providers` entry serves it |
| `openai_base_url` | base URL override for the built-in `openai` provider only |

Documented at [config reference](https://developers.openai.com/codex/config-reference);
`docs/config.md` at this tag defers to it. The built-in ids `openai`, `ollama`
and `lmstudio` are reserved and cannot be overridden, so a catalog row is routed
by a new id rather than by replacing one.

`wire_api` takes only `responses` at this pin, so only a row on the OpenAI
Responses wire is routable; `experimental_bearer_token` carries a token inline
and its own documentation discourages it in favour of `env_key`.
`--strict-config` errors on a key this version does not recognise, which is what
makes a config written for a later Codex fail loudly rather than route wrongly.

Verified at the pin: the installed `codex-cli 0.157.0` binary carries
`model_providers`, `base_url`, `env_key`, `wire_api`, `query_params` and
`env_http_headers`, and `"responses"` is the only `wire_api` value among its
strings.

## Session reload at 0.157.0

Decision 0039's evidence table cites the 8d7cc24 ledger for this row. Every
line below is read from the source at this pin's commit
`00c972ed5d6ff6499317fd41b7f23605b8e6850d`, and the last paragraph says what
that does and does not establish.

**A thread list exists, and it is a query, not a dump.** `thread/list` takes
`cursor` and answers `data` with `nextCursor` and `backwardsCursor`
(`ThreadListParams` / `ThreadListResponse` in
`codex-rs/app-server-protocol/schema/json/v2/`), and it filters on `archived`,
`cwd`, `searchTerm`, `sourceKinds`, `originators`, `modelProviders`,
`sortKey`/`sortDirection` and `useStateDbOnly`. The method set around it is
wider than resume: `thread/read`, `thread/items/list`, `thread/turns/list`,
`thread/loaded/list`, `thread/fork`, `thread/archive`, `thread/unarchive`,
`thread/delete`, `thread/unsubscribe`, `thread/compact/start`,
`thread/revert`, `thread/name/set`, `thread/metadata/update`,
`thread/goal/{get,set,clear}`, `thread/section/move`, `thread/shellCommand`,
`thread/attachment/{add,list,remove}` and
`thread/approveGuardianDeniedAction`. The *Schema diff* above names every
method this move added, and it is `account/gatewayOAuth/*` and
`thread/attachment/*` — the list and the resume a reattach needs are not among
them, so a reattach can enumerate the threads it may reattach to, and it can
do so before it has any id.

**A reload restores the conversation and the thread's configuration.**
`ThreadResumeParams` requires only `threadId` and offers `model`,
`modelProvider`, `cwd`, `sandbox`, `approvalPolicy`, `approvalsReviewer`,
`baseInstructions`, `developerInstructions`, `config`, `personality`,
`serviceTier` and `excludeTurns`. `ThreadResumeResponse` then *requires*
`approvalPolicy`, `approvalsReviewer`, `cwd`, `model`, `modelProvider`,
`sandbox` and `thread`, and adds `itemsBackwardsCursor` and
`turnsBackwardsCursor` with optional `collaborationMode`,
`disabledPluginIds`, `instructionSources`, `reasoningEffort` and
`serviceTier`. So unlike a harness that returns only the messages, Codex
answers a resume with the model, provider, working directory, sandbox and
approval policy the thread last ran under: those are not the resumed
process's configuration, they are the thread's. The path that produces them
is `request_processors::persisted_resume_settings::latest_persisted_resume_settings`,
which walks the rollout backwards for the last `TurnContext` or
`ThreadSettingsApplied` and takes the approval policy, the approvals reviewer
and the active permission profile from it.

**Where the store lives.** `codex_rollout` is the store
(`codex-rs/rollout/src/lib.rs`: "Rollout persistence and discovery for Codex
session files"), under the Codex home: `SESSIONS_SUBDIR` is `sessions` and
`ARCHIVED_SESSIONS_SUBDIR` is `archived_sessions`, and a file is named
`rollout-<YYYY-MM-DDTHH-MM-SS>-<threadId>.jsonl`, with an extra `_rolloutId`
appended for a reverted thread (`rollout_file_name::RolloutFileName`). The
recorder's own doc comment shows the form
(`~/.codex/sessions/rollout-2025-05-07T17-24-21-<uuid>.jsonl`). Alongside it
there is a SQLite state database (`state_db`, `sqlite_config`) and a
`session_index`, and a thread's history can be served from that index instead
of the file — `ThreadHistoryMode::Paginated` — which is what
`read_stored_thread_for_resume` checks after reading by rollout path. Rollout
names also go through `compression::parse_rollout_file_name`, so a rollout
need not be a plain `.jsonl` on disk.

**What it answers when the store is gone.** `thread_store_resume_read_error`
in `request_processors/thread_processor.rs` maps
`ThreadStoreError::ThreadNotFound` to `invalid_request("no rollout found for
thread id {thread_id}")` — JSON-RPC `-32602`, not a not-found code — and
`ThreadStoreError::Unsupported` to an unsupported-operation error. The Go
adapter surfaces that from `Open` as `resume Codex thread: …`, which is
neither `ErrSessionClosed` nor a typed refusal, so a client sees
`open_failed` (502). That is the shape Decision 0039 calls
`unsupported_feature`: the host had the binding, the harness cannot load the
thread, and a 502 is not the answer for it. This ledger records the answer,
not the fix; mapping it is [#443](https://github.com/lsm/open-agent-protocol/issues/443)'s
neighbour, not this step.

**What the adapter does with all of it.** `Config.ResumeThreadID` selects
`thread/resume` in `Open` and sends **only** `threadId`
(`go/adapter/codex/appserver/adapter.go`), then refuses a response whose
`thread.id` is empty or different with `ErrNativeProtocol`.
`TestOpenResumesExplicitNativeThread` pins that the resume is the one native
call an open makes. Nothing in the adapter reads the seven configuration
members the response requires, so a thread resumed under a different model,
sandbox or approval policy than the process was configured with is resumed
*silently* under the process's own settings — which is the one thing about
this row a reattach cannot inherit from `thread/resume` alone.

**What is not established here.** Every claim above is source-read at
`00c972ed…`, and this pin's real-process evidence (above) does not include a
resume: no corpus case drives `thread/resume`, and the only place the
fixtures name it is the `run.resume` capability's **reason** string
("thread/resume restores native attachment; canonical replay is bounded
process memory"), which is prose about the method rather than a frame from
it. So the *answers* above — the response's
shape, the store's layout, the error for a missing rollout — are read from the
source at this pin rather than observed from this pin's binary. The
`thread/rollback` refusal in that evidence is the one adjacent data point: a
method this move removed answers `-32600` as an unknown variant, so Codex
refuses an absent method with an invalid-request code rather than a
method-not-found one, which is the same shape as the missing-rollout answer.
