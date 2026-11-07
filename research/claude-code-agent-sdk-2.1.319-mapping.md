# Claude Code / Claude Agent SDK 2.1.319 mapping ledger

This pin advances the adapter from [2.1.302](claude-code-agent-sdk-2.1.302-mapping.md). Prior ledgers remain authoritative for unchanged mappings. The corpus is `fixtures/adapters/claude-code-2.1.319`. It is a synthetic adaptation of the prior corpus, not a capture of 2.1.319; its `credential_expires_at` values are invented. No account, credential, or real model was used. In particular, the synthetic fixture does not establish which optional fields the executable emits in a given run.

The `@anthropic-ai/claude-code@2.1.319` and `@anthropic-ai/claude-agent-sdk@0.3.319` npm tarballs and their linux-x64 and darwin-arm64 CLI artifacts are pinned by their computed SHA-256 values in `harnesses/claude-code.json`. The SDK manifest identifies build `fbdd7e02e8530e4c2ade8cc67d30104e53d2c8f2`, dated 2026-11-05T19:38:09Z; its two binary digests match the downloaded binaries. Python SDK v0.2.179 declares the same bundled CLI version. The artifact and source hashes in the manifest and corpus are provenance, not live behavioral proof.

A comparison of published TypeScript SDK `sdk.d.ts` for 0.3.302 and 0.3.319 finds that the `SDKMessage` union adds only `SDKSessionWritesLostMessage` and removes no members. At the top level of `SDKResultSuccess` and `SDKResultError`, the only added field is optional `credential_expires_at`, and no fields were removed; nested types were not fully compared. The synthetic settlement case supplies it; both adapters should ignore it and preserve the existing OAP settlement, not publish credential metadata. The 0.3.319 declarations also add optional pending elicitation information to initialization and expand interrupt receipt descriptions to cover messages read but not yet queued. Changes in queued-message timing require separate behavioral investigation; declarations alone do not prove execution order. Other declaration differences were not exhaustively adjudicated.

The [2.1.319 release notes](https://github.com/anthropics/claude-code/releases/tag/v2.1.319) describe changed auto-mode decisions when telemetry or feature flags are off and backgrounding MCP tasks. The adapter does not select auto mode or implement an MCP task control channel; these changes may affect when upstream asks for permission and reports tool outcomes. They are not established as wire-compatible by this synthetic corpus. Credential-free real-process verification and a complete comparison of new event kinds remain unverified.

## Artifact and source digests

- `cli` / `any` / `@anthropic-ai/claude-code@2.1.319`: `733a926c19df1c84f35a8e4285a637edce7bf2de13672cfe13a1397efd0bf823`
- `cli` / `any` / `cli-wrapper.cjs`: `6815b1e3ca9f700b325e642b6b2434ac90190bd2b453b6a3fad27bcc79b576a5`
- `cli` / `linux-x64` / `@anthropic-ai/claude-code-linux-x64@2.1.319`: `9b7b53954b8ffb886fafeefe9ef1e0e9e5c15e8aaf2f54d65ba64d0cca795dc7`
- `cli` / `linux-x64` / `claude`: `dabafec82ca1319fa43d8be36a10fc61b1d11821cb98ad5df04ad95e199b8eed`
- `cli` / `darwin-arm64` / `@anthropic-ai/claude-code-darwin-arm64@2.1.319`: `15d4176d3186db97836842e2c903c4a8d614246d8cb19b7bef2e5a2a4ed86e16`
- `cli` / `darwin-arm64` / `claude`: `1f53587e92dee8e089fa349188a4cda64fa34ace6e7b0ca2ee278ddaa3cc2ebd`
- `typescript-sdk` / `any` / `@anthropic-ai/claude-agent-sdk@0.3.319`: `6bfb9ccb6a5d15686f5c7dd12d7a7da6d7b486ca6e1331ac8a7ab98a2d302001`
- `typescript-sdk` / `any` / `sdk.d.ts`: `89b188a33abd97ed31fdaaee8e78b3b73855b0d8cec9d349d21141cae4941182`
- `cli` commit `fbdd7e02e8530e4c2ade8cc67d30104e53d2c8f2`
- `typescript-sdk` commit `49dd8ef10dd5cb012dc9fe10703376489850c36c` tree `e368e7942a0b4fe1130cb991f02e93a9b65e31ed`
- `python-sdk` commit `2209f2c134fee914c0f75733b631a5fc238ca846` tree `ce678b4efb371eda296626a5ebb98f6ffef1dffd`

The SDK message union additionally introduces `system/session_writes_lost` after the last result when transcript or global configuration writes were lost. Both existing adapter decoders treat unrecognized system subtypes as observations with no OAP projection. Consequently this warning is not surfaced, and a later `--resume` may load an incomplete transcript. The upgrade does not verify or repair this loss-detection gap; it must not be interpreted as a guarantee of complete recovered history.

A credential-free process smoke was run against the downloaded darwin-arm64 binary with its SHA-256 bound, a temporary home and config, a `security` stand-in, and a macOS sandbox denying all network and securityd/trustd lookup. The initialize exchange and EOF teardown passed; this does not exercise an actual model turn, permission decision, MCP task, transcript loss, or result message.
