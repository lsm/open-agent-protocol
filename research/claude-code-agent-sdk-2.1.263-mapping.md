# Claude Code / Claude Agent SDK 2.1.263 mapping ledger

Status: frozen implementation contract for the eighth (final) production OAP
adapter. Every artifact hash below was re-verified at freeze time, and the
pinned CLI binary was exercised live (credential-free, hermetic loopback, and
interrupt probes; findings recorded in "Live-binary verification"). This is an
implementation input, not an interoperability claim about other Claude Code
surfaces.

## Provenance

Three pinned surfaces are mapped together because they are one product
boundary at three altitudes:

1. the Claude Code CLI process and its stream-json wire protocol;
2. the TypeScript Agent SDK, the reference host implementation;
3. the Python Agent SDK, the second reference host implementation.

### Claude Code CLI 2.1.263

- npm artifact: `@anthropic-ai/claude-code@2.1.263`
  - tarball sha256: `b325aaaf748065ebce116c50893120384ce6ec56c1133f42f45177f8d1030c66`
  - tarball sha1 (npm `dist.shasum`): `724f3282ee21318b227a6a20cbc66af331ee0fa4`
  - npm `dist.integrity`:
    `sha512-kvvBK6/69iTRYnq0TKVyxVZs1CxYCJGojshQSP+2qaDb66A2xtI4zbCuqkZUWLkFGmHSRqhFf/ATpzH2UNKcwg==`
  - the wrapper tarball itself is 27,425 bytes: `bin/claude.exe` is a stub
    that `install.cjs` replaces with the platform binary from an optional
    dependency; `cli-wrapper.cjs` is the `--ignore-scripts` fallback launcher
    (sha256 `61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e`)
- native linux-x64 artifact: `@anthropic-ai/claude-code-linux-x64@2.1.263`
  - tarball sha256: `8b6207348ad56fdcde085a0ad1f7cff0dfe06ce2c6c1bf97f69f1a1a7b6d0945`
  - binary sha256 (verified against the SDK manifest below):
    `26d020351e8112f4006790f3cfce43b4c9df0c1bb1d0e542364d64151b81d5ba`
  - binary size: 215,662,064 bytes
  - `claude --version` self-reports `2.1.263 (Claude Code)`
- build manifest (carried by the Agent SDK artifact, `manifest.json`):
  - CLI build commit: `37ae3f38d765199d54a6913cd61c6c9ad8576cc6`
  - build date: `2026-09-06T01:17:56Z`
  - declares per-platform binary checksums; the linux-x64 entry matches the
    independently computed hash above, so the artifact is authenticated
    against the publisher manifest

Reproduce:

```sh
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code/-/claude-code-2.1.263.tgz' -o claude-code-2.1.263.tgz
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code-linux-x64/-/claude-code-linux-x64-2.1.263.tgz' -o claude-code-linux-x64-2.1.263.tgz
sha256sum claude-code-2.1.263.tgz claude-code-linux-x64-2.1.263.tgz
tar -xzf claude-code-linux-x64-2.1.263.tgz
sha256sum package/claude   # 26d020351e8112f4006790f3cfce43b4c9df0c1bb1d0e542364d64151b81d5ba
./package/claude --version # 2.1.263 (Claude Code)
```

The CLI's primary implementation is the closed native binary; its behavior is
pinned here through the artifact hashes above and through the two SDK source
trees below, which encode the wire contract the CLI must honor. Features not
exercisable through the SDK sources are treated as unverified.

### TypeScript Agent SDK 0.3.263

- public repository: `https://github.com/anthropics/claude-agent-sdk-typescript`
  - commit: `69f318f92fd5bd21614b35d5c35ef9e35445aecb`
  - tree: `8354081eae81488850715bac91a97f8d40333481`
  - the public repo at this pin carries changelog, README, and docs, but not
    the implementation source; it is provenance, not runtime evidence
- npm artifact: `@anthropic-ai/claude-agent-sdk@0.3.263`
  - tarball sha256: `e1d6b68b557fc3c57430cafa8cc65eea9d40ff7348b238fe2c431284d727901d`
  - tarball sha1 (npm `dist.shasum`): `e689ea59019ac1687ab2706bddee93635c57eaec`
  - npm `dist.integrity`:
    `sha512-0QWoHgWWlSmgXfEqZRbVYRoXT9p4tx/yKaZ4eLJ8l1MR65SIvMGR+cd5ZXUpGYZpj9RzbYiYU9LoFg90mdIjmw==`
  - `package.json` declares `claudeCodeVersion: "2.1.263"` and bundles the
    same CLI via `@anthropic-ai/claude-agent-sdk-<platform>` optional deps
  - file hashes inside the artifact:
    - `sdk.mjs` (bundled runtime): `3d690c23ec82b4ba05f7ac8c26e2510f84e112c68ced68053ec40cf7d5dcbfe2`
    - `sdk.d.ts` (public declarations): `59560e31f91e47ed93e7cbcaa846e3fc3c96d8dcc41ea36cda64de96c0c4edf4`
    - `bridge.mjs`: `bcf2e62ded19e90b1da9fb17bc5ccd6f2a90e3f705db9fa373fa8e4ecad0ea7c`
    - `bridge.d.ts`: `3aa177a4c5859dbacd768cead5e660b148e854b834d5bf430e0c489339785295`
    - `manifest.json`: `96f64bb75b98b1ca93008826a2028cb8b7d0f41727cdf188cf316a275328116a`
    - `manifest.zst.json`: `03e651583cec33d0150f9874940003a565dee82ab1c507ea2298a89d42bc6198`

Evidence class: `sdk.d.ts` is the authoritative public API and wire-shape
declaration set; `sdk.mjs` is bundled/minified and is used only to confirm the
declarations are shipped as-is. `bridge.d.ts` documents the claude.ai remote
bridge, which is out of scope for the stdio adapter.

### Python Agent SDK 0.2.152

- repository: `https://github.com/anthropics/claude-agent-sdk-python`
  - commit: `efd4d865ef1795daffee3cd24cce45307aed8a51`
  - tree: `d617ca6d630c7bab54f3c0cd1376dcbb938103a2`
  - `pyproject.toml` blob: `ebece50404bb77b0d02aa47be14375fde76eea60`
    (version `0.2.152`)
- normative inspected source blobs:
  - `src/claude_agent_sdk/client.py`: `bba76b10e4c2ecb6b0d526ad302122b7549c3ac4`
  - `src/claude_agent_sdk/types.py`: `308b76cb7fd928d124666c255b253c92c343f15d`
  - `src/claude_agent_sdk/_internal/query.py`: `4d5f0070e0568778255a39cc6351aaf40429da7c`
  - `src/claude_agent_sdk/_internal/message_parser.py`: `931cc2a632f296aab43f3f98209020138431ce7d`
  - `src/claude_agent_sdk/_internal/transport/subprocess_cli.py`: `58abc438ddadc7406330a32d90f743ae60d10c69`
  - `src/claude_agent_sdk/_internal/session_resume.py`: `a50e578fdaea7b10de83697fe355145b7351cecc`
  - `src/claude_agent_sdk/_internal/session_store.py`: `bb6a2155b08ad546227eba9f2349d95bffd910fa`
  - `src/claude_agent_sdk/_internal/session_store_validation.py`: `16addd216281eecaadaedbe7ed361ad8205d0433`

Evidence class: full readable implementation source. The Python transport,
control protocol, parser, and resume paths below cite this tree.

## Live-binary verification (freeze evidence)

At freeze time every hash above was recomputed and matched, `claude --version`
self-reported `2.1.263 (Claude Code)`, and the pinned linux-x64 binary was
driven directly (not through either SDK) under a fully replaced environment:
`env -i` plus only HOME, CLAUDE_CONFIG_DIR (fresh temp), PATH, and — for the
behavioral probes — ANTHROPIC_BASE_URL pointed at an in-process scripted
Anthropic Messages loopback with a test-owned key. No ambient credential was
forwarded. Findings, each observed on the wire:

1. **`command_lifecycle` frames exist and are nowhere typed.** Every submitted
   turn emits `{"type":"command_lifecycle","command_uuid":<submitted uuid>,
   "state":...,"uuid":...,"session_id":...}` transitions: `queued` →
   `started` → terminal (`completed` on success, `cancelled` when the turn
   dies before running, e.g. after an auth-failure result). The family is
   gated by the `msg_lifecycle_v1` capability (present on this build). It is
   not modeled by the Python 0.2.152 parser (skipped as an unknown type), and
   the TypeScript 0.3.263 declarations mention it only in interrupt prose.
   The adapter therefore treats it as **corroborating admission evidence
   only**; the `user_message_uuid` echo is the primary correlation.
