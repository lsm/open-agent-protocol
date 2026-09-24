# Claude Code / Claude Agent SDK 2.1.280 mapping ledger

Status: the Claude Code adapter's pin. This ledger moves the adapter from
2.1.263 to 2.1.280 and records only what the move changed or settled.
[The 2.1.263 ledger](claude-code-agent-sdk-2.1.263-mapping.md) remains the
mapping of record for every surface not restated here. Since 2026-09-24 the
adapter's corpus is `fixtures/adapters/claude-code-2.1.280`, recorded against
the pinned darwin-arm64 binary (see *Corpus recorded at 2.1.280*).
`fixtures/adapters/claude-code-2.1.263` stays as the floor: its frames are
2.1.263's, and both corpora run in Go and in Zig. Every hash below was computed
on 2026-09-22 from the artifacts named, the darwin-arm64 pair again on
2026-09-24, and the live checks ran the 2.1.280 binary.

## Provenance

### Claude Code CLI 2.1.280

- npm artifact: `@anthropic-ai/claude-code@2.1.280`
  - tarball sha256: `1326e6b8cf00404fc3f9bd101d806b3fdec9588264e5a1aa8d98d1f7afe50170`
  - tarball sha1 (npm `dist.shasum`): `485647e4ae633102044d27837e1ff22fe65cf23e`
  - npm `dist.integrity`:
    `sha512-EZlX8jqNf+e7q9v+UoPbLYAbEGth7aDbcTytHzPYYohbP/fCfrjboCbcv85ZYGEq1Rq7Amm8hXLhuCKxLsabwA==`
  - the wrapper tarball is 27,538 bytes; `cli-wrapper.cjs` is byte-identical
    to 2.1.263's (sha256
    `61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e`)
- native linux-x64 artifact: `@anthropic-ai/claude-code-linux-x64@2.1.280`
  - tarball sha256: `3d95573100e302f79d536ef3eb64f5da0d537433af6ae8de571dfeda6d52bfd3`
  - tarball sha1: `fb69f5be9a205b5dbeb0046d3111820f1030e966`
  - npm `dist.integrity`:
    `sha512-dJHWFrDSIZ26hdLucnK3ehLmzdzYl3MsPC1RzUctAUaYnZlH5kfAZxnK8qYRAQ89GX3OPrLsnFdt/QnP+0Ck6Q==`
  - binary sha256: `1e08503dbdf3c2cb0d706d32f3408277388d1c76ef108673e8fe42c1b322925b`
  - binary size: 233,709,640 bytes
- native darwin-arm64 artifact, the binary the live checks below ran:
  `@anthropic-ai/claude-code-darwin-arm64@2.1.280`
  - tarball sha256: `76170ceef79015e118fdea65e3b11663342153d3559f301ab8e6a7dfecc7f4a3`
  - tarball sha1: `1ff0c6b70def07b1913169af1e0e24ab2258aa0b`
  - npm `dist.integrity`:
    `sha512-ctkNgja8Yi2kngVFPO2667k6zbtJwjQ+dOTeEp1XmzHcoDFdaee4h4WVgZllsewm/Io+pPPPSFQVdGHOtdE/1A==`
  - binary sha256: `387a5c5dcdbb815085edf0baf79591f9d8894efe922bceaf3d75b1b08055229d`
  - binary size: 217,254,576 bytes
  - `claude --version` self-reports `2.1.280 (Claude Code)`
- build manifest (carried by the Agent SDK artifact, `manifest.json`):
  - CLI build commit: `80abbfe7d7232280011ff01a21ae3338f4c6e372`
  - build date: `2026-09-21T20:55:27Z`
  - its linux-x64 and darwin-arm64 checksums match the hashes computed above,
    so both binaries are authenticated against the publisher manifest

Reproduce:

```sh
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code/-/claude-code-2.1.280.tgz' -o claude-code-2.1.280.tgz
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code-linux-x64/-/claude-code-linux-x64-2.1.280.tgz' -o claude-code-linux-x64-2.1.280.tgz
sha256sum claude-code-2.1.280.tgz claude-code-linux-x64-2.1.280.tgz
tar -xzf claude-code-linux-x64-2.1.280.tgz
sha256sum package/claude   # 1e08503dbdf3c2cb0d706d32f3408277388d1c76ef108673e8fe42c1b322925b
./package/claude --version # 2.1.280 (Claude Code)
```

