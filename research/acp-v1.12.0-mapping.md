# ACP v1.12.0 mapping ledger

Status: pin move over [`acp-v1.11.0-mapping.md`](acp-v1.11.0-mapping.md),
which with the v1.10.2, v1.9.1 and v1.7.0 ledgers remains the mapping. This one
records only what moved between the two pins and the evidence for it.
Everything not named here is unchanged, including the model list, model
switch, usage and cancellation sections of the v1.11.0 ledger.

## Provenance and version boundary

- Repository: `https://github.com/agentclientprotocol/agent-client-protocol`
- Stable release: `v1.12.0`, latest stable on 2026-10-17 (published
  2026-10-16)
- Schema release: `schema-v1.26.0`, the same commit as the release
- Commit: `e2d83349bc9e28a4059ea3acefa1c91c966f74e2`, committed
  2026-10-16T10:02:22Z
- Stable specification tree SHA-256:
  `b825ee09ad97c363529a8cd5e6ca4920520afc4131b499588f77ec68d79b4968`
- Stable generated-schema tree SHA-256:
  `aa699550eae9f084ab1741ebe7ac9880c65659ef54b856b97c1cb74a89b6701e`
- Hash recipes: unchanged from the v1.7.0 ledger, run with `shasum -a 256`
  over shallow clones of both tags; the same run reproduces the v1.11.0
  digests `1b490aed…91a3` and `4bd0b3e7…b5ea`, so the pairs are comparable.

ACP wire version stays `1`; `initialize` negotiation is unchanged.

## Wire difference, v1.11.0 to v1.12.0

`schema/v1/schema.json` and `schema/v1/meta.json` are byte-identical. What
changed:

| Surface | Change | Effect on the adapters |
|---|---|---|
| `schema.unstable.json`, `meta.unstable.json` | an unstable client system prompt schema; next edit suggestions (`nes`) removed; a forked session clarified as active | none; the adapters speak the stable schema only |
| `agent-client-protocol-schema/src/v1/*.rs` | the `unstable_nes` module and its imports removed; `Eq` derived beside `PartialEq` on many types | none; neither changes a serialized shape |
| `docs/protocol/v1/cancellation.mdx` | restates that the client answers pending `session/request_permission` requests `cancelled` when it sends `session/cancel`, without waiting for `$/cancel_request`, and makes the agent's own `$/cancel_request` cascade optional | none at this pin: the rule was already in `prompt-turn.mdx`, and both adapters follow it since lsm/open-agent-protocol#1168, recorded in the v1.11.0 ledger's cancellation section |
| draft docs under `docs/protocol/v1/draft/` | prose only | none |

No stable definition was added, removed or changed. The adapter code does not
change in either tree, and the capability descriptor is unchanged, so only the
revision's version prefix moves: `acp-v1.12.0-schema-v1.26.0-oap-v7`.

## Corpus

`fixtures/adapters/acp-v1.12.0` is the v1.11.0 corpus carried forward
(`corpus_from: v1.7.0`). No case was re-recorded: every `native.jsonl` is
byte-identical. Only the provenance (`acp_release`, each case's `release`,
`commit` and `schema_release`, and the two tree digests) and the expectations'
`capability_revision` moved.

## Process gate

Re-run at this pin against `docker/cagent` moved to its latest release:

- tag `v1.150.0`, commit `5277e7842058fc3fbb9943a6e88bc05a1c8e6fe2`, tree
  `723bca4c57ad119ba8187b082f2e17e067e93905` (2026-10-09). Still on
  `coder/acp-go-sdk v0.13.5`.
- Built from that tag with `go build .` and Go 1.27.2 on darwin/arm64, the
  recipe of the v1.10.2 ledger:
  `988e40cd9fe5dcfa621443ba6c4bc53cb1f62f46e151578b8e4a044cf564a3d2`
  (180,732,082 bytes).
- `OAP_ACP_SMOKE=1` and `OAP_ACP_INTEGRATION=1` with that binary and
  `OAP_ACP_SHA256` bound pass 3x: `TestACPProcessSmoke`,
  `TestACPProcessAgainstChatCompletionsMock` and
  `TestACPProcessReopensItsBoundConversation`. Each run had a temporary
  `HOME`, the loopback Chat Completions mock and its fixture key, no other
  credentials, and `sandbox-exec` denying the keychain daemons and every
  non-loopback connection.

From `v1.145.0` to `v1.150.0`, cagent's `pkg/acp` changes one line outside
its tests: `outcome.go` adds `ErrorCodeRoutingFailed` to the runtime errors
that fail the prompt, as `ErrorCodeRateLimited` and the others already do. The
adapter settles a failed prompt `run.failed` whatever its code, so nothing it
decodes changes. The corpus's `session-reopen` case still reports
`agentInfo.version` `v1.145.0`, because it was captured from that release; it
is carried unchanged.

## Usage under Decision 0063

[Decision 0063](../decisions/0063-usage-counts-cache-and-reasoning-tokens.md)
gives core `usage` a member for each kind of token: `input_tokens` excludes
cache, `output_tokens` includes reasoning, and the cache and reasoning counts
have their own members. The prompt-result `usage` the v1.11.0 ledger maps does
not say which convention its counts follow. Its fields say
"Total input tokens across all turns", not whether that total includes
`cachedReadTokens` and `cachedWriteTokens`, and nothing says whether
`outputTokens` includes `thoughtTokens`. The End-Turn Token Usage RFD is still
Draft "while token accounting semantics are still being refined".

So the adapter fills none of the new core members. `thoughtTokens`,
`cachedReadTokens` and `cachedWriteTokens` stay in
`extensions["com.agentclientprotocol.turn_usage"]`, renamed only, and the
first-turn core `usage` keeps `input_tokens`, `output_tokens` and
`total_tokens` as reported. Splitting cache out of `inputTokens`, or reasoning
into `outputTokens`, would pick one reading of an open upstream question.

Keeping them as reported does not make them 0063's figures, and this record
says so rather than imply it. Only `total_tokens` means the same thing under
either reading. `input_tokens` and `output_tokens` carry ACP's own counts, and
whether they match 0063's definitions is unverified. The one sample in hand,
the #1090 counts the `usage-fold` case uses, suggests they do not:
`inputTokens` 7224 plus `outputTokens` 77 is the reported `totalTokens` 7301,
beside `cachedReadTokens` 6976. That is what an input count including cache
looks like, the OpenAI convention, against 0063's cache-free `input_tokens`. A
consumer pricing by 0063 should read these two members as ACP's, not as 0063's.
Classification: lossy, recorded; the semantics of `input_tokens` and
`output_tokens` are unverified. The pinned agent, cagent `v1.150.0`, sends no
prompt-result `usage` at all, so no traced run is affected today. When ACP
defines the convention, the adapter maps it and this section is superseded.
