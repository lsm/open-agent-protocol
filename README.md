# Open Agent Protocol

Draft public-domain semantic protocol for agent software layers, agent loops,
model IO, tools, resources, and adapters for existing agent SDKs.

The first conformance target is an agent-control core covering the boundary
between a control layer and an agent loop: capabilities, session state, message
submission, run lifecycle, streamed content, cancellation, and state recovery
without binding to a specific transport.

OAP layers are logical software boundaries, not deployment sides. A
presentation layer, control layer, agent loop, model provider, tool executor,
resource provider, and binding can live in one process or across multiple
transports while preserving the same protocol semantics. Adapters for existing
SDKs or protocols are implementation shims, not a separate semantic layer.

Current drafts:

- [Conformance Draft](drafts/conformance.md)
- [Presentation Control Profile](drafts/presentation-control-profile.md)
- [Agent Control Core](drafts/agent-control-core.md)
- [Agent Control Profile](drafts/agent-control-profile.md)
- [Layered Agent Protocol](drafts/layered-agent-protocol.md)

Protocol artifacts:

- [Illustrative protocol envelopes](examples/README.md)
- `fixtures/`: normative executable conformance traces
- `schema/v0.1/`: JSON Schema bundle for the agent-control core

Executable core (Go 1.27 or later):

```sh
go run ./cmd/oap check
```

The command validates positive and negative fixtures and drives the deterministic
in-memory reference adapter. The reference adapter proves the public adapter
boundary and bounded process-memory recovery; it is not a production harness or
a durable persistence implementation.

Research:

- [Harness interoperability study](research/harness-interoperability.md)
- [P0 protocol gaps from harness interoperability](research/p0-protocol-gaps.md)
- [Pinned Codex app-server mapping](research/codex-app-server-8d7cc24-mapping.md)
- [Pinned ACP v1 and Devin Desktop mapping](research/acp-v1.7.0-mapping.md)
- [Pinned Makai agent-protocol mapping](research/makai-agent-67ad514-mapping.md)
- [Pinned Pi coding-agent mapping](research/pi-v0.85.1-mapping.md)
- [Pinned DeepSeek Harness mapping](research/deepseek-harness-47f9438-mapping.md)
- [Pinned Hermes agent mapping](research/hermes-v2026.8.31-mapping.md)
- [Pinned Claude Code CLI and Agent SDK mapping](research/claude-code-agent-sdk-2.1.263-mapping.md)
- [Z.ai China Coding Plan evidence matrix](research/zai-china-coding-plan-evidence.md)
- [Protocol feedback from eight adapter tranches](research/protocol-feedback-2026-09.md)

Decisions:

- [0001 — agent-control v0.1 executable core](decisions/0001-agent-control-v0.1-executable-core.md)
- [0002 — admission before started](decisions/0002-admission-before-start.md)

Provider compatibility is tested independently from harness conformance. Inspect
the credential-free China Coding Plan presets with:

```sh
go run ./cmd/oap providers zai-cn
```

Ordinary tests use fake credentials and loopback provider servers. Credentialed
provider evidence is separately and explicitly gated as documented in the matrix;
credential presence alone never enables network traffic.

Pi real-process checks are also explicitly opt-in and skipped by ordinary CI.
Provide an absolute Pi v0.85.1 executable in `OAP_PI_BIN`, then set
`OAP_PI_SMOKE=1` for the credential-free readiness check or
`OAP_PI_INTEGRATION=1` for the loopback-provider path. The executable's reported
semver is runtime-version evidence only; it does not prove the source commit.
Set `OAP_PI_SHA256` to the expected 64-character artifact digest when exact
artifact provenance is required. The gate never downloads an executable and
passes no ambient credentials to it.

