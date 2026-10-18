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

Not re-run at this pin, for the reason the v1.11.0 ledger gives: the agent the
gate drives, `docker/cagent`, stays pinned at `v1.145.0`, and the stable schema
sends nothing new to or from it. Its latest release is `v1.150.0`; moving it
means building it from source, which this move does not do.
