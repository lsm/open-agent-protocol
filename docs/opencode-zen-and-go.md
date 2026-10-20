# OpenCode Zen and OpenCode Go

OpenCode sells model access two ways, and oapx keeps them as two provider rows.
One OpenCode key works for both: the balance and the plan bill the same account
through different offerings.

## One credential, two offerings

Both rows name the catalog's `credential_group` `opencode`, so they share one
credential:

- `/login opencode-go` or `/login opencode-zen` stores a single OpenCode key,
  under the group's name, and both rows read it. `OPENCODE_API_KEY` already
  signed in both.
- `/logout` through either row removes it, and both rows sign out.
- `auth.providers` reports the two rows with their own ids and auth kinds, as
  before; only their status now moves together.
- The model catalog lists Go and Zen models together under that one login, each
  model on its own row and wire.
- A key that is not entitled to one offering, such as Go without a subscription,
  leaves that offering's models listed: they fail at use with OpenCode's own
  error, and the other offering is unaffected.

A key saved for one row before this change is still read for that row; the next
login through either row moves it to the shared credential.

| | `opencode-zen` | `opencode-go` |
|---|---|---|
| What it is | Pay as you go against a prepaid balance (OpenCode's docs now call it the OpenCode Console) | A monthly subscription with rolling caps: 5 hours, a week, a month |
| OpenCode's model ids | `opencode/<id>` | `opencode-go/<id>` |
| Base URL | `https://opencode.ai/inference/v1`, serving chat completions, messages and responses alike | `https://opencode.ai/inference/go/v1`, the same, and it also requires `x-opencode-session` |
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
