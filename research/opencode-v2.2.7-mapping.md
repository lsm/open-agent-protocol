# OpenCode v2.2.7 mapping ledger

This moves the current pin over [v2.2.6](opencode-v2.2.6-mapping.md); the
older ledgers retain the remaining mapping and its known losses.

## Provenance and release

The stable v2 git tag `v2.2.7` resolves to commit
`fedc6f208aa2bd54c1019227214568083f6380ad`, tree
`ff57370148d7897dd17de9f491e4d2499526ebeb`. Its upstream release workflow
built CLI and npm packages and published the [v2.2.7
release](https://github.com/anomalyco/opencode/releases/tag/v2.2.7).

Both standalone release archives were downloaded and verified against the
GitHub asset digests; each holds a single `opencode` binary, and the
darwin-arm64 binary reports `opencode v2.2.7`. No account, login, external
model, or model call was used for these checks.

| Platform | Kind | SHA-256 | Bytes |
| --- | --- | --- | ---: |
| linux-x64 | archive | `89a6cee227b54bc011179c528a5e63176032462b1a5e21723b616801cddc2ca7` | 121783361 |
| linux-x64 | binary | `589253d36810f21217edd51545cd435b328cdc3cf4f451fbe0cce6f88afcf5e1` | 258848944 |
| darwin-arm64 | archive | `811c42be9cad489ea8c411ead49e06f691deee4957ba9a0a56a159a7d6c1abdc` | 108290453 |
| darwin-arm64 | binary | `b9d576000191d7f441198076f6d50a843064259709ed31f7de995cbfc540d85f` | 231824800 |

## Wire changes

Source comparison of the adapter's pinned surface between the two tags —
`packages/schema/src/session-event.ts`,
`packages/schema/src/session-inbox.ts`,
`packages/core/src/session/execution.ts`,
`packages/protocol/src/groups/session.ts`,
`packages/server/src/handlers/session.ts` and
`packages/core/src/session.ts` — shows:

- `session.usage.recorded` widens its `source` literal from
  `title | compaction` to `title | compaction | generate`. No tree decodes
  that durable event (usage reaches the adapters through message parts and
  step results), so nothing on the adapter's surface observes the change.
- The `generate` endpoint now returns a result object carrying text and usage
  instead of a bare string, and its usage counts toward the session totals.
  Neither adapter calls `generate`, so the handler result shape is not on the
  adapter's surface.
- `session-inbox.ts` and `core/src/session/execution.ts` are byte-identical
  at both tags. The remaining edits in `groups/session.ts`,
  `handlers/session.ts` and `core/src/session.ts` are internal service
  wiring (`App.Metadata` to `App.Service`, blob enablement, bus provision).

The v2.2.7 release notes describe TUI-level changes (a Copilot Auto model
picker entry, side-panel tab shortcuts, mark-read); none touch the server
API the adapter drives. These source comparisons do not prove every upstream
API outside the adapter's surface.

## Corpus

The corpus carries the v2.2.6 cases forward unchanged in what they replay,
with provenance moved to the v2.2.7 tag and the four changed source blobs,
and every expectation now carries `opencode-v2.2.7-oap-v5`. No case was
re-recorded, since no surface the adapter uses changed.
