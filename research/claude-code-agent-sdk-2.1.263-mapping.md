# Claude Code / Claude Agent SDK 2.1.263 mapping ledger

Status: pinned evidence boundary for the fourth production OAP adapter (after
Codex app-server, ACP, and Makai). This is an implementation input, not an
interoperability claim. No adapter code exists yet; this ledger defines what a
truthful Go wrapper may claim.

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
  capabilities list) — it recurs, it is not a one-time ready frame;
- `assistant`: one frame per completed content block while streaming; several
  consecutive frames can share `message.id`; the turn's stop reason and final
  usage arrive only on the result frame (`sdk.d.ts` `SDKAssistantMessage`);
- `user`: the CLI's own user-role content, chiefly `tool_result` blocks
  answering the assistant's `tool_use`;
- `stream_event`: raw Anthropic API stream events, present only with
  `--include-partial-messages`;
- `result`: exactly one per turn, after that turn's messages; the
  turn-complete signal;
- `system` informational subtypes after a result are legal (task
  notifications, `session_state_changed`, prompt suggestions) — a result does
  not imply stream quiescence;
- reverse `control_request`: `can_use_tool`, `hook_callback`, `mcp_message`
  (Python); TS additionally `request_user_dialog` and elicitation routing;
  `control_cancel_request` withdraws an in-flight reverse request;
- `transcript_mirror` frames when `--session-mirror` is enabled.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| CLI session UUID (`session_id` on emitted messages) | `session_id` | Typed endpoint-scoped association. It is also the resume input, but possession of it is not proof of recoverability. |
| logical stream label (`"default"` on written user messages) | private | Never an OAP identity; must not be conflated with the session UUID. |
| `user_message_uuid` (host-stamped on submit, echoed on first reply frame and result) | `submission_id` correlation | Strongest native admission correlation; host mints it, CLI echoes it. |
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
| per-turn `system/init` frame | capability truth refresh | normalized | emulated, per-session descriptor | `initialize-minimal` |
| complete user JSONL frame written | allocate submission and run; admit | synthesized | emulated | `message-admitted` |
| first reply frame of the turn (`user_message_uuid` echo) | `run.started` | synthesized | emulated | `completed-text` |
| assistant block frame | portable message lifecycle | normalized | degraded (block granularity) | `completed-text` |
| `stream_event` deltas (`--include-partial-messages`) | `content.delta` | native | native once exercised | `streaming-deltas` |
| `tool_use` block in assistant content | `action.call.requested` then `started` | normalized | degraded | `tool-roundtrip` |
| `tool_progress` frames | action progress | normalized | degraded | `tool-progress` |
| user frame carrying `tool_result` | action terminal (completed/failed by `is_error`) | normalized | degraded | `tool-roundtrip`, `tool-failed` |
| reverse `can_use_tool` control request | `interaction.requested` (permission), resolved by host response | normalized | degraded pending fixture | `permission-gate` |
| `task_started` / `task_progress` | child lifecycle begin/progress | normalized | degraded | `subagent-task` |
| `task_notification` terminal | child settled | normalized | degraded | `subagent-task` |
| `task_updated` patch with terminal status | child settled (second legal shape) | normalized | same ledger | `task-updated-terminal` |
| `result` success, `is_error=false` | `run.completed` after child settlement | normalized | terminal normalization | `completed-text` |
| `result` success, `is_error=true` (API error text in `result`) | `run.failed` | normalized | degraded | `api-error-result` |
| `result` `error_max_turns` | completed with explicit limit reason | normalized | degraded | `max-turns` |
| `result` `error_during_execution` / other error subtypes | `run.failed` | normalized | degraded | `error-result` |
| `terminal_reason` `aborted_streaming`/`aborted_tools` | `run.cancelled` | normalized | degraded | `interrupt-cancel` |
| `interrupt` control response (ack, `still_queued`) | cancellation intent acknowledged | normalized | distinct from settlement | `interrupt-cancel` |
| `stop_task` control + `task_notification stopped` | child-targeted stop, not run cancel | normalized | degraded | `stop-task` |
| injected user turn (`origin` != human/absent) | session-scope observation, not a new run | lossy/observed-only | attribution rule | `injected-turn-origin` |
| second turn on same process | new run, same session | normalized | native server behavior | `second-turn` |
| nonzero exit after error result | `run.failed` already terminal; session retires | synthesized | transport failure handling | `process-exit` |
| EOF/decode failure before terminal | settle children, one `run.failed` | synthesized | transport failure handling | `malformed-stdout-line` |
| `--resume` / `--fork-session` / `--resume-session-at` | conversation recovery, distinct semantics | native input | reclassified, see below | `resume-fork` |
| session store mirroring / transcript files | persistence, not event replay | lossy | unavailable as OAP replay | `no-implied-replay` |

### Terminal arbitration

One reducer owns settlement:

1. exactly one `result` frame closes each turn; it is the sole turn-terminal
   candidate;
2. `subtype` + `is_error` + `terminal_reason` jointly pick completed,
   cancelled, or failed — notably `subtype:"success"` with `is_error:true` is
   an API-error failure, and `is_error:true` error subtypes never mean
   cancelled;
3. cancellation is proven only by `terminal_reason` in
   {`aborted_streaming`, `aborted_tools`}; the interrupt control response is
   intent acknowledgement and may list `still_queued` work that will still
   run;
