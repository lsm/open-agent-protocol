# Codex app-server 0.160.1 mapping ledger

Status: pinned implementation boundary, superseding
[`codex-app-server-0.160.0-mapping.md`](codex-app-server-0.160.0-mapping.md),
which records what moved from 0.157.0 over the full mapping in
[`codex-app-server-8d7cc24-mapping.md`](codex-app-server-8d7cc24-mapping.md).
This one records only what the move to 0.160.1 changed; everything it does not
mention carries over unchanged, including the live session settings and the
reopen rules recorded at 0.160.0.

## Provenance

- Repository: `https://github.com/openai/codex`
- Release: `rust-v0.160.1` (latest stable on 2026-10-06, published
  2026-10-05T18:29:37Z), workspace version `0.160.1`
- Commit: `d27764b82f7118f674371e6d6e76271d9d606edb` (the annotated tag
  object is `c3e23d4c4385619ecec78408766e46b7fa7dd9ad`), tree
  `1055b6282cb6b0a594bad187d291f717db9dd428`, committed 2026-10-05T17:16:58Z
- Generated schema tree SHA-256:
  `14e6b8ee86b3b1953702bd1ba2bca20594fa751669cc390a779817e589e699d2`, by the
  recipe of the previous ledgers over the tag's
  `codex-rs/app-server-protocol/schema`; 1050 files. The same recipe over
  `rust-v0.160.0` reproduces that ledger's digest, byte for byte the same.
- Release artifacts, digests from the release metadata and checked on
  download:
  - `codex-aarch64-apple-darwin.tar.gz`:
    `670af2b049d9c95afb74d7da385f30c5033d13a07175001dd8958c51944984d0`
    (95904314 bytes), holding the binary `codex-aarch64-apple-darwin`:
    `09fa44fdc37a5fc70dc1ace31235f90468a2e193d0e85f7552eab068ea2582be`
    (241556032 bytes), which reports `codex-cli 0.160.1`. `codex update`
    installs the same binary.
  - `codex-x86_64-unknown-linux-musl.tar.gz`:
    `9226581be592d18f7e7f740a352fdb63aa61e45e39f7eb9b09d3888c84bba33f`
    (109327135 bytes), from release metadata only; not downloaded or run

## What moved

Two commits and two files separate the tags: the workspace version in
`codex-rs/Cargo.toml`, and the exec server's child environment, which now keeps
`SYSTEMROOT`, `TEMP` and `TMP` for a Windows executor. Nothing under
`codex-rs/app-server-protocol` changed, so the schema digest above is the
0.160.0 one and `src/rpc.rs` is byte-identical.

The adapters do not change. The capability descriptor changes only in the
endpoint version it reports, which the revision names, so the revision moves
to `codex-appserver-0.160.1-oap-v2`, served by both trees.

## Corpus

`fixtures/adapters/codex-appserver-0.160.1` carries every `native.jsonl`,
`mapping.json` and `omissions.json` of the 0.160.0 corpus forward unchanged;
none was re-recorded. Only `manifest.json` (the commit) and the expectations'
revision changed.

`fixtures/adapters/codex-appserver-0.160.1-writes/conversation.json` was
re-recorded with `OAP_UPDATE_CODEX_CONVERSATION=1`: every frame is
byte-identical to the 0.160.0 recording, and only `codex_commit`,
`capability_revision` and the descriptor's endpoint version changed.

## Real-process evidence

- **Go.** `TestPinnedCodexProcessAgainstResponsesMock` and
  `TestPinnedCodexProcessTakesALiveReasoningLevel`, with
  `OAP_CODEX_INTEGRATION=1`, the darwin-arm64 binary above,
  `OAP_CODEX_COMMIT=d27764b8…` and `OAP_CODEX_SHA256=09fa44fd…`, pass 3x each
  against the loopback Responses mock, with a temporary `HOME` and no
  credentials.
- **Zig.** `oapx serve --stdio` with a `codex` entry running the same binary
  under the user's own login and default model (`gpt-6.1-sol`): `work.start`
  ran a turn to `run.completed`, and after `serve` restarted, `work.read`
  answered both turns from `thread/turns/list`. The same model was refused at
  0.157.0 ("not supported when using Codex with a ChatGPT account"), which is
  what prompted this move.

Not re-run: approval, file-change, MCP and user-input turns against the real
process; the schema and wire are unchanged.