### TypeScript Agent SDK 0.3.280

- public repository: `https://github.com/anthropics/claude-agent-sdk-typescript`
  - tag `v0.3.280`, commit `58d2e4b81bdca2c6ce10e6e5db22ad7acdc1d58c`, tree
    `a7a56fd729bc5be9752b8015080f6fbc09ae8a1a`; as at 0.3.263 the repository is
    provenance, not runtime evidence
- npm artifact: `@anthropic-ai/claude-agent-sdk@0.3.280`
  - tarball sha256: `5d3f5706261215c352d8b41606fb320f92a63cf252f020b47e7eed598bcb7ba8`
  - tarball sha1 (npm `dist.shasum`): `7d9bf3a89f0e18d08b0e6acdbf820cc1d19958dd`
  - npm `dist.integrity`:
    `sha512-aIQSTKcCJcOgi125GAGQaNZUYgxbEBQwV2Ac+Utp0+gGUjIEWjTqz4B8kXr9XjkcCTYiRUMTDsXPAUfIcBDjnw==`
  - `package.json` declares `claudeCodeVersion: "2.1.280"`
  - file hashes inside the artifact:
    - `sdk.mjs`: `ef4c2c0fc286d8c7dab7771516cf95206f9f670e99e74dc62f245b7fc8224955`
    - `sdk.d.ts`: `b7ac9c0ed0db5c1792a5394e72c75d69d85f4ce9edc0279487ec55d32eabfa76`
    - `bridge.mjs`: `b817ee4520eb37f62610fc6d819c5ec151246d768f681b4ca8481ea0caf5b17f`
    - `bridge.d.ts`: `7ec1e671ef30bcd5c39cb2cf636d30790e6e50d3ad04918028a64b648503796c`
    - `manifest.json`: `6d9840c779f76b2a7e974aa3476be24d1ea477f5dc96abd0096be28a58cb7120`
    - `manifest.zst.json`: `fc5e0cb17230f5e64eb904bacf0b155352728f66784df264aef171797edf8435`

### Python Agent SDK 0.2.158

- repository: `https://github.com/anthropics/claude-agent-sdk-python`
  - tag `v0.2.158`, commit `2c24c8248d0b52d44ff352854d7b679ac37b0db7`, tree
    `698715c4e378cd259f3513eb44c3932fa56d502c`
  - `pyproject.toml` blob: `916afbc6977b9d1572fd0d970d362c7f31da39d2`
  - `src/claude_agent_sdk/_cli_version.py` blob
    `e813827a892c6028a799380e8a79cf88684087e6` declares the bundled CLI
    `2.1.280`, which is why this release is the pinned one
- normative inspected source blobs:
  - `src/claude_agent_sdk/client.py`: `f3155011c17fb5ca5d44ff21a43ecd66dca51282`
  - `src/claude_agent_sdk/types.py`: `861c316936565edc413896d27e280028c4660125`
  - `src/claude_agent_sdk/_internal/query.py`: `63bac7d43eedab56e4d7adbb68e1e6d92d21eb6d`
  - `src/claude_agent_sdk/_internal/message_parser.py`: `931cc2a632f296aab43f3f98209020138431ce7d` (unchanged since 0.2.152)
  - `src/claude_agent_sdk/_internal/transport/subprocess_cli.py`: `7e53b8131c7e003543ebf005dc4dd5c28f9986d9`
  - `src/claude_agent_sdk/_internal/session_resume.py`: `a50e578fdaea7b10de83697fe355145b7351cecc` (unchanged)
  - `src/claude_agent_sdk/_internal/session_store.py`: `bb6a2155b08ad546227eba9f2349d95bffd910fa` (unchanged)
  - `src/claude_agent_sdk/_internal/session_store_validation.py`: `16addd216281eecaadaedbe7ed361ad8205d0433` (unchanged)

## Wire delta from 2.1.263

Every change `sdk.d.ts` makes between 0.3.263 and 0.3.280 to a frame this
adapter decodes is an optional member or a new subtype:

