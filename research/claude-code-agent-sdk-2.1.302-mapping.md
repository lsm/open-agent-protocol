# Claude Code / Claude Agent SDK 2.1.302 mapping ledger

Status: the Claude Code adapter's pin. This ledger moves the adapter from
2.1.296 to 2.1.302 and records only what the move changed or settled.
[The 2.1.296 ledger](claude-code-agent-sdk-2.1.296-mapping.md) and the ledgers
beneath it ([2.1.288](claude-code-agent-sdk-2.1.288-mapping.md),
[2.1.282](claude-code-agent-sdk-2.1.282-mapping.md),
[2.1.280](claude-code-agent-sdk-2.1.280-mapping.md),
[2.1.263](claude-code-agent-sdk-2.1.263-mapping.md)) remain the mapping of
record for every surface not restated here. The adapter's corpus is
`fixtures/adapters/claude-code-2.1.302`. 2.1.296 is retired: its corpus is
removed, because its expectations name a capability revision the adapter no
longer advertises. Every hash below was computed on 2026-10-18 from the
artifacts named, and the live checks ran the 2.1.302 darwin-arm64 binary.

## Provenance

### Claude Code CLI 2.1.302

- npm artifact: `@anthropic-ai/claude-code@2.1.302`
  - tarball sha256: `ac1a6a3364c467392bc40a8a405490e821575e3e858d3013b11258862c01dfc8`
  - npm `dist.integrity`:
    `sha512-+5GARMH0vX8a+o8xGrmx67KH+aDI5aYVO2qFustRhUJrSMdxzb4LtUmF2rL+PyaxCpHfXYZz38vfIuT2irg/Cg==`
  - the wrapper tarball is 29,082 bytes; `cli-wrapper.cjs` is byte-identical
    to 2.1.296's (sha256
    `61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e`)
- native linux-x64 artifact: `@anthropic-ai/claude-code-linux-x64@2.1.302`
  - tarball sha256: `3bc5817207175089cbe0a02a989bdf35b228497f8f76d3c3ba13d568dc8f25e2`
  - npm `dist.integrity`:
    `sha512-+d4RUq3E3K+3/J4MysB3z4QXA4rkDNt70VYpTLE6kJCnUe/Z6SB8VJsVJG/NV4bECrvkxkLHjXk+vrgBstfZkw==`
  - binary sha256: `60d7eb7f12fbb28a52533fe8be5884097403562aa02dc73991a021c7067e29a5`
  - binary size: 285,793,528 bytes; downloaded and hashed, not run
- native darwin-arm64 artifact, the binary the live checks below ran:
  `@anthropic-ai/claude-code-darwin-arm64@2.1.302`
  - tarball sha256: `fb216fa683cb2e4c0d65bfb3393a5c0d47a942efb3e0ddc9b1c4c660b3b86554`
  - npm `dist.integrity`:
    `sha512-h0MlQtldJnHXdPB6KGQ6rYQocvNTEXBI+NW4791OD2mIZcq6fHrV4PsM+XK297OL0+wVENlDHtlBj1BL/3S52A==`
  - binary sha256: `5262ce9de86243fed0d21edc39b271555d56780d0112e2b194439fe1c6662d37`
  - binary size: 267,956,720 bytes
  - `claude --version` self-reports `2.1.302 (Claude Code)`
- build manifest (carried by the Agent SDK artifact, `manifest.json`):
  - CLI build commit: `2ef144e6f98c1c99d082b735e4b38f50e3391e55`
  - build date: `2026-10-16T16:13:51Z`

Every npm tarball above was checked against its registry `dist.integrity` and
`dist.shasum` on download. npm's `latest` and `next` tags named 2.1.302 that
day; its `stable` tag named 2.1.292. The same download of the 2.1.296
tarballs reproduced that ledger's digests.

### TypeScript Agent SDK 0.3.302