2. **The `user_message_uuid`/`user_message_uuids` echo is verified on all
   three promised surfaces**: the first `stream_event` of the turn (with
   `--include-partial-messages`), the first complete `assistant` frame
   (without partials), and both success and error `result` frames. The
   submitted uuid rides the inbound user frame's `uuid` field.
3. **`system/init` recurs per turn and arrives only after the first
   submit** — there is no init frame before input, so the CLI session UUID
   cannot be learned at spawn time. Observed fields beyond the Python model:
   `capabilities` (`["interrupt_receipt_v1","interrupt_cancel_queued_v1",
   "msg_lifecycle_v1"]` on this build), `apiKeySource`, `claude_code_version`,
   `terminal_slash_commands`, `analytics_disabled`,
   `product_feedback_disabled`, `memory_paths`, `messaging_socket_path`,
   `fast_mode_state`, `fast_mode_disabled_reason`.
4. **Assistant block frames interleave mid-stream.** With partial messages
   on, the complete `assistant` frame carrying a finished text block was
   emitted between that block's `content_block_delta` and its
   `content_block_stop`. The reducer must treat complete frames and stream
   events as one interleaved arrival stream, not two phases.
5. **Auth failure (no credentials)**: a synthetic assistant frame arrives
   with `model:"<synthetic>"`, `error:"authentication_failed"`,
   `is_api_error_message:true`, content "Not logged in · Please run /login",
   followed by a `result` with `subtype:"success"`, `is_error:true`,
   `terminal_reason:"api_error"`, `api_error_status:null`, and the error text
   in `result` (exactly the `_error_result_text` errors→result preference
   case), then process exit 1.
6. **Tool round trip**: `assistant` tool_use block → (permission ask when the
   command is not auto-approved) → `user` frame whose content carries the
   matching `tool_result` **plus** a structured `tool_use_result` object
   (Bash: `stdout`, `stderr`, `interrupted`, `isImage`, `noOutputExpected`)
   → turn continues. Safe commands (`echo probe`) were **auto-approved with
   no ask** in default permission mode; an ask-gated command (`touch
   /tmp/...`) produced the `can_use_tool` control_request below. Corpus
   fixtures must use ask-gated commands to exercise the gate.
7. **`can_use_tool` reverse request, verbatim shape**: `{"type":
   "control_request","request_id":<uuid>,"request":{"subtype":"can_use_tool",
   "tool_name":"Bash","display_name":"Bash","input":{...},
   "description":<command>,"permission_suggestions":[{"type":"addRules",
   "rules":[{"toolName","ruleContent"}],"behavior":"allow","destination":
   "localSettings"},{"type":"addDirectories","directories":[...],
   "destination":"session"},{"type":"setMode","mode":"acceptEdits",
   "destination":"session"}],"blocked_path":...,"tool_use_id":...}}`. An
   `allow` response `{"behavior":"allow","updatedInput":{...}}` was accepted
   and the tool ran the updatedInput verbatim; the round trip completed.
8. **Interrupt (mid-stream)**: `interrupt` control_request → immediate
   `control_response` success `{"still_queued":[]}` (the `interrupt_receipt_v1`
   contract honored); then a synthetic `user` frame with text
   `[Request interrupted by user]`; then the result with
   `subtype:"error_during_execution"`, `is_error:true`,
   `terminal_reason:"aborted_streaming"`, `errors:["[ede_diagnostic] ..."]`,
   `num_turns:2` (the synthetic user frame counts), `stop_reason:null`;
   exit 1. **Cancellation therefore keys off `terminal_reason`, never off
   subtype or `is_error`.**
9. **Sequential turns on one process**: a second submit after the first
   result produces a fresh `command_lifecycle` pair, a fresh `system/init`,
   the same CLI session UUID, and cumulative `modelUsage` on the second
   result. Stdin held open across turns is the native mode.
10. **Teardown**: stdin EOF after a clean result exits 0 promptly. A
    nonzero exit after an error result corroborates the already-terminal
    error (the CLI exits non-zero on purpose for shell consumers).
11. **Provider request shape** (first turn): `POST {base}/v1/messages`,
    header `x-api-key` from ANTHROPIC_API_KEY, `anthropic-version:
    2023-06-01`, no `Authorization` bearer, a fixed `anthropic-beta` list,
    UA `claude-cli/2.1.263 (external, sdk-cli)`, `stream:true`. Even with
    `--system-prompt ""` the CLI injects its own `<system-reminder>`
    blocks (agent and skill listings) into the first user message — so
    provider-body assertions must be structural (path/model/auth/stream),
    never byte-exact.
12. **`initialize` exchange verified**: request
    `{"subtype":"initialize","hooks":null,...}` → success response carrying
    `commands`, `agents`, `models`, `account`, `output_style`/
    `available_output_styles`, `current_permission_mode`, `session_state`,
    `pid`, `analytics_disabled` (superset of the TS-documented shape). Basic
    turns work without any initialize (probes ran without it); initialize is
    required only to register hooks, declare `perTaskStopAffordance`, or
    load plugins.
13. **TerminalReason vocabulary (from sdk.d.ts, 19 values)**: blocking_limit,
    rapid_refill_breaker, prompt_too_long, image_error, model_error,
    api_error, malformed_tool_use_exhausted, aborted_streaming, aborted_tools,
    stop_hook_prevented, hook_stopped, tool_deferred, max_turns,
    background_requested, completed, budget_exhausted,
    structured_output_retry_exhausted, tool_deferred_unavailable,
    turn_setup_failed.

## Boundary selection

The Go OAP adapter wraps the **CLI process directly**, not the Python or
TypeScript SDK. Both SDKs are themselves subprocess wrappers; depending on
either would add a Node/Python host layer between OAP and the wire, import
their language-specific session-store machinery, and still hide nothing the
Go process cannot do itself. The SDKs remain the reference implementations
that define correct wire behavior, and the adapter must stay byte-compatible
with the contract they encode.

Process boundary:

```text
claude --output-format stream-json --verbose ... --input-format stream-json
```

(`subprocess_cli.py` `_build_command`; `--input-format stream-json` is always
appended, matching the TypeScript SDK, so agents and other large configs can
travel over the initialize control request rather than argv.)

## Wire protocol

Two logical streams share one stdio pipe pair, distinguished by frame `type`:

- **message stream** (CLI -> host): NDJSON on stdout, one JSON object per
  newline-terminated line; bounded per-line buffering with an explicit
  maximum; a truncated tail at EOF is dropped, not parsed
  (`subprocess_cli.py` `_read_messages_impl`);
- **control plane** (bidirectional, same pipe): `control_request` /
  `control_response` frames correlated by sender-chosen `request_id`;
  the Python SDK mints `req_{counter}_{8 hex}`; each request receives
  exactly one response, and unmatched responses are ignored by the sender
  (`query.py` `_send_control_request`, `_read_messages`).

### Host -> CLI

- user turns: `{"type":"user","message":{...MessageParam...},"parent_tool_use_id":null,"session_id":"default"}`
  — the `session_id` here is a **logical stream label** (default `"default"`),
  never the CLI session UUID (`client.py` `query`);
- control requests implemented by the Python SDK: `initialize`, `interrupt`,
  `set_permission_mode`, `set_model`, `rewind_files`, `mcp_reconnect`,
  `mcp_toggle`, `mcp_status`, `stop_task`, `get_context_usage`;
- the TypeScript surface at the same CLI generation additionally exposes:
  `rename_session`, `set_color`, `set_max_thinking_tokens`, `list_models`,
  `get_session_cost`, `get_usage`, `get_binary_version`, `mcp_call`,
  `file_suggestions`, `read_file`, `seed_read_state`, `mcp_set_servers`,
  `register_repo_root`, `reload_plugins`, `reload_skills`,
  `reload_output_styles`, `background_tasks`, `apply_flag_settings`,
  `get_settings`, `update_settings`, `cancel_async_message`
  (`sdk.d.ts` `SDKControlRequestInner`). The adapter initially implements only
  the Python-verified subset and treats the rest as unexercised.

### CLI -> host

- `system` / `init`: session metadata at the start of **each turn** (session
  UUID, model, cwd, tools, MCP servers, slash commands, permission mode,
  capabilities list) — it recurs, it is not a one-time ready frame; the first
  one appears only after the first submit;
- `command_lifecycle` (verified live, `msg_lifecycle_v1`-gated): per-submitted-
  uuid state transitions `queued`/`started`/terminal — corroborating evidence
  only, untyped in both SDK pins;
- `assistant`: one frame per completed content block while streaming; several
  consecutive frames can share `message.id`; the turn's stop reason and final
  usage arrive only on the result frame (`sdk.d.ts` `SDKAssistantMessage`);
  with partial messages on, these interleave with `stream_event` frames
  mid-stream (verified live);
