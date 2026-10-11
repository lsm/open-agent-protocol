# Codex app-server 0.162.1 mapping ledger

Status: pinned implementation boundary, superseding
[`codex-app-server-0.160.1-mapping.md`](codex-app-server-0.160.1-mapping.md),
over [`codex-app-server-0.160.0-mapping.md`](codex-app-server-0.160.0-mapping.md)
and the full mapping in
[`codex-app-server-8d7cc24-mapping.md`](codex-app-server-8d7cc24-mapping.md).
This one records only what the move to 0.162.1 changed; everything it does not
mention carries over unchanged, including the live session settings and the
reopen rules recorded at 0.160.0.

## Provenance

- Repository: `https://github.com/openai/codex`
- Release: `rust-v0.162.1` (latest stable on 2026-10-10, published
  2026-10-09T19:44:51Z), workspace version `0.162.1`
- Commit: `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d` (the annotated tag
  object is `7a078f6bb4ce0dc2a81408b334ce52979d92c603`), tree
  `572cdecd45cd985f0ad80b63d8b86ffcb345f47a`, committed
  2026-10-09T10:39:05-07:00
- Generated schema tree SHA-256:
  `075997d5cb497bd6518a973a6d7b85fef0eaa64c02428af866e46c725c55fad7`, by the
  recipe of the previous ledgers over the tag's
  `codex-rs/app-server-protocol/schema`; 1062 files. The same recipe over
  `rust-v0.160.1` reproduces that ledger's digest
  (`14e6b8ee86b3b1953702bd1ba2bca20594fa751669cc390a779817e589e699d2`, 1050
  files), so the two digests differ only by the release.
- Release artifacts, digests from the release metadata and checked on
  download:
  - `codex-aarch64-apple-darwin.tar.gz`:
    `b293a0fcdd871900a44c0a4f40a1aa401b31e02854f790ebdb8bd5d1177fd1fe`
    (98104184 bytes), holding the binary `codex-aarch64-apple-darwin`:
    `74c6a6d263f40d65f99c27a815275f5b4ed0bcc2baecc396b4cf3255498bdfc0`
    (246642624 bytes), which reports `codex-cli 0.162.1`
  - `codex-x86_64-unknown-linux-musl.tar.gz`:
    `86f268d81b898f3e144c5ecff2ad2fda2c5802e042d6195b72538fe0fa125057`
    (111350218 bytes), holding `codex-x86_64-unknown-linux-musl`:
    `628e35487d888a28f0427fc37501da529dd3d81fcbad7a2c3421c1e8670d92e1`
    (294415048 bytes); downloaded and hashed, not run

## What moved

382 commits separate the tags. Under `codex-rs/app-server-protocol`,
`src/rpc.rs` is byte-identical, so the transport is unchanged;
`src/protocol/common.rs` and the generated schema changed. For the v2 surface
the adapters touch:

- `Turn` gains `rootTurnId`, and `TurnStartParams` gains `parentTurnId` and
  `rootTurnId`, for attributing delegated work; both optional.
- The sub-agent activity record gains `model` and `reasoningEffort`.
- `MessagePhase` gains `partial_answer`, stable answer text that more output
  may follow, beside `commentary` and `final_answer`.
- `codexErrorInfo` becomes an `anyOf` that also admits a bare string or
  object, and the turn error's `misalignment` details gain an optional
  `reviewTarget`.
- `ModelProviderCapabilitiesReadResponse` drops `namespaceTools`.
- New: `thread/attachmentOwner/list` (params and response), the
  `thread/prediction/updated` notification, and TypeScript types for them.

None of it reaches either adapter. Neither sends `parentTurnId` or
`rootTurnId`, nor reads them, the sub-agent record or `namespaceTools`. Both
keep `codexErrorInfo` as raw JSON. A native read takes a turn's last
`agentMessage` text whatever its phase, so `partial_answer` does not change
which text a reply is. Unknown members and methods are ignored, as before.

The adapters do not change. The capability descriptor changes only in the
endpoint version it reports, which the revision names, so the revision moves
to `codex-appserver-0.162.1-oap-v1`, served by both trees.

## Corpus

`fixtures/adapters/codex-appserver-0.162.1` carries every `native.jsonl`,
`mapping.json` and `omissions.json` of the 0.160.1 corpus forward unchanged;
none was re-recorded. Only `manifest.json` (the commit and the schema digest)
and the expectations' revision changed.

`fixtures/adapters/codex-appserver-0.162.1-writes/conversation.json` was
re-recorded with `OAP_UPDATE_CODEX_CONVERSATION=1`: every frame is
byte-identical to the 0.160.1 recording, and only `codex_commit`,
`capability_revision` and the descriptor's endpoint version changed.

## Real-process evidence

- **Go.** `TestPinnedCodexProcessAgainstResponsesMock` and
  `TestPinnedCodexProcessTakesALiveReasoningLevel`, with
  `OAP_CODEX_INTEGRATION=1`, the darwin-arm64 binary above,
  `OAP_CODEX_COMMIT=092d3acd…` and `OAP_CODEX_SHA256=74c6a6d2…`, pass 3x each
  against the loopback Responses mock, with a temporary `HOME`, no
  credentials, and `sandbox-exec` denying the keychain daemons and every
  non-loopback connection.

Not re-run: a turn under a real login, and approval, file-change, MCP and
user-input turns against the real process; the transport is unchanged and the
schema changes above touch none of those frames' members the adapters read.

## Model-provider settings at this pin

The 0.160.1 ledger's *Model-provider settings* section holds at 0.162.1,
checked the same way against the darwin-arm64 binary above (empty
`CODEX_HOME`, `--strict-config`, `app-server --listen stdio://` on a closed
stdin): the routed `-c` settings `oapx` writes for a codex entry's
`model_provider` load and exit 0, the same entry with `base_ulr` and no `name`
is refused under `model_providers`, and `wire_api="chat"` is refused at
`model_providers.oapx-openai.wire_api`. `wire_api` still takes only
`responses`.