- `assistant`: `resume_reason`, `usage_report`; stream events: `resume_reason`;
- `result`: `result_index` and `resume_reason` on both outcomes, timing members
  and `local_command` on success, `startup_failure_reason` on error;
- `system`: `source`; `can_use_tool`: `mcp_server` provenance;
- user frames: `client_composed`, `inline_pastes`, `pasted_content`; origin:
  `fireReason`; the assistant error enum gains `verification_required`;
- new control subtypes: hooks listing, permission-rules listing, MCP resource
  read.

The native decoder ignores members it does not name, so none of these changes
a projection. The echo rules gained two clauses. `user_message_uuids` now rides
every frame that carries `user_message_uuid`, not only the first reply frame.
And a user message folded into a running turn takes the echo over on the next
reply frame of each kind. This adapter admits `auto` only on an idle session,
so it never has a message to fold.

The Python SDK's changes are the `verbatim_prompts` option, a custom
system-prompt form, and a `systemPromptSnapshot` initialize member. Its message
parser is byte-identical. `verbatim_prompts` marks each user frame
`client_composed`, which stops Claude Code expanding `@path` mentions and
dispatching slash commands in that text. This adapter now sets it by default;
see *Submitted text is delivered verbatim* below.

## Live verification at 2.1.280

Hermetic, as at the 2.1.263 freeze: a replaced environment carrying only a
temporary HOME, PATH, `ANTHROPIC_BASE_URL` at an in-process scripted Anthropic
Messages loopback, a test-owned key, and dead proxies. The spawn argv is this
adapter's, including the caller args a review host passes
(`--append-system-prompt`, `--no-session-persistence`).

1. **Echo placement.** `user_message_uuid` rides the turn's first
   `stream_event` (`message_start`), its first `assistant` frame, and the
   `result`. No later frame of the turn carries it, so the run starts at the
   first stream event, before any tool call can ask.
2. **`system/init.capabilities`**: `interrupt_receipt_v1`,
   `interrupt_cancel_queued_v1`, `msg_lifecycle_v1`, `mcp_read_resource_v1`,
   `mcp_tool_ui_meta_v1`.
3. **The tool flags.** The 2.1.263 ledger could not say whether
   `--allowedTools` names the tools that skip the prompt or the tools that
   exist, and listed five smoke assertions to decide it. All five ran here:
   1. an ask-gated command (`touch` in the workspace) raised `can_use_tool`
      before it settled, after the turn's echo;
   2. a safe command (`git status`) raised none;
   3. `--allowedTools Bash` let the same `touch` run without an ask;
   4. with `--tools Read,Grep,Glob`, `system/init.tools` and the tool list the
      provider received were exactly `Glob`, `Grep`, `Read`, and a `Bash` call
      came back as an error result without an ask; without `--tools`, init
      listed every built-in;
   5. `--allowedTools Read Grep Glob` alone left `Bash` attemptable, and the
      `touch` asked.

   So `--allowedTools` is a pre-approval list and bounds nothing; `--tools`
   is the surface. `--tools` takes comma- or space-separated names. It drops a
   permission-rule specifier such as `Bash(git *)`, and any name outside the
   built-in set, without an error. An MCP server's tool stayed listed and ran
   under `--tools Read`, so it does not narrow MCP tools. `Read`, `Grep` and
   `Glob` pre-approved by `--allowedTools` asked nothing, including for paths
   outside the working directory.

## What changed in the adapter

- `AllowTools(rules...)` passes `--tools` with the tools its rules name, then
  `--allowedTools` with the rules: the posture is now the surface as well as
  its pre-approval. Before, it was the pre-approval alone. Every unlisted
  built-in stayed available, and an ask-gated call opened a gate that a host
  which never answers gates, such as a review bot, waits on forever. A rule
  naming no tool is refused. `UnrestrictedTools()` still passes no tool flag.
  Caller args still follow the posture and can override it.
- Endpoint version `v2.1.280`; capability revision `claude-code-2.1.280-oap-v1`.
  At this move the 2.1.263 corpus was carried forward with expectations naming
  the new revision; the 2.1.280 corpus has since replaced it as the current
  one.
