# ACP v1.11.0 mapping ledger

Status: pin move over [`acp-v1.10.2-mapping.md`](acp-v1.10.2-mapping.md),
which with [`acp-v1.9.1-mapping.md`](acp-v1.9.1-mapping.md) and
[`acp-v1.7.0-mapping.md`](acp-v1.7.0-mapping.md) remains the mapping. This one
records only what moved between the two pins and the evidence for it.
Everything not named here is unchanged.

## Provenance and version boundary

- Repository: `https://github.com/agentclientprotocol/agent-client-protocol`
- Stable release: `v1.11.0`, latest stable on 2026-10-10 (published
  2026-10-09)
- Schema release: `schema-v1.25.0`, the same commit as the release, so its
  `schema/v1` tree is the release's
- Commit: `4cf3dd858c819fc3ab99ae53f76883ede0345a10`
- Commit date: 2026-10-09
- Stable specification tree SHA-256:
  `1b490aed9948c0e3cfc4a4e3ebd5d2d8fe1d3457794de539e30c77c93e4191a3`
- Stable generated-schema tree SHA-256:
  `4bd0b3e7d768f72a8d04181ddc3be48a7cdf8f1a692271b79b9899afbbc3b5ea`
- Hash recipes: unchanged from the v1.7.0 ledger, run with `shasum -a 256`
  over shallow clones of both tags; the same run reproduces the v1.10.2
  digests `da13cda3…6280` and `78daaae1…a0d9`, so the pairs are comparable.

ACP wire version stays `1`; `initialize` negotiation is unchanged.

## Wire difference, v1.10.2 to v1.11.0

`schema/v1/meta.json` is byte-identical. `schema/v1/schema.json` changed:

| Surface | Change | Effect on the adapters |
|---|---|---|
| `ClientSessionCapabilities` | new optional `compaction` and `notices`, each `null` or an object | none; the adapters do not advertise either |
| `SessionUpdate` | three new variants: `compaction_summary_chunk`, `compaction_update` and `notice`, with new definitions `CompactionCapabilities`, `CompactionId`, `CompactionStatus`, `CompactionSummaryChunk`, `CompactionUpdate`, `Notice`, `NoticeCapabilities` and `NoticeSeverity` | none; an agent sends them only to a client that advertised the matching capability (`Notice`: "Agents MUST only send notices when the Client advertised `ClientSessionCapabilities::notices`"; `compaction`: "Omitted or `null` means unsupported") |
| `AuthMethodTerminal`, `CreateTerminalRequest`, `LoadSessionRequest`, `NewSessionRequest`, `ReadTextFileRequest`, `ResumeSessionRequest` | the Rust deserializer hints `x-deserialize-default-on-error` and `x-deserialize-skip-invalid-items` are dropped from some members, so a Rust receiver decodes those members strictly | none; the hints only say how a Rust deserializer treats an invalid member. The adapters send `NewSessionRequest`, `LoadSessionRequest` and `ResumeSessionRequest` well formed, and decode what an agent sends them (`AuthMethodTerminal`, `CreateTerminalRequest`, `ReadTextFileRequest`) by their own rules, which did not change |

No member was removed and no requirement added on a definition the adapters
decode.

The adapter code does not change in either tree. The capability descriptor
is unchanged, so only the revision's version prefix moves:
`acp-v1.11.0-schema-v1.25.0-oap-v5`.

## Corpus

`fixtures/adapters/acp-v1.11.0` is the v1.10.2 corpus carried forward
(`corpus_from: v1.7.0`). No case was re-recorded: every `native.jsonl` is
byte-identical. Only the provenance (`acp_release`, each case's `release`,
`commit` and `schema_release`, and the two tree digests) and the
expectations' `capability_revision` moved.

## Process gate

Not re-run at this pin. The agent the gate drives, `docker/cagent`, stays
pinned at `v1.145.0` (commit `502549c3bb4b2647ffc81fdd5f9ebec29b4dd42d`), the
build the v1.10.2 ledger ran `OAP_ACP_SMOKE=1` and `OAP_ACP_INTEGRATION=1`
against 3x. Its latest release is now `v1.150.0`; moving the agent means
building it from source with its Go modules, which this move did not do. The
schema difference above sends nothing new to or from an agent that has not
been offered the new capabilities, so the gate's frames are the v1.10.2
ones.