- public repository: `https://github.com/anthropics/claude-agent-sdk-typescript`
  - tag `v0.3.302`, commit `9cf9f23b757f881c82fc2bfee459429ea26187df`, tree
    `597ca3facbe2ba99b8763c8c1df356458d2f35d2`; provenance, not runtime
    evidence
- npm artifact: `@anthropic-ai/claude-agent-sdk@0.3.302`
  - tarball sha256: `c7d7d61d6b0b7b7bb05cc053f229c5df88f151550ead0624c7393939d0d8e1f1`
  - npm `dist.integrity`:
    `sha512-VUxgraOIxnu4COhS9IkP3AF21kh9KOOF8oyU7pc6q0PfzMPlarbnhXvuhCNIPPYddhIev+17LLq1nZ700GdLBg==`
  - `package.json` declares `claudeCodeVersion: "2.1.302"`
  - file hashes inside the artifact:
    - `sdk.mjs`: `baf41bb694a59df1a1ac2577b06d759c5cd81f4f13953161c1a91c4b54434d6b`
    - `sdk.d.ts`: `26f9a0c91ad64e2020f0d3734932302e3cc09685bddf2b2f9ef60839ce278773`
    - `manifest.json`: `d7d4f193bfebb48733027b0dc43d6996d99f5335d152961a729730e55bbd5aa1`

### Python Agent SDK 0.2.168

No Python release bundles 2.1.302. The newest, `v0.2.168`, bundles 2.1.301
(`_cli_version.py` blob `676651e167bbb2f9088236c4e998dc5769b52797`). It is
pinned because it is the closest.

- repository: `https://github.com/anthropics/claude-agent-sdk-python`
  - tag `v0.2.168`, commit `a39c96edf8cc414e640620ff3360f17475db0bd9`, tree
    `11b81e563f72a8186c1c3f1a248c38f8d46226d0`
  - `pyproject.toml` blob: `3d3628a282e4a1cce251a554a98dd3575d47a501`
- between `v0.2.165` and `v0.2.168`, `client.py`, `_internal/query.py`,
  `message_parser.py`, `_internal/transport/subprocess_cli.py`,
  `session_resume.py`, `session_store.py` and `session_store_validation.py`
  keep their blobs. Only `types.py` changed (now
  `f5effd7da3d97be54afc108573d83e37e9402204`): it adds typed definitions for
  frames the CLI already sent (`system/init`'s MCP servers, plugins and plugin
  errors, background task and cron summaries, fast-mode state) and for hook
  outputs. It changes no frame on the wire.

## Wire delta from 2.1.296

The TypeScript SDK's changelog from 0.3.297 to 0.3.302, for the frames and
behaviour this adapter depends on:

| Change | Classification |
| --- | --- |
| **An omitted `permissionMode` now starts in `default` instead of `auto` on third-party providers, with telemetry off or on a fresh install** | **behaviour change for hosts; the adapter is unchanged.** Checked on both binaries with the adapter's own flags (`-p --input-format stream-json --output-format stream-json --verbose --permission-prompt-tool stdio`), a fresh `HOME` and `CLAUDE_CONFIG_DIR`, a loopback `ANTHROPIC_BASE_URL` and a placeholder key: 2.1.296's `system/init` reports `permissionMode: "auto"`, and 2.1.302's reports `"default"`. The adapter passes no `--permission-mode`. So under a custom base URL, a tool call that `auto` mode would have approved now reaches the host as a `can_use_tool` request, which the adapter projects as `action.permission.requested`. A host that does not answer permission requests will see such a run wait. |
| A structured-output result whose API response was refused is now an error with no `structured_output` | unchanged mapping: an error result settles `run.failed`, and the adapter relays `structured_output` only when present |
| Fork subagents (`subagent_type: "fork"`) are on by default | ignored: subagent frames already carry `agent_id` and are passed over, as recorded at 2.1.296 |
| `thinking: { type: 'disabled' }` and `maxThinkingTokens: 0` run as adaptive thinking on models that cannot turn thinking off | unused: the adapter sets neither |
| The `elicitation` control request gains `property_order`; sdk-type MCP server configs gain per-tool `permission_policy`; `reloadPlugins` gains `addPlugins`; `mcp_call` gains `result_not_for_model` | unused: the adapter sends none of these requests and receives no elicitation |
| A full-disk login refresh failure reports `invalid_request` instead of `authentication_failed` | unused: the adapter does not classify the assistant `error` member |
| `system/init.capabilities` and the `initialize` response's `capabilities` add `mcp_call_result_not_for_model_v1` | ignored by both decoders; recorded in the corpus (below) |

