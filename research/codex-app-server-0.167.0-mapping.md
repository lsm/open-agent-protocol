# Codex app-server 0.167.0 mapping ledger

This pin supersedes [0.163.0](codex-app-server-0.163.0-mapping.md); its
unmodified mappings carry forward. The upstream tag `rust-v0.167.0`, published
2026-11-06, resolves to `086ccc833bc9cf01024ad2dd6af7dbd421b6bf9a`, tree
`930790b05c58ba0be420a4abe240d206f5dfd48e`. The generated schema tree digest,
using the original ledger's sorted path and SHA-256 recipe, is
`15a28c10689057d59a02fc041bd2841a1b733b7021e8256857ca7627b9cde956`.
Release metadata gives darwin-arm64 archive SHA-256
`4b22a2a971be2791c3e31f912f3e77a88ebd64a65812a7289ccba65dd6425fe9`
(108379331 bytes) and linux-x64 musl archive SHA-256
`9d64836106d5b374e90ee4454c7ebcde7ca7106558bd7e41a34b9f26ebda251a`
(122587028 bytes). Archives and binaries were not downloaded or run.

## Schema comparison

The generated schema diff from 0.163.0 to 0.167.0 has 92 changed files.
The classification below applies to the adapter, not arbitrary clients. JSON
schemas embed the same definitions in each response, so repeated copies are
listed together. Unknown fields are ignored by Go and Zig; neither adapter
opts into experimental API or sends audio, review or hook requests.

- `ClientRequest`, `FeedbackSaveParams`, `FeedbackSaveResponse`: feedback/save
  is additive and unused. `ServerNotification`, `ServerNotificationEnvelope`,
  `ThreadSectionChangedNotification`: section notification is additive and
  ignored. `ServerRequest`, `CommandExecutionRequestApprovalParams` and
  `CommandExecutionApprovalKind`: `humanCommand` is an additional kind gated
  by the optional `requiredHumanApproval` initialize capability, which this
  adapter does not enable. `InitializeCapabilities` and `v1/InitializeParams`
  carry that optional capability.
- `ThreadItem`, `ItemStartedNotification`, `ItemCompletedNotification`,
  `ThreadForkResponse`, `ThreadItemsListResponse`, `ThreadListResponse`,
  `ThreadMetadataUpdateResponse`, `ThreadReadResponse`, `ThreadResumeResponse`,
  `ThreadRevertResponse`, `ThreadStartResponse`, `ThreadStartedNotification`,
  `ThreadTurnsListResponse`, `ThreadUnarchiveResponse`, `TurnCompletedNotification`,
  `TurnStartResponse`, `TurnStartedNotification` and `ReviewStartResponse`
  embed item changes. Item `startedAtMs` and `completedAtMs` are optional and
  ignored. `ImageGenerationItem` replaces required `result` with a `fileId` or
  `result` union, adds nullable `generationId` and optional `inputImages`;
  `ImageGenerationInputImage` is new. This is a shape change for clients that
  require `result`, but image generation items are not mapped by this adapter.
- `ContentItem`, `UserInput`, `RawResponseItemCompletedNotification`,
  `ThreadResumeParams`, `TurnStartParams`, `TurnSteerParams`: audio gains a
  file ID alternative; old URL input remains valid. The adapter sends text
  inputs and no resume history, and does not consume raw response items.
- `ReviewDelivery` removes `detached`, a breaking request contraction;
  `ReviewStartParams` and `ReviewStartResponse` document inline-only review.
  The adapter does not use review/start.
- `CodexErrorInfo`, `ErrorNotification` add error variants; error info is
  handled as opaque data. `PlanType`, `Account`, `AccountSession`,
  `AccountSessionWorkspace`, `AccountSessionWorkspaceKind`,
  `AccountRateLimitsUpdatedNotification`, `AccountUpdatedNotification`,
  `GetAccountRateLimitsResponse`, `GetAccountResponse`,
  `GetAccountTokenUsageResponse` add account variants and fields; these
  account methods and notifications are unused.
- `AdditionalContextEntry`, `HookEventName`, `HookMetadata`, `HookRunSummary`,
  `HooksListParams`, `HooksListResponse`, `HookStartedNotification`,
  `HookCompletedNotification`, `ManagedHooksRequirements` add hook fields,
  event variants or change path typing. Hooks are unused.
- `ApplicationRequirements`, `ApplicationStorageRequirements`,
  `ConfigRequirements`, `ConfigRequirementsReadResponse`, `NetworkRequirements`,
  `OtelExporter`, `OtelHttpProtocol`, `OtelRequirements`, `OtelTlsConfig`,
  `OtelToolResultLogConfig`, `ModelProviderError`, `PluginReadResponse` add
  or alter configuration data on unused methods. Some new nullable members
  are required by the new schema, so these are not all optional additions for
  strict clients.
- `McpServerStatus`, `ListMcpServerStatusResponse` add nullable tool metadata
  to the unused status endpoint. `ProcessExitedNotification` adds an optional
  drain timeout flag to an unused process notification. `ThreadUsage` adds
  optional native cost to an unused usage endpoint; run token usage uses the
  separate unchanged `thread/tokenUsage/updated` notification.
- Both `index` files add type exports; both aggregate
  `codex_app_server_protocol*.schemas.json` files include the changes above.

The inherited corpus is copied into `fixtures/adapters/codex-appserver-0.167.0`;
its endpoint revision and provenance are updated. The `command-completed` case
adds synthetic item timestamps. The writes fixture is a copied synthetic peer
transcript with updated pin and revision, not a recording of the new binary.
No real account, credential, model call, or real-process compatibility check
was used. Binary behavior and full upstream compatibility remain unverified.

The Rust adapter currently provides native decoding and a process launcher, not
an event reducer; its corpus test checks decoding and IDs rather than full OAP
trace agreement. Go and Zig exercise the reducer corpus.
