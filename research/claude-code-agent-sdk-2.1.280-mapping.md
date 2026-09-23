# Claude Code / Claude Agent SDK 2.1.280 mapping ledger

Status: the Claude Code adapter's pin. This ledger moves the adapter from
2.1.263 to 2.1.280 and records only what the move changed or settled.
[The 2.1.263 ledger](claude-code-agent-sdk-2.1.263-mapping.md) remains the
mapping of record for every surface not restated here. Its corpus,
`fixtures/adapters/claude-code-2.1.263`, was recorded against 2.1.263 and is
carried forward: the native frames are unchanged, and the expectations name the
new capability revision. Every hash below was computed on 2026-09-22 from the
artifacts named, and the live checks ran the 2.1.280 binary.

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
dispatching slash commands in that text. This adapter does not set it, so text
a host submits is still expanded, which matters to a host that submits
third-party text such as a diff. Recorded as a surface; not adopted here.

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
  The corpus expectations name the new revision; their native frames are
  unchanged.
- The Zig port's spawn argv and capability revision match.
- `TestClaudeProcessReadOnlyReviewCompletesWithoutAGate` drives the pinned
  binary through a review host's configuration: `AllowTools("Read", "Grep",
  "Glob")`, an appended system prompt, and a provider that asks for a `Read`
  and then an ask-gated `Bash`. It asserts the `Read` completes with the file's
  content, the `Bash` is refused without a gate and does not run, the run
  completes with the final text and usage, and the provider saw the appended
  prompt and only the three tools. Against the previous argv the same test
  fails at once on the opened gate.

## Issue #232

The report ran CLI 2.1.241, which is outside this pin. That build stamps
`user_message_uuid` on the `result` only, so the adapter could not start the
run before its first `can_use_tool`. That ask was ask-gated `Bash`, available
because `AllowTools` did not bound the surface. On 2.1.280 the echo precedes
any ask, and with the surface bounded no call under a read-only posture can
ask at all.