4. child tasks settle before the parent terminal is published (dual terminal
   shapes honored);
5. informational system frames arriving after the result belong to session
   scope or a successor turn — they must never be appended to a settled run
   (OAP terminality invariant);
6. a nonzero process exit after an error result corroborates the existing
   terminal; the Python SDK's `ResultError` rewrite (`query.py`
   `_error_result_text`) is the reference for preferring `errors[]`, then
   `result`, then subtype text.

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
   allocates `run_id` and uses `user_message_uuid` for submission
   correlation.
2. **Queued continuation turns:** `queued_turn_count` / `still_queued` mean a
   submission's settlement may be a later result than the first one after
   the send. OAP needs an explicit rule (bind run to send-echo UUIDs, not to
   result order).
3. **Cancellation is three mechanisms** (turn interrupt, queued-cancel,
   per-task stop) with acknowledgement distinct from settlement; OAP
   `run.cancel` maps to interrupt + terminal-aborted evidence only.
4. **Child settlement has two legal terminal shapes** (`task_notification`,
   `task_updated` patch) and the SDK's stdin heuristic for it is knowingly
   incomplete (#1088); prefer `session_state_changed: idle`.
5. **Result is turn-terminal, not stream-terminal:** informational frames
   legally follow it; the reducer must attribute them outside the settled
   run.
6. **Identity conflation traps:** logical stream label vs session UUID;
   assistant block UUID vs message identity; `task_id` vs `tool_use_id`;
   control `request_id` vs any OAP identity.
7. **Injected turns need origin attribution:** runtime-injected user-role
   frames (task notifications, peer messages) must not become phantom runs;
   `origin.kind` is the discriminator, with the caveat that host-sent prompts
   carry no origin unless the host stamps `{"kind":"human"}`.
8. **No native sequence or replay:** ordering is arrival-only; replay is
   unavailable; persistence is not replay.
9. **Environment and credentials:** Python merges ambient env by default and
   resume materialization copies (redacted) credentials into a temp dir; the
   OAP adapter must instead use a sanitized child environment and must never
   forward ambient Anthropic credentials, consistent with the repository's
   standing credential policy.
10. **Capability truth is per-turn, not negotiated:** the recurring
    `system/init` frame (tools, models, capabilities list) is richer than a
    static descriptor; the adapter synthesizes its descriptor from it and
    refreshes per turn.

Per the standing rule, each mismatch must lead to an explicit OAP
decision/revision or a documented adapter boundary; none is silently
compensated.

## Initial capabilities

- initialize / capability revision: `emulated`
- session association (process + initialize + init frame): `emulated`
- submission/admission (write completion + `user_message_uuid`): `emulated`
- run identity/status/sequence: `emulated`
- one foreground turn per process at a time: enforced locally
- text streaming: `native` with `--include-partial-messages`, else
  `degraded` (block-level)
- tool lifecycle and progress: `degraded` until fixtures pass
- permission interactions: `degraded` pending `permission-gate` fixture
  (native reverse-control surface exists)
- cancellation: `degraded` (intent/settlement split, queue semantics)
- child/background tasks: `degraded`
- conversation resume/fork inputs: native CLI inputs; OAP `run.resume` is
  reclassified conversation-level and advertised `degraded` until pinned
- replay/reconciliation: `unavailable` (`reinitialize` is the only
  reconnect-shaped primitive)
- steer/BTW/side runs: `unavailable` initially (`shouldQuery`/`priority`
  exist on inbound messages but are unexercised)
- model catalog: `degraded` (init frame + `list_models` exist; unexercised)
- hooks, SDK MCP hosting, dialogs, elicitation: `unavailable` initially

## Evidence corpus plan

`fixtures/adapters/claude-code-2.1.263/`, each case with native JSONL
(sanitized, reduced), expected OAP, mapping, omissions, provenance pinning
the artifact hashes above:

1. `initialize-minimal` — spawn, initialize exchange, init frame, descriptor
2. `message-admitted` — user write, `user_message_uuid` echo, admission
3. `completed-text` — assistant blocks + result success -> run.completed
4. `streaming-deltas` — `stream_event` -> content.delta
5. `max-turns` — `error_max_turns` -> completed with limit reason
6. `api-error-result` — success subtype with `is_error` -> run.failed
7. `interrupt-cancel` — interrupt ack, `still_queued`, aborted terminal ->
   run.cancelled
8. `permission-gate` — `can_use_tool` round trip -> interaction lifecycle
9. `tool-roundtrip` / `tool-failed` / `tool-progress` — action lifecycle
10. `subagent-task` — task edges settle before parent terminal
11. `task-updated-terminal` — child settles only via `task_updated`
12. `stop-task` — child-targeted stop distinct from run cancel
13. `injected-turn-origin` — origin-attributed user frame creates no run
14. `second-turn` — sequential turns, distinct runs, same session
15. `process-exit` / `malformed-stdout-line` — transport failure paths
16. `resume-fork` — resume vs fork session-UUID behavior
17. `no-implied-replay` — persistence inputs do not constitute replay

A gated live-process test (explicit opt-in, sanitized env, hermetic or
explicitly authorized provider) may replicate the Makai integration gate
pattern; credential policy from `research/zai-china-coding-plan-evidence.md`
applies unchanged.