- The Zig port's spawn argv and capability revision match.
- `TestClaudeProcessReadOnlyReviewCompletesWithoutAGate` drives the pinned
  binary through a review host's configuration: `AllowTools("Read", "Grep",
  "Glob")`, an appended system prompt, and a provider that asks for a `Read`
  and then an ask-gated `Bash`. It asserts the `Read` completes with the file's
  content, the `Bash` is refused without a gate and does not run, the run
  completes with the final text and usage, and the provider saw the appended
  prompt and only the three tools. Against the previous argv the same test
  fails at once on the opened gate.

## Submitted text is delivered verbatim

Probed against the pinned binary with this adapter's argv, hermetic as above.
Without `client_composed`, Claude Code treats a submitted turn as text typed at
its own prompt:

- An `@` mention is read and inlined before the model call, as a synthetic
  `Read` result, with no permission ask. An absolute path outside the working
  directory was read too: `@/…/outside/secret.txt` put the file's contents in
  the first provider request. A relative mention resolved against the working
  directory. The review posture, `AllowTools("Read", "Grep", "Glob")`, does not
  prevent it.
- A leading slash command runs as a harness command instead of reaching the
  model. `/cost` and `/context` were answered locally in the `result`, the
  latter after its own analysis requests. `/compact` compacted the session and
  settled with `local_command: "compact"`, no model turn and no
  `terminal_reason`.
- The turn-start attachment pass runs. The first request carries environment,
  model identity, agent and skill listings, and a token budget, as
  `<system-reminder>` blocks.

With `"client_composed": true` on the frame, the mention stays text and no file
is read, and the slash commands reach the model as written. The turn-start pass
is skipped: the first request carries only the date and attribution reminders.
The environment and model-identity reminders then arrive appended to the first
tool result, as the Python SDK documents, so a turn that calls no tool is
answered without them. A `CLAUDE.md` in the working directory reached the
provider in neither mode, because `--setting-sources=` loads no project
settings.

The adapter now marks every submitted turn `client_composed` unless
`Config.ExpandPrompts` is set. An OAP submit is a message, and the trace
records its text. Expansion let the model see a file the trace never names,
and a slash command produced a harness result with no model turn at all. A
host submitting text it did not write, such as a review host submitting a diff,
could otherwise have any readable file pulled into context without an ask.
`ExpandPrompts` restores the CLI's own handling, for a host that submits only
its own text and wants the turn-start context on the first call. The Zig port's
`expand_prompts` matches, and the corpus's expected turns carry the member.

`TestClaudeProcessMentionReachesTheProviderOnlyWhenPromptsExpand` drives both
settings against the pinned binary under the review posture. The mentioned
file reaches the provider only when prompts expand, and the submitted text
reaches it as written either way.

## Issue #232

The report ran CLI 2.1.241, which is outside this pin. That build stamps
`user_message_uuid` on the `result` only, so the adapter could not start the
run before its first `can_use_tool`. That ask was ask-gated `Bash`, available
because `AllowTools` did not bound the surface. On 2.1.280 the echo precedes
any ask, and with the surface bounded no call under a read-only posture can
ask at all.

## Issue #238: resume replays the adapter journal

`run.resume` and `run.replay` move from `unavailable` to `degraded`, and the
journal's replay with them; the capability revision is now
`claude-code-2.1.280-oap-v2`. The 2.1.263 ledger's finding stands: the CLI has
no cursor, no redelivery contract and no gap semantics, and its resume, fork
and transcript persistence are conversation-level. What changes is that
`Session.Resume` no longer defers to the CLI. It replays the envelopes the
adapter itself emitted, from the bounded process-memory journal every session
already kept, as the ACP, Codex, OpenCode, Pi and memory adapters do.

Why it matters: `ErrEventStreamOverflow` tells a consumer to resume from its
last sequence, and `Resume` answered `unavailable` unconditionally. An
in-process consumer whose drain loop stalled for longer than the 64-slot stream
lost the run. That was hyperneo-review's reviewbot, stalling while it resolved
an interaction synchronously. The daemon's `?after=` reconnect drives the same
`Resume`, so it could not resume a claude session either.

- A cursor inside the journal replays the suffix, detached from the journal,
  then follows the live run. A run that has ended replays its retained tail and
  closes; the adapter keeps each ended run's last sequence so that a resume
  racing the terminal still finds it.
- A cursor older than the journal returns `*adapter.ReplayGap` with the retained
  bounds, and a cursor past the run returns `ErrReplayCursorFuture`. Neither is
  papered over.
- A native session UUID is not an OAP run: resuming one is `ErrRunNotFound`.
  The `hygiene-recovery` case now executes both labels this way.
  `no-implied-replay` replays a delivered run and requires the replay to equal
  what its stream delivered. `resume-fork` resumes by the native session id and
  requires the refusal.

Live at 2.1.280, through a loopback provider streaming N text deltas, with the
consumer stalled 3s before reading. At `e0301b9a` the run overflowed after
sequence 64 and `Resume` returned `operation unavailable`, #238's exact error.
With this change, N=300 and the default 256-entry journal replayed 65–302, and
the run completed with contiguous sequences and the text intact. N=1000 with
the same journal returned a `ReplayGap`: the stall outran what the journal
kept. With a 2048-entry journal it replayed 65–1002 and completed. So
`JournalCapacity` should cover the events of the longest stall a consumer can
have.

The Zig port has no per-subscriber stream or journal, so it has no `Resume`.
Its corpus harness skips `resume` ops, and only its capability revision
follows this change.

## Issue #122: `run.tool_selection` is enforced per call

`run.tool_selection` moves from unadvertised to `emulated`, scoped to the run,
and the capability revision is now `claude-code-2.1.280-oap-v3`. Decision 0031
made the projection conforming: a call the endpoint refuses because the
admitted `tool_choice` excludes it settles `action.call.failed` with
`refused_by_policy`, after the `requested` and `started` the harness really
produced.

The `can_use_tool` gate alone cannot carry the rule. The CLI asks only for
calls it would prompt for: a tool `--allowedTools` pre-approves, a read-only
tool such as `Read`, and a safe command such as `git status` or `pwd` run
without an ask (item 3 above). An excluded tool on any of those paths would
run. What covers every call is an SDK hook. Probed against the pinned binary
with this adapter's argv, hermetic as above:

1. `initialize` carrying
   `{"PreToolUse":[{"matcher":null,"hookCallbackIds":["oap_tool_selection"]}]}`
   makes the CLI send a `hook_callback` control request for every tool call,
   after the `assistant` frame naming the `tool_use` and before it runs. It
   carries `callback_id`, `tool_use_id`, and an `input` with
   `hook_event_name: "PreToolUse"`, `tool_name`, `tool_input` and
   `tool_use_id`.
2. It fired for an ungated `Bash` `pwd` and an ungated `Read` under
   `UnrestrictedTools()`, and for both again when `--tools Bash,Read
   --allowedTools Bash Read` pre-approved them.
3. Answering `{"hookSpecificOutput":{"hookEventName":"PreToolUse",
   "permissionDecision":"deny","permissionDecisionReason":…}}` stopped the
   call in every case, pre-approved ones included, with no `can_use_tool`. The
   `tool_result` came back `is_error: true` with the reason as content and
   `tool_result_meta` `non_execution_kind: "permission-rule"`.
4. Answering `{}` left the call to the CLI's own handling: `pwd` and `Read`
   ran and returned their output.

What the adapter now does:

- Open registers that hook in `initialize`. `hooks` was `null` before. A
  caller-supplied `Factory` does no `initialize`, so it registers nothing.
- Submit accepts `tool_choice` and admits it as Decisions 0022 and 0023
  judge it, with `protocol.ToolChoice.Unsatisfiable` and `Permits`.
- A `hook_callback` for a tool the run's policy excludes is answered with the
  deny above. One for a permitted tool, or outside a run with a policy, is
  answered `{}`. A callback id the adapter did not register is refused and
  treated as foreign activity.
- A `can_use_tool` for an excluded tool, which only arrives if no hook denied
  first, is answered `{"behavior":"deny"}` with the same reason. Neither path
  opens an OAP interaction.
- The call settles `refused_by_policy` only when the adapter itself denied it
  and the `tool_result` is an error. An excluded call the adapter never denied
  keeps its honest settlement, which the validator reports as
  `unapplied_control`.

The catalog a policy is judged against is the descriptor's, and this adapter
publishes none. The CLI's tools are known only per turn, from `system/init`, and
`action.tools.list` serves that session view. Under
[Decision 0034](../decisions/0034-an-unpublished-catalog-is-unknown.md) a
descriptor with no `tools` member leaves the catalog unknown, so the validator
and the adapter judge a policy by its names alone. `disallowed: ["Bash"]`
excludes exactly `Bash`, and `allowed: ["Read"]` is admitted and permits
exactly `Read`. Before 0034 the catalog was known and empty, so `allowed`
naming any tool was refused `unsatisfiable` and every admitted policy excluded
every tool.

`TestClaudeProcessExcludedToolIsRefusedByPolicy` drives the pinned binary with
`disallowed: ["Bash"]` and a provider that asks for `Bash` `touch`, under
`AllowTools("Bash")`, where the call is pre-approved and never asks, and under
`UnrestrictedTools()`. In both the trace validates with the submit it was
admitted under, the call settles `refused_by_policy`, no gate opens, the file
is not written, and the run completes.

The Zig port carries the new revision and does not implement the control or
register the hook, so its descriptor lacks `run.tool_selection`. A revision
names one descriptor, so this is a recorded divergence until the port follows.

## Corpus recorded at 2.1.280

Recorded 2026-09-24 against `@anthropic-ai/claude-code-darwin-arm64@2.1.280`:
tarball sha256 `76170ceef79015e118fdea65e3b11663342153d3559f301ab8e6a7dfecc7f4a3`,
binary sha256 `387a5c5dcdbb815085edf0baf79591f9d8894efe922bceaf3d75b1b08055229d`
(217,254,576 bytes), both checked against the provenance above before the
binary ran; it self-reports `2.1.280 (Claude Code)`. Every case's provenance
names these and the linux-x64 pin.

### Capture

`TestClaudeProcessRecordsCorpusProbes` (`capture_integration_test.go`) is the
capture path. It skips unless `OAP_CLAUDE_CAPTURE_DIR` names an absolute
directory outside the repository, and takes the binary from
`OAP_CLAUDE_BIN`/`OAP_CLAUDE_SHA256` like the other gates. Each probe spawns
the adapter's argv in `claudeEnvironment`, against the `providertest`
loopback, with one change: the three proxy variables point at a loopback sink
that records a connection and refuses it. One probe adds
`CLAUDE_CODE_CONTAINER_ID`. A process factory tees both pipes, so
`<probe>.jsonl` holds every line in each direction even after the adapter has
failed a run. The test fails if the sink saw a connection.

Seventeen probes: two text turns, a text turn under `UnrestrictedTools()`,
`Bash` calls (`ls`, `sleep 6; ls`, a loop printing a line a second, the same
loop with `CLAUDE_CODE_CONTAINER_ID` set, `false`), `Read`, an
allowed `touch` and a denied `rm -rf`, an interrupt, `--max-turns 1`, a 429
(ten retries, about three minutes), the credential-free login failure, a
background subagent finishing before the turn's result and one finishing
after it, and a background `Bash`. The corpus probes run under
`AllowTools("Task", "Bash(git status)")`, for which `system/init.tools` is
exactly `["Task","Bash"]`, the list the 2.1.263 corpus carried. That posture
still asks for `touch` and `rm`, and approves `ls` and `false` without asking.
`Read` ran under `UnrestrictedTools()`, which reports 27 built-ins.

On macOS every start of this binary reads the keychain. It runs `security
find-generic-password` for `Claude Code-<hash>` and
`Claude Code-credentials-<hash>`, the hash taken from `CLAUDE_CONFIG_DIR`, so
neither lookup can name the login stored for the default configuration
directory. Its runtime also calls `SecItemCopyMatching` once at start-up, with
a query the backtrace does not show. Both are reads. The recording ran with a
`security` stand-in first on `PATH` and under a sandbox denying the keychain
daemons and non-loopback connections, so neither reached the keychain. It
recorded no outbound attempt and no proxy connection.

### How a case was built

Each case keeps its 2.1.263 script: the same submits, controls and frame order,
except where the capture shows 2.1.280 doing something else (listed below). A
live-derived frame keeps the members its 2.1.263 counterpart carried, with the
values 2.1.280 reported. It also gains the wire-delta members the capture
shows: `result_index` on every `result`, and `ttft_ms`, `ttft_stream_ms`,
`time_to_request_ms`, `first_content_frame_ms` and `request_sent_wall_ms` on a
successful one. A `result` carrying `user_message_uuid` now carries
`user_message_uuids` too, and each turn's first `assistant` frame carries the
echo. Ids, paths and prompts take the 2.1.263 placeholders. Model text, tool
input and output, token counts, durations and costs keep the case's own values,
because the probe chose them, not the harness. Values the harness writes itself
are 2.1.280's. That covers `system/init`'s version, capabilities (all five),
`apiKeySource`, `slash_commands` and tools; `num_turns`; the interrupt
diagnostic; the turn-limit run's `stop_reason` and error; and the results of a
failed, a silent and a denied command and of a 429.

| Case | Live-derived | Constructed |
| --- | --- | --- |
| initialize-lifecycle | every frame | — |
| admission-corroboration | the human turn; the injected turn | — |
| settlement-statuses | the completed, turn-limit and 429 runs | the `error_during_execution` run |
| interrupt-cancel | every other frame | the text delta before the cancel |
| queued-continuation | — | every frame |
| background-children | the `local_agent` run | the `local_workflow` run |
| streaming-provenance | every frame's shape | the split and thinking deltas |
| tool-lifecycle | the `ls` run (with its task pair and `tool_progress`) and the `false` run | the `Read` run |
| permission-gates | every frame | — |
| process-exit | every frame (the exit is the harness's) | — |
| malformed-stdout | every frame but the truncated line | the truncated line |
| hygiene-recovery | the turn | `keep_alive`, `prompt_suggestion` |
| tools-catalog-sources | — | every frame (the MCP servers) |

Where 2.1.280's sequence differs from the 2.1.263 case:

- **The injected turn has no `user` frame.** Both background probes surface it
  as a fresh `system/init`, an echo-free stream and `assistant` frame, and a
  `result` whose `origin` is `task-notification`. The case now carries that
  shape instead of the constructed `user` frame.
- **A rejected request starts its run at the synthetic `assistant` frame.** No
  `message_start` precedes it, so the echo first arrives there. One of the ten
  `system/api_retry` notices is kept.
- **A foreground `Bash` call past about three seconds becomes a `local_bash`
  task**: `task_started` with `is_backgrounded: false`, and on completion a
  `task_notification` whose `output_file` is `""`. The `ls` run carries that
  pair.
- **Bash `tool_progress` needs `CLAUDE_CODE_REMOTE` or
  `CLAUDE_CODE_CONTAINER_ID`,** which the adapter's environment never sets;
  without one, the looping probe emitted none. With
  `CLAUDE_CODE_CONTAINER_ID` it emitted one, beside `task_started`, shaped
  differently from the 2.1.263 frame: `tool_use_id` is a synthetic
  `bash-progress-0`, `parent_tool_use_id` names the call, and `task_id` names
  the `local_bash` task. The case's `tool_progress` takes that shape. A frame
  of this kind therefore carries a `parent_tool_use_id` without coming from a
  subagent. The adapter ignores `tool_progress`, so nothing reads it.
- **The interrupt marker is a text block** (`[{"type":"text",...}]`), not a
  string.
- **The first `background-children` run is live.** The turn's `result` arrives
  while the `local_agent` child still runs, and the run settles at its
  `task_notification`. The shapes needed no change.

The constructed frames carry over with 2.1.280's shape changes only.
`prompt_suggestion` takes the shape the 2.1.280 emitter writes (`suggestion`,
`uuid`, `session_id`), and `keep_alive` already had it. The `Read` result drops
`is_error`, which 2.1.280 omits from a successful `Read`.

### Expected OAP against 2.1.263

Four envelopes differ, each a text 2.1.280 writes itself. Every other envelope
in the thirteen cases is identical, the reshaped frames above included.

| Case | Envelope | 2.1.263 | 2.1.280 |
| --- | --- | --- | --- |
| permission-gates | `action.call.completed` result, allowed `touch` | `done` | `(Bash completed with no output)` |
| permission-gates | `action.call.failed` message, denied call | `User denied the operation` | `Denied by the operator`: the tool result is the host's deny message |
| tool-lifecycle | `action.call.failed` message, `false` | `command failed` | `Exit code 1` |
| settlement-statuses | `run.failed` message, the 429 | `API Error: rate limited` | `API Error: Request rejected (429) · fixture rate limit` |

Three evidence rules in `corpus_test.go` quoted 2.1.263 text and now read the
case's own frames, keeping their strength: `api-error-result` compares the
failure with the 429 `result` text, `interrupt-cancel` accepts the marker as a
string or as text blocks, and `injected-turn-origin` takes the origin from a
`user` or a `result` frame and checks that none of that turn's text reaches an
envelope. `TestClaudeCurrentCorpusRecordsThePinnedVersion` holds the current
corpus's manifest, provenance and every `system/init` version to
`PinnedVersion`. The Zig harness runs both corpora, and asserts that the
current one is the version its capability revision names.

### Findings

- **The adapter failed any `Bash` call that ran longer than about three
  seconds.** Its `task_notification` carries `"output_file": ""`. The Go and
  Zig decoders both required a non-empty `output_file` and failed the run with
  `claude_process_exit`. The frozen strictness policy makes a known
  frame fatal when a required member is missing, and this one is present. It
  now has to be present (a string when not null) and may be empty, pinned by a
  unit test on each side and by `tool-lifecycle`.
- **`system/init` names the subagent tool `Task`; the provider is offered
  `Agent`, and calls arrive as `Agent`.** `--tools Task` still selects it. The
  catalog projected from `init` therefore lists `Task`, and an `Agent` call
  carries no `source`. Recorded, not compensated; no case exercises the call.
- The login-failure run matches the 2.1.263 finding, and its `system/init`
  also lists `ToolSearch`, which the loopback runs omit.

## Served by `oapx serve agent --backend claude`

The Zig port now serves a live child as well as replaying the corpus:
`zig/src/adapter/claude/adapter.zig` implements the adapter contract in
`zig/src/adapter/contract.zig`, and `zig/src/adapter/endpoint.zig` serves it
over the endpoint stdio binding. It spawns the same argv, writes the same
`initialize`, answers `can_use_tool` with the same `control_response` frames
(`updatedInput` echoes the ask's own input; a deny carries `Denied by the
operator`), and cancels with `interrupt`. An ask outside an owned run, and one
left open when its run settles, is refused back to the child with the Go
adapter's messages. Against a scripted child, `goap conformance` passes every
check but the two model-switch checks, and `goap validate` and `oapx validate`
accept the assembled trace.

What differs from the Go adapter, or cannot be done through this path:

| Area | oapx | Go adapter | Why |
| --- | --- | --- | --- |
| Capability revision | `claude-code-2.1.280-oapx-v1`: Go's descriptor with `run.resume` and `run.replay` `unavailable` | `claude-code-2.1.280-oap-v3`, both `degraded` | oapx keeps no journal, and a revision names one descriptor. The replay control answers `unsupported_control`. |
| `models.request`, `session.model.switch.request` | `unsupported_feature` | the same | The CLI's model control is unexercised in both trees, so both fail the conformance runner's model-switch checks. |
| Tool sources and provided tools at open | `unsupported_feature`, before any child starts | the same | Not advertised. |
| Admission | A turn the child has not echoed within 10 minutes is abandoned: the child is stopped and the session closes | waits on the caller's context, and a cancelled wait closes the session | The endpoint serves one request at a time, so a submit cannot wait unbounded. |
| `initialize` and `interrupt` | The child's receipt is awaited at most 60 s, then the request is answered `internal` | context-bound | Same reason. |
| Session memory | Once a settled session has grown 256 KiB past its last compaction, its arena is rebuilt from what later runs consult: the model, the MCP servers, the catalog and the native tool ids | garbage-collected | Bounded between runs, not within one: a run's frames stay until it settles. |
| Configuration | `--config` reads the `oap-serve.json` shape: unknown members refused, `environment` an explicit allowlist. Without it, the built-in entry passes `HOME` and `PATH` only and takes the harness-default tool posture | `goap serve` reads the same file | Member names are exact; Go's decoder matches them case-insensitively. |
| A frame read while memory is exhausted | The codec folds the allocation failure into a decode refusal, so the run fails as a transport failure | not applicable | `rpc.zig` and `gojson.zig` never propagate `OutOfMemory`. |
