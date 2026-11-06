# OpenCode v2.2.6 mapping ledger

This moves the current pin over [v2.0.32](opencode-v2.0.32-mapping.md); the
older ledgers retain the remaining mapping and its known losses.

## Provenance and release

The stable v2 git tag `v2.2.6` resolves to commit
`dc2ffcf8bf26f9e669b6aa22b95f50e12be15e06`, tree
`bb6cdba40a593a0281e3ef5ea38deee9019580ed`. Its upstream release workflow built
CLI and npm packages and published the [v2.2.6 release](https://github.com/anomalyco/opencode/releases/tag/v2.2.6).
The v1 `opencode-ai` npm package and GitHub's latest-release selector track a
different line; they are not the source of this v2 pin. The standalone archives
also appear under `https://opencode.ai/files/bin/2.2.6/`, the installer's channel.
Downloaded release archives match both GitHub asset digests and `SHA256SUMS`.

| Platform | Kind | SHA-256 | Bytes |
| --- | --- | --- | ---: |
| linux-x64 | archive | `08bb21d6326ce740d91cce7ebe88a6536933f6e1e30353b91a06b01f25da306c` | 121793402 |
| linux-x64 | binary | `50503d24577fa4117a7c95ee85b2cd679606a87eacf20ca54ed5db5c9e1cf895` | 258824368 |
| darwin-arm64 | archive | `15491608f6b0bc4debe5eac35eab629a2b07ea9a1fbac68ad53bcea59fcc4b45` | 108306944 |
| darwin-arm64 | binary | `90e4d03597b03d2051b65b4d793467764e55bacd8e22bf4824479c7233ea0e15` | 231791968 |

The macOS binary is signed by team `5NZ4Q7NXJ4`, matches the binary in
`@opencode/cli-darwin-arm64@2.2.6`, and reports `opencode v2.2.6`. No account,
login, external model, or model call was used for these checks.

## Wire changes

Source comparison of `packages/protocol/src/groups/{event,session,server}.ts`,
`packages/server/src/handlers/{event,session,server}.ts`, and
`packages/schema/src/{session-event,session-inbox,session-message,session}.ts`
shows:

- `session.archived` and `session.unarchived` are new durable session events.
  The adapters accept their positions and observe them without projecting a run
  event. Rust's native decoder recognizes them too.
- Step ended/failed, reasoning started/ended, text ended and tool called/success/
  failed replace optional `providerState`, `state`, or `resultState` with
  optional `native`. Go and Zig accept both generations; neither maps that
  opaque data into OAP. Compaction events remove `recent` and replace provider
  fields with `native`; these events remain observed only.
- Session info gains optional `time.busy`, and server info gains optional
  `hostname`. Strict HTTP decoders accept these fields. The session list gains
  an archive filter and session update gains an archive flag; the adapters send
  neither. The opt-in experimental media URI response is not requested.
- `/api/event` gains an optional `types` filter; omission continues to subscribe
  to all events. The adapter does not use this filter. v2.2.5 also accepts a
  percentage for `compaction.buffer`; the adapter does not write that config.

These source comparisons do not prove every upstream API outside the adapter's
surface. Run usage and the previously documented compaction and title losses are
unchanged by this mapping.

## Corpus

The corpus carries forward the v2.0.24-derived cases. The old live `completed-text` recording is replaced by a fully synthetic
case; the other synthetic cases have `native` values added to applicable
frames to exercise the new field. Provenance records the v2.2.6 source blobs; this is not
a claim that the frames were captured from a v2.2.6 account or model. The first
synthetic `completed-text` case failed before the decoder change with
`opencode_invalid_text_event` on the unknown `native` field. Expectations use
the new capability revision. Rust tests read the same current corpus.
