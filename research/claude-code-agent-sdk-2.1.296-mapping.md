# Claude Code / Claude Agent SDK 2.1.296 mapping ledger

Status: the Claude Code adapter's pin. This ledger moves the adapter from
2.1.288 to 2.1.296 and records only what the move changed or settled.
[The 2.1.288 ledger](claude-code-agent-sdk-2.1.288-mapping.md),
[the 2.1.282 ledger](claude-code-agent-sdk-2.1.282-mapping.md),
[the 2.1.280 ledger](claude-code-agent-sdk-2.1.280-mapping.md) and, beneath
them, [the 2.1.263 ledger](claude-code-agent-sdk-2.1.263-mapping.md) remain
the mapping of record for every surface not restated here. The adapter's
corpus is `fixtures/adapters/claude-code-2.1.296`. 2.1.288 is retired: its
corpus is removed, because its expectations name a capability revision the
adapter no longer advertises. Every hash below was computed on 2026-10-10 from
the artifacts named, and the live checks ran the 2.1.296 darwin-arm64 binary.

## Provenance

### Claude Code CLI 2.1.296

- npm artifact: `@anthropic-ai/claude-code@2.1.296`
  - tarball sha256: `f6c375d51d4c22a7a850e185a0d7ddda85173f89dbbeb3262aa7971e3bd670b5`
  - npm `dist.integrity`:
    `sha512-OX/k/rpcthMqnFKOWcpNcbiLsMKtAjLOpQNJHlYiDCFUznWCIwdRTyl/p9SdHVrsrzZuPMCNqZjnpakIni6upw==`
  - the wrapper tarball is 29,001 bytes; `cli-wrapper.cjs` is byte-identical
    to 2.1.288's (sha256
    `61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e`)
- native linux-x64 artifact: `@anthropic-ai/claude-code-linux-x64@2.1.296`
  - tarball sha256: `eef5a2e2b09a5e7d2ce784d1b3ba335ae7fc6c7f6256c802a0ebf2c21e4fd0e1`
  - npm `dist.integrity`:
    `sha512-+m01urgelnI+VJxPaKPEK0wixgq42uZ8PeCaUk5tX/8YoDBvSmC/REVQG2jQfJAr8dxunpjJ600FvLrcj1WWTw==`
  - binary sha256: `24972e3bc859fab2b46ed4c1e51f7d6130f06d3bd550811a114640de3370d0de`
  - binary size: 257,068,216 bytes
- native darwin-arm64 artifact, the binary the live checks below ran:
  `@anthropic-ai/claude-code-darwin-arm64@2.1.296`
  - tarball sha256: `50fbf594d6526c9aae7d66b8b45a08d89ae6b863e2bc9777bfb277d6521a7458`
  - npm `dist.integrity`:
    `sha512-vqtx3kS3r1hNLMk+N8JMG5J4kcLHIbI3t/NSIdcFQZrRVCFA6NtcxJyn/B+Sf3GxJaFJOCEfoBQXsFo10+Oj9A==`
  - binary sha256: `c9b5341637becbd423ddffc5b254afb645682a3868cb708bbc6cc0e7bb419937`
  - binary size: 240,664,432 bytes
  - `claude --version` self-reports `2.1.296 (Claude Code)`
- build manifest (carried by the Agent SDK artifact, `manifest.json`):
  - CLI build commit: `fdb6c17a97b6e0e3cf15e3566663fca58e7c43a9`
  - build date: `2026-10-09T14:46:35Z`

Every npm tarball above was checked against its registry `dist.integrity` and
`dist.shasum` on download. npm's `latest` tag named 2.1.296 that day; its
`stable` tag still named 2.1.287.

Reproduce:

```sh
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code/-/claude-code-2.1.296.tgz' -o claude-code-2.1.296.tgz
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code-darwin-arm64/-/claude-code-darwin-arm64-2.1.296.tgz' -o claude-code-darwin-arm64-2.1.296.tgz
shasum -a 256 claude-code-2.1.296.tgz claude-code-darwin-arm64-2.1.296.tgz
tar -xzf claude-code-darwin-arm64-2.1.296.tgz
shasum -a 256 package/claude # c9b5341637becbd423ddffc5b254afb645682a3868cb708bbc6cc0e7bb419937
./package/claude --version   # 2.1.296 (Claude Code)
```

### TypeScript Agent SDK 0.3.296

