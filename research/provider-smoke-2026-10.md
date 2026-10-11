# Provider smoke evidence, 2026-10-11

Status: evidence for #356, one reading of the opt-in live gate
(`test-e2e-provider-smoke`, `zig/test/e2e/provider_smoke.zig`) against the
`current` rows a key was available for. Provider compatibility evidence, separate
from harness conformance.

## How it was run

One row per invocation, from this branch's gate, with `HOME` redirected to a
fresh temporary directory and `OAPX_KEYCHAIN_SERVICE` set to a unique service
that was deleted afterwards:

```sh
HOME=$(mktemp -d) OAPX_KEYCHAIN_SERVICE="oapx-smoke-$(uuidgen)" \
  OAP_PROVIDER_SMOKE=<row> <row's key variable>=… \
  zig build --build-file zig/build.zig test-e2e-provider-smoke
```

Each row runs four cases: one completion, one streamed completion (more than one
delta), one tool call (`record_quirk`), and one request naming a model that does
not exist. No log printed a key.

## Results

| Row | Model | completion | streamed | tool call | unknown model | Reading |
|---|---|---|---|---|---|---|
| `deepseek` | `deepseek-chat` | pass | pass | pass | refused | all four, twice |
| `opencode-go` | `deepseek-v4-flash` | pass | pass | pass | refused | all four once the budget fix below landed |
| `kimi` | `kimi-k2.7-code` | pass | pass | pass | **answered** | the quirk below; the gate now records it |
| `zai-coding-plan` | `glm-4.6` | pass | pass | pass | refused | every case passed in at least one of three runs; the rest hit HTTP 429 |
| `opencode-zen` | `deepseek-v4.1-flash` | — | — | — | refused | not verifiable with this account (below) |
| `openai`, `anthropic`, `openrouter` | | | | | | no key was available; not run |

## Findings

**The gate's token budgets were too small for a model that reasons first.** The
completion case allowed 32 tokens and the streamed and tool-call cases 128.
`deepseek-v4-flash` through OpenCode Go spent 109 of 128 tokens on
`reasoning_content`, opened the tool call, and stopped with `finish_reason:
"length"`. oapx drops a partial tool call, so the case failed, but the provider
was right. `glm-4.6` did the same with 32 tokens and no text. The gate now
allows 1024 tokens for those three cases. The unknown-model case keeps 16.

**Kimi's coding endpoint answers a model id it does not have.** A request for
`oapx-provider-smoke-no-such-model` to `https://api.kimi.com/coding/v1/chat/completions`
returns HTTP 200, echoes the bogus id in `model`, and completes. This reaches
no oapx host: `oapx serve provider` fails a create naming a model its catalog
does not serve with `model_not_found` before contacting the provider (checked
against a loopback DeepSeek mock). The gate's row now says
`answers_unknown_model = true`, so the case passes on the recorded behaviour and
fails if Kimi starts refusing.

**The Z.AI coding plan rate-limits back-to-back requests.** Runs one second
apart hit `HTTP 429 (Rate limit reached for requests)` on one or two of the four
cases, a different one each time. The models endpoint answered 200 throughout.
oapx reports this failure as `provider_unavailable`, although the profile defines
`rate_limited`; that mapping is a separate change.

**OpenCode Zen could not be read with this account.** The gate's default model,
`grok-code-fast-1`, is no longer in Zen's model list and answers HTTP 400 "Model
is unavailable". The default is now `deepseek-v4.1-flash`, which is listed, but
this account answers it with HTTP 402 (insufficient funds). Zen's free models
answer HTTP 403 `FreeTierError`, because the free tier only serves requests from
OpenCode itself. The unknown-model case was refused as expected.

**OpenCode Go refuses a request without `x-opencode-session`.** It answers
`MissingSessionID`. The catalog already names that header for the row, and oapx
sends it; only a hand-made request without it fails.

## Not done here

No row's status changes. A `goap check` rule that requires a ledger reference for
every `current` row would fail today on `openai`, `anthropic`, `openrouter` and
`opencode-zen`, which have no reading yet. It lands with the change that records
the last of them, or with a decision to move them to `supported`.
