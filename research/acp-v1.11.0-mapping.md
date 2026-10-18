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

One case was added later, `usage-fold`, for the usage mapping below. It is
written from the schema, not recorded from an agent: two identical
`usage_update`s around a message chunk, then a prompt result carrying the
unstable `usage` with the counts from the #1090 sample.

## Process gate

Not re-run at this pin. The agent the gate drives, `docker/cagent`, stays
pinned at `v1.145.0` (commit `502549c3bb4b2647ffc81fdd5f9ebec29b4dd42d`), the
build the v1.10.2 ledger ran `OAP_ACP_SMOKE=1` and `OAP_ACP_INTEGRATION=1`
against 3x. Its latest release is now `v1.150.0`; moving the agent means
building it from source with its Go modules, which this move did not do. The
schema difference above sends nothing new to or from an agent that has not
been offered the new capabilities, so the gate's frames are the v1.10.2
ones.

## Usage (lsm/open-agent-protocol#1090)

Until this change both adapters dropped `usage_update` with the other observed-only
updates, and decoded only `stopReason` from the prompt result, so a run that the
agent measured settled with no usage at all. The v1.7.0 ledger already named the
row (`usage_update` → "retained usage, normally folded into terminal/state",
fixture `usage-fold`); the fixture did not exist until now.

What the pinned schema says, at commit `4cf3dd85`:

| Member | Schema | Meaning there |
|---|---|---|
| `UsageUpdate` (`sessionUpdate: "usage_update"`) | stable; `used` and `size` required `uint64`, `cost` optional and default-on-error | "Tokens currently in context", "Total context window size in tokens", "Cumulative session cost" |
| `PromptResponse.usage` | unstable only (`schema.unstable.json`); `Usage` with `totalTokens`, `inputTokens`, `outputTokens` required, `thoughtTokens`, `cachedReadTokens`, `cachedWriteTokens` optional and default-on-error | the member says "Token usage for this turn"; its fields say "Sum of all token types across session" and "Total input tokens across all turns" |

The two descriptions disagree, and upstream says so: the End-Turn Token Usage RFD
lists "Per-turn vs cumulative" as open question 1 and stays Draft "while token
accounting semantics are still being refined". `usage_update` carries no token
counts at all. The pinned agent, `docker/cagent` `v1.145.0`, sends only
`usage_update` (`pkg/acp/commands.go` `emitUsage`: `Size`, `Used`, and `Cost`
when positive), never a prompt-result `usage`.

The mapping, identical in Go (`go/adapter/acp/usage.go`) and Zig
(`zig/src/adapter/acp/session.zig`):

| Native | OAP | Fidelity |
|---|---|---|
| prompt-result `usage`, on the **first turn of a session opened by `session/new`** | `usage` on the run's terminal: `input_tokens`, `output_tokens`, `total_tokens` | normalized: a turn's count and a session's count are the same number on that turn, so either reading of the RFD gives the same figure |
| prompt-result `usage` on any later turn, or on any turn of a reopened session | no core `usage`; the counts stay in the extension below | lossy, recorded: projecting them would pick one reading of an open question, and a reloaded session may already carry earlier turns |
| prompt-result `usage`, every turn | `extensions["com.agentclientprotocol.turn_usage"]`: `total_tokens`, `input_tokens`, `output_tokens`, and `thought_tokens`, `cached_read_tokens`, `cached_write_tokens` when reported | native values, renamed only |
| the latest valid `usage_update` the session received, before or during the run | `extensions["com.agentclientprotocol.context"]`: `used_tokens`, `size_tokens`, and `cost` (`amount`, `currency`) exactly as reported | native values; folded, not summed |

Rules both trees hold, pinned by tests and the `usage-fold` case:

- Absent usage is not zero usage. A prompt result without `usage`, or with a
  `usage` missing a required count or carrying a negative or fractional one,
  settles with no core `usage` and no `turn_usage`. Reported zeros are kept as
  measured zeros.
- A bad optional count (`thoughtTokens: -4`) drops only that count, per the
  member's default-on-error.
- A `usage_update` missing `used` or `size`, or carrying a bad one, is ignored and
  the previous snapshot stands; it does not fail the run. A bad `cost` drops only
  the cost.
- Repeated `usage_update`s replace the snapshot rather than add to it. `_meta` is
  never read ("Implementations MUST NOT make assumptions about values at these
  keys"), so a subagent's copy of an update cannot be told apart from the root's
  and the latest wins. The sample in #1090 sent identical copies.
- A prompt that fails with a JSON-RPC error carries the context snapshot but no
  turn counts. No cost is derived from token counts.
- Member names are matched as Go's `encoding/json` matches them: case-folded, last
  duplicate wins.

## Models list

Both adapters now answer `models.request` from the agent's own model
configuration. That changes the capability descriptor, so the revision moves
to `acp-v1.11.0-schema-v1.25.0-oap-v6`. No ACP wire changed.

| ACP | OAP |
|---|---|
| the `configOptions` entry whose `category` is `model`, from `session/new` (or from the reopen result) | the session's models list |
| each of its values, nested groups flattened | a `ModelDescriptor`: `value` is `id`, `name` is `display_name` when present |
| its `currentValue`, unless empty, `default` or `current` | `current_model_id`, in `session.state` as well as in the list, and `default: true` on that entry |

It is advertised **degraded** and needs `allow_degraded_features:
["models.list"]`. The v1.7.0 ledger records why: ACP categories are UI hints,
and a `model` option "does not prove OAP `models.list`, provider metadata,
context limits, pricing, or per-run selection". The list carries an id and a
name only, with no `provider_id` or `context_window`. An agent that reports no
`model` option refuses with `unsupported_feature` (`unsatisfiable`), as one
with no `thought_level` option refuses a reasoning level.

Live evidence, from lsm/hyperneo-review: Devin's `session/new` (via `devin
acp`, no prompt) reports a `model` option with about 130 values, alongside
`mode`, `thought_level` and `model_config`. The corpus's `session-reopen` case
carries a `model` option, but no corpus case sends `models.request`, so no
expectation changed beyond the revision. The behaviour is pinned by unit tests
in both trees.