None of it names a member the native decoder requires or projects, and both
decoders ignore members and frame kinds they do not name.

## Live verification at 2.1.302

`TestClaudeProcessRecordsCorpusProbes` ran all seventeen probes against the
2.1.302 binary under the method of the 2.1.280 ledger's *Capture*: a
`security` stand-in first on `PATH`, which answered not-found to each of its 34
lookups (every one `find-generic-password`), and `sandbox-exec` denying the
keychain daemons and every non-loopback connection. The proxy sink saw no
connection and every probe passed.

Against the frames the corpus carries:

- Every frame kind the corpus carries that these probes exercise appeared, and
  every member the corpus carries on those kinds is still emitted. The kinds
  the capture lacks (`keep_alive`, `prompt_suggestion`,
  `system/compact_boundary`) and the members it lacks (`system/status`'s
  `compact_result`, `compact_error` and `compact_output_chars`, and the
  compact summary's `isCompactSummary`) are on exchanges the probes do not
  make; the compaction gate of the 2.1.296 ledger covers them.
- Frame kinds and members the capture shows that the corpus does not carry are
  ignored by both decoders. That covers `stream_event` block and message ends,
  `system/background_tasks_changed`, and new optional members on `assistant`,
  `result`, `system/init`, `system/task_*` and `user`. The corpus does not add
  them, as the earlier moves did not add theirs.
- `system/init`: `claude_code_version` is `2.1.302`, `capabilities` adds
  `mcp_call_result_not_for_model_v1` after `ui_surface_v1`, `slash_commands` is
  identical to 2.1.296's, and `permissionMode` is `default`. The capture's
  `apiKeySource` is `none` because the probe environment sets no key; that is
  the probe's choice, not the harness's, and the corpus keeps its value.

The Go process gates (`OAP_CLAUDE_SMOKE=1`, `OAP_CLAUDE_INTEGRATION=1`) pass
against 2.1.302 under the same sandbox, all seven of them, including the
read-only review posture, the `@` mention gate, the excluded-tool refusal and
`TestClaudeProcessReopensItsBoundConversation`.

## What changed in the adapter

Nothing in the adapter code of either tree. The catalog moves the pin, and
with it the endpoint version `v2.1.302` and the capability revision
`claude-code-2.1.302-oap-v1`. The advertised features are the
`claude-code-2.1.296-oap-v2` revision's.

## Corpus at 2.1.302

`fixtures/adapters/claude-code-2.1.302` holds the 2.1.296 corpus's fifteen
cases, moved forward:

- **Re-derived from the 2.1.302 capture**: every `system/init` frame (25
  across the cases) and the `initialize` response's `capabilities` in
  `session-reopen`. Each keeps the members its 2.1.296 counterpart carried,
  with 2.1.302's values: `claude_code_version` `2.1.302`, and `capabilities`
  with `mcp_call_result_not_for_model_v1`.
- **Carried forward unchanged**: every other native frame, script step and
  expectation.
- Expectations change only the capability revision. Provenance in
  `manifest.json` and every `case.json` names the 2.1.302 artifacts above.

## Client-provided tools at this pin

Whether `action.tools.provide` ([Decision 0011](../decisions/0011-control-layer-provided-tools.md))
could be served natively, so that a host's own tools reach a Claude Code
session. Read from the TypeScript Agent SDK 0.3.302's `sdk.d.ts` (sha256
`26f9a0c9…`, above), which describes the CLI's stream-json control protocol at
2.1.302, and then probed against the pinned darwin-arm64 binary with a
throwaway `HOME`, a loopback Messages mock and no credentials: the CLI sends
MCP `initialize` over `mcp_message` before it answers the `initialize` control
request, then `notifications/initialized` and `tools/list`; a call arrives as
`tools/call` with `_meta["claudecode/toolUseId"]` naming the `tool_use` it
serves; and the CLI echoes the server's `CallToolResult` as that call's
`tool_result`.

