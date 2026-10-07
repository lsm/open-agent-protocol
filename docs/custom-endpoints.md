# Custom OpenAI- and Anthropic-compatible endpoints

`~/.oapx/providers.json` declares endpoints the runtime does not ship knowledge
of: a self-hosted vLLM or llama.cpp server, an aggregator such as OpenRouter, a
corporate gateway that speaks the Anthropic wire format, or any vendor with an
OpenAI-compatible API.

Declared providers appear in `/model`, the status bar and print mode alongside
the built-in ones.

```json
{
  "providers": [
    {
      "id": "groq",
      "name": "Groq",
      "api": "openai-completions",
      "base_url": "https://api.groq.com/openai/v1",
      "auth": { "env": "GROQ_API_KEY" },
      "models": ["llama-3.3-70b-versatile"]
    },
    {
      "id": "gateway",
      "name": "Internal Gateway",
      "api": "anthropic-messages",
      "base_url": "https://gw.internal/anthropic",
      "headers": { "X-Tenant": "acme" },
      "capabilities": { "cache_ttl": true }
    },
    {
      "id": "local",
      "api": "openai-completions",
      "base_url": "http://localhost:8000/v1",
      "auth": "none"
    }
  ]
}
```

## Declaring one from the TUI

`/provider add <id> <base_url> [--api <api>] [--env <NAME> | --no-auth]` appends an
entry to the file instead of editing it by hand. The api defaults to
`openai-completions`; with neither flag the key comes from `/login <id>`, which the
command names when it finishes. The new entry is checked with the same parser the
loader uses before anything is written, so the command refuses exactly what the
loader would, with the loader's error name (`ReservedProviderId`,
`DuplicateProviderId`, `InvalidBaseUrl`, …), and it refuses to touch a file that
does not parse. Existing entries, their unmodelled members and `overrides` are kept
as written, since the entry is added to the file's JSON rather than to the parsed
providers, and the file is replaced by a rename, never rewritten in place. Fields the
command has no flag for — `name`, `headers`, `models`, `capabilities` — are still
added by editing the file.

`/provider del <id>` removes that entry the same way, keeping every other entry and
`overrides` as written, deletes the key `/login <id>` saved for it, and refreshes
the models. It refuses an id the file does not declare (`ProviderNotDeclared`), and
waits while a turn is running.

`/provider list` prints what the file declares: one line per custom provider with its
api, base URL, where its key comes from (`no credential`, `key from <NAME>`, or a key
saved by `/login`), and how many models it declares, followed by any `overrides` and
which members each one sets. It reads the file without writing it, and with nothing
declared it says so and names `/provider add` rather than printing an empty list.

## Fields

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Provider id. Letters, digits, `-`, `_`, `.`. Becomes the provider half of a model ref and the keychain account, so it must not collide with a built-in id. |
| `base_url` | yes | Endpoint origin. A trailing `/v1` is stripped; see Base URLs below. |
| `api` | no | `openai-completions` (default), `openai-responses` or `anthropic-messages`. |
| `name` | no | Display name; defaults to the id. |
| `auth` | no | Either `{ "env": "VAR" }` naming a non-empty environment variable, or the string `"none"` to send no credential at all. Anything else is a load error, including an object with no `env`, a non-string `env`, an empty `env`, and any other string. Omitting `auth` is fine and means the key comes from the keychain. |
| `headers` | no | Extra request headers, as a flat object of strings. |
| `models` | no | Allowlist and fallback list; strings, or objects with `id`, `name`, `context_window`, `max_tokens`. |
| `capabilities` | no | Overrides for what the endpoint supports; see below. |
| `reasoning` | no | Whether models expose reasoning. Default `false`. |
| `carries_version` | no | `true` when `base_url` already contains the API version, so the request path is the wire's path without its leading `/v1`. Absent means the wire's full path is appended. This is the only place a user states the fact; see Base URLs below. |
| `context_window`, `max_tokens` | no | Defaults for models that do not state their own. Default 128000 and 8192. |

A malformed entry fails the whole file rather than being skipped, so a typo
cannot silently drop one provider while the rest keep working. Reserved ids,
unparseable base URLs, unsupported `api` values and duplicate ids are each
rejected by name. Because that disables every custom provider at once, the TUI
reports it on startup as an error row naming the file and the reason, for
example `InvalidBaseUrl` or `ReservedProviderId`. Without that row a typo would
look identical to having no config at all: nothing in `/model`, and
`/login <id>` answering "unknown login provider".

