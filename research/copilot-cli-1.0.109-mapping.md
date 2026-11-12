# GitHub Copilot CLI 1.0.109 mapping ledger

The adapter retains the mapping in [the 1.0.107
ledger](copilot-cli-1.0.107-mapping.md), subject to the differences below.
This is a schema-based upgrade, not a live recording. The new corpus carries
the 1.0.107 synthetic cases forward with moved provenance and revision; no
new case was authored. The old live probe belongs only to 1.0.98.

## Provenance

- [Release v1.0.109](https://github.com/github/copilot-cli/releases/tag/v1.0.109),
  published 2026-11-10, not a prerelease; tag commit
  `a73dd6977439f238e66e5328a131ef783fee30d4`, tree
  `2971d0b703abf0bb2ec4eaa23adb918c57361e29`.
- Release asset `github-copilot-1.0.109-darwin-arm64.tgz`: 100847453 bytes,
  SHA-256 `0e315203a2a90f78dd11def8b44c02083518ac7a94276aa56ca3584a630ff083`,
  downloaded and matched to the release asset digest.
- Its `package/schemas/api.schema.json` SHA-256 is
  `dc0f9b87b8d9892e0cd51652755763f2b1ad93ab72229c3ba30fc96b065389be`;
  `package/schemas/session-events.schema.json` SHA-256 is
  `9e5b3a761d62ee463640513bd83002027d6515babbf6cd57f82ab28a52ff19d1`;
  `package/schemas/lifecycle-diagnostics.schema.json` is byte-identical to
  1.0.107's (`6e6fa7ab402106d76d82daf8a8308d800ddf96b61c7c6cecb791c072cc21f18b`).

## Comparison and limits

The public CLI repository still does not contain the CLI implementation; the
release asset supplies the schemas, and both the 1.0.107 and 1.0.109 assets
were compared structurally (prose, titles and generator hints excluded).

The adapter's event data objects survive unchanged in shape:
`AssistantMessageData`, `AssistantMessageDeltaData`, `SessionIdleData`,
`SessionErrorData` and `AssistantUsageData` are identical, and no event
definition was removed. `UserMessageData` adds optional `clientTurnId`;
`ToolExecutionStartData` and `ToolExecutionCompleteData` add optional
`programToolCallId`. The native decoder holds event data as raw members read
by name, so the additions decode and are not read. The added event kinds
(`Decision*`, `Program*`, `AutoModeRecovery*`, `CompactionSummarized`,
`ExternalToolRequested.hasHandler`, `InfoData.responsesReasoning`, permission
extras) are surfaces this adapter does not project; `ExtensionsLoadedExtensionStatus`
gains a `dormant` value on an unprojected event. No `clientSession` method
the adapter could call was removed or changed; `clientSession.memoryStore`
and `clientGlobal.network` are new, and `clientGlobal.kerberosAuth` is gone,
none of them on the adapter's surface.

The 1.0.109 release notes describe sandbox CIDR rules, model-picker entries,
TUI fixes and an SDK file-attachment error path; the 1.0.108 notes add
`/index` commands and a device-file attachment error tag. None of these
changes the methods the adapter sends (`session.create`, `status.get`,
`session.send`), whose schema presence is unchanged. Checkpoint replay
behavior, protocol negotiation, real process behavior, authentication, and
model responses at 1.0.109 remain unverified. No login, provider call, or
real-account fixture was used in this upgrade.
