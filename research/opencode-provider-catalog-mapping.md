# OpenCode provider breadth and its model catalog: mapping ledger

Status: recorded 2026-09-26 against `dev`, the branch the installed binary
(`opencode v2.0.18`) is not cut from. This ledger is **not** an adapter pin. The
agent-control wire of this project is the subject of
[`opencode-v1.18.32-mapping.md`](opencode-v1.18.32-mapping.md) and
[`opencode-v1.18.29-mapping.md`](opencode-v1.18.29-mapping.md); what follows
records the *provider* side of the same upstream, which nothing here had read.

## Provenance

- Repository: `https://github.com/anomalyco/opencode` (`sst/opencode` redirects)
- Branch: `dev`
- Commit: `696f41bc8e7586657375d53390925fc54c25d34c` (2026-09-25T23:27:56Z)
- Commit tree: `93c203247ba9dc0ed662b3b3680b035363de9570`
- Local binary consulted for its version string only: `opencode v2.0.18`
  (`/opt/homebrew/bin/opencode`, a compiled Bun artifact; no source shipped)

```sh
git clone https://github.com/anomalyco/opencode.git
git -C opencode checkout 696f41bc8e7586657375d53390925fc54c25d34c
git -C opencode rev-parse HEAD 'HEAD^{tree}'
gh api repos/anomalyco/opencode/git/trees/dev?recursive=1
```

Model catalog fetched once, read-only, no credential, for the counts below:

- Source: `https://models.opencode.ai/api.json`
- Bytes: 4929526
- SHA-256: `d5b9557331839e96b9273acef897d8721bf9db74933d6d3cc00febf698067f7a`

```sh
curl -s https://models.opencode.ai/api.json -o models.json
shasum -a 256 models.json && wc -c models.json
```

No vendor API was called with a credential for this ledger, and no catalog entry
was selected on a key's behalf.

## What was read

| Path | Why it matters here |
| --- | --- |
| `packages/core/src/models-dev.ts` | the catalog fetch, cache and refresh policy |
| `packages/core/src/catalog.ts` | how a catalog record is projected onto a runtime provider and model |
| `packages/opencode/src/provider/provider.ts` | the provider registry, the bundled-SDK map, key resolution |
| `packages/opencode/src/session/llm/ai-sdk.ts` | the bridge from `ai`'s `streamText` events to their internal `LLMEvent` |
| `packages/schema/src/provider.ts` | the `native` / `aisdk` API tag on a provider and on a model |
| `packages/llm/src/provider.ts`, `packages/llm/src/providers/*` | the hand-written provider definitions |
| `packages/llm/src/protocols/*` | the six wire protocols those providers are written against |
| `patches/@ai-sdk%2F*.patch` | nine local patches to AI SDK packages |
| `packages/opencode/package.json` | the dependency set |

## The provider layer is two mechanisms, and the choice is data

`packages/schema/src/provider.ts` types a provider's and a model's API as a
tagged union: `{ type: "native" }` or `{ type: "aisdk" }`.
`packages/core/src/catalog.ts` merges a model's `api` over its provider's, so
one model can speak a different protocol than its siblings. That is the same
separation OAP already draws between `provider` and `wire`.

The native side is small and deliberately so: ten provider modules
(`anthropic`, `amazon-bedrock`, `azure`, `cloudflare` — exporting both the AI
Gateway and Workers AI variants — `github-copilot`, `google`, `openai`,
`openai-compatible`, `openrouter`, `xai`) written against **six** wire
protocols (`anthropic-messages`, `bedrock-converse`, `gemini`, `openai-chat`,
`openai-compatible-chat`, `openai-responses`; `bedrock-event-stream`, `shared`
and `index` are the supporting files beside them). Everything not in that list
is reached through `openai-compatible`.

The AI SDK side is a lazy `import()` table of **22** factories
(`packages/opencode/src/provider/provider.ts:113`, `BUNDLED_PROVIDERS`), keyed
by the npm package name the catalog names, and the dependency set behind it is
18 `@ai-sdk/*` vendor packages plus `@openrouter/ai-sdk-provider`,
`gitlab-ai-provider`, `venice-ai-sdk-provider` and `ai`. A package that is not
bundled is installed at runtime (`Npm.add(model.api.npm)`) and imported from its
entrypoint; `file://` is accepted too. Eight bundled packages carry local
patches under `patches/`, so the SDK path is not maintenance-free.

## The breadth is a dataset, not code

`fromModelsDevModel` (`packages/opencode/src/provider/provider.ts:1265`) resolves
the package to load like this:

```ts
npm: cloudflareGatewayNpm(...) ?? model.provider?.npm ?? provider.npm ?? "@ai-sdk/openai-compatible"
```

The last term is the whole trick: a provider with no package of its own is an
OpenAI-compatible endpoint, which is most of them. Keys are data too —
`provider.env` is a list of environment variable names and the key is
`provider.env.map((e) => envs[e]).find(Boolean)`.

At the pinned catalog, counted over the digest above:

| Fact | Count |
| --- | --- |
| Providers | 223 |
| Models | 8179 |
| Distinct npm packages named | 28 |
| Providers with no `npm` of their own | 0 |
| Providers carrying `env[]` | 223 |
| Models with `cost` | 7755 |
| Models with `modalities` | 8179 |
| Models with `reasoning_options` | 4795 |
| Models with `interleaved` | 1206 |
| Models with a per-model `provider{npm,api}` override | 335 |
| Models with `experimental.modes` | 60 |
| `status` values in use | `beta`, `deprecated` |