**Possible, over the control channel this adapter already speaks.** The SDK's
in-process MCP servers (`createSdkMcpServer`, `McpSdkServerConfig` with
`type: 'sdk'`) are a stable, public SDK feature. On the wire they are three
control messages:

- the `initialize` control request names them, `sdkMcpServers: string[]`, with
  optional `sdkMcpServerConfigs` per server: a per-call `timeout` in
  milliseconds, and a per-tool permission policy (`always_ask` or
  `always_deny`);
- `mcp_message` (`SDKControlMcpMessageRequest`) "carries one MCP JSON-RPC
  message for an SDK-hosted MCP server … the CLI sends it to the client to
  reach the in-process server". The client answers with a success response
  whose `mcp_response` is the server's JSON-RPC reply. That covers MCP
  `initialize`, `tools/list` and `tools/call`;
- `mcp_set_servers` replaces the dynamically managed servers mid-session, so
  tools can also be added after the spawn.

The adapter already sends `initialize` (its `PreToolUse` hook) and answers
`can_use_tool` on the same channel. Hosting a server means answering MCP's
`initialize` and `tools/list` from the provided definitions, and turning each
`tools/call` into an OAP call. The scope is the CLI process, which is one
session, as Decision 0011 requires. The tools reach the model as
`mcp__<server>__<tool>`, and each call passes the CLI's permission check.
`--tools` does not narrow MCP tools: the 2.1.280 ledger records that it drops
names outside the built-in set, and a live probe at 2.1.302 confirms it for an
SDK-hosted server, whose tool stayed listed and ran under `--tools Read`. What
governs the check is `--allowedTools`. With `mcp__oap__echo` pre-allowed, the
probe's call reached `tools/call` with no `can_use_tool`; without it, a
`can_use_tool` naming the tool, with `mcp_server.source: "sdk"`, came first,
which the adapter projects as `action.permission.requested`.

What `action.tools.provide` would map to:

| OAP | Claude Code |
|---|---|
| `tools` on `session.open.request` | one SDK server named in `initialize.sdkMcpServers`, whose `tools/list` returns exactly the provided definitions (`input_schema` as `inputSchema`) |
| a provided tool at reopen | the same server named again in the reopened process's `initialize` |
| `action.call.requested` | an `mcp_message` carrying `tools/call`; the JSON-RPC id and the stream's `tool_use` id correlate it |
| `action.call.resolve.request` | the `control_response` to that `mcp_message`, its `mcp_response` an MCP `CallToolResult` (`content` text, `isError` for a failure) |
| the five refusal reasons | the adapter's own |
| limits | the per-server `timeout` (the CLI's `MCP_TOOL_TIMEOUT` otherwise), MCP's tool-name rules, and whether `--allowedTools` pre-approves the names (otherwise each call is a permission request first) |

**Recommendation.** The best fit of the three harnesses: session-scoped and
stable, carried on the channel the adapter owns, and needing no MCP transport
of its own, only MCP's JSON-RPC shapes. It matters for a host that runs other
models on the Claude Code harness; HyperNeo keeps Claude itself on the Agent
SDK.
