# Codex app-server 0.160.0 mapping ledger

Status: pinned implementation boundary, superseding
[`codex-app-server-0.157.0-mapping.md`](codex-app-server-0.157.0-mapping.md),
which in turn records what moved from
[`codex-app-server-8d7cc24-mapping.md`](codex-app-server-8d7cc24-mapping.md),
the full mapping. This one records only what the move to 0.160.0 changed;
everything it does not mention carries over unchanged.

## Provenance

- Repository: `https://github.com/openai/codex`
- Release: `rust-v0.160.0` (latest stable on 2026-10-03, published
  2026-10-01), workspace version `0.160.0`
- Commit: `a956835d020762cb2b570053af06f643a11c0ecc`, tree
  `c09968bc06fc543c9d8bd9ffbaaa72829b31f5d9`, committed 2026-10-01T17:13:37Z
- Generated schema tree SHA-256:
  `14e6b8ee86b3b1953702bd1ba2bca20594fa751669cc390a779817e589e699d2`, by the
  recipe of the previous ledgers (`find codex-rs/app-server-protocol/schema -type f -print0 | sort -z | xargs -0 shasum -a 256 | shasum -a 256`)
  over the tag's tree (`git archive rust-v0.160.0 codex-rs/app-server-protocol/schema`);
  1050 files. The same recipe over `rust-v0.157.0` reproduces that ledger's
  `8ab8212e…` (1056 files), so the two digests are comparable.
- Release artifacts, digests from the release metadata and checked on
  download:
  - `codex-aarch64-apple-darwin.tar.gz`:
    `07c3c7ca376a8f791115342f53138dda37e97cfa29b8125d0652d93784894b5d`
    (95893648 bytes), holding the binary `codex-aarch64-apple-darwin`:
    `112fae7a5a1223e673c8a1791d32338f37df8b527ff1159bb8adac6c4dbf1b4b`
    (241555024 bytes), which reports `codex-cli 0.160.0`
  - `codex-x86_64-unknown-linux-musl.tar.gz`:
    `306865417d4ee7a927785852910a527f41e1e159add390ac5ae3accb67d44a13`
    (109304578 bytes), from release metadata only; not downloaded or run

## Wire boundary

Unchanged. `codex-rs/app-server-protocol/src/rpc.rs` is byte-identical at both
pins.

## Schema diff

Of the schema files, 47 differ, mostly plugin listings removed and a thread
item listing reworked. Method sets are identical at both pins:

| Set | 0.157.0 | 0.160.0 | Added | Removed |
|---|---|---|---|---|
| `ClientRequest` | 104 | 104 | none | none |
| `ServerNotification` | 83 | 83 | none | none |
| `ServerRequest` | 10 | 10 | none | none |
| `ClientNotification` | 1 | 1 | none | none |

Per payload the adapters read or write, with `description` and `title`
ignored:

| Payload | Change | Effect on the adapters |
|---|---|---|
| `TurnStartResponse`, `TurnStartedNotification`, `TurnCompletedNotification`, `ThreadStartResponse`, `ThreadResumeResponse`, `ErrorNotification` | `CodexErrorInfo` gains `flexUnavailable` and `tooManyDenials` | none; `codexErrorInfo` is carried opaque and never mapped |
| `Turn.error` | description only: "Error associated with a failed or interrupted turn" (was "only populated when the Turn's status is failed") | none; an `interrupted` turn settles `run.cancelled` and its error, if any, is not read |
| `AccountUpdatedNotification` and account responses | plan type gains `promax` | none; account payloads are not read |
| `ListMcpServerStatusParams`, `ThreadItemsListParams`, plugin responses | new optional `serverName`; a cursor or item anchor; plugin entry points, icons, quick actions and search providers removed | none; never sent or read |

The adapter code does not change, and `internal/native` in Go and its Zig
counterpart are unchanged. The capability descriptor is unchanged except for
the endpoint version it reports, which a revision names, so the revision moves
to `codex-appserver-0.160.0-oap-v1`, served by both trees, until the live settings below moved it to `v2`.

## Corpus

`fixtures/adapters/codex-appserver-0.160.0` carries every `native.jsonl`,
`mapping.json` and `omissions.json` of the 0.157.0 corpus forward unchanged;
none was re-recorded. Only `manifest.json` (commit and schema digest) and the
expectations' revision changed.

`fixtures/adapters/codex-appserver-0.160.0-writes/conversation.json` was
re-recorded with `OAP_UPDATE_CODEX_CONVERSATION=1`: every frame is
byte-identical to the 0.157.0 recording, and only `codex_commit`,
`capability_revision` and the descriptor's endpoint version changed.

## Real-process evidence

All runs used the darwin-arm64 binary above, a temporary `HOME` and
`CODEX_HOME` and no credentials, and answered model requests from a loopback
Responses mock.

- **Go.** `TestPinnedCodexProcessAgainstResponsesMock` with
  `OAP_CODEX_INTEGRATION=1`, `OAP_CODEX_COMMIT=a956835d…` and
  `OAP_CODEX_SHA256=112fae7a…` passes 3x: one run, completed, one Responses
  request.
- **Zig.** `oapx serve agent --backend codex`, with `codex` on `PATH` a
  wrapper that points `CODEX_HOME` at the mock's config and execs the binary,
  answers `protocol.initialize`, opens a session and settles a submitted run
  `run.completed` with the fixture text; every envelope names
  `codex-appserver-0.160.0-oap-v1`.

Not re-run: approval, file-change, MCP and user-input turns against the real
process, which need a mock that scripts tool calls. The schema shows those
payloads unchanged.

## Live session settings

Decision 0045's `session.settings.update.request` is served for
`reasoning_level` only, by both trees:

- `TurnStartParams.effort` is documented as overriding the effort "for this
  turn and subsequent turns". The adapter keeps an accepted level and sends it
  as the next `turn/start`'s `effort` (`off` as `none`, as at open), then stops
  sending it once a turn is admitted. A `turn/start` Codex refuses keeps it for
  the next one.
- `compaction_policy` is refused `unsupported_feature` (unadvertised): the
  limit is `model_auto_compact_token_limit` in `thread/start`'s config, and no
  turn-level parameter or thread request at this pin changes it.
  `config/value/write` writes the user's `config.toml`, which is not a session
  setting.
- Refused: a busy session (`run_active`) and another session's id
  (`run_not_found`).

Codex validates nothing about the value: against the Responses mock, with the
model's metadata unknown, `high`, `xhigh`, `max` and an invented `bogus` all
reached the request's `reasoning.effort` verbatim, and a turn that omitted
`effort` after one that sent `high` still asked for `high`. The adapter takes
the same levels it takes at open.

`TestPinnedCodexProcessTakesALiveReasoningLevel` (`OAP_CODEX_INTEGRATION=1`,
with the binary digest above) updates the level to `high` and runs two turns;
both Responses requests ask for effort `high`. It passed 3x.
`oapx serve agent --backend codex` served the same update, refused a live
policy, and ran both turns at `high`; the trace validates.

`session.reasoning` adds `session_live`, so the revision moves to
`codex-appserver-0.160.0-oap-v2`. The writes conversation was re-recorded with
`OAP_UPDATE_CODEX_CONVERSATION=1`; only its descriptor and revision changed.
