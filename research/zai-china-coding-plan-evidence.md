# Z.ai China Coding Plan evidence matrix

Status: provider compatibility evidence policy, separate from OAP harness
conformance.

## Official configuration evidence

| Preset | Wire | Base and path | Model | Evidence class | Official source |
|---|---|---|---|---|---|
| `zai-cn-responses-control` | OpenAI Responses | `https://open.bigmodel.cn/api/v1` + `/responses` | `glm-5.3` | documented control | [Codex](https://docs.bigmodel.cn/cn/coding-plan/tool/codex) |
| `zai-cn-responses-glm-5.3-flash` | OpenAI Responses | `https://open.bigmodel.cn/api/v1` + `/responses` | `glm-5.3-flash` | compatibility candidate | [Latest model](https://docs.bigmodel.cn/cn/coding-plan/latest-model.md) |
| `zai-cn-anthropic` | Anthropic Messages | `https://open.bigmodel.cn/api/anthropic` + `/v1/messages` | `glm-5.3-flash[1m]` | documented configuration | [Claude Code](https://docs.bigmodel.cn/cn/coding-plan/tool/claude) |
| `zai-cn-chat` | OpenAI Chat Completions | `https://open.bigmodel.cn/api/coding/paas/v4` + `/chat/completions` | `glm-5.3-flash` | compatibility candidate | [Coding Plan quick start](https://docs.bigmodel.cn/cn/coding-plan/quick-start.md) |

The Codex guide explicitly supplies the Responses base, `wire_api = "responses"`,
and `glm-5.3`; it does not print the complete `/api/v1/responses` URL. That path
is a composition of the documented base with the Codex Responses wire. Separate
official material establishes that `glm-5.3-flash` is available to China Coding
Plan users, but does not explicitly establish that exact Codex/model combination.
Consequently `glm-5.3` is the control and Flash is reported independently as an
observed compatibility candidate.

The generic OpenAI API base (`/api/paas/v4`) is not substituted for the China
Coding Plan base (`/api/coding/paas/v4`). Coding Plan credentials are specific to
the plan and must not be assumed interchangeable with platform API credentials.

The executable list is available without credentials:

```sh
go run ./cmd/oap providers zai-cn
go run ./cmd/oap providers zai-cn --format=json
```

## Hermetic evidence

`internal/providertest` implements loopback mocks for all three wires with fake
credentials, exact paths, provider-native auth, deterministic SSE, tool calls,
429 responses, malformed streams, and controlled slow streams. These tests run
in ordinary CI. `provider.RunEvidence` exercises the same bounded request and
structural stream-inspection path used by optional live tests.

Provider compatibility and adapter conformance are independent claims. A live
backend failure does not invalidate Codex-to-OAP reduction, and backend success
does not prove OAP lifecycle conformance.

## Live evidence gate

Live evidence is test-only and cannot be activated by credential presence. It
requires all of:

```sh
OAP_LIVE_ZAI=1
OAP_ZAI_CN_AUTHORIZED=1
OAP_ZAI_CODING_PLAN_KEY=<dedicated test-owned secret>
go test ./provider -run '^TestLiveZAIChinaCodingPlan$' -count=1 -v
```

`OAP_ZAI_CN_AUTHORIZED=1` is an explicit operator assertion that the supplied key
was verified for `open.bigmodel.cn` and the China Coding Plan. The default run
makes one bounded streaming request for the documented `glm-5.3` Responses
control and one separately labelled `glm-5.3-flash` candidate. Add
`OAP_LIVE_ZAI_ALL_WIRES=1` only when Anthropic and Chat evidence is wanted.

The runner:

- has a 45-second timeout per row;
- requests at most eight output tokens;
- performs no retries;
- requires a 2xx SSE response and a provider-native completion marker;
- reports only preset ID, wire, model, HTTP status, media type, event names, and
  structural completion;
- never reports headers, request body, prompts, output text, IDs, or credential
  material.

The key is passed as an HTTP header only. Never place it in argv, URLs, query
strings, committed configuration, fixture names, snapshots, traces, logs, or raw
captures. Do not map ambient `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or
`ANTHROPIC_AUTH_TOKEN` automatically. Ordinary CI must not define any live gate
or credential variable.

## Capture and sanitization policy

The current evidence runner intentionally emits a reduced structural result
rather than retaining raw traffic. This is fail-closed by construction: response
bodies are bounded to 1 MiB, parsed in memory, and discarded; non-JSON SSE data,
oversized bodies, non-SSE success, and missing completion markers fail the run.
No raw-capture command exists yet.

If native/raw capture is introduced later, it must default outside the repository
and must not become committable until a fail-closed sanitizer has:

1. classified every field;
2. removed `Authorization`, `x-api-key`, all environment secrets, account/org and
   request IDs, prompts/output, absolute paths, billing data, and trace metadata;
3. replaced native identities and timestamps deterministically;
4. passed determinism, idempotence, secret/path/email canary-removal, and pinned
   codec-decodability tests;
5. received explicit human review before reduction into a fixture.

Unknown fields must stop sanitization rather than pass through. `.claude/`, live
output, credentials, and raw captures remain outside commits.
