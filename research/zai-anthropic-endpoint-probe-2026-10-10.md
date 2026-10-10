# Z.AI coding plan: the Anthropic-compatible endpoint, probed 2026-10-10

Why: #361 routes Claude Code to a catalog row on the `anthropic-messages` wire,
and the `zai-coding-plan` row listed only its OpenAI-compatible endpoint. This
records what the global host answered to a key the coding plan endpoint accepts, so the row can list
the second endpoint on evidence rather than on documentation alone.

The key was the owner's, read from the shell profile as `GLM_API_KEY` and never
written down; the first row below is the evidence that it is a coding plan key. Each request was made once with `curl`.

| Request | Auth header | Answer |
| --- | --- | --- |
| `GET https://api.z.ai/api/coding/paas/v4/models` | `Authorization: Bearer` | `200`, an OpenAI-shaped list (`object: list`, `data[].id`), `glm-4.5` through `glm-5.2` among them |
| `GET https://api.z.ai/api/anthropic/v1/models` | `x-api-key` alone | `200`, an Anthropic-shaped list (`data[].id`, `display_name`, `created_at`, `type: model`), the same ids |
| `POST https://api.z.ai/api/anthropic/v1/messages`, model `glm-5.1`, `anthropic-version: 2023-06-01` | `x-api-key` alone | `200`, an Anthropic `message` with `stop_reason: end_turn` and `usage` |
| `GET https://api.z.ai/api/anthropic/models` | `x-api-key`, then `Authorization: Bearer` | `200` carrying `{"code":500,"msg":"404 NOT_FOUND","success":false}` both times |

What follows for the catalog:

- **The endpoint is `https://api.z.ai/api/anthropic/v1`, with `carries_version:
  true`.** The row's `models_endpoint` is `/models`, shared by both endpoints, and
  the unversioned base would join it to `/api/anthropic/models`, the one path
  that does not exist. With the version in the base, the pinned models and
  request URLs are the two that answered. This is the shape the
  `minimax-coding-plan` row already uses.
- **A missing path is not an HTTP error here.** The 404 arrives as a `200` with
  an error object in the body, so a status-only check would have read the wrong
  path as live.
- **`x-api-key` is enough.** It is the header `oapx` sends for a key on this
  wire and the one `ANTHROPIC_API_KEY` makes Claude Code send, so a harness
  routed to this endpoint authenticates as `oapx` itself would.
- **`GLM_API_KEY` is accepted beside `ZHIPU_API_KEY`.** It is the name the Hermes
  ledger records for Z.AI (`research/hermes-v2026.9.24-mapping.md`), and the key
  probed here was held under it. `ZHIPU_API_KEY` stays first.

The `openai-completions` endpoint stays the row's first wire, so `oapx`'s own
model listing and requests for this row are unchanged.

The China host's Anthropic endpoint, recorded as a documented configuration in
`research/zai-china-coding-plan-evidence.md`, was not probed and is not added.
