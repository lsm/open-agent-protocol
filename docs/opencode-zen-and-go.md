# OpenCode Zen and OpenCode Go

OpenCode sells model access two ways, and oapx keeps them as two provider rows.
Both read `OPENCODE_API_KEY`, but a key's balance and plan are separate, so each
row logs in on its own.

| | `opencode-zen` | `opencode-go` |
|---|---|---|
| What it is | Pay as you go against a prepaid balance (OpenCode's docs now call it the OpenCode Console) | A monthly subscription with rolling caps: 5 hours, a week, a month |
| OpenCode's model ids | `opencode/<id>` | `opencode-go/<id>` |
| Base URL | `https://opencode.ai/zen/v1` | `https://opencode.ai/zen/go/v1`, which also requires `x-opencode-session` |
| A usage limit | Spends the balance | `GoUsageLimitError` until the cap resets |

## Free models

Both gateways list models whose id ends in `-free`. The gateway serves them only
to the OpenCode client: a direct call answers `FreeTierError` ("OpenCode's free
tier can only be used from within OpenCode"). So the `opencode-zen` and
`opencode-go` rows offer no `-free` id, discovered or named in
`~/.oapx/providers.json`, unless that file moves the row to another base URL, and
oapx reaches those models only through an OpenCode server it runs, never by
presenting itself as OpenCode's client from its own loop.

The terminal UI is that route: when an `opencode` it has been tested with is
available (the one `oapx opencode install` fetched, else `opencode` on `PATH`),
it starts that server in the background and lists the models the server reports
in `/model` under `opencode-server`, free ones included. Picking one opens a new
session on OpenCode's own loop and tools.
