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

## Reading 2026-10-17, Zig 0.17.0

Run the same way, from `d8b42da` plus this change.

| Row | Model | completion | streamed | tool call | unknown model | Reading |
|---|---|---|---|---|---|---|
| `deepseek` | `deepseek-chat` | pass | pass | pass | refused | all four |
| `kimi` | `kimi-for-coding` | pass | pass | pass | answered | all four; the default model moved (below) |
| `zai-coding-plan` | `glm-4.6` | pass | pass | pass | refused | all four, no 429 this time |
| `opencode-zen` | `deepseek-v4.1-flash` | — | — | — | refused | HTTP 402 again (insufficient funds) |
| `opencode-go` | | | | | | not run: every model answers HTTP 429 `GoUsageLimitError` (monthly limit) |

**The gate's result was cached across rows.** Under Zig 0.17.0 the build
runner caches a test run whose inputs did not change, and an environment
variable is not an input. After one row passed, `OAP_PROVIDER_SMOKE=<another
row>` printed `run test cached` and reported success without running a case.
The gate's run steps, and those of the other key-gated e2e tests, now declare
side effects, so each invocation runs.

**Kimi's coding endpoint no longer lists `kimi-k2.7-code`.** Its models
endpoint answers `kimi-for-coding`, `kimi-for-coding-highspeed`, `k3` and
`k3-256k`. The old default still completed, but only because Kimi answers any
model id, so it proved nothing about a listed model. The gate's default is now
`kimi-for-coding`. The catalog row's static model list still names
`kimi-k2.7-code`.

## Reading 2026-10-18, `openai-codex`

The gate takes a key, and this row signs in with the Codex CLI's ChatGPT login,
so it was read through `oapx serve provider --stdio` instead, from `006e056`,
with the same four cases as newline requests. `HOME` and
`OAPX_KEYCHAIN_SERVICE` were isolated as above, and `CODEX_HOME` pointed at the
real `~/.codex`, read only: `auth.json` had the same digest before and after,
and its token was days from expiry, so nothing refreshed it.

| Row | Model | completion | streamed | tool call | unknown model | Reading |
|---|---|---|---|---|---|---|
| `openai-codex` | `gpt-6-luna` | pass | pass | pass | refused | all four |

The completion answered `ready`, the stream carried 79 deltas, the tool call
settled `tool_use` with `record_quirk({"note":"smoke"})`, and the unknown model
failed `model_not_found` before any request left the process.

**Two fixes came first.** `serve provider` resolved stored credentials only, so
it never saw the Codex CLI login (#1154). And the Responses decoder left parts
unended and dropped a finished tool call from the settled message, which the
tool-call case caught (#1157).

**The served ref names the wire the row maps to.** The catalog row lists
`openai-codex-responses`, but the provider profile serves it as
`openai-codex/openai-responses@gpt-6-luna`; the agent path still takes
`openai-codex/openai-codex-responses@gpt-6-luna`. Both reach the same endpoint.

The row moves to `current` with this reading. The gate cannot reach it: a test
build never imports the Codex CLI login, and the gate sends neither the
`ChatGPT-Account-ID` nor the `version` header the row needs. So the gate's test
names `openai-codex` as the one `current` row read through `serve provider`, and
holds that it has no key a row could be gated on.

## Not done here

Only `openai-codex` changes status. A `goap check` rule that requires a ledger reference for
every `current` row would fail today on `openai`, `anthropic`, `openrouter` and
`opencode-zen`, which have no reading yet. It lands with the change that records
the last of them, or with a decision to move them to `supported`.
