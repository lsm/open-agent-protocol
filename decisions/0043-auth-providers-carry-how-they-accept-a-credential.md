# Decision 0043: Auth Providers Carry How They Accept a Credential

Status: accepted 2026-10-06 (#505 put `auth_kinds` on every provider row,
required with `minItems: 1` in `auth.schema.json`, copied from the catalog's own
`auth` array, with the `auth-provider-without-kinds` schema-invalid fixture; the
Go, TypeScript, Python and Rust SDKs each drop an unknown kind and keep the rest,
pinned by their own tests)
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
  Python all do the same two things: a missing list reads as empty, and a kind
  this build has no name for is **dropped while the rest of the list survives**.
  Decision 0029's own additive-only rule requires it in both directions: a new
  client must stay usable against a runtime that has not shipped the field, and
  against one that has shipped a kind this build predates. Keeping the entries
  that parse is the useful half — `["api_key", "passkey"]` still tells the
  caller the provider takes an API key — and it is why the rule is per entry
  rather than all-or-nothing, since discarding the whole list over one unknown
  entry throws away the part that is understood.
  The consequence is that a **wholly** unrecognised list also reads as empty, so
  emptiness means "this build cannot tell you how to authenticate" rather than
  "there is nothing to authenticate with". That is the right trade: `none` is a
  *known* kind, so a provider that genuinely needs no credential always arrives
  as `["none"]` and is never confused with an unusable field.

The values come from the catalog's own `auth` array rather than a second list, so
a row's kinds cannot disagree with the row's `auth_kinds` — the runtime reads
one and sends it.

**The list is required and non-empty on the wire, and the SDKs filter it on
read.** Each of the four reads a missing list as empty and drops kinds it has no
name for, keeping the rest: one unrecognised entry costs the caller only that
entry, not the list, and a list that is wholly unrecognised reads as empty.
Discarding a whole list over one unknown entry would throw away the part the
build does understand, and `["api_key", "passkey"]` from a newer runtime still
says something true and useful. The four must agree on this, because a caller
comparing two SDKs' output for the same runtime should not see the field parsed
differently; that they once did not — Rust filtering, TypeScript and Python
all-or-nothing, Go verbatim — is why the rule is written here rather than left
to each implementation.

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