- `user`: the CLI's own user-role content, chiefly `tool_result` blocks
  answering the assistant's `tool_use` (plus a synthetic
  `[Request interrupted by user]` text frame after an interrupt), carrying a
  structured per-tool `tool_use_result` twin;
- `stream_event`: raw Anthropic API stream events, present only with
  `--include-partial-messages`;
- `result`: exactly one per turn, after that turn's messages; the
  turn-complete signal;
- `system` informational subtypes after a result are legal (task
  notifications, `session_state_changed`, prompt suggestions) — a result does
  not imply stream quiescence;
- `keep_alive`: payload-free liveness heartbeat, must be ignored;
- `tool_progress`: long-running tool heartbeat/progress keyed by
  `tool_use_id`;
- reverse `control_request`: `can_use_tool`, `hook_callback`, `mcp_message`
  (Python); TS additionally `request_user_dialog` and elicitation routing;
  `control_cancel_request` withdraws an in-flight reverse request;
- `transcript_mirror` frames when `--session-mirror` is enabled (not enabled
  by this adapter);
- `conversation_reset`, `rate_limit_event`, `tool_use_summary`,
  `prompt_suggestion`, `active_goal`, `auth_status`, and the informational
  `system` subtypes: session-scope observations.

### Strictness policy (frozen)

The two reference hosts split tolerance by discriminator, and the adapter
mirrors that split exactly rather than imposing a blanket rule:

- **Unknown top-level `type` values and unknown `system` subtypes are
  ignored as observed-only** — both SDKs document and implement this
  ("the set grows over time"; `parse_message` returns None; `parse_stdout_line`
  skips non-JSON lines). Unlike the Hermes gateway (a closed, versioned
  vocabulary), this wire is explicitly forward-compatible.
- **A known discriminator with a violated shape is fatal** — the native
  `parse_message` raises `MessageParseError` on missing required fields of
  known types, and `_parse_stdout_line` raises on JSON-looking lines that do
  not parse.
- The adapter codec is stricter than the native reader where the repo
  convention demands it: exact LF-terminated single-object frames, UTF-8 and
  duplicate-key rejection, bounded frame size. The native reader strips
  surrounding whitespace, silently skips blank and non-`{`-prefixed lines,
  and drops a truncated tail at EOF; the adapter fails closed on all three
  (recorded mismatch, same policy as the DeepSeek and Hermes ledgers).

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| CLI session UUID (`session_id` on emitted messages) | `session_id` association evidence | Adapter mints the OAP session id; the CLI UUID is observed on frames (first available only after the first submit) and recorded as association evidence. It is also the resume input, but possession of it is not proof of recoverability. |
| logical stream label (`"default"` on written user messages) | private | Never an OAP identity; must not be conflated with the session UUID. |
| submitted `uuid` → echoed `user_message_uuid`/`user_message_uuids` (first reply frame and result) | `submission_id` correlation | Strongest native admission correlation; host mints it, CLI echoes it (verified live on all three surfaces). |
| `command_lifecycle.command_uuid` | corroborating admission evidence | Same uuid as the submit; observed-only (untyped in both SDK pins, capability-gated). |
| one user turn ending in one `result` | `run_id` | Adapter allocates; Claude Code has no native run identity. |
| assistant `uuid` | transcript `message_id` | One per emitted frame; block-level, not turn-level. |
| `tool_use` block `id` / `tool_use_id` | `tool_call_id` | Namespace by endpoint and session; also the `can_use_tool` correlation key. |
| `task_id` | child action/task identity | Target of `stop_task`; distinct from `tool_use_id` though joinable via `tool_use_id`. |
| `parent_tool_use_id` | child attribution | Marks subagent-produced frames; links task children to the spawning call. |
| control `request_id` | private control correlation | Interrupt acknowledgement must never be surfaced as run settlement. |
| per-turn `system/init` capabilities | descriptor input | Recurs per turn; newest frame wins. |

Native frames carry **no sequence numbers**; ordering is arrival order on one
stream. OAP's positive contiguous per-run sequence is therefore entirely
adapter-owned, as with ACP.

## Lifecycle mapping

Fidelity is `native`, `normalized`, `synthesized`, `lossy`, or `unsupported`.
Fixture names are requirements, not claims that captures exist.

| Native observation | OAP meaning | Fidelity | Initial support | Required fixture |
|---|---|---|---|---|
| subprocess spawn + `initialize` request/response | initialize response and descriptor | synthesized | emulated | `initialize-minimal` |
| per-turn `system/init` frame | capability truth refresh | normalized | emulated, per-session descriptor | `per-turn-init` |
| `system/init` `tools` + `mcp_servers` | `action.tools.list.response`: one catalog entry per tool, one `ToolSourceDescriptor` per listed server, a tool attributed to a server only when its `mcp__<server>__` name matches one the same frame listed — the **longest** match, since server names may themselves contain `__` and overlap | normalized | `action.tools.list`: degraded (per-turn refresh) | `tools-catalog-sources` |
| no `system/init` frame yet (before the first turn) | `action.tools.list.response` declaring the native source with an empty `tools` — served, never refused, because the key is advertised and a refusal of a request within every disclosed constraint is an unhonoured capability | synthesized | `action.tools.list`: degraded (this is what the level discloses) | `tools-catalog-sources` |
| complete user JSONL frame written (host-minted `uuid`, `origin` human) | allocate submission and run | synthesized | emulated | `message-admitted` |
| `command_lifecycle` transitions (`msg_lifecycle_v1`) | admission corroboration only | observed-only | observed | `command-lifecycle` |
| first reply frame of the turn (`user_message_uuid` echo — first stream event or first assistant frame) | admission confirmed + `run.started` | synthesized | emulated | `message-admitted`, `completed-text` |
| assistant block frame | portable message lifecycle (interleaves with stream events) | normalized | degraded (block granularity) | `completed-text`, `interleaved-blocks` |
| `stream_event` deltas (`--include-partial-messages`, enabled by default in this adapter) | `content.delta` (text/thinking) | native | native | `streaming-deltas` |
| `tool_use` block in assistant content | `action.call.requested` then `started` | normalized | degraded | `tool-roundtrip` |
| `tool_progress` frames | observed-only (OAP has no action-progress event) | observed-only | observed | `tool-progress` |
| user frame carrying `tool_result` (+ structured `tool_use_result`) | action terminal (completed/failed by `is_error`) | normalized | degraded | `tool-roundtrip`, `tool-failed` |
| reverse `can_use_tool` control request | `user_input.requested` (allow/deny), resolved by host response | normalized | native | `permission-gate` |
| `task_started` / `task_progress` | child lifecycle begin/progress | normalized | degraded | `subagent-task` |
| `task_notification` terminal | child settled | normalized | degraded | `subagent-task` |
| `task_updated` patch with terminal status | child settled (second legal shape) | normalized | same ledger | `task-updated-terminal` |
| `result` success, `is_error=false` | `run.completed` (final response from `result`, usage from `usage`/`modelUsage`) after child settlement | normalized | terminal normalization | `completed-text` |
| `result` success, `is_error=true` (API error text in `result`) | `run.failed` | normalized | degraded | `api-error-result` |
| `result` `error_max_turns` | completed with explicit limit stop reason | normalized | degraded | `max-turns` |
| `result` `error_during_execution` / other error subtypes (non-aborted) | `run.failed` | normalized | degraded | `error-result` |
| `terminal_reason` `aborted_streaming`/`aborted_tools` (any subtype) | `run.cancelled` | normalized | degraded | `interrupt-cancel` |
| `interrupt` control response (ack, `still_queued`) | cancellation intent acknowledged, never settlement | normalized | distinct from settlement | `interrupt-cancel` |
| synthetic `[Request interrupted by user]` user frame / `<synthetic>` assistant frame | in-turn evidence only | observed-only | observed | `interrupt-cancel` |
| `stop_task` control + `task_notification stopped` | child-targeted stop, not run cancel | normalized | degraded | `stop-task` |
| injected user turn (`origin` != human/absent) | session-scope observation, not a new run | lossy/observed-only | attribution rule | `injected-turn-origin` |
| second turn on same process | new run, same session | normalized | native server behavior | `second-turn` |
| nonzero exit after error result | `run.failed` already terminal; session retires | synthesized | transport failure handling | `process-exit` |
| EOF/decode failure before terminal | settle children, one `run.failed` | synthesized | transport failure handling | `malformed-stdout-line` |
| `--resume` / `--fork-session` / `--resume-session-at` | conversation recovery, distinct semantics | native input | reclassified, see below | `resume-fork` |
| session store mirroring / transcript files | persistence, not event replay | lossy | unavailable as OAP replay | `no-implied-replay` |

