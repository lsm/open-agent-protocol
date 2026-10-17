# GitHub Copilot CLI 1.0.98 mapping ledger

Status: the first pin, not yet catalogued. No adapter implements it yet; this
ledger is the spec the Zig and Go adapters will be built against, and it
chooses the native surface they drive. `harnesses/copilot-cli.json` lands with
the first adapter, because a current version needs a capability revision and a
corpus.

## Which surface

Copilot CLI offers three ways to drive it. The adapter drives the first.

| Surface | What it is | Why or why not |
| --- | --- | --- |
| `copilot --headless --stdio` | A JSON-RPC 2.0 server, one long-lived process, the surface GitHub's own `@github/copilot-sdk` spawns | Sessions, streamed deltas, permission and user-input requests answered by the client, abort, resume, history read, model switch, per-call usage |
| `copilot --acp` | An Agent Client Protocol server | A generic lowest common denominator; loses usage, the queue, the history read and the permission detail below, and the owner prefers a native surface |
| `copilot -p … --output-format json` | One prompt per process, JSONL out | Needs `--allow-all-tools` to run unattended, so no gated tool can reach the client; no multi-turn session |

## Provenance

- Repository: `https://github.com/github/copilot-cli` (releases and docs; the
  CLI's source is not published there)
- Release: `v1.0.98`, the latest stable on 2026-10-17, published
  2026-10-14T19:52:33Z
- Tag commit: `76c3172eb38f5e7537b90b9800a7023d81156438`, tree
  `dfc13180abc66a21eedd4699dfa7e4ba25198f13`
- Release artifacts, checked against the release's `SHA256SUMS.txt` on
  download:

| Platform | Kind | Name | SHA-256 | Bytes |
| --- | --- | --- | --- | --- |
| darwin-arm64 | archive | `copilot-darwin-arm64.tar.gz` | `a87018a0194789313c979906e45e8874699fee50549ea11d0be7bdde88b6b3c8` | 105462854 |
| darwin-arm64 | binary | `copilot` | `b1a76d9eec3c88e8545ee96834189ab08c3d75ef65500a60925187fa6c33fdf1` | 167681584 |
| darwin-x64 | archive | `copilot-darwin-x64.tar.gz` | `dfa26894b0c178239daaac527d5aac0a6cfce4bc1ede6fa33d54ccf77a5bb3b5` | 118588094 |
| linux-arm64 | archive | `copilot-linux-arm64.tar.gz` | `a7509dcac0e5f22886788ba4c1e2f8b9b07367e84714e42b9e7cee4d8f9fa5f2` | 118208926 |
| linux-x64 | archive | `copilot-linux-x64.tar.gz` | `87ddb4b3f98462b7cb13c0a6857c68ee410ec9673e2e5f64ee5bedad45825b05` | 116812784 |
| darwin-arm64 | npm-tarball | `github-copilot-1.0.98-darwin-arm64.tgz` | `26c6165cb45bce1b37a3a8d6bbcd484e2d938cf513d5970cbdf9ef0da8b92a70` | 70377164 |

Only the darwin-arm64 archive was unpacked and run; the other archives are
pinned from `SHA256SUMS.txt`. The binary reports `1.0.98` from `status.get`.

The npm tarball carries the wire's own JSON Schemas, which this ledger reads:

| Path in the tarball | SHA-256 | Bytes |
| --- | --- | --- |
| `package/schemas/api.schema.json` | `7f4758fdd7e68e726316217bfbad41904524158176334f2945a22c97b76431f6` | 2560957 |
| `package/schemas/session-events.schema.json` | `81c4a1eb9f0f3bd21a68219f30aaa774fd7c041eafa76c62ff691ac53efae1ed` | 990556 |

`api.schema.json` names 597 RPC methods; the adapter uses about a dozen. Its
`package/copilot-sdk/` is the SDK that drives this surface, and it is the
reference for the parameters below. A Copilot CLI installed through npm
updates its native binary in place, so the package version a machine reports
and the binary it runs can differ (one machine had the 1.0.5 package running
a 1.0.97 binary). The adapter checks the version `status.get` reports, not the
package, and runs the binary with `--no-auto-update`.

## Transport

`copilot --headless --stdio --no-auto-update` reads and writes JSON-RPC 2.0
framed with `Content-Length` headers, as LSP does, not one object per line. No
other pinned harness frames this way.

- `ping` answers `{message:"pong", timestamp, protocolVersion:3}`;
  `status.get` answers `{version:"1.0.98", protocolVersion:3}`. The catalog's
  `endpoint_version` is that protocol version, `3`.
- With only `HOME` and `PATH` in its environment the server authenticates
  through `gh`: `auth.getStatus` answers `{isAuthenticated:true,
  authType:"gh-cli", …}`. No token is passed on the command line; the SDK's
  `--auth-token-env <NAME>` names an environment variable instead, which fits
  the registry's `environment` allowlist.
- The server sends `session.event` notifications `{sessionId, event}` and
  `session.lifecycle` notifications. With the session options below it sent
  the recorder no server-to-client requests.

## Live probe

2026-10-17, the darwin-arm64 binary above, `HOME` and `PATH` only, a fresh
working directory per scenario, one server process per scenario (two for
resume). Nine scenarios, fourteen billed model calls in all.

| Scenario | Drives | Outcome |
| --- | --- | --- |
| completed-text | one prompt | `session.idle` |
| tool-approved | a file write, permission `approve-once` | file written, `session.idle` |
| tool-rejected | the same, permission `reject` with feedback | no file, the tool fails `denied`, `session.idle` |
| shell-approved | a shell command, permission `approve-once` | output streamed, `session.idle` |
| cancel-active | `session.abort` after the first delta | `assistant.idle {aborted:true}`, `session.idle {aborted:true}` |
| ask-user | the model's ask-user tool, answered | `user_input.requested`, `user_input.completed`, `session.idle` |
| resume | a prompt, the process exits, a new process resumes the id | the second process recalls the first turn |
| model-switch | `session.model.switchTo` before a prompt | `{status:"applied"}`, but see Models |
| queue | two `session.send` back to back | two runs, one `session.idle` |

## Session lifecycle

| OAP | Native |
| --- | --- |
| `session.open` | `session.create {clientName, model?, requestPermission:true, requestUserInput:true, workingDirectory, streaming:true, envValueMode:"direct"}` → `{sessionId, workspacePath, …}` |
| `session.open.reopen` | `session.resume {sessionId, …same options}` in any later process; state persists under `~/.copilot/session-state/<id>` |
| history read | `session.getMessages {sessionId}` → `{events:[…]}`, the persisted event log from `session.start` on |
| model switch | `session.model.switchTo {sessionId, modelId}` → `{status, modelId, …}` and a `session.model_change` event |
| close | process exit; `session.shutdown` / `session.destroy` exist and were not needed |

`requestPermission:true` is what makes a gated tool wait for the client: the
SDK sets it whenever an `onPermissionRequest` handler exists, and its handler
is required. Without it the server decides alone.

## Run boundaries

The pattern is OpenCode v2's: an idle is a **busy period**, not a run.
`session.send {sessionId, prompt, mode?}` answers `{messageId}` at once and
the default `mode` is `enqueue`. Each admitted prompt then appears as
`user.message {messageId, interactionId, delivery}`, `delivery` being `idle`
when it started a busy period and `queued` when it waited behind another.
Every assistant and tool event of that prompt carries its `interactionId`.
In the queue probe two prompts produced two `user.message`s, two
`assistant.turn_start`/`turn_end` pairs, and one `session.idle`.

| Native | OAP |
| --- | --- |
| `session.send` → `{messageId}` | admission: `start` when idle, `queue` when a run is open |
| `user.message` whose `messageId` is ours | `run.started`; its `interactionId` binds the run's events |
| a later `user.message` | settles the open run `completed` |
| `session.idle` | settles the open run `completed` |
| `session.idle {aborted:true}` after an accepted cancel | `run.cancelled` |
| `session.error` | settles the open run `failed` |
| `assistant.turn_start` / `turn_end` | turn bookkeeping inside a run; `turnId` restarts at `"0"` per interaction |

`session.send {mode:"immediate"}` interjects into the running turn, which is
Decision 0002's steer; the probe did not exercise it.

## Content

| Native | OAP |
| --- | --- |
| `assistant.message_delta {messageId, deltaContent}` (ephemeral) | `content.delta` |
| `assistant.message {messageId, content, phase, toolRequests}` | authoritative text for the final response; `phase:"final_answer"` marks the last |
| `assistant.reasoning_delta` / `assistant.reasoning {reasoningId, content}` | reasoning, same shape |
| `assistant.streaming_delta {totalResponseSizeBytes}` | observed-only: a byte counter, no text |
| `assistant.usage {inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, reasoningTokens, cost, model, isAuto, …}` | one per model call; the run's usage is their sum. `cost` is premium requests and goes to a namespaced extension |

## Tools and permissions

| Native | OAP |
| --- | --- |
| `tool.execution_start {toolCallId, toolName, arguments, toolTitle}` | `action.call.started` |
| `permission.requested {requestId, permissionRequest{kind, toolCallId, intention, …}}` | `action.permission.requested`; `kind` is `write`, `shell`, `read`, `mcp`, `url`, … with kind-specific detail (`fileName` and `diff` for write, `fullCommandText` and `commands` for shell) |
| `session.permissions.handlePendingPermissionRequest {requestId, result:{kind:"approve-once"}}` | an allow answer |
| the same with `{kind:"reject", feedback}` | a deny answer; the tool then fails with `error.code:"denied"` and the feedback in its message |
| `permission.completed {requestId, toolCallId, result}` | `action.permission.resolved` |
| `tool.execution_partial_result`, `tool.shell_output` | `action.call.progress` |
| `tool.execution_complete {toolCallId, success, result \| error}` | `action.call.completed` or `action.call.failed` |

Native emits `tool.execution_start` **before** `permission.requested`: the
permission is asked from inside the execution. OAP reads a call as started
once it runs, so the adapter holds `action.call.started` until the permission
resolves allow, and a call refused at the gate never starts.

`approve-for-session`, `approve-for-location` and
`approve-read-only-for-session` persist an approval; OAP approves one call,
so the adapter sends only `approve-once` and `reject`.

## User input

`user_input.requested {requestId, question, choices, allowFreeform,
toolCallId}` is OAP's `user.input.requested` with a single question, single
choice from `choices`, freeform allowed when `allowFreeform`. The answer is
`session.ui.handlePendingUserInput {sessionId, requestId, response:{answer,
wasFreeform}}`, after which `user_input.completed` arrives.

## Cancel

`session.abort {sessionId}` ends the busy period: the probe saw
`assistant.idle {aborted:true}` then `session.idle {aborted:true}`, with no
final `assistant.message`. Whether an abort also drops prompts still queued
behind the running one was not probed; until it is, the adapter cancels a
queued run by withdrawing it (`session.queue.withdrawMessage`) and treats an
abort as ending only the running one.

## Models

`session.create {model}` and `session.model.switchTo` are honoured on the
wire (`session.start.selectedModel`, `session.model_change.newModel`), but the
account probed routes every call through Copilot's automatic model selection:
`session.auto_mode_resolved` listed one available model, and every
`assistant.usage` reported `isAuto:true` and a model other than the one
selected. Which model actually answers is therefore an account policy, and
the adapter reports the model from `assistant.usage`, not the one it asked
for. `models.list` and `session.model.list` returned empty lists on this
account; `session.model.list` did return `modelPriceCategories`.

## Not mapped

Plan and autopilot modes (`session.mode.*`, `exit_plan_mode.*`), fleets,
sub-agents, background tasks, workflows, canvases, schedules, remote control,
and the MCP and plugin management methods have no OAP counterpart. Their
events are observed-only and their requests are never sent. Telemetry-like
events (`model.*`, `session.usage_info`, `session.usage_checkpoint`,
`session.title_changed`, `session.mcp_*`, `session.tools_updated`,
`pending_messages.modified`, `system.message`, `sandbox.decision`) are
observed-only.
