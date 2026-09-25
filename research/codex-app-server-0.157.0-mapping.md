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
  trace names `codex-appserver-0.157.0-oapx-v1` and endpoint version
  `00c972ed…`.

Not re-run: approval, file-change, MCP and user-input turns against the real
process, which need a mock that scripts tool calls. The schema shows those
payloads unchanged.