- public repository: `https://github.com/anthropics/claude-agent-sdk-typescript`
  - tag `v0.3.296`, commit `85d8f8e0772199ec7965a0c2e874ac88343f2a01`, tree
    `4147bf2378991c1a1a48775a7954da420490d23a`; provenance, not runtime
    evidence
- npm artifact: `@anthropic-ai/claude-agent-sdk@0.3.296`
  - tarball sha256: `e966be99b6f047d56165e2ec03822f17a49beebd48ac8efb91dab1e48bcf69a7`
  - npm `dist.integrity`:
    `sha512-Y+cZ2BT8X94mQ9GJOVyIqhjC/nWGvwozUVLjdkzX7aSt+zuo5tgk7Hdv9V7tEUQ9fKnEuiL6moYNWDeHqCD4Lw==`
  - `package.json` declares `claudeCodeVersion: "2.1.296"`
  - file hashes inside the artifact:
    - `sdk.mjs`: `5cf6f15237f9c84e6e5b724a10180478732457168921d7aa3e02632037cce299`
    - `sdk.d.ts`: `fc2866dfe2c01468edb19b1d94f26156bfacecdf1e7f1452b276ed2f8cc0b08f`
    - `manifest.json`: `e50e872d91f3b7da2fb3d8d38505d709e0a90f48da1951dd51176930c8da8270`

### Python Agent SDK 0.2.165

No Python release bundles 2.1.296. The newest, `v0.2.165`, bundles 2.1.294
(`_cli_version.py` blob `9922ce8e46963c4f99a3e71a11897ab8e67edb5e`). It is
pinned because it is the closest.

- repository: `https://github.com/anthropics/claude-agent-sdk-python`
  - tag `v0.2.165`, commit `f7529348747235da34af3ed202251228ff31db08`, tree
    `9301455d8adffe263d3d53d827b11b752fa37551`
  - `pyproject.toml` blob: `79fa7ee29759c13a1ea545d20141e1b1a04c5fea`
- between `v0.2.163` and `v0.2.165` none of the normative sources changed:
  `client.py`, `types.py`, `_internal/query.py`, `message_parser.py`,
  `_internal/transport/subprocess_cli.py`, `session_resume.py`,
  `session_store.py` and `session_store_validation.py` keep the blobs the
  2.1.288 ledger records. The releases moved the bundled CLI only.

## Wire delta from 2.1.288

The TypeScript SDK's changelog from 0.3.289 to 0.3.296, for the frames this
adapter decodes:

- the `initialize` control response gains `claude_code_version`;
- subagent `assistant` and `user` frames gain `agent_id`, and `task_started`
  and `background_tasks_changed` entries gain `parent_task_id` and an optional
  `subagent_type`; background task events gain `run_id`;
- a `background_tasks_changed` for a finishing task now arrives after that
  task's `task_updated` and `task_notification`;
- an `includePartialMessages` stream that is cut, interrupted or falls back to
  non-streaming now still ends its message with `message_stop`;
- `rate_limit_event` with `allowed_warning` carries the overage members the
  other statuses already did, and `SDKRateLimitInfo` gains `overageEnabled`;
- the SDK now passes its named options as `--flag=value`; the CLI still
  accepts the separate form this adapter sends, as the probes below show.

None of it names a member the native decoder requires or projects, and both
decoders ignore members and frame kinds they do not name.

## Live verification at 2.1.296

`TestClaudeProcessRecordsCorpusProbes` ran all seventeen probes against the
2.1.296 binary under the method of the 2.1.280 ledger's *Capture*: a
`security` stand-in first on `PATH`, which answered not-found to each of its 34
lookups (every one `find-generic-password` for `Claude Code-<hash>` or
`Claude Code-credentials-<hash>` of a temporary `CLAUDE_CONFIG_DIR`), and
`sandbox-exec` denying the keychain daemons and every non-loopback connection.
The proxy sink saw no connection and every probe passed.

Unlike the 2.1.288 move, no 2.1.288 capture was taken beside it, so the
comparison is against the frames the corpus carries rather than frame by frame
against the previous release:

- Every frame kind the corpus carries that these probes exercise appeared, and
  every member the corpus carries on those kinds is still emitted. The members
  the corpus carries that the capture lacks are on exchanges the probes do not
  make: `get_settings`'s `applied`, `effective` and `sources` (covered by the
  reopen gate below), and `mcp_servers[].name` and `.status`, since no probe
  configures an MCP server.
