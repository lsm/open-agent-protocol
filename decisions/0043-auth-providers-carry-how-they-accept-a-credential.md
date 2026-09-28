# Decision 0043: Auth Providers Carry How They Accept a Credential

Status: proposed
Date: 2026-09-28
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `auth` (claim term `+auth`)
Amends: [Decision 0029](0029-authentication-over-agent-control.md), whose
`auth.providers.response` row listed only a provider's ID, name, status and an
optional last error

## Context

Decision 0029 made `auth.providers.response` the wire for "which providers can I
log in to, and are they logged in now". Its payload carries `id`, `name`,
`auth_status` and an optional `last_error`. That describes a provider's *state*
and not its *means*.

The distinction is not academic once the rows come from a catalog. An
API-key-only provider — DeepSeek, any of the coding plans, every gateway — and an
OAuth provider cannot be told apart from `auth_status` alone, because
`login_required` is what both look like before anything has been entered. A
caller that has to guess will offer a browser flow to a provider that wants a
pasted key, or ask for a key from a provider whose only path is a browser. The
request half of `auth.login.start` does carry the kind it wants; the response
half did not carry what was available, so the two could not be matched up.

Approximating was available and is not what this does. Deriving the kinds from
the provider id would be a second, hand-maintained table beside the catalog, and
would be wrong for exactly the rows the catalog exists to keep correct. Folding
them into `auth_status` would invent vocabulary that says "state" and mean
"means".

## Decision

Each provider row in `auth.providers.response` carries `auth_kinds`: a non-empty
ordered list drawn from `common.authKind` — `api_key`, `oauth`, `none`.

- **Required, with `minItems: 1`.** A conforming runtime always sends at least
  one. Emptiness is therefore reserved: it can only mean "a runtime predating
  this field", never "this provider needs no credential", which is what `none`
  is for.
- **Ordered as the catalog records it, and this decision does not set the
  order.** The values come from each row's `auth` array and the runtime copies
  that array verbatim, so the order is the catalog's and whatever the catalog
  says today is what goes on the wire. Today no row lists `oauth` first —
  `anthropic` is `["api_key", "oauth"]` and `ollama` is `["none", "api_key"]` —
  which means a caller taking the first entry it can perform gets an API key
  for Anthropic. That is a fact about the catalog, not a preference this
  decision states, and a later step may reorder the catalog to say otherwise;
  doing so needs no wire change, only a new reading of the same field.
- **`none` is a real answer.** Ollama needs no credential, and a row that said so
  is more useful than one that omits itself.
- **The SDKs tolerate what they do not recognise.** Rust, TypeScript, Go and
  Python read a missing list as empty and drop kinds they do not have a name
  for, rather than failing the whole listing. Decision 0029's own additive-only
  rule requires it in both directions: a new client must stay usable against a
  runtime that has not shipped the field, and against one that has shipped a
  kind this build predates. A partly-new list is still useful, so what survives
  is the entries that parse. A caller that needs the kinds can tell the two
  cases apart, because only an older runtime produces a wholly empty list.

The values come from the catalog's own `auth` array rather than a second list, so
a row's kinds cannot disagree with the row's `auth_kinds` — the runtime reads
one and sends it.

## Consequences

`common.authKind` is a new vocabulary alongside `common.authStatus`, and the two
are deliberately different things: `authStatus` is how usable a credential is
(`authenticated`, `expired`, `login_required`), `authKind` is what kind it is.
Conflating them would have needed a value for "no credential", which is not a
state at all.

A caller gains the ability to branch on how to authenticate without a table of
its own: the first entry it supports is the one to drive. A caller that ignores
the field is unaffected, which is the point of making it additive on the reading
side and required on the writing side.