## Base URLs

Paste whichever form the vendor documents. A trailing `/v1` is stripped when the
file is read, because the providers append their own versioned path. Both of
these reach `https://api.groq.com/openai/v1/chat/completions`:

```
"base_url": "https://api.groq.com/openai/v1"
"base_url": "https://api.groq.com/openai"
```

Without that normalisation the first form would produce `/v1/v1/chat/completions`
and a 404: the builder drops a version segment only on the wires whose descriptor
says it does, and stripping at read time is what covers the rest.

That trailing-`/v1` strip is the whole of the rule, and it is deliberately
narrow: it handles `/v1` at the end and nothing else. A version that is **not**
trailing — Z.AI's `/api/coding/paas/v4`, Deep Infra's `/v1/openai` — is left
alone, and the wire appends its full path, so a base like that now reaches
`/v4/v1/chat/completions` where it used to reach `/v4/chat/completions`. Say so
with `carries_version`, which is the only way to state it:

```
"base_url": "https://api.z.ai/api/coding/paas/v4",
"carries_version": true
```

Stating it also turns the strip off, so the two cannot both fire and delete the
version between them. With `carries_version` set, `base_url` is used as written
and the wire appends only its tail; without it, a trailing `/v1` is stripped as
above. So write the base the way the vendor documents it and state the fact only
when the version is not trailing.

This is the one place a user can state the fact, and an environment variable
cannot: a `*_BASE_URL` override is a bare string, so a base it names that ends in
something other than `/v1` has no way to say so. Use `providers.json` for an
endpoint whose version is not trailing.

A catalogued row is not affected. `providers/catalog.json` records
`carries_version` per endpoint, and a discovered model resolves its path from
that, so the fourteen catalogued bases keep the URLs they had. Only a base the
user names — a custom entry, an override, or one a provider-protocol client sends
— reaches the rule above.

A trailing `/` is ignored too, on every wire, so all three of these reach the same
URL:

```
"base_url": "https://api.groq.com/openai"
"base_url": "https://api.groq.com/openai/"
"base_url": "https://api.groq.com/openai/v1/chat/completions"
```

No vendor serves `//`, and a base that already ends with its wire's path is used
as it is rather than having the path appended again.

## Overriding a catalogued row

A `providers` entry may not take a built-in id, because a provider that shadows
one would answer for a credential the catalog resolves elsewhere. That is why a
built-in provider cannot be pointed at a proxy, a gateway or a regional mirror
from here. An `overrides` entry is the other half of the file: it names a
**catalogued** id and says where that row's requests should go, keeping the
row's own id and wire.