- Frame kinds the capture shows that the corpus does not carry, all ignored by
  both decoders: `stream_event` `content_block_start`, `content_block_stop`,
  `message_delta` and `message_stop`; `system/status`; and
  `system/background_tasks_changed`. Whether 2.1.288 emitted them is not
  established here; the corpus never carried them.
- `system/init` gained no member the corpus carries; its harness-written values
  changed: `claude_code_version` is `2.1.296`, and `capabilities` adds
  `request_marker_lists_v1` after `msg_lifecycle_v1`. `slash_commands` is
  identical to 2.1.288's.
- The adapter settled every probe with the terminal event the probe expects,
  which is what each probe asserts of the OAP it records.

The Go process gates (`OAP_CLAUDE_SMOKE=1`, `OAP_CLAUDE_INTEGRATION=1`) pass
against 2.1.296 under the same sandbox, including the read-only review
posture, the `@` mention gate, the excluded-tool refusal and
`TestClaudeProcessReopensItsBoundConversation`, which reopens a bound
conversation through `--resume <uuid>` and reads `get_settings`.

## Structured output and budget (`--json-schema`, `--max-budget-usd`)

Carried from the 2.1.288 ledger, whose section of this name the adapter's
relay of `structured_output` (#1023) cites. Both are spawn flags, not per-turn
controls: the TypeScript Agent SDK's stream-json transport pushes
`--max-budget-usd` and `--json-schema` onto the CLI's argv when its
`maxBudgetUsd` and `outputFormat` options are set, and its success result
declares `structured_output?: unknown` beside `total_cost_usd`. A schema
therefore binds the whole session, which is why the adapter does not advertise
`run.structured_output`: that control is per run.

- **Budget:** a host passes `--max-budget-usd` through the adapter's `Args`.
  Exhausting it ends the run with subtype `error_max_budget_usd`, already
  settled `run.failed` (`claude_error_max_budget_usd`).
- **Structured output:** a host passes `--json-schema` through `Args`, and the
  adapter relays the result frame's `structured_output` on the terminal as
  `extensions["com.anthropic.claude-code.structured_output"]`, verbatim, beside
  the cost extension. A null or absent `structured_output` carries nothing.
  The CLI's own schema failure ends the run with
  `error_max_structured_output_retries`, settled `run.failed`.

Verified live on 2026-10-10 against Claude Code 2.1.295 (#1028): with
`--input-format stream-json --output-format stream-json --json-schema <object
schema>` and one user frame on stdin, the `result` frame came back
`subtype: "success"` with `structured_output` holding the schema's object. Not
re-run on 2.1.296; the changelog from 0.3.295 to 0.3.296 names no change to
`structured_output` or to either flag.

## What changed in the adapter

Nothing in the adapter code of either tree. The catalog moves the pin, and
with it the endpoint version `v2.1.296` and the capability revision
`claude-code-2.1.296-oap-v1`. The advertised features are the
`claude-code-2.1.288-oap-v3` revision's.

## Corpus at 2.1.296

`fixtures/adapters/claude-code-2.1.296` holds the 2.1.288 corpus's fourteen
cases, carried forward:

- **Re-derived from the 2.1.296 capture**: every `system/init` frame (23
  across the cases). Each keeps the members its 2.1.288 counterpart carried,
  with 2.1.296's values: `claude_code_version` `2.1.296`, and `capabilities`
  with `request_marker_lists_v1`. The members it never carried are still not
  added.
- **Carried forward unchanged**: every other native frame, script step and
  expectation. The new optional members and frame kinds above are not added,
  as the earlier moves did not add theirs.
- Expectations change only the capability revision. Provenance in
  `manifest.json` and every `case.json` names the 2.1.296 artifacts above.

## The cost extension carries turns and cache counts

`extensions["com.anthropic.claude-code.cost"]` on a terminal now carries, beside
`total_cost_usd`, the result frame's `num_turns` and its `usage`
`cache_read_input_tokens` and `cache_creation_input_tokens`, each only when
the frame reports it. `usage.input_tokens` excludes cache reads, which in a
live GLM-5.3 review via Z.AI came to 715K against 57K uncached input
(2026-10-10, Claude Code 2.1.295), so a host comparing engines by input tokens
alone compares the wrong thing. The corpus's recorded results carry
`num_turns` and no cache counts, so only `num_turns` was added to those
expectations.
