# Claude Code / Claude Agent SDK 2.1.322 mapping ledger

This pin advances the adapter from
[2.1.319](claude-code-agent-sdk-2.1.319-mapping.md). Prior ledgers remain
authoritative for unchanged mappings. The corpus is
`fixtures/adapters/claude-code-2.1.322`, a synthetic adaptation of the prior
corpus, not a capture of 2.1.322; its `credential_expires_at` values are
invented. No account, credential, or real model was used.

The `@anthropic-ai/claude-code@2.1.322` and
`@anthropic-ai/claude-agent-sdk@0.3.322` npm tarballs and their linux-x64 and
darwin-arm64 CLI artifacts were downloaded and pinned by their computed
SHA-256 values in `harnesses/claude-code.json`; the darwin-arm64 wrapper
`cli-wrapper.cjs` is byte-identical to 2.1.319's. The v2.1.322 tag resolves
to build commit `6e1e44f6433bfa17d198e932bff53373aa229eea`, dated
2026-11-10T02:29:21Z. Python SDK v0.2.180 (commit
`97102885fa5539f3d9459141807117477286dc8b`, tree
`adb45adeb4fe6768c5595897b3ff8ffaecd1b477`) differs from v0.2.179 only in
`pyproject.toml`; every pinned source blob is byte-identical, but its
`_cli_version.py` now declares the bundled CLI as 2.1.321, not 2.1.322. The
artifact and source hashes in the manifest and corpus are provenance, not
live behavioral proof.

A comparison of the published TypeScript SDK `sdk.d.ts` for 0.3.319 and
0.3.322 finds no interface or type member changes: the 18 changed lines are
documentation prose (per-server MCP settings describing
`autoModeVerdictTools`, an interrupt `cancelled` field exception behind a
remote-session flag, login-provider policy wording, a Vertex fast-mode note)
and blank lines. No `SDKMessage` union member was added or removed, and no
result field changed. Declaration comparison alone still does not prove
execution order or runtime behavior.

The [2.1.321 release
notes](https://github.com/anthropics/claude-code/releases/tag/v2.1.321)
describe a Fable 5.2 model addition, a `remote-control --settings` flag, and
permission and sandbox fixes (an MCP OAuth redirect sending secrets to
another host, a read-only Bash command running outside the sandbox, Bash
permission checks judging a write from the wrong folder, deny rules and
protected-file prompts for `cp`/`mv` targets); 2.1.322 adds fast mode on
Vertex. The permission and sandbox fixes may change when upstream asks for
permission; they are not established as wire-compatible by this synthetic
corpus. Credential-free real-process verification and a complete comparison
of any new event kinds remain unverified.

## Artifact and source digests

- `cli` / `any` / `@anthropic-ai/claude-code@2.1.322`: `39f9d22aa2938cf276494d46b0c2bb593b4ef923323d02bf3cc5805b9d9a71c9` (31606 bytes)
- `cli` / `any` / `cli-wrapper.cjs`: `6815b1e3ca9f700b325e642b6b2434ac90190bd2b453b6a3fad27bcc79b576a5` (unchanged from 2.1.319)
- `cli` / `linux-x64` / `@anthropic-ai/claude-code-linux-x64@2.1.322`: `3cc175d82980c20ab66057d345bd2d913244f8cf1791d10722a1225214b57219`
- `cli` / `linux-x64` / `claude`: `bfb3703ad5380242328459bc62ada4bde8ae7ba57c6e1eeadb9c00e08874c478` (341914128 bytes)
- `cli` / `darwin-arm64` / `@anthropic-ai/claude-code-darwin-arm64@2.1.322`: `906bde9402dfe0d2a38e931f582b7c831addef31b02b4dfa5bf05047075f4768`
- `cli` / `darwin-arm64` / `claude`: `a9d237949db816a0b7afd88a9a8e1df36b3a597a30f5a41d1c0539eb180a3450` (323702464 bytes)
- `typescript-sdk` / `any` / `@anthropic-ai/claude-agent-sdk@0.3.322`: `5db6881a4da75539d8b090f0a4b791043f9cbdbae439e5f9cf27356b28c59a93`
- `typescript-sdk` / `any` / `sdk.d.ts`: `c228b60931a217ea96e222ef41f47a0a83bee0a394d1735e3eb668e3f8838b2f`
- `cli` commit `6e1e44f6433bfa17d198e932bff53373aa229eea`
- `typescript-sdk` commit `2007ac4092a58963176cb9b1a583a1c2ed4fc455` tree `d41ce6d23963943324b249aeaab2268281583b62`
- `python-sdk` commit `97102885fa5539f3d9459141807117477286dc8b` tree `adb45adeb4fe6768c5595897b3ff8ffaecd1b477` (`pyproject.toml` blob `7488c33e92558624533acbec61d1fbcb4f322400`; all other pinned blobs unchanged)

The `system/session_writes_lost` loss-detection gap recorded at 2.1.319
remains: this upgrade does not verify or repair it.