**An override's `base_url` redirects the row.** The order is the environment
first (`OAPX_BASE_URL`, then the row's own `base_url_env`), then the override,
then the catalog, so a variable set for one run still beats the file. The row's
models are listed from the override's base, its requests go there, its
`carries_version` decides where the wire's path joins, and its `headers` ride
every request. This applies to the rows the catalog loader serves and to the
Anthropic and Codex rows, which keep their own loaders: their models are listed as
before (Anthropic falls back to its built-in list when no login can list them) and
then carry the override's base and headers. GitHub Copilot cannot be overridden,
because the base URL it uses is issued inside its token; an override naming
`github-copilot` is refused at load as `UnsupportedOverrideRow`.
An override's `models` narrow the row exactly as a custom entry's do: discovery is filtered
by the list, and the list is what the row offers when discovery returns nothing,
with each entry's `name`, `context_window` and `max_tokens` taking precedence
over what discovery reported.

**An overridden row says where it goes.** `auth.providers.response` names the
host in `override_host` while an override that moves its requests or adds
headers to them is in effect, so `listProviders()` in
both SDKs and the TUI's `/login` list (`deepseek via proxy.internal`) show it.
When an environment base outranks an override's `base_url`, the row carries no
mark. An override without `base_url` still applies its headers wherever the row
resolves, the environment's base included, so its mark names that host.

**A redirected row's stored credential does not follow it.** A key or OAuth token
saved with `/login` reaches an override's endpoint only when the override says
`"forwards_credential": true`. Without it, a request to that endpoint carries a
key from the row's environment variable when one is set, and otherwise only the
override's own `headers` — which is where a proxy's key belongs. The Anthropic, Codex and Copilot rows hold a
saved API key to the same origins as their OAuth tokens: their own, the
environment's, or an override's that forwards it. A file that
cannot be read or parsed fails closed: a stored credential then reaches only an
origin the row is known to use, its catalogued endpoints or a base named in the
environment.

```json
{
  "overrides": [
    {
      "id": "deepseek",
      "base_url": "https://proxy.internal/deepseek/v1",
      "carries_version": true,
      "headers": { "X-Tenant": "acme" }
    }
  ]
}
```

An override may name only these members:

| Member | Meaning |
| --- | --- |
| `id` | Required. The catalogued row to override. An id the catalog does not record is `UnknownProviderId`, and `github-copilot` is `UnsupportedOverrideRow`. |
| `base_url` | Where the row's requests should go. A trailing `/v1` is stripped unless `carries_version` says otherwise, exactly as for a custom entry. Without it the row keeps its own endpoint, and the override's `headers` ride the row's requests there. |
| `carries_version` | `true` when this base already carries the API version. The same fact a custom entry states, and for the same reason: only the endpoint's owner knows where its version sits. |
| `forwards_credential` | `true` to send the row's stored key or OAuth token to this endpoint. Absent or `false`, it is withheld. |
| `headers` | Extra request headers for this row. |
| `models` | Allowlist over what discovery returns, as for a custom entry. |

Everything else is refused at load by name, and the refusal is
`ForbiddenOverrideMember` for all of it. `api` and `wire` are the two that
matter most: a row's wire is what its credential and its descriptor are bound
to, so an override that changed it would be a different provider wearing the
row's id. `auth`, `name`, `capabilities`, `context_window`, `max_tokens` and
`reasoning` are refused for the same reason — they describe the provider rather
than the route to it, and the catalog already answers them. So is any member not
in the table above, because a name nobody reads is a name that looks like it
worked.

A row may be overridden at most once, so a file that names `deepseek` twice
fails with `DuplicateOverride` rather than depending on which line won.

## Credentials

**Keys are never read from this file.** There are two supported sources:

- **Keychain**, which is preferred. `/login <id>` prompts for the key and stores
  it under that provider id, the same path Kimi uses. The input is masked.
  `/logout <id>` removes that one provider's credential from the keychain and
  from `~/.oapx/auth.json`, and leaves every other credential alone. A stored key
  belongs to the row it was saved for: providers reading the same environment
  variable (`opencode-zen` and `opencode-go`, the Xiaomi regions) each log in on
  their own. Earlier builds saved a login's key under every such row; those
  copies stay until `/logout` names each row. It fails
  rather than touching only the file when the keychain is locked or busy, since
  a later load would read the untouched keychain item first. It cannot
  remove a credential set in the environment (a catalog variable or a custom
  provider's `auth.env`), nor the Codex CLI login that
  `openai-codex` imports, and says so when either still applies.
- **Environment**, by naming a variable in `auth.env`. The name is not a secret;
  the value never enters the file.

The keychain is checked first. A provider with neither source still appears in
`/model` and still lists its models, but by default it cannot complete a turn:
the providers raise `MissingApiKey` before a request leaves the process when no
key resolves. That default is deliberate, so that forgetting to log in fails
loudly instead of quietly sending an unauthenticated request.

An `auth` block that is present must be one of the two valid shapes. A typo such
as `{ "environment": "KEY" }`, or a `{ "env": 5 }`, used to parse as "no key" and
surface much later as a confusing `MissingApiKey`; both are now load errors. A
load error disables **all** custom providers until the file parses, and the TUI
prints a startup row naming the file and the error rather than failing silently.

A server that wants no credential at all, such as a local llama.cpp, vLLM or
LM Studio, says so with `"auth": "none"`. It is an opt-in and nothing else
implies it: an absent `auth` block still means "a key is required and none was
found". A provider that declares it still prefers a real key when one resolves,
from the keychain or from a request, and only falls back to sending nothing.

`/login` with no argument shows the built-in providers. Custom providers are
reached by naming them, `/login gateway`, and only if they are declared in the
file. Listing them in the picker is not implemented yet.

## Model discovery

Startup never touches the network. Loading the catalog reads
`~/.oapx/model_catalog/custom-<id>.json`, preferring a copy younger than 24
hours and still using an older one rather than nothing, and falls through to the
declared `models` list when there is no cache at all. Keeping the network off
the startup path is worth doing on its own, and the discovery fetch is now
bounded as well: it runs through `compat.http.fetch`, which gives up after
`catalog_fetch_timeout_ms` and reports `error.Timeout` rather than waiting on
the peer.

Bounding it took a detour worth recording, because the two obvious mechanisms
do not work. `std.http.Client.ConnectTcpOptions` in Zig 0.16.0 declares a
`timeout` field, but that is the only mention of it anywhere under `std/http/`
and `connectTcpOptions` never reads it, so it is inert. Setting `SO_RCVTIMEO`
on the connected socket is worse than useless: the timeout does fire, but
`netReadPosix` in `std.Io.Threaded` treats the resulting `EAGAIN` as a
programmer bug and panics in debug builds. What `compat.http.fetch` does
instead is run the whole request on its own thread that owns its client, its
allocations and a copy of every input, and wait on a `std.Io.Event` with a
deadline. On expiry the caller abandons the thread and returns; the thread
frees everything it owns whenever the peer finally answers or the connection
drops, so nothing the caller owns is touched afterwards.

That primitive covers requests that read one whole response: catalog discovery,
and the OAuth device-code, token-poll and token-exchange calls. **It does not
cover provider streaming**, deliberately. A streaming response has no whole-body
read to bound, and a read deadline there would kill a turn whenever a model
thinks for longer than the timeout.

The fetch happens off that path. A successful `/login <id>`, a `/logout <id>`
that removed a credential, and `/model refresh` each refresh every catalog, which
is what populates the cache the first time, and a refresh
requests `<base_url>/v1/models`, writes the cache, and falls back to the cached
copy however old when it fails. An endpoint keyed from `auth` rather than the keychain has
no login step, so it serves its declared `models` list until `/model refresh` or
some other login triggers one. When the refreshed list no longer holds the active
model, the TUI moves to the first model it does hold and says so. The refresh
runs in the background — the status bar shows `refreshing models` — and the new
list is swapped in once no turn is running, before any queued message starts the
next one. A source that failed is named in the transcript, as
`model refresh: <provider>: <reason>`: the URL and the error or HTTP status for a
custom provider, or the parse error for `providers.json`.

The declared list is a fallback **only** when discovery produced nothing at all.
When discovery succeeds, its result is filtered by the list and that is what you
get, even if the filter removes everything. A provider that discovers only models
you did not allow therefore contributes nothing, rather than quietly falling back
to its declared entries.

A declared `models` list acts as an **allowlist** over whatever discovery
returns. This is what keeps an aggregator usable: OpenRouter lists hundreds of
models, and naming three keeps `/model` readable. Omit the list entirely and
every model the endpoint advertises is offered, which is what you want for a
server hosting one.

### Catalogued rows

A row in `providers/catalog.json` is discovered the same way, from the models URL
that row's `models_endpoint` and base URL compose into, and cached per row at
`~/.oapx/model_catalog/catalog-<id>.json` — a name of its own, so a catalogued
row and a custom row sharing an id never share a cache entry. The row's
credential comes from `provider_credential.lookup`, so an environment variable
beats a stored key, and a row with no credential anywhere contributes nothing
rather than being fetched unauthenticated.

A catalogued row has no declared list to fall back on, so a row whose discovery
produced nothing contributes nothing. The models it does build carry the row's
own id, wire and base URL, which are read through the catalog rather than
written here: nothing in this runtime spells a catalogued base URL. The two
values discovery does not carry are the same defaults a custom provider starts
from, 128000 and 8192, until a wire reports better ones.

A row whose listing names models without their limits can name a `models_dev`
key, as both OpenCode rows and the coding-plan rows do. A discovered model with no context window, output
limit, reasoning flag or image input of its own then takes the figure
[models.dev](https://models.dev) publishes for the same model id under that key;
a figure the listing or the row's `models` entry gives is never replaced, and a
model models.dev does not list keeps the defaults. The full listing is fetched
without credentials only when such a row has a credential and a model lacking a
limit, and the providers the catalog names are kept at
`~/.oapx/model_catalog/models-dev.json`, together with the keys that fetch
sought. An ordinary load reads that copy however old and fetches only when there
is none, or when the catalog has gained a key the copy was not fetched for. That
second fetch is tried once: if it fails, the copy is kept and marked as having
sought the new keys, so models.dev being unreachable delays at most one start
after an upgrade. A model refresh fetches it again, falling back to the copy, then
to the defaults.

The base URL a discovered row uses is resolved the way every other row's is, in
the order `provider_base_url` documents: `OAPX_BASE_URL` first, then the row's
`base_url_env` (`DEEPSEEK_BASE_URL`, `OPENAI_BASE_URL`, …), then the catalog. So
`DEEPSEEK_BASE_URL` points a discovered row at a proxy, and it points **discovery**
at the proxy too — the models listing is read from the override, not from the
vendor, so a key is never sent to an endpoint the operator redirected away from.
A versioned override keeps the rule above: a trailing `/v1` is dropped, because
the wire adds its own.

A row's own `base_url_env` is read from the catalogued row rather than named in
code, so every row that declares one is routed by it — `OLLAMA_BASE_URL`,
`AZURE_OPENAI_BASE_URL` and `GOOGLE_BASE_URL` as much as `DEEPSEEK_BASE_URL`. A
row that declares no variable has no per-row override and can only be moved by
`OAPX_BASE_URL`.

A base the catalogue already records is **not** an override. A catalogue target
is re-resolved onto an override wire only when the base in hand differs from the
base the catalogue records for the selected wire and region, so a provider whose
catalogueued base becomes resolvable keeps the wire it is configured for instead
of being read as though an operator had redirected it. `deepseek` is the case
this covers: with nothing set it resolves the `anthropic-messages` base
`https://api.deepseek.com/anthropic`, and that catalogueued value is not an
override.

**The test for an override is value inequality, and the boundary is recorded
rather than left implied.** An operator who sets a row's `base_url_env` to
exactly the value the catalogue already records is indistinguishable here from an
operator who sets nothing, so the configured wire is kept. That is a change, and
it is observable: setting `DEEPSEEK_BASE_URL` to the catalogueued Anthropic base
used to be read as an override and moved the daemon onto `openai-completions`, so
naming a provider's own default changed which protocol it spoke. Keeping the
configured wire is the point. The cost is that a provider which ever gains an
override *wire* mapping will find an override equal to its catalogueued base does
not select that mapping, and a base differing only in trailing whitespace or a
trailing `/v1` is normalised before it is compared.

Which wire an override selects is unchanged and remains a separate question: the
mapping is still per provider, `deepseek` still maps an override to
`openai-completions`, and a provider with no mapping keeps its own wire whatever
base it is given. Only the question of *whether* an override is present changed.

The precedence is pinned where callers reach it rather than through the resolver
alone. `OAPX_BASE_URL` and a row's own `base_url_env` are set through the
runtime's test environment seam and read back through `defaultBaseUrlForRef`, so
each step is exercised on the public path: an empty override falls back to the
catalogue, a row override wins over the catalogue, a global override wins over
the row, and a versioned override is normalized. The resulting wire is then pinned
through `catalogEndpointWithOverrides` in both directions — a base equal to the
catalogueued one keeps `anthropic-messages`, and a differing one moves the target
and hands back a target that owns what it built. Both directions are pinned
because the defect is a false positive that only appears once the catalogueued
base starts resolving, so a control that only exercises the resolver would have
passed against the broken code.

## Capabilities

Capability detection is otherwise a hostname guess, which cannot work for an
endpoint on your own domain. Anything you declare wins; anything you leave out
falls back to that guess, so existing providers are unaffected.

That fallback is per key, not per block. Declaring one capability does not opt
the others into anything: every key you leave out stays genuinely unset and is
answered by the guess on its own. In particular `max_tokens_field` stays
`max_tokens` and strict tool schemas stay off for an endpoint the guess does not
recognise, so declaring `cache_ttl` on a gateway cannot quietly start sending
`max_completion_tokens` and `strict` to an endpoint that implements neither.

An endpoint the guess *does* recognise keeps what it detects, for the keys you
did not declare. A custom entry pointing at a host the guess knows to want the
`zai` or `qwen` thinking format still gets that format, and declaring an
unrelated capability no longer resets it. Declare a key explicitly when you want
to override the guess, not to protect the rest of the block from it.

| Key | Effect |
| --- | --- |
| `cache_ttl` | Endpoint honours long Anthropic prompt-cache TTL. This is the one that matters for an Anthropic-compatible gateway, which otherwise silently loses long cache TTL because `isAnthropicHost` matches on hostname. |
| `reasoning_effort` | Accepts a reasoning-effort parameter. |
| `developer_role` | Accepts the `developer` role. |
| `store` | Supports the `store` parameter. |
| `strict_mode` | Supports strict tool schemas. |
| `thinking_as_text` | Requires thinking to be sent as plain text. |
| `usage_in_streaming` | Reports usage in streaming responses. |
| `max_tokens_field` | `max_tokens` or `max_completion_tokens`. |
| `thinking_format` | `openai`, `zai` or `qwen`. |

Every key in the table is optional in the same sense: on the provider protocol,
an absent capability field means *unset, detect*, never a default value. A
capability block travelling between an SDK client and a provider server carries
only the keys that were declared, and the receiving side resolves the rest from
the model's base URL exactly as if no block had been sent.

## Headers

`headers` entries are sent on every request to that provider. A header whose
name already exists is skipped rather than duplicated, so an `Authorization` or
`anthropic-version` in configuration cannot shadow the credential the runtime
resolved or the API version it requires.

## The Anthropic wire format and vendor credentials

The provider protocol refuses to use a vendor OAuth credential for a provider it
was not issued to, which is why an Anthropic subscription token cannot be pointed
at a third-party endpoint. A custom provider on `anthropic-messages` is not that
case: it authenticates with its own key, resolved from the keychain under its id
or from its declared environment variable, and the vendor token is never
consulted.

A provider that declares `"auth": "none"` sends no credential: no
`Authorization` header on the OpenAI formats, no `x-api-key` on the Anthropic
one. The rest of the request is unchanged, so `anthropic-version` and
`content-type` still go out.

The opt-in cannot be turned against a vendor. It is carried on the model as
`allows_anonymous`, and each provider refuses to honour it for the vendor ids it
serves: `anthropic` and `deepseek` on the Anthropic format, and `openai`, `deepseek`, `kimi`,
`github-copilot`, `openai-codex` and `azure` on the OpenAI ones. A declared
provider can never hold one of those ids anyway, since they are reserved, so the
check only matters for a request arriving over the protocol. Honouring it there
would cost nothing more than an unauthenticated request to a URL the caller
already chose, but refusing keeps the vendor paths uniformly credentialed.

Four rules keep those apart. `base_url` arrives from the request, so the first
three bound which credential a request can name and the fourth bounds where an
OAuth credential may be sent. When a request names a vendor wire
format (`anthropic-messages`, `openai-codex-responses`) but a different
`provider`, the server refuses outright if that `provider` is itself a vendor id,
so claiming `provider: "openai-codex"` on `anthropic-messages` cannot pull the
stored Codex token. Otherwise it resolves the credential for that id under an
api-key-only rule that skips OAuth entries entirely. A custom provider's
credential is always a stored API key or an environment variable, so the rule
costs it nothing, and the `anthropic` and `openai-codex` OAuth tokens cannot be
resolved under a borrowed identity.

The third rule covers the wire formats that have no vendor of their own. Only
`anthropic-messages` and `openai-codex-responses` declare an `auth_provider_id`;
the other six registered APIs leave it null, so the credential is resolved under
whatever id the request's `provider` field claims. That id used to be honoured
for OAuth entries as well, which meant a request naming `openai-completions`
with `provider: "anthropic"` and any `base_url` resolved the stored Anthropic
OAuth token and sent it there, bypassing the first two rules entirely. A request
claiming a vendor id on an API that is not that vendor's now resolves under the
same api-key-only rule, so the token stays put and an ordinary custom provider
with a stored key is unaffected.

The fourth rule bounds the destination, which the first three deliberately do
not. A stored OAuth credential is now bound to the origins its provider is
expected to serve: before the token is handed to a provider, `model.base_url` is
compared against an allowed set, and a request pointing somewhere else is
refused with `auth_required` rather than being sent the token. Origin means
scheme, host and port; the path is not compared, so a proxy route under an
allowed host stays usable. An empty `base_url` is allowed, because the server
then fills in the endpoint itself from the same defaults and overrides.

The allowed set for a provider is built from three sources, and the split
between them is the whole point of the rule: an environment variable is set by
whoever runs the process, while `base_url` can arrive from a remote protocol
client.

- The vendor's own origin. `anthropic` is bound to `https://api.anthropic.com`
  and `openai-codex` to `https://chatgpt.com`. `github-copilot` is bound to any
  host under `githubcopilot.com`, which covers both the individual endpoint and
  an `api.<tenant>.githubcopilot.com` enterprise tenant.
- Whatever the environment names for that provider: `OAPX_BASE_URL`, and the
  per-provider variable where one exists (`ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`,
  `DEEPSEEK_BASE_URL`). An operator who can already route a vendor through a
  corporate proxy can still reach it; nothing new has to be configured, and no
  new variable was added to relax the check. Codex and Copilot have no
  per-provider variable, so `OAPX_BASE_URL` is their override here exactly as it
  already is for routing.
- The endpoint the credential itself recorded at login. GitHub Copilot writes the
  base URL it was issued into the credential's `provider_data`, so an enterprise
  deployment that does not sit under `githubcopilot.com` keeps working without
  configuration.

A provider id with none of the above — today only `test-fixture`, tomorrow any
OAuth provider added without an entry — has **no** allowed origin, so a stored
OAuth token under that id is withheld from every non-empty `base_url`. That is
deliberate: a new OAuth provider fails loudly at its first request rather than
silently reopening the gap.

An endpoint a configuration **file** names for a row is a fourth source, and it
is the only one that needs a second signal. The environment is trusted without
one because whoever runs the process sets it, and a variable disappears when the
process ends. A file persists, is synced, and is edited by hand, so an endpoint
only a file names is not a destination for a vendor token unless that same file
also says the row's stored credential may go there. An override that omits that
leaves the token where it is.

The rule reaches API keys stored in the OAuth shape, which is why "an `.oauth`
entry" is not the same as "an OAuth credential" here. `/login kimi` records a
region, and a credential carrying `provider_data` is persisted as `.oauth` with
an empty `refresh` and `expires` at `maxInt` rather than as `.api_key` — the
same shape the legacy `region` field migrates into. Those are API keys, so an
entry with no refresh token under a provider id with no policy is exempt and
goes wherever the user pointed it. A missing refresh token does **not** exempt
`anthropic`, `openai-codex` or `github-copilot`: an id with a policy is always
bound, so the exemption cannot be used to unbind a vendor token. What the
fail-closed rule above therefore covers is an entry that has a refresh token and
no policy.

Plain `.api_key` entries, and keys from a declared provider's environment
variable, are not checked at all. That pairing is the user's own; constraining
it would break custom endpoints for no gain.

`github-copilot` is why this could not be fixed by widening the refused set
instead. Copilot is stored as an OAuth credential and its models genuinely run
on `openai-completions`, an API that declares no `auth_provider_id`, so the
legitimate request and the exfiltrating one are the same request with a
different `base_url`; adding `github-copilot` to the refused set would make
Copilot resolve no credential at all. The test in
`protocol/provider/server.zig` that pins Copilot resolving its stored token on
`openai-completions` still does so, now against a `githubcopilot.com` base URL
rather than an arbitrary one, and a sibling test pins the refusal at any other
origin.

**What this still does not do.** It bounds where a credential goes, not what a
request may ask for. A caller that legitimately holds a vendor credential can
still drive it with any prompt, and the origin set is per provider rather than
per credential, so two logins to the same vendor are interchangeable. It also
takes the environment and `provider_data` at face value: anything that can write
those already runs as the user.

Each provider's own environment fallback is scoped the same way. When the server
resolves nothing it still calls the provider without a key, and the provider then
looks at its own variables; the Anthropic provider used to read
`ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` without checking which provider
the model belonged to, so a custom endpoint could be handed the vendor key that
happened to be in the environment. It now reads them only when
`model.provider` is `anthropic`, matching what the OpenAI providers already did,
and a custom provider that resolves no key of its own fails with `MissingApiKey`
instead of borrowing one.

## Limits

- Custom providers reach the TUI and the CLI. The TypeScript SDK's `models.list`
  is served by a separate catalog and does not show them. SDK consumers are not
  blocked: the provider server already accepts a client-supplied `base_url`, so
  they can stream against any endpoint by passing it explicitly.
- Only the three wire formats above are accepted. Google, Bedrock and Azure
  shapes are not expressible here.
- Pricing is not declared, so the status bar hides cost for custom models rather
  than guessing a rate.