DeepSeek Harness real-process checks follow the same opt-in gate. Provide an
absolute runtime built from the pinned source commit in
`OAP_DEEPSEEK_HARNESS_BIN` — the build emits
`deepseek-harness-sdk-runtime-linux-x64` from the pinned release — then set
`OAP_DEEPSEEK_HARNESS_SMOKE=1` for the credential-free initialize/shutdown
check or `OAP_DEEPSEEK_HARNESS_INTEGRATION=1` for the loopback-provider path.
The gates never download a runtime and pass no ambient credentials. The
runtime boots the shipped `sdk` profile (`--profile sdk`) against an isolated
`DSH_HOME`; the loopback gate redirects the stock deepseek provider with
`DEEPSEEK_BASE_URL`. The wire `serverInfo` version and any release text are
runtime-version evidence only; the pinned source commit and tree in the
mapping ledger remain the provenance. Set `OAP_DEEPSEEK_HARNESS_SHA256` to the
expected 64-character artifact digest when exact artifact provenance is
required.

Hermes agent real-process checks follow the same opt-in gate. Provide an
absolute python interpreter in `OAP_HERMES_BIN` (able to import the pinned
checkout's dependencies) and the pinned hermes-agent v2026.8.31 checkout in
`OAP_HERMES_ROOT` (the gateway's cwd), then set `OAP_HERMES_SMOKE=1` for the
credential-free ready/session.create/EOF-teardown check or
`OAP_HERMES_INTEGRATION=1` for the loopback-provider path (streaming OpenAI
chat completions against an in-process mock, test-owned key only). The gates
never download anything and pass no ambient credentials; teardown evidence is
stdin EOF, matching the pinned gateway, which has no shutdown RPC. Set
`OAP_HERMES_SHA256` to the expected 64-character interpreter digest when exact
artifact provenance is required.

Claude Code real-process checks follow the same opt-in gate. Provide an
absolute path to the pinned claude 2.1.263 binary in `OAP_CLAUDE_BIN`, then
set `OAP_CLAUDE_SMOKE=1` for the credential-free spawn/initialize/EOF-teardown
check or `OAP_CLAUDE_INTEGRATION=1` for the loopback-provider path (streaming
Anthropic Messages against an in-process mock, test-owned key only;
structural request assertions only, per the mapping pin). The gates never
download anything and pass no ambient credentials; readiness is the
initialize control exchange, and teardown evidence is stdin EOF. Set
`OAP_CLAUDE_SHA256` to the expected 64-character binary digest when exact
artifact provenance is required.

### Real-process gate coverage

CI runs `gofmt`, `go vet`, the full and race suites, and `oap check` on every
push and pull request. The real-process gates above are **not** run in CI:
they need pinned third-party binaries, are skip-by-default, and require an
explicit opt-in variable plus an absolute binary path (and optionally a
64-hex digest). Credential presence alone never enables them, and they never
download anything.

| Adapter | Gate variables | CI |
| --- | --- | --- |
| Codex app-server | `OAP_CODEX_INTEGRATION`, `_BIN`, `_COMMIT` | skipped |
| Makai | `OAP_MAKAI_INTEGRATION`, `_BIN`, `_COMMIT` | skipped |
| OpenCode | `OAP_OPENCODE_INTEGRATION`, `_BIN` | skipped |
| pi | `OAP_PI_SMOKE` / `OAP_PI_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| DeepSeek Harness | `OAP_DEEPSEEK_HARNESS_SMOKE` / `_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| Hermes | `OAP_HERMES_SMOKE` / `OAP_HERMES_INTEGRATION`, `_BIN`, `_ROOT`, `_SHA256` | skipped |
| Claude Code | `OAP_CLAUDE_SMOKE` / `OAP_CLAUDE_INTEGRATION`, `_BIN`, `_SHA256` | skipped |
| ACP / Devin | none — no process gate (see the ACP ledger) | n/a |

Each adapter additionally has a hermetic corpus that runs in CI: it decodes
sanitized native frames through the production codec and drives the
production reducer, so codec and reducer regressions are caught without any
external process.

The repository is dedicated under CC0-1.0 so any presentation layer, control
layer, agent loop, model provider, tool executor, resource provider, tool
source, or SDK adapter can implement the protocol without project-specific
licensing friction.
