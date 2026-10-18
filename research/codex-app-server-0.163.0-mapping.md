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
moves to `codex-appserver-0.163.0-oap-v1`, served by both trees.

## Corpus

`fixtures/adapters/codex-appserver-0.163.0` carries every `native.jsonl`,
`mapping.json` and `omissions.json` of the 0.162.1 corpus forward unchanged;
none was re-recorded. Only `manifest.json` (the commit and the schema digest)
and the expectations' revision changed.

`fixtures/adapters/codex-appserver-0.163.0-writes/conversation.json` was
re-recorded with `OAP_UPDATE_CODEX_CONVERSATION=1`: every frame is
byte-identical to the 0.162.1 recording, and only `codex_commit`,
`capability_revision` and the descriptor's endpoint version changed.

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
