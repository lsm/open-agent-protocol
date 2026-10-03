# ACP v1.10.2 mapping ledger

Status: pin move over [`acp-v1.9.1-mapping.md`](acp-v1.9.1-mapping.md), which
with [`acp-v1.7.0-mapping.md`](acp-v1.7.0-mapping.md) remains the mapping.
This one records only what moved between the two pins and the evidence for
it. Everything not named here is unchanged.

## Provenance and version boundary

- Repository: `https://github.com/agentclientprotocol/agent-client-protocol`
- Stable release: `v1.10.2`, latest stable on 2026-10-03 (published
  2026-10-01)
- Schema release: `schema-v1.24.1` (tag commit
  `1761180eeddf0828d4ecc367106a632c61be06d9`, 2026-09-30); its `schema/v1`
  tree is identical to the release's
- Commit: `9e032156545412be9bba5e092f12d0080c499b6d`
- Commit date: 2026-10-01
- Stable specification tree SHA-256:
  `da13cda3a2bf7509e8f5940ef84b9b7b0f89246716d62373c81d994f48d06280`
- Stable generated-schema tree SHA-256:
  `78daaae13575a1d97e60fb1dcececadaf49b98eee120c6344def4f002a74a0d9`
- Hash recipes: unchanged from the v1.7.0 ledger, run with `shasum -a 256`;
  the same run reproduces the v1.9.1 digests `676ed241…3de7` and
  `d9a3a86a…2ce9`, so the pairs are comparable.

ACP wire version stays `1`; `initialize` negotiation is unchanged.

## Wire difference, v1.9.1 to v1.10.2: none in the stable boundary

`schema/v1/schema.json` and `schema/v1/meta.json` are byte-identical at both
pins, so no stable method, notification, `sessionUpdate` discriminator, stop
reason, tool status or member moved. What did change:

| Surface | Change | Effect on the adapters |
|---|---|---|
| `schema.unstable.json`, `meta.unstable.json` | `schema-v1.24.0`: subagents (#1992) and request-scoped MCP-over-ACP (#2223); `schema-v1.24.1`: MCP response extensions (#2265) | none; outside the stable boundary |
| `agent-client-protocol-schema/src/v1/*.rs` | the unstable surfaces above; in `content.rs`, `plan.rs` and `tool_call.rs` only the Rust skip-listener plumbing of `VecSkipError` | none; the generated schema is unchanged |
| `docs/protocol/v1/prompt-turn.mdx` | a table of the eleven `sessionUpdate` variants | none; documents the existing schema |
| `docs/protocol/v1/overview.mdx`, `docs/protocol/v1/draft/` | a link to the test kit; draft prose | none |

The adapter code does not change in either tree. The capability descriptor is
unchanged, so only the revision's version prefix moves:
`acp-v1.10.2-schema-v1.24.1-oap-v4`.

## Corpus

`fixtures/adapters/acp-v1.10.2` is the v1.9.1 corpus carried forward
(`corpus_from: v1.7.0`). No case was re-recorded: every `native.jsonl` is
byte-identical. Only the provenance (`acp_release`, each case's `release`,
`commit` and `schema_release`, and the two tree digests) and the
expectations' `capability_revision` moved.

## Process gate

- `docker/cagent` tag `v1.145.0`, commit
  `502549c3bb4b2647ffc81fdd5f9ebec29b4dd42d`, tree
  `bcc43ad221987eef166744416a88653b47bdbc9c` (2026-09-28), the latest
  release. Still on `coder/acp-go-sdk v0.13.5`.
- Built locally with `go build .` and Go 1.27.0 on darwin/arm64:
  `151766f90499cf517dc6b5264ee0b59a6437ef507378e770a598b0d24ebf7109`.
- `OAP_ACP_SMOKE=1` and `OAP_ACP_INTEGRATION=1` with that binary and
  `OAP_ACP_SHA256` bound both pass 3x (`TestACPProcessSmoke`,
  `TestACPProcessAgainstChatCompletionsMock`).

### First live evidence of the `name` mapping

The v1.9.1 ledger recorded that the pinned agent could not emit the stable
`name` its SDK lacks. Between v1.143.0 and v1.145.0 cagent added
`pkg/acp/tool_name.go`: it carries the tool's programmatic name in `_meta`
under `docker-agent/internal-acp-tool-name` and, at the output writer,
promotes it to the stable `name` member of `tool_call`, `tool_call_update` and
the `session/request_permission` tool call.

A one-off probe, not committed, ran the integration test's setup with the
agent given the `think` toolset and the loopback mock answering one tool call
then text. Through the production Go adapter, `action.call.requested`,
`.started` and `.completed` carried `name: "think"` against v1.145.0 and
`name: "Think"`, the ACP `title`, against a v1.143.0 build from the same
tree. That is the v1.9.1 rule, prefer `name` and fall back to `title`,
observed against a live agent for the first time. No corpus case carries it:
the probe's frames were not recorded, so the coverage of record is still the
unit tests.

The Zig adapter was not driven against the live agent; its evidence is the
corpus replay and reducer tests.
