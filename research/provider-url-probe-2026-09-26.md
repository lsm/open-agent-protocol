# Every catalogued provider URL, probed

Status: evidence for [#350](https://github.com/lsm/open-agent-protocol/issues/350)
step 5. One reading, 2026-09-26, unauthenticated, no credential sent.

## What was probed and how

Each row's two URLs as `modelsUrl(id, region)` and `requestUrl(id, wire, region)`
resolve them at `main`: the models listing with a `GET`, a request path with a
`POST` carrying an empty JSON body, because a chat path answers `405` or `404` to
a `GET` and says nothing about itself. A path that exists answers `200`,
`401`, `403`, or `400` for a body it rejects before it authenticates; a path that
does not exist answers `404`.

Forty-one URLs, twenty-two endpoints. Every one is served: six `200`, thirty-three
`401`, one `403`, one `400`. No `404`.

`google` is in the catalog and not in the table because it resolves neither URL:
its wire is model-scoped, so there is no fixed request path to probe, and the row
records no models listing of its own. `openai-codex` contributes one URL for the
same reason on the models side.

| Provider | Wire | Region | Kind | URL | Status |
| --- | --- | --- | --- | --- | --- |
| `alibaba-coding-plan` | openai-completions | — | models | `https://coding-intl.dashscope.aliyuncs.com/v1/models` | 200 |
| `alibaba-coding-plan` | openai-completions | — | request | `https://coding-intl.dashscope.aliyuncs.com/v1/chat/completions` | 401 |
| `anthropic` | anthropic-messages | — | models | `https://api.anthropic.com/v1/models` | 401 |
| `anthropic` | anthropic-messages | — | request | `https://api.anthropic.com/v1/messages` | 401 |
| `deepinfra` | openai-completions | — | models | `https://api.deepinfra.com/v1/openai/models` | 200 |
| `deepinfra` | openai-completions | — | request | `https://api.deepinfra.com/v1/openai/chat/completions` | 401 |
| `deepseek` | openai-completions | — | models | `https://api.deepseek.com/v1/models` | 401 |
| `deepseek` | openai-completions | — | request | `https://api.deepseek.com/v1/chat/completions` | 401 |
| `kimi` | openai-completions | china | models | `https://api.kimi.com/coding/v1/models` | 401 |
| `kimi` | openai-completions | china | request | `https://api.kimi.com/coding/v1/chat/completions` | 401 |
| `kimi` | openai-completions | global | models | `https://api.moonshot.ai/v1/models` | 401 |
| `kimi` | openai-completions | global | request | `https://api.moonshot.ai/v1/chat/completions` | 401 |
| `minimax-coding-plan` | anthropic-messages | — | models | `https://api.minimax.io/anthropic/v1/models` | 401 |
| `minimax-coding-plan` | anthropic-messages | — | request | `https://api.minimax.io/anthropic/v1/messages` | 401 |
| `openai` | openai-completions | — | models | `https://api.openai.com/v1/models` | 401 |
| `openai` | openai-completions | — | request | `https://api.openai.com/v1/chat/completions` | 401 |
| `openai` | openai-responses | — | models | `https://api.openai.com/v1/models` | 401 |
| `openai` | openai-responses | — | request | `https://api.openai.com/v1/responses` | 401 |
| `openai-codex` | openai-codex-responses | — | request | `https://chatgpt.com/backend-api/codex/responses` | 401 |
| `opencode` | openai-completions | — | models | `https://opencode.ai/zen/v1/models` | 200 |
| `opencode` | openai-completions | — | request | `https://opencode.ai/zen/v1/chat/completions` | 401 |
| `openrouter` | openai-completions | — | models | `https://openrouter.ai/api/v1/models` | 200 |
| `openrouter` | openai-completions | — | request | `https://openrouter.ai/api/v1/chat/completions` | 401 |
| `tencent-coding-plan` | openai-completions | — | models | `https://api.lkeap.cloud.tencent.com/coding/v3/models` | 401 |
| `tencent-coding-plan` | openai-completions | — | request | `https://api.lkeap.cloud.tencent.com/coding/v3/chat/completions` | 401 |
| `vercel` | openai-completions | — | models | `https://ai-gateway.vercel.sh/v1/models` | 200 |
| `vercel` | openai-completions | — | request | `https://ai-gateway.vercel.sh/v1/chat/completions` | 400 |
| `volcengine-coding-plan` | openai-completions | — | models | `https://ark.cn-beijing.volces.com/api/coding/v3/models` | 401 |
| `volcengine-coding-plan` | openai-completions | — | request | `https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions` | 401 |
| `xiaomi` | openai-completions | — | models | `https://api.xiaomimimo.com/v1/models` | 401 |
| `xiaomi` | openai-completions | — | request | `https://api.xiaomimimo.com/v1/chat/completions` | 401 |
| `xiaomi-token-plan-ams` | openai-completions | — | models | `https://token-plan-ams.xiaomimimo.com/v1/models` | 401 |
| `xiaomi-token-plan-ams` | openai-completions | — | request | `https://token-plan-ams.xiaomimimo.com/v1/chat/completions` | 401 |
| `xiaomi-token-plan-cn` | openai-completions | — | models | `https://token-plan-cn.xiaomimimo.com/v1/models` | 401 |
| `xiaomi-token-plan-cn` | openai-completions | — | request | `https://token-plan-cn.xiaomimimo.com/v1/chat/completions` | 401 |
| `xiaomi-token-plan-sgp` | openai-completions | — | models | `https://token-plan-sgp.xiaomimimo.com/v1/models` | 401 |
| `xiaomi-token-plan-sgp` | openai-completions | — | request | `https://token-plan-sgp.xiaomimimo.com/v1/chat/completions` | 401 |
| `zai-coding-plan` | openai-completions | — | models | `https://api.z.ai/api/coding/paas/v4/models` | 401 |
| `zai-coding-plan` | openai-completions | — | request | `https://api.z.ai/api/coding/paas/v4/chat/completions` | 401 |
| `zenmux` | openai-completions | — | models | `https://zenmux.ai/api/v1/models` | 200 |
| `zenmux` | openai-completions | — | request | `https://zenmux.ai/api/v1/chat/completions` | 403 |

## The one 400

`ai-gateway.vercel.sh/v1/chat/completions` answers a `POST` with
`{"error":{"message":"Invalid input: expected string, received undefined","param":"model"}}`:
the path is served and the body is rejected before authentication. The same
server answers `404` for `/v1/v1/chat/completions` and for `/v1/nope`, so the
`400` distinguishes a served path from a missing one rather than hiding it.

## What this does not settle

Three hosts answer `401` for any path, so their rows are served but not
*distinguished*: `api.z.ai`, `api.lkeap.cloud.tencent.com` and
`ark.cn-beijing.volces.com` would answer the same for a doubled version. Their
rows are settled by the version rule [#383](https://github.com/lsm/open-agent-protocol/pull/383)
fixed and by `go/provider/zai.go`, which already pairs Z.AI's `/v4` base with a
versionless path.

`minimax-coding-plan` is the one row where a served path may still be the wrong
one: `https://api.minimax.io/anthropic/v1/models` answers `401` like the
provider's real listing at `https://api.minimax.io/v1/models`, so an
unauthenticated probe cannot say which is a listing. #352's shared-key probe is
what settles it, and this table says so rather than calling it proved.

Nothing here is a conformance claim. No probe runs in CI, and no result in this
file is a substitute for a request carrying a credential.