A provider record is `{ id, name, env[], api (base URL), npm, doc, models{} }`.
A model record is `{ id, name, description, family, attachment, reasoning,
reasoning_options[{toggle|effort|budget_tokens}], tool_call, temperature,
interleaved, release_date, last_updated, modalities{in,out},
limit{context,input,output}, cost{input,output,cache_read,cache_write,tiers,
context_over_200k}, status, experimental.modes{}, provider{npm,api} }`.

`packages/core/src/models-dev.ts` holds it with a five-minute on-disk cache, a
cross-process lock around the write, a background refresh every sixty minutes,
a snapshot compiled into the binary, and three escape hatches
(`OPENCODE_MODELS_URL`, `OPENCODE_MODELS_PATH`, `OPENCODE_DISABLE_MODELS_FETCH`).
The refresh never fails the process; it logs and keeps the previous catalog.

### Kimi, as the catalog has it

Four provider records cover the two regions and both plans. `kimi-code-plan-cn`
is `env: ["KIMI_API_KEY"]`, `api: "https://api.kimi.com/coding/v1"`, `npm:
"@ai-sdk/openai-compatible"`; `kimi-code-plan-global` is the same under
`https://api.kimi.ai/coding/v1`. `moonshotai-cn` and `moonshotai` carry
`MOONSHOT_API_KEY` against `api.moonshot.cn/v1` and `api.moonshot.ai/v1`.

`kimi-for-coding` in that record: context 1048576, output 32768, modalities
`text,image,video` in and `text` out, `interleaved.field` `reasoning_content`,
`reasoning_options` a toggle plus effort `low|high|max`, cost all zeroes.

## Mapping against this protocol

`drafts/model-provider-core.md` §"The model entry" already carries what a
caller needs to *place* a model: `model_ref`, `model_id`, `display_name`,
`provider_id`, `wire`, `context_window`, `max_output_tokens`, `capabilities`,
`lifecycle`, `source`, `reasoning_default`, `auth_status`. Per-model protocol
selection is already data here, which is the one thing the catalog had to invent
a structure for.

The gaps, each with what the catalog shows a caller actually wants:

1. **Price.** OAP publishes no cost anywhere in the provider profile, while
   `drafts/conformance.md` already puts "pricing" outside every current unit
   ("unless a richer profile defines them"). The Zig internal model carries cost
   (`ai_types.Model.cost`) and the TUI shows it, so the fact exists one layer
   below the boundary and cannot cross it.
2. **Modalities past vision and audio.** `capabilities` enumerates `vision`,
   `audio_input`, `audio_output`; every catalog model carries a full modality
   list including `video` and `pdf`, and Kimi's own listing says it takes video.
3. **Which reasoning levels a model accepts.** `reasoningLevel` exists and
   `reasoning_default` names one value, but the entry cannot say that a model
   offers `low|high|max` and no `medium`. A caller asked to pick has nothing to
   pick from.
4. **Age and family.** `release_date` and `family` are absent, so a client
   cannot prefer a newer sibling over a retired one except through `lifecycle`.
5. **Listing completeness.** The list response cannot say it is a subset. A
   provider whose own listing paginates makes `model_not_in_catalog` unsound,
   and nothing in the profile lets an implementation deny that.
6. **Provenance of the facts.** `source` (`discovered` / `fallback`) says
   whether the implementation read a catalog or fell back, but not how old it is.

Findings 5 and 6 are protocol-level regardless of this upstream: both are
about what a list response can assert, and neither needs a catalog to motivate.

## Impedance mismatches

1. **Price cannot cross.** Classified: needs a decision, and the decision is
   policy, not plumbing — the conformance unit that would judge it does not
   exist, and no cost member exists anywhere in the provider schema.
2. **Modality vocabulary is partial.** Classified: carriable additively; a new
   member duplicates three existing capability values unless they are deprecated
   alongside it.
3. **Reasoning effort levels are unrepresentable.** Classified: carriable
   additively — the enum already exists, only a list of accepted values is
   missing.
4. **No completeness signal.** Classified: protocol gap, provider-independent.
5. **No age signal.** Classified: protocol gap, provider-independent; an
   `observed_at_ms` is a fact about the response, not about the model.
6. **Env var names and catalog URLs are deliberately not carried.** Classified:
   refused. Decision 0030 keeps a vendor URL and a credential off any envelope an
   agent may read, and `auth_status` already answers the caller's only legitimate
   question ("can I call this?"). A provider resolves its own credentials.
7. **Per-mode variants and body overrides are not carried.** Classified: refused.
   The catalog's `experimental.modes` carries per-mode price *and* raw body and
   header overrides; publishing body overrides on a model entry is a vendor
   payload channel the profile does not otherwise have, and per-call shaping
   belongs on `inference.create.request`.
8. **The AI SDK itself is not adoptable here.** Classified: out of scope. It is
   a TypeScript dependency; both trees in this repository are Zig and Go. What
   transfers is the record shape, the per-model protocol selection, the key-name
   list, and the six wire protocols.

## What this ledger does not claim

- Nothing here was run. The catalog was read, not consumed by a binary; the
  native providers, the bundled-SDK table and the AI SDK bridge were read as
  source.
- No claim that the catalog is correct for any model; it is one vendor's
  dataset, mutable, and the digest above dates one reading.
- No claim about opencode's agent-control wire, which the two pinned ledgers
  own.
- No proposal is made here. The proposals live in
  [`decisions/0035-…`](../decisions/0035-a-model-entry-publishes-its-facts-and-absence-means-unknown.md).