### Terminal arbitration

One reducer owns settlement, keyed off `terminal_reason` first (live-verified:
an interrupt settles as `subtype:"error_during_execution"` + `is_error:true` +
`terminal_reason:"aborted_streaming"`, so subtype and `is_error` alone can
never discriminate cancellation):

1. exactly one `result` frame closes each turn; it is the sole turn-terminal
   candidate;
2. **cancelled ⟺ `terminal_reason ∈ {aborted_streaming, aborted_tools}`** —
   the interrupt control response is intent acknowledgement and may list
   `still_queued` work that will still run; it never settles anything;
3. **completed ⟺ `is_error:false ∧ subtype:"success"`** with the reason from
   `terminal_reason` (`completed` or absent → `completed`; `max_turns` or
   subtype `error_max_turns` → completed with explicit limit stop reason —
   a native error subtype projected as a graceful OAP stop, recorded as a
   mismatch);
4. **failed** — everything else: `subtype:"success"` with `is_error:true`
   (API-error text in `result`, optional `api_error_status`), error subtypes
   (`error_during_execution`, `error_max_budget_usd`,
   `error_max_structured_output_retries`), and any other error terminal
   reason. Error text preference follows the reference `_error_result_text`:
   `errors[]`, then `result`, then non-success subtype, then HTTP status;
5. child tasks settle before the parent terminal is published (dual terminal
   shapes honored);
6. informational system frames arriving after the result belong to session
   scope or a successor turn — they must never be appended to a settled run
   (OAP terminality invariant);
7. a nonzero process exit after an error result corroborates the existing
   terminal (verified: exit 1 after both the auth-failure and interrupted
   results);
8. a synthetic `[Request interrupted by user]` user frame and the synthetic
   `<synthetic>`-model assistant frame are evidence within the turn, not
   projections of their own.

### Turn-vs-run and queue semantics

`queued_turn_count` on a result reports user sends still waiting in the
command queue: more turns (and results) follow without further input. An
interrupt receipt's `still_queued` UUIDs are work that **will** run unless
cancelled. The adapter must therefore model a submission as possibly spanning
 queued continuation turns, and must not treat the first result after a
 queued send as that submission's settlement without checking these signals.
 This is a genuine OAP semantic decision, recorded below as mismatch 2.

## Cancellation

- `interrupt` aborts the current turn; with the `interrupt_receipt_v1`
  capability the response carries `still_queued`, and with
  `interrupt_cancel_queued_v1` a `cancel_queued:true` variant cancels queued
  work in the same round-trip;
- `perTaskStopAffordance` changes whether an interrupt also kills background
  tasks (fail-closed kill when undeclared) — the adapter must declare its
  intent explicitly at initialize;
- `stop_task` targets one child and is followed by a `task_notification` with
  status `stopped`;
- closed-input one-shot runs (string prompt, stdin closed) still kill
  hold-back tasks at held-result release regardless of declarations;
- OAP mapping: `Cancel(runID)` -> `interrupt` (+ optionally
  `cancel_queued:true`), acknowledged immediately, settled only by the
  terminal-aborted result. Cancellation is run-scoped in OAP but
  turn/process-scoped natively: advertised degraded until fixtures prove the
  race behavior (completion winning an interrupt, queued survivors).

## Tools, permissions, hooks, MCP

- tool lifecycle evidence is complete on the message stream
  (`tool_use` -> `tool_progress` -> `tool_result`), joinable by `tool_use_id`;
- permission is a first-class reverse interaction: the CLI blocks on
  `can_use_tool` until the host answers allow/deny (with updated input and
  permission suggestions). This is materially richer than Makai (no
  permission surface) and is the first adapter where OAP permission
  interactions can be exercised natively — but only when the host routes
  prompts over stdio (the Python SDK does this by forcing
  `permission_prompt_tool_name="stdio"` in `_configure_can_use_tool`);
- `permissionPrompts: 'none'` fails closed (immediate denial) — a distinct
  mode the adapter must not silently substitute for host-mediated prompts;
- hooks and SDK-hosted MCP servers are reverse control traffic
  (`hook_callback`, `mcp_message`); the OAP adapter initially configures
  neither and advertises them unavailable, because hosting them re-creates
  the stdin-lifetime coupling described below;
- stdin must stay open while any reverse control may arrive; the Python SDK
  holds it until a result with no in-flight deferring tasks
  (`DEFERRING_TASK_TYPES = {local_agent, local_workflow}`, issue #1088).
  That heuristic is explicitly documented as incomplete ("a task that settles
  before the turn's result frame leaves the set empty at that result"); the
  adapter should prefer the `session_state_changed: idle` authoritative
  signal and bound its own waits.

## Session state and recovery — four distinct concepts

1. **Resume** (`--resume=<uuid>`, `--continue`): a new process loads an
   existing conversation. The CLI session UUID is the key.
2. **Fork** (`--fork-session`, optionally with explicit `--session-id`):
   resumed sessions fork to a new UUID rather than continuing in place.
3. **Truncating resume** (`--resume-session-at=<uuid>` +
   `--resume-drops-turn=<uuid>`): resume up to a chain entry, validated to
   discard only the declared turn; refusal is deterministic and must map to a
   rewind-recovery path, never a retry.
4. **Transcript persistence / mirroring**: `persistSession` (default on)
   writes `~/.claude/projects/<dir>/<sessionId>.jsonl` plus
   `<sessionId>/subagents/agent-<agentId>.jsonl`; a `SessionStore` mirror
   dual-writes entries (`--session-mirror`) and materializes resume through a
   temp `CLAUDE_CONFIG_DIR` seeded with redacted `.credentials.json`.

None of these is OAP event replay: there is no native cursor, no redelivery
contract, no gap semantics. Transcript reconstruction from the JSONL is a
separate, explicitly degraded capability if ever added. `reinitialize()`
re-sends `initialize` to re-arm in-flight permission prompts after a host
restart — reconciliation-shaped, and the only native reconnect primitive.

## Python / TypeScript / CLI parity

| Aspect | Python 0.2.152 | TypeScript 0.3.263 | Adapter consequence |
|---|---|---|---|
| runtime surface | `query()`, `ClaudeSDKClient` | `query()` returning `Query` (AsyncGenerator + controls) | same wire; Go replicates the control frame shapes |
| initialize payload | hooks, agents, excludeDynamicSections, skills, forwardSubagentText | additionally sdkMcpServers(+configs), systemPrompt(Snapshot), toolAliases, title, promptSuggestions, agentProgressSummaries, supportedDialogKinds, perTaskStopAffordance, plugins, planModeInstructions | send only the verified minimal set; extend per fixture |
| env handling | merges inherited env minus `CLAUDECODE`, sets `CLAUDE_CODE_ENTRYPOINT=sdk-py` | `env` option REPLACES the environment entirely (documented) | OAP adapter uses an explicit sanitized env, never ambient inheritance |
| submission correlation | `session_id` label only | `user_message_uuid`/`user_message_uuids` echo on reply and result | stamp a UUID on every submitted turn |
| background tasks | task lifecycle frames, `stop_task` | additionally `backgroundTasks()` control, `background_tasks_changed` level frames | track edges, not levels, for settlement |
| reverse requests | can_use_tool, hook_callback, mcp_message | additionally request_user_dialog, elicitation | implement can_use_tool first |
| session store | same contract (`@alpha` both) | same contract | out of initial scope |
| default system prompt | sends `--system-prompt ""` when unset (SDK strips the CLI default) | equivalent option surface | adapter must decide and document its default |

## P0 mismatches

1. **No native run identity:** one turn = one candidate run; adapter
   allocates `run_id` and uses the submitted-uuid/`user_message_uuid` echo
   for submission correlation (verified live on all three echo surfaces).
2. **Queued continuation turns:** `queued_turn_count` / `still_queued` mean a
   submission's settlement may be a later result than the first one after
   the send. OAP needs an explicit rule (bind run to send-echo UUIDs, not to
   result order). v1 closes the surface conservatively: one outstanding
   submission per process, overlap rejected locally before any write
   (queue/steer delivery modes stay unavailable).
3. **Cancellation is three mechanisms** (turn interrupt, queued-cancel,
   per-task stop) with acknowledgement distinct from settlement; OAP
   `run.cancel` maps to interrupt + terminal-aborted evidence only
   (live-verified: the aborted result is an `error_during_execution` subtype
   — only `terminal_reason` discriminates).
4. **Child settlement has two legal terminal shapes** (`task_notification`,
   `task_updated` patch) and the SDK's stdin heuristic for it is knowingly
   incomplete (#1088); prefer `session_state_changed: idle`.
5. **Result is turn-terminal, not stream-terminal:** informational frames
   legally follow it; the reducer must attribute them outside the settled
   run.
6. **Identity conflation traps:** logical stream label vs session UUID;
   assistant block UUID vs message identity; `task_id` vs `tool_use_id`;
   control `request_id` vs any OAP identity; `command_uuid` vs `tool_use_id`.
7. **Injected turns need origin attribution:** runtime-injected user-role
   frames (task notifications, peer messages) must not become phantom runs;
   `origin.kind` is the discriminator, with the caveat that host-sent prompts
   carry no origin unless the host stamps `{"kind":"human"}` (the adapter
   stamps it — only `human` is honored from an SDK host).
8. **No native sequence or replay:** ordering is arrival-only; replay is
   unavailable; persistence is not replay.
9. **Environment and credentials:** Python merges ambient env by default and
   resume materialization copies (redacted) credentials into a temp dir; the
   OAP adapter must instead use a sanitized child environment and must never
   forward ambient Anthropic credentials, consistent with the repository's
   standing credential policy (live-verified: the CLI honors a replaced env
   and ANTHROPIC_BASE_URL with x-api-key auth and no bearer).
10. **Capability truth is per-turn, not negotiated:** the recurring
    `system/init` frame (tools, models, capabilities list) is richer than a
    static descriptor; the adapter synthesizes its descriptor from it and
    refreshes per turn.
11. **`command_lifecycle` is live but untyped in both reference SDKs**
    (`msg_lifecycle_v1`-gated): the adapter observes it as corroborating
    admission evidence only, never as the correlation contract.
12. **Permission asks are content-dependent:** safe commands are auto-approved
    without any ask (verified: `echo`), so a permission-gate fixture must use
    an ask-gated command; `permission_denials` on the result is the
    authoritative denial record, the `permission_denied` system frame is
    best-effort.
13. **The CLI injects `<system-reminder>` content into the first provider
    request even under `--system-prompt ""`** — provider-request assertions
    in gates must be structural, never byte-exact.

Per the standing rule, each mismatch must lead to an explicit OAP
decision/revision or a documented adapter boundary; none is silently
compensated.

## Initial capabilities (frozen v1 surface)

- spawn argv (frozen): `<cli> --output-format stream-json --verbose
  --input-format stream-json --system-prompt "" [--model <m>]
  --include-partial-messages --permission-prompt-tool stdio
  --setting-sources= [--allowedTools <names...>] [<caller args>]` — the
  partial-messages and permission-prompt flags always on: partial messages
  make text streaming native, and routing every permission ask to the control
  channel is the only headless-correct prompt surface (without it, asks fail
  closed to deny). The child environment is fully replaced (no ambient
  inheritance, no ambient credentials)
- tool posture: the caller states one and there is no default. `--allowedTools`
  is present when the posture names tools and absent when it states the
  harness default, and `Config.Tools` is refused when it states neither, so
  the surface the child receives is always a decision somebody made rather
  than one nobody did. The flag form is taken from a working invocation
  against this pin in one-shot mode and is **unverified in stream-json mode**;
  the smoke gate is where that closes. Caller args follow the posture, so a
  caller can still add or override flags
- tool gating versus tool surface: these are two mechanisms and the ledger
  states only the second. `--permission-prompt-tool stdio` routes every call
  that consults the permission system to the control channel, and
  `--setting-sources=` stops a settings file pre-approving one behind the
  control layer's back. That is a per-call gate, not an allowlist: it decides
  what runs this time, while the posture decides what the child can attempt at
  all, and an excluded tool never enters the prompt. **Whether every tool
  consults the permission system is not established** — a spike observed a
  Bash call starting with no gate arriving, which the argv above says should
  not happen. Until the smoke gate asserts it, this adapter must not be
  described as gating every call
- initialize / capability revision: `emulated` (initialize exchange at open;
  per-turn `system/init` refresh recorded)
- session association (process + initialize + first-turn init frame): `emulated`
- submission/admission (host-minted uuid + `user_message_uuid` echo on the
  first reply frame): `emulated`
- run identity/status/sequence: `emulated` (adapter-owned contiguous
  sequence; native frames carry none)
- one foreground turn per process at a time: enforced locally (overlap
  rejected before any native write)
- text streaming: `native` (`--include-partial-messages` always on)
- tool lifecycle: `degraded` (requested + synthesized started + terminal by
  `is_error`; `tool_progress` observed-only)
- permission interactions: `native` (`can_use_tool` round trip live-verified;
  allow/deny surfaced as a single-choice input gate; deny → run continues
  with the CLI's denial semantics)
- cancellation: `degraded` (interrupt intent; settlement only via
  `terminal_reason` aborted_* → `run.cancelled`)
- child/background tasks: `degraded` (edge tracking for settlement;
  `stop_task` observed-only in v1)
- conversation resume/fork inputs: native CLI inputs; OAP `run.resume` is
  reclassified conversation-level and advertised `unavailable` in v1
- replay/reconciliation: `unavailable` (`reinitialize` is the only
  reconnect-shaped primitive)
- steer/BTW/side runs: `unavailable` in v1 (`shouldQuery`/`priority` exist on
  inbound messages but are unexercised)
- model catalog: `degraded` (init frame models; `list_models` unexercised)
- hooks, SDK MCP hosting, dialogs, elicitation: `unavailable` in v1

## Evidence corpus plan (frozen)

`fixtures/adapters/claude-code-2.1.263/`, each case with native JSONL
(sanitized, reduced), expected OAP, mapping, omissions, provenance pinning
the artifact hashes above. Required labels:

- handshake/session: `initialize-minimal`, `per-turn-init`,
  `second-turn`
- admission: `message-admitted`, `command-lifecycle`,
  `injected-turn-origin`, `queued-turn-count`
- run lifecycle: `completed-text`, `max-turns`, `api-error-result`,
  `error-result`, `interrupt-cancel`, `process-exit`,
  `malformed-stdout-line`
- streaming: `streaming-deltas`, `interleaved-blocks`
- tools: `tool-roundtrip`, `tool-failed`, `tool-progress`,
  `auto-approved-tool`, `tools-catalog-sources` (the empty pre-turn catalog;
  then the `system/init` tool and MCP server lists projected into one catalog,
  with a namespaced tool whose server the frame lists attributed to it, one
  whose server it does not attributed natively, and an overlapping pair
  (`files`, `files__nested`) resolving to the longest match)
- interactions: `permission-gate`, `permission-deny`
- children: `subagent-task`, `task-updated-terminal`, `stop-task`
- hygiene: `keep-alive-ignored`, `unknown-frame-ignored`,
  `no-implied-replay`, `resume-fork`

Case sources: the live-binary probe transcripts (credential-free auth-failure
run, loopback text/tool/interrupt runs, initialize exchange) reduced and
sanitized to the pinned frame shapes, plus constructed variants for the
shapes the probes cannot produce hermetically (subagent/task edges, resume
inputs). Corpus fixtures never contain real paths, ids, or prompts from the
probe environment.

A gated live-process test (explicit opt-in, sanitized env, hermetic loopback
provider) follows the Hermes/DeepSeek gate pattern; credential policy from
`research/zai-china-coding-plan-evidence.md` applies unchanged.

## Corpus outcome (delivered)

`fixtures/adapters/claude-code-2.1.263/` carries 12 cases covering all 29
required labels; `adapter/claude/corpus_test.go` drives the production codec,
transport, and reducer through the public adapter over real pipes (the scripted
peer writes fixture lines the reader must decode; every adapter write is
compared against the transcript with the minted control-request id collapsed
to a `@request` placeholder). The initialize exchange at open is issued
exactly like the production process factory, so its wire shape is behavioral
evidence, not metadata.

Two properties the expected traces pin that no type in either implementation
holds, recorded here because a reader refactoring one allocation site has no
other way to learn them. **Submission, message, run and event ids come from one
counter**, so `message-2`, `run-3`, `event-4` in a single turn is a statement
about allocation order across four kinds rather than four independent
sequences; reordering two allocations changes every later id in the trace. And
**a run reports the model captured when its submission was accepted**, not the
one session state holds when it starts, even though `system/init` publishes a
model earlier in the same turn. The corpus demonstrates the second only at the
first run of a session: every `init` frame in every case publishes
`claude-sonnet-4-5`, so from the second run onward a reducer that reads session
state live is indistinguishable from one that captured it. Both properties
therefore want a named test beside the corpus rather than the corpus alone;
`zig/src/adapter/claude/session.zig` carries one each.

The second property is guarded differently in each implementation, which
matters to anyone changing either. The Go reducer buffers an observation whose
echo does not match the run's submission uuid (`reserveObservation`), and
`system/init` carries no `user_message_uuid`, so the turn's own init is still
in the buffer when `run.started` is built and is replayed only afterwards; the
captured and live values are necessarily equal at that point, and a Go mutation
from one to the other is unobservable rather than untested. The Zig reducer is
fed frames synchronously and applies init when it sees it, so there the capture
is what holds the rule. Same emitted trace, different guard: removing the
buffering in Go, or the capture in Zig, breaks only that side, and the shared
corpus reports neither. The consequence is protocol-visible rather than internal:
`current_model_id` is a wire field, so when an adapter adopts an init is
observable behaviour and the oracle governs it. The rule both sides now hold is
that **a pending run's init is not adopted until the run starts**; an init
arriving with no pending run, or with a started one, is adopted when it
arrives. The Zig reducer buffers every observation whose echo does not match
the pending run's submission uuid and replays after `run.started`, which is the
Go mechanism rather than an init special case. No corpus case can express this:
the corpus feeds native frames and compares emitted envelopes, while a state
read is an inbound OAP request, so this is a unit test on both sides or it is
nothing.

A third property surfaced porting `tool-lifecycle`, and this one the corpus
does pin: **`source` on an action call is looked up, not stamped.** The
reducer projects a name-to-source map from `system/init`'s tool list, so a
call to a tool that init never advertised carries no source at all -- which is
why the case's second run, calling `Read` against an init advertising only
`Task` and `Bash`, emits three call envelopes with the member absent while the
first and third runs' `Bash` calls carry `claude-code-native`. The Go oracle
then publishes only the entries whose source is the endpoint's own declared
source, so a tool namespaced to a declared MCP server is not attributed either
until a served `action.tools.list` response supplies one. The Zig port asks
that as a predicate (`servedByHarness`) rather than deriving an `mcp:<server>`
id: it serves no tool catalog, and a helper named for a source it never
returns is the shape that rots invisibly in a tree with no comments.

A **third** property the corpus cannot discriminate came out of porting the
terminal deferral, and it is the sharpest of the three because a reducer can
get it wrong in both directions and pass every case. A result frame does not
always settle its run: a positive `queued_turn_count`, or an unsettled child
whose `task_type` is `local_agent` or `local_workflow`, holds the frame, and
the run settles later -- when the child reports terminal through
`task_notification` or a terminal `task_updated` patch, or when a
`session_state` of `idle` publishes whatever is held regardless of children.
`queued-continuation` pins the first half, because a second result frame
arrives and its content, not the held one's, is what `run.completed` carries.
`background-children` pins nothing: its two runs each defer and then publish,
and a reducer that ignored children entirely emits the identical trace with
identical ids, sequences and timestamps, because the clock only advances when
an envelope is emitted and no envelope is emitted in between. The Zig port
settled at the result frame for one commit and `background-children` passed.
Four rules therefore carry named tests on the Zig side, each verified by
mutation: a local child holds the terminal, a child of any other kind holds
nothing, a non-terminal patch does not settle a child, and `idle` -- and only
`idle` -- publishes past a child that is still running.

One of the thirteen expectations is not portable, and that is a fact about
the corpus rather than about either reducer. `process-exit` fails its run with
`message: "io: read/write on closed pipe"`, which is the Go runtime's own text
for a closed pipe, reached through `fmt.Sprint(s.client.Err())`. No
implementation on another runtime can emit that string, so the Zig side does
not claim the case; it asserts instead that every envelope matches and that
the last one differs at exactly `payload.error.message` and nowhere else, with
both texts named in the test. If the expectation is ever regenerated against a
transport-neutral message the assertion fails and the case can be claimed.
`malformed-stdout` is the contrast that shows the line is real: its failure
message is the adapter's **own** rpc error, so the Zig codec reproduces it
verbatim and the case is claimed like any other.

The Zig reducer keeps no per-run terminal flag. It clears its run slot at every
settlement -- native terminal, transport death, decode failure -- so
`self.run == null` is the only reachable "this run is over" state, and a
mutation removing any `run.terminal` disjunct killed no test because none of
them could fire. The flag and its seven readers are gone rather than left as
branches a reader would assume were load-bearing. The oracle needs its own,
because a gate or a tool there holds a pointer to a run the session has already
dropped. `Tool.terminal` and `Gate.resolved` stay on the Zig side for the same
reason the oracle needs them: the sweep at settlement has to tell an open call
from a settled one.

`tools-catalog-sources` closes the `source` story and shows why the predicate
that `tool-lifecycle` alone justified was not enough. Attribution has two
modes, and a **state read changes which one is in force**: before anything
serves a catalog, only tools whose source is the endpoint's own declared
source are attributed, so an MCP-namespaced call carries no `source` at all;
once `action.tools.list` has served the catalog once, the served map is the
attribution and the same call carries `mcp:files`. The case is two identical
runs on either side of one `action.tools.list`, and they differ in exactly
that member. So the Zig port now resolves the real source id -- longest
matching server namespace after `mcp__`, native when nothing matches -- rather
than asking whether the harness owns the tool. Serving a catalog before an
`init` has published one is the boundary case that matters: it must not latch
an empty served map, or the native tools lose their source for the rest of the
session, and the corpus does not exercise it because its first assertion is
followed by an assertion after `init`.

Four resolution rules carry tests on the Zig side, because the case's three
tool names cover only one of them: the longest of two overlapping server
namespaces wins regardless of the order the servers are advertised in, a
server name that matches without the `__` separator is not a match, a
namespace no server claims is native, and a name repeated in `init` is served
once.

Two divergences in the Zig port are deliberate and bounded. A gate's prompt is
built by re-encoding the parsed `input` value, where Go interpolates the raw
`json.RawMessage` bytes; the two agree for compact native frames, which is
every frame the corpus holds, and differ on interior whitespace. And the Zig
reducer clears its run slot at settlement while the oracle keeps a run
addressable through `gate.run` and `tool.run`, so the `run.terminal` disjunct
that guards `openGate` and `resolve` on the Go side cannot fire on the Zig
side yet -- a mutation removing it kills no test. Both disjuncts were removed with the
flag, as recorded above.

Cases: initialize-lifecycle, admission-corroboration, settlement-statuses,
interrupt-cancel, queued-continuation, background-children,
streaming-provenance, tool-lifecycle, permission-gates, process-exit,
malformed-stdout, hygiene-recovery. Each has the five-file layout with the
full artifact-pin provenance block; expected OAP traces are byte-stable
across runs (only wall-clock stamps normalize) and every admitted run passes
executable OAP schema + state-machine validation, with cancel envelopes
prepended behind `run.cancelled` terminals per protocol requirement.

Implementation discoveries made executable by the corpus (both fixed):

1. **thinking deltas carry their payload under `thinking`, not `text`.** The
   Messages streaming API shapes `content_block_delta` as
   `{"delta":{"type":"thinking_delta","thinking":...}}`; the reducer first
   read only the `text` member and emitted empty reasoning parts. The corpus
   streaming case fails before the fix (schema-invalid reasoning part) and
   passes after.
2. **Resolved permission gates must retire from the interaction table.** A
   resolved or CLI-withdrawn `can_use_tool` gate that lingers makes a later
   gate in the same session unresolvable (lookup finds the dead entry). Gates
   are now deleted on resolution and on control_cancel withdrawal.

The `stop-task` label is behavioral: v1 never writes any stop_task-shaped
control request (the only cancellation write is interrupt; children settle
via task frames), asserted across every corpus case by the allowed-write
check.

## Process integration gates (delivered)

`adapter/claude/process_integration_test.go` follows the Hermes/DeepSeek
gate pattern; ordinary and short runs skip both gates, and credential
presence alone never enables execution.

- `OAP_CLAUDE_SMOKE=1` with absolute `OAP_CLAUDE_BIN`: credential-free
  startup evidence — spawn, initialize exchange (readiness on this
  boundary), idle state, and clean stdin-EOF teardown inside the bounded
  grace. No submit occurs, so no provider traffic is possible; the child
  environment contains no credentials and every proxy is dead.
- `OAP_CLAUDE_INTEGRATION=1`: the hermetic behavioral gate — an in-process
  loopback speaking streaming Anthropic Messages is the only reachable
  endpoint; the child gets a fixed allowlisted environment whose single
  credential is the test-owned fixture key. Asserts echo-converged
  admission, streamed deltas, `run.completed` carrying the fixture text,
  loopback receipt of a `/v1/messages` request with the passed model, the
  `x-api-key` + `anthropic-version` headers, and no bearer token, then a
  clean close. Request assertions are structural, per the pin.
- `OAP_CLAUDE_SHA256` (64 hex) binds either gate to the exact frozen binary.

Both gates were executed against the pinned linux-x64 binary
(sha256 `26d02035…d5ba`) with the digest bound, five consecutive times each,
all green.

### What the corpus cannot see, and why that is structural

Review of the Zig port found two divergences from the oracle that all thirteen
cases pass over, a sweep of every entry point for the same shape found four
more, and a second review round found that the sweep had itself been incomplete:
it walked the reducer's entry points and not the codec's, which is where the
largest of them was. The class is specific: **any oracle branch reachable only from a frame the
fixtures happen not to contain.** The corpus was assembled from observed
sessions, so it holds the shapes a healthy harness produces and almost none of
the shapes a misbehaving one does. Seven divergences, none visible to a single
case:

- A repeated `tool_use` id and a `tool_result` naming no call in flight both
  fail the run with `claude_tool_lifecycle`. Every fixture pairs its calls.
- A failure message joins several `errors` with `"; "` and falls back to
  `API error (HTTP N)`. Every fixture's error list has exactly one entry.
- A reverse control request that is not `can_use_tool` is **external activity**:
  it fails the run with `claude_external_activity` and marks the session
  unusable. The corpus's only observed control requests are `can_use_tool`.
- That unusable mark is a latch, not a flag, and its scope is narrower than it
  first looks: `applyObservation` consults it only when a run is current, so a
  broken session still adopts an idle `system/init` while refusing to reduce
  anything into a run and admitting no further submission. No case continues
  past its terminal, so nothing could see either half.
- A `control_cancel_request` withdraws the matching open gate, resolving it
  `cancelled` and returning the run to `running`. No case contains one.
- A `text_delta` carrying no `text` member still produces a content part rather
  than nothing, because the oracle decodes into a struct whose zero value is the
  empty string. Every fixture's delta carries its text.

  **The two implementations do not agree on that part's shape, and the
  divergence is in the port's favour**, which is why it is listed here as a
  divergence rather than among the matched behaviours. `protocol.ContentPart`
  tags `text` and `reasoning` `omitempty`, so the oracle emits `{"type":"text"}`
  with no `text` member at all -- and `contentPart` in
  `schema/v0.1/common.schema.json` carries no `required` list, so that shape is
  schema-legal but says less than the port's `{"type":"text","text":""}`. The
  port emits the member. Nobody should read this as parity: it is a deliberate
  choice to emit the more explicit of two legal shapes, and if the schema ever
  requires `text`, the oracle is the side that breaks.
- **The codec is fail-closed on shape, and that is the largest of the six.**
  `native.DecodeObservation` and `DecodeControlRequest` enforce a required-member
  table per frame type -- a `user` frame needs `message.role` and
  `message.content`, an `assistant` frame needs `message.model` and an array
  `message.content`, `system/init` needs `session_id`, `model` and `tools`, a
  `task_notification` needs six members *and* a terminal `status`, a
  `can_use_tool` needs `tool_name`, `tool_use_id` and `input` -- and a failure
  is not a skipped frame but `client.closeWith`, which fails the run
  `claude_process_exit` and makes the session unusable. Every fixture is
  well-formed, so no case reaches any of it.

Three refinements came out of review of the port, and each is a bound worth
stating rather than a detail. **A refusal must name itself**: the oracle wraps
every one in a named error -- `claude rpc: invalid stream-json message`,
`claude rpc: invalid control-plane message`, `claude native: invalid frame for
a known type` -- and the run it fails carries that text as its message, so a
port that refuses the right frames with an empty diagnostic still diverges
where anyone would look first. **The typed decode refuses more than missing
members**: `is_error: "yes"` or `queued_turn_count: "2"` on an otherwise
complete result frame kills the Go transport, because the members are declared
`bool` and `*int`. The Zig port now carries a second table for that. It began as
the four frames whose members the reducer reads, which review showed was the
wrong bound twice over: Go's decode is recursive, so a top-level-only table was
tolerant exactly where no fixture could see it, and Go's declared integers are
`int64`, so a fractional or oversized number is fatal there and was accepted
here. Every frame `DecodeObservation` types now has a table covering its members
to the depth Go declares them, with an integer need that refuses a float and an
item list for object and object-array members.

**The diagnostics the port reproduces are the ones `message.go` composes, not
the ones it borrows**, and separating those two took a probe rather than a read.
`parseObject` reports whatever `rejectDuplicateKeys` and its decode return, and
that is a mix. Fourteen malformed frames put through the oracle produced twelve
distinct strings: `{"type":}` reports `missing value after object key`,
`{"a":tru}` reports `invalid character '}' in literal true (expecting 'e')`,
`{3:1}` reports `object member name must be a string`. Those are
`encoding/json`'s scanner vocabulary, and reproducing it would bind this port to
the wording of a parser it does not use and rot silently on a Go upgrade with no
test on either side able to notice, so the port substitutes one string, `frame
is not decodable JSON`, for all of them. The classification is identical either
way -- both sides refuse with the invalid-message error, which is what the
reducer acts on -- and only the text a human reads differs.

`rejectDuplicateKeys` also composes three strings of its own, and exactly one of
them is reachable. `trailing JSON value` fires for a frame carrying a second
object, and the port now reproduces it from its own token walk: the walk already
tracks container depth for the duplicate check, so a value arriving after the
top-level container closes is a fact it can state. `object key is not a string`
and `unexpected closing delimiter` are dead, because Go's scanner errors before
the adapter's own check is reached -- a non-string key in any of the three
nesting positions reports `object member name must be a string`, and every input
that would reach the closing-delimiter branch reports a scanner error first. That
is the same "dead by construction" claim the replay guard taught us to distrust,
so it is recorded as a probe result against a pinned commit rather than as a
property, and the two strings are named here so a future reader can retest them.

The nested-cause chain in `requireObject` is reproduced verbatim rather than
substituted, because a member that reached it came out of a successful decode, so
its only possible failure is the bookend check whose text the adapter owns.

Two limits of the Go decoder are not diagnostics at all but behaviour, and the
port had to reimplement both. `encoding/json` refuses past 10,000 levels of
nesting, and the port accepted such frames; worse, its own duplicate-key walk
pushed a frame per level before the value tree was built, so the frame limit had
stopped bounding memory the way the oracle's does. The cap is now enforced at the
boundary a probe established -- 9,999 nested containers inside the frame object
accepted, 10,000 refused, arrays and objects alike -- and it keeps the oracle's
`exceeded max depth` wording, because a limit the port implements itself is one
whose message it owns. And `encoding/json` replaces an unpaired `\uD800`-`\uDFFF`
escape with U+FFFD and accepts the frame, where Zig's `std.json` returns a syntax
error: the port was killing runs the oracle keeps alive. A lone surrogate escape
is now rewritten before the walk and the parse, with the oracle's pairing rule, so
`\ud83d\ud83d\ude00` is a replacement character followed by an emoji rather than
two emoji, and a backslash that is itself escaped starts no escape.

The quoting bound is the one place review asked for more and the answer was no.
`quoteGo` matches `%q` through U+00FF, including invalid UTF-8, which it escapes
byte by byte as `\xNN`. Above that it passes unprintable runes through. Matching
there is not a predicate that can be extended: `strconv.IsPrint` is a table, and
one of its inputs is the set of unassigned code points, so a partial extension
leaves the claim false and a complete one ships a copy of the Unicode database
bound to a Go version, with nothing on either side able to notice when it drifts.
The consequence is bounded and worth stating rather than hiding: both
implementations refuse the same frames, and only the text of an already-failing
run differs.

A tool block the harness leaves nameless has no correct handling in the oracle,
and this is the one place the port deliberately does something else. `ContentBlock`
declares `name` as a plain string, so an absent one decodes to `""` and the
oracle calls `startTool` with it; only an empty `id` skips the block. The payload
struct then carries `json:"name,omitempty"`, so the member is dropped, and
`action.schema.json` makes `name` **required** on `callRequested` and
`callStarted` and types it `nonEmptyString`. Feeding that frame to the Go adapter
and handing the trace to the shared validator returns
`schema_invalid: missing property 'name'` twice. Emitting `""` instead, which is
what this port did first, fails the same validator on `minLength`. There is no
shape a nameless block can take that the schema accepts, so the reducer fails the
run with `claude_tool_lifecycle` rather than emitting either one: a failed run is
a valid trace, and the invariant every adapter exists to hold is that the
validator accepts what it emits. Filed upstream against the Go adapter.

The codec refuses a *wrong-typed* id or name, which is what the oracle's decode
does.

A user frame whose content is not a list of content blocks is skipped whole.
`UserFrame.Message.Content` is a `json.RawMessage` and `Blocks()` decodes it
lazily into `[]ContentBlock`, so one malformed item makes the whole decode fail
and the oracle's `if !ok { return }` drops every block in that frame, leaving any
open call to be cancelled at settle. This port iterated the array and skipped
only the offending item, so a `tool_result` carrying `"is_error":"yes"` beside a
well-formed sibling completed a call the oracle never completes. The codec's
block typing is now reachable from the reducer for user frames as well, and the
frame is skipped rather than filtered.

Three members a harness may write as JSON `null` each needed the oracle's decode
rather than a type test. Go reaches all three through `encoding/json`, where
unmarshalling `null` into a non-pointer is a documented no-op: `name: null` on a
`tool_use` block leaves `ContentBlock.Name` as `""` and takes the nameless path
above, `origin: null` leaves `UserFrame.Origin` nil so the frame is reduced
rather than skipped as foreign, and `content: null` on a `tool_result` leaves the
string empty so `normalizedToolResult` emits `""`. This port read `.string` off
the raw value for the first, which is an inactive-union-field access and a panic;
treated the second as a foreign origin and dropped every `tool_result` in the
frame, leaving calls open that the oracle completes; and passed the third through
as `null`. All three verdicts were read off the oracle with a probe.

Two numeric conversions were illegal behavior rather than a divergence. Go's
declared integers are `int64` and its decode refuses a fractional or oversized
number outright, so the typed table is what keeps a float away from
`integerMember`; a gap there would have reached `@intFromFloat` and panicked in
Debug and ReleaseSafe and been undefined in ReleaseFast. The conversion is now
range-checked at the site, because a table that is correct today is not the same
thing as a conversion that cannot trap. The usage total is computed with `+%`:
Go's `InputTokens + OutputTokens` is int64 arithmetic that wraps silently, and
matching the oracle means wrapping, not trapping.

Two payload shapes came from the same review and are worth separating from the
codec, because they are what a *schema* requires rather than what a decoder
refuses. `arguments_json` is required on `action.call.requested`, and the oracle
satisfies it for a `tool_use` block with no `input` by marshalling the absent
raw message to the literal `null` before the payload's `omitempty` can see it --
so the member is emitted, not dropped. A port that skips the member instead
emits a trace the shared validator rejects, which is the one invariant every
adapter here exists to hold. And `duration_ms` is `omitempty` on all three
terminal payloads, so a turn that settles instantly omits it rather than sending
a zero. **And a submission arriving under a live run is refused**, with
`ErrRunActive` in the oracle, rather than replacing it; silently substituting
orphans the first run's tools and gates and restarts its sequence.

The last one carries a lesson about the sweep itself. Porting it rejected
thirty-three of this port's own unit-test frames, because they had been written
minimal -- an `assistant` frame with content and no model, an `init` with a
model and no tools. Those frames are ones the oracle refuses outright, so a
suite built on them was asserting reducer behaviour on inputs that can never
reach a reducer. Writing tests against the frame vocabulary rather than against
the *decoded* vocabulary is a trap any port can fall into, and the codec's
validation table is what makes it visible.

The lesson generalises past this adapter, which is why it is recorded here
rather than in a commit. A hermetic corpus of observed traffic proves a reducer
handles what a harness *does*; it says nothing about what the reducer does when
the harness misbehaves, and that is exactly the half a second implementation is
most likely to get wrong. Every port should sweep its oracle's entry points for
branches no fixture reaches, and carry a named test for each, rather than
trusting a green corpus.

One of those removals was wrong, and the way it was wrong is the more useful
record. The replay loop's "stop once the run is gone" guard was deleted as dead
because the only settlement reachable from a buffered frame was `settle`, which
needs an echo match a buffered frame cannot have. That was true when it was
written and stopped being true two commits later, when `failRun` arrived for the
tool lifecycle: a buffered `assistant` frame carrying a duplicate `tool_use`
fails the run mid-replay, and without the guard the next buffered `system/init`
still reaches `observeIdle` and overwrites the model and catalog the *following*
run will report. **"Dead by construction" is a claim about the code at a moment,
not a property of the guard**, and deleting on that basis needs re-checking
whenever a new settlement path appears. The oracle's own loop returns on
`run.terminal && run.deferred == nil`, which is the same rule with the deferred
case spelled out.

`sweepRun` now prunes what the oracle prunes. Go deletes the settled run's gates
from `s.interactions` and its children from `s.children`; it does **not** delete
from `s.tools`, which is keyed by native id and lives as long as the session, so
the port keeps its tool list too and only the two collections Go deletes from are
pruned here.

Three guards found during the same sweep were dead by construction rather than
untested, and were removed rather than left: an empty cancel id cannot reach
`cancelGate` because the codec refuses one, and two guards in the tool paths
could not fire. What survived removal has a test.

### Open question: is the permission gate the tool boundary, or only most of it?

The child is spawned with `--permission-prompt-tool stdio` and
`--setting-sources=` and no `--allowedTools` (`adapter/claude/adapter.go`). Read
together those say the boundary is the gate: every tool call arrives as a
`can_use_tool` control request that the control layer answers, and no settings
file can pre-approve anything behind it. If that reading is right, the absence
of an allowlist is not a gap, because nothing runs unanswered.

It is not established. A spike reported a `Bash` call completing with no gate
observed. Both cannot be true. Either some tools do not consult the permission
system at that pin, in which case an allowlist is the only thing that stops
them and the posture is materially weaker than the flags suggest; or the
spike's auto-allowing harness answered a gate without recording that it had,
in which case the boundary held and the report was an artifact of the
instrument.

Nothing in the tree settles it, and the corpus cannot: its `can_use_tool`
frames are scripted, so they prove the reducer surfaces a gate it is given,
never that the CLI raises one for every tool. The decidable form is a
real-process assertion, and it belongs in the smoke gate rather than in prose
here: provoke a `Bash` call against the pinned binary and fail if the call
settles without a `can_use_tool` arriving first. Until that runs, an adapter
must not be described as gating every tool call.

Implementation discovery made executable by the smoke gate (fixed): the
default factory originally ran the initialize exchange inside
`Factory.Start`, i.e. before the session's dispatch loop existed — and the
control response's ordering barrier needs a reducer to acknowledge it, so
every production `Open` deadlocked until the initialize timeout. The
corpus missed this because its injected factory returned before waiting on
the exchange. The exchange now runs in `Open` after dispatch is live; the
corpus-style injected-factory path is unchanged.

## Review outcome (tranche verified)

An independent adversarial review of the tranche produced eight findings;
every one was reproduced against the code (seven by failing regression
tests, one by inspection) before any fix. All confirmed findings are fixed,
each with a fail-before/pass-after regression test:

1. **A run terminal published while a tool call or permission gate was open
   produced an OAP-invalid trace** (`pending_tool_at_terminal`,
   `pending_interaction_at_terminal`) and left the CLI's ask unanswered.
   Terminals now sweep first: open tools emit `action.call.cancelled`,
   abandoned gates are answered with a control error and resolved as
   cancelled, and the run's child edges are pruned.
2. **Submit-context cancellation after the write released the reservation
   over a still-executing native turn**, allowing overlap, and turn-1
   deltas were misattributed into run 2's transcript. The ambiguous loss
   now retires the session (the rpc layer already retired the transport on
   the same race), and stream events that attribute themselves to another
   turn are that turn's evidence only.
3. **A deferred terminal was never published on the
   `session_state_changed: idle` signal**, wedging the session while the
   process lived. The idle signal now releases a held terminal candidate —
   background tasks can outlive their turn, so idle closes it regardless
   of unsettled tracked children.
4. **Child and tool bookkeeping was session-global**: a prior run's stale
   unsettled child deferred every later run's terminal, and a late
   `tool_result` for a prior run's tool failed the current run. Children
   are now run-scoped and pruned at their run's terminal; tool completions
   for another run are evidence only.
5. **An unknown `result` subtype killed the transport.** The reference
   hosts type the subtype as success-or-string; an unknown error subtype
   now arbitrates as `run.failed` with `claude_<subtype>`, keeping the
   session usable.
6. **Codec strictness edges had no tests** (CR, empty frame, unterminated
   tail, non-UTF-8, exact/oversize limits); now covered in
   `internal/rpc/codec_test.go`.
7. **The native session UUID was write-only**; it is now surfaced as
   session state metadata (`claude_native_session_id`), making the
   association evidence observable.
8. **Two weak tests**: the concurrent-correlation test used identical
   payloads (it now tags each call and verifies per-caller delivery), and
   the overlap-rejection test claimed "no second user turn reached the
   wire" without asserting it (it now counts user-turn writes).

Categories with no findings: concurrency (lock order, barrier acks,
teardown, no races under `-race`), wire/codec correctness, and the terminal
arbitration happy paths. After the fixes, the full repository battery
(vet, full, short, race, `oap check`, gofmt, `git diff --check`) is green,
the corpus remains byte-stable, and both process gates re-ran green three
times against the pinned digest-bound binary.
