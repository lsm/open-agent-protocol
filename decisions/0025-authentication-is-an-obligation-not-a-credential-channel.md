# Decision 0025: Authentication Is an Obligation, Not a Credential Channel

Status: superseded by [Decision 0029](0029-authentication-over-agent-control.md) before acceptance
Date: 2026-09-22
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`, relocating one enum
`open-agent-protocol.model-provider-core` references; no member is added to
either
Unit: `auth` (claim term `+auth`)
Extends: [Decision 0017](0017-provider-provisioning.md), applying its credential
rule to a channel it did not anticipate
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

This protocol has no authentication vocabulary. The executable core is
session, run, action, models, capabilities and user-input, plus `inference.*`
on the provider profile. The single exception is `auth_status` on a model
descriptor (`schema/v0.1/provider.schema.json`, the `auth_status` member on
`modelEntry`), which reports a state and
describes no flow.

The four SDKs in this tree do have one. They drive the makai stdio protocol's
`auth` namespace: list the providers, start an interactive login, receive URL
and progress events, answer prompts. When those SDKs are rewritten as OAP
clients that vocabulary has to land somewhere, and "somewhere" has been
unspecified — which is the real reason this record exists now rather than
later.

It cannot be a straight port, because [Decision 0017](0017-provider-provisioning.md)
already settled the part that matters: credentials never travel. A caller sends
bare `NAME` entries, a literal `NAME=value` is refused at the daemon, and any
member that passes caller text through to the upstream request is a credential
channel whatever it is named. An `auth` namespace that carries tokens would
reopen, under a friendlier name, the exact channel that decision closed.

So the question is not how to move a credential. It is what is left of
authentication once you have decided a credential may not move at all.

## Decisions

### Three things are separable, and OAP carries two of them

Authentication decomposes into **reporting** that an endpoint cannot proceed
without a credential, **obliging** a human to do something about it, and
**completing** the exchange — redeeming a code, receiving a token, storing it,
refreshing it.

This unit carries the first two and never the third. Completion happens between
the endpoint and its identity provider over a channel this protocol does not
describe. What agent-control-core gives the rule is narrower than it is
tempting to claim: **no member of its envelope set is a credential**, so a
token has no place it belongs — but `action.schema.json` types
`arguments_json`, `result` and `progress` as `true`, any JSON value at all, and
a token put in one of those would pass the schema. The rule is declared and
policed, not structural, and `credential_in_trace` is what polices it.

That is a scope claim about this unit, not about the protocol, and the
difference is worth stating because the protocol has already priced the other
answer. `model-provider-core` does admit a caller-held credential, and what it
costs is visible in the schema: two tiers with the strong one mandatory
wherever it is achievable, a `value` member on
`provider.credential.grant.request` used only by the `on_envelope` fallback, a
nonce, an arrival deadline, a per-grant channel for the strong tier, and a
`credential_in_trace` diagnostic that refuses any trace carrying the value.
Authentication needs none of it, because a login has something a provider call
does not: a human, who can finish the exchange somewhere this protocol cannot
see.

### A login is an interaction, not a new request pair

`+auth` is defined as `+user-input` plus the two rules below. An endpoint
cannot claim `+auth` without claiming `+user-input`.

The alternative was an `auth.login.request` / `auth.login.response` pair with
its own event stream, mirroring the makai namespace. That pair would have had
to re-derive everything `+user-input` already has and the validator already
enforces: a stable `interaction_id`, the rule that only the declared responder
resolves an interaction and resolves it once, a cancel that is distinct from a
refusal, a `run.status.updated` carrying `waiting_for_input`, and a resolution
event that lets a run resume, fail, or cancel through the ordinary lifecycle.
A second machinery for the same shape is a second place for the arbitration to
be wrong, and the arbitration is the part that took three decisions to settle.

What a login needs beyond an interaction is a rule about what may be in it.
That is a payload constraint, not a new envelope.

### An auth interaction's questions are display-only

The endpoint puts in them exactly what a human must read in order to act: a
verification URL, a code to type there, an expiry, a human-readable provider
name. It must not put in them anything a machine could present as proof of
identity.

This is [Decision 0017](0017-provider-provisioning.md)'s rule turned around.
There, the danger was a member carrying caller text *out* to the upstream
request, and the generalization was that any such member is a credential
channel whatever it is named. Here the danger runs the other way — a member
carrying endpoint secrets *in* to the caller — and the generalization is the
same shape: **any member carrying machine-presentable proof is a credential
channel, whatever it is named.** Neither rule is a list of forbidden field
names, because a list of names is defeated by choosing another name.

The resolution carries no secret either. A human's answer to an auth
interaction is that they completed the step, or that they could not. It is
never a token, and an endpoint that would need one in the answer has not
implemented this unit.

A violation is `credential_in_trace` — the code the provider validator already
emits for the same claim, that a credential appeared in a trace. It is declared
in `go/validation/provider.go` today because only that validator had a rule
needing it, and `diagnostic.go`'s fifty-five codes carry no credential case at
all. `+auth` gives the agent-control validator one, so the code is hoisted to
where both can cite it rather than copied into a second name. Two codes naming
one defect is the shape of
[#177](https://github.com/lsm/open-agent-protocol/issues/177), which is what the
provider profile carrying its own `ProtocolError` already cost — and the same
argument that moves `authStatus` in this record.

### RFC 8628 has two codes, and only one of them may be displayed

[RFC 8628](https://www.rfc-editor.org/rfc/rfc8628) §3.2 returns both a
`user_code` and a `device_code` in one response. The `user_code` is the short
string a human types into the verification page. The `device_code` is what the
client sends to the token endpoint, and it is bearer material: anything holding
it can complete the exchange.

Only `user_code` may appear in an auth interaction. `device_code` may not,
and neither may an authorization code, an access token, a refresh token, or a
client secret.

This is written down because the draft of this record got it wrong. It said the
code is the thing a human types and therefore is not a credential — a sentence
that is true of one of the two codes, false of the other, and which named the
dangerous one. The mistake is easy in a specific way worth recording: both are
called a code, both arrive in the same response, and the safe one is the one
that appears in every screenshot of the flow. A rule phrased as "do not put the
code on the wire" is ambiguous exactly where it needs not to be, so this one
names both.

### Status is reported through one enum, in one place

`authStatus` — `authenticated`, `login_required`, `expired`, `failed`,
`unknown` — moves from `provider.schema.json` to `common.schema.json`. One
profile references it there: `modelEntry.auth_status`, on the provider side.

**The state attaches to the thing it describes, and this protocol already
decided where that is.** It is not the provider descriptor.
[Decision 0014](0014-provider-descriptors.md) considered `auth_status` there,
against a real request for it, and refused: a descriptor is fixed for a
capability revision while auth state changes the moment someone logs in, so a
revision-fixed carrier would be either stale or unstable. Provider *identity*
is stable and belongs on the descriptor; provider *usability* is dynamic and
belongs somewhere generated per read.
[Decision 0017](0017-provider-provisioning.md) restates it and does not rejoin
them, and [the provider draft](../drafts/model-provider-core.md) resolves it:
the volatile fact goes on the entry in the response, and "that distinction is
the whole of 0014's objection and it is satisfied, not overridden."

So the provider profile already carries this correctly. `modelEntry` in
`provider.models.list.response` has `auth_status` beside `provider_id`, and the
response is built per request. A caller asking whether one vendor needs a login
reads it off that vendor's entries, freshly, every time.

That also answers the case that made an endpoint-level member look necessary.
A single `auth_status` on `capabilities.response` cannot say that one provider
is authenticated and another is not — but the per-read entry can, because every
entry names its provider, and it does so without a revision-fixed carrier going
stale between two reads.

Agent-control has no equivalent member today: `modelsResponse` carries
`modelDescriptor`s with no auth state. If it needs one, it goes there, for the
same reason — a response is generated per read — and not on
`capabilities.schema.json`'s `providerDescriptor`. This record does not add it,
because nothing in `+auth` requires the agent-control side to report provider
auth state, and adding a member to answer a question nobody has asked is how
the descriptor accumulated the pressure 0014 had to refuse.

**So the only schema change this record makes is the enum's move**, and it
changes no validation outcome: the same JSON validates, no diagnostic differs,
and `oap check` passes its 550 fixtures untouched.

It moves rather than being copied because a second enum is a second thing to
drift, which is the defect [#177](https://github.com/lsm/open-agent-protocol/issues/177)
already is one instance of: the provider profile's `ProtocolError` dropped
`retriable` because it had been modelled as its own struct rather than as the
common shape narrowed by `allOf`. `protocolError` is defined once and
referenced from both profiles; `authStatus` is now defined once and referenced
from one, and moving it is what makes the second reference, whenever
agent-control needs one, a `$ref` rather than a fresh enum. One vocabulary in
one place is the pattern that held; two vocabularies naming the same states is
the one that did not.

The enum's distinctions are the reason a feature support level is not enough on
its own. An endpoint that cannot reach a model for want of a credential does
report the affected feature as `unavailable` with a reason, because that is
what effective fidelity means. But `unavailable` plus free text loses the
branch a caller needs: `login_required` means start a flow, `expired` means the
credential is stale and a refresh may fix it without troubling a human,
`failed` means the credential is wrong and retrying will not help. Those are
three different next actions.

`unknown` is not a synonym for `login_required`. Per
[Decision 0017](0017-provider-provisioning.md), some providers need no
credential at all: absence is a configuration, not an error state, and an
endpoint must not report an anonymous provider as one that needs a login.

## Evidence

Verified against this tree at `68168956`:

- No authentication vocabulary exists in the core schemas. `auth` appears in
  exactly one schema file, `provider.schema.json`, as the `authStatus` enum and
  the `auth_status` member that references it, the latter on a *model*
  descriptor. Neither is cited by line, and the reason is stronger than drift. A
  line number means something only relative to a commit. Within one tree, a
  commit that edits the file invalidates its own citations — the draft's `:184`
  pointed at the enum rather than the member when written, and after the move it
  points at `modelLifecycle`. Across two trees it is worse than stale: while
  this record was in review, `auth_status` sat at `:275` on `main` and `:266` on
  this branch, so a line cited between two readers was **wrong at the moment it
  was spoken, with both of them having verified correctly**. Time does not have
  to pass. A member name resolves in any tree that has the thing; a line number
  without a ref does not resolve at all. `allows_anonymous` is a member of
  that file's `providerDescriptor`, and `auth_status` is on `modelEntry`, so the
  anonymous-versus-`login_required` contradiction spans `provider.describe.response`
  and `provider.models.list.response`. It is a `"scope": "trace"` check, not a
  `"frame"` one. An earlier draft of this record had the status on the
  descriptor beside the flag and called it frame-decidable; that shape is
  refused by 0014 and the scope claim went with it.
- The refused `providerDescriptor` member was a different failure from those
  three, and wants a different check. Those were claims stronger than the tree
  supports, and a grep for the negative catches them. This was a claim the tree
  had already **adjudicated**: `0014:217` contains the exact motivation offered
  for the member — a client that cannot tell "logged into one vendor, not the
  other" cannot render a model picker — as the reason for the request it then
  refuses. No care about phrasing finds that. `grep -rn auth_status decisions/`
  does, in one command, and it was not run because the question felt like design
  rather than fact.

  The check has a failure mode worth naming beside it, because it defeated the
  check *while it was being verified*: run with `| head -4`, that grep returned
  0017 and this record and truncated 0014 — the one record that decides it.
  Three times in this work a `head` has hidden the answer to a question about
  whether something exists. A search asking whether something exists is not
  allowed to be truncated.

- [Decision 0020](0020-error-codes-are-declared.md)'s `error_codes` descriptor,
  which carries a code and the action a caller should take, is **decided and
  not built**. `error_codes` exists only in `pack.schema.json:36`, as a bare
  array of strings with no action member, and `capabilities.schema.json` has no
  such member at all. `CodeUnhonouredCapability` is declared in
  `go/validation/diagnostic.go:44` for the pack case. This record therefore does
  not lean on that descriptor, and an auth code does not need it: the obligation
  travels as an interaction, and the state travels as `auth_status`.
- [Decision 0017](0017-provider-provisioning.md)'s two-stage allowlist was
  verified against `906b2a1` when that record was written; the behaviour this
  record extends is the one recorded there, not a fresh claim.
- The draft of this record claimed no OAP envelope has a member that could hold
  a credential. That is false of the profile the record itself edits:
  `provider.schema.json` declares an optional `value` on
  `provider.credential.grant.request`, `credentialGrantTier` includes
  `on_envelope`, and `drafts/model-provider-core.md` says the request carries
  the value "only in the fallback tier". Enforcement there is
  `CodeCredentialInTrace` (`go/validation/provider.go:15`), exercised by
  `go/validation/provider_test.go:147`, which refuses a trace carrying
  `"value":"sk-abc"`. Caught in review; both statements are now scoped to the
  agent-control-core envelope set.
- Three mistakes in this record ran the same way, and the direction is the
  useful part. The third arrived *in the sentence rewritten to fix the first
  two*: scoped to agent-control-core, the claim became "structural rather than
  policed", which is still false, because seven members of
  `action.schema.json` are typed `true` and accept any JSON value, across four
  names — `arguments_json`, `result`, `progress` and `updated_arguments_json`.
  A pattern named in a record is not a pattern fixed by it. The count itself
  was wrong on first writing for a fourth instance of the same habit: it came
  from grepping three names I expected rather than asking which members are
  typed `true`, so `updated_arguments_json` could not have appeared however
  carefully I read the output. "The code is what a human types, so it is not a credential" and "no
  envelope has a member that would hold one" both *sound* structural, and a
  structural claim reads as self-evident, so it is the kind that does not get
  checked. The second was one `rg` away from either confirmed or refuted. The
  rule worth carrying out of this: a security claim of the form "there is no X"
  names where it was checked and what it is scoped to, or it is an assertion
  nobody ran.

## Consequences

The SDK rewrite has somewhere to put a login. A control layer can drive one
without knowing whether the endpoint speaks device flow, an API key prompt, or
a browser redirect, because all three reduce to "show the human this, tell me
when they are done."

An endpoint whose auth genuinely cannot be expressed as an interaction — one
needing a token in the answer — declares `+auth` unavailable rather than
approximating it. That is the same discipline every other unit follows.

Moving `authStatus` into `common.schema.json` needs no fixture. It relocates a
definition and repoints one `$ref`, so the same JSON validates and no
diagnostic changes; `oap check` passes its 550 fixtures untouched. The
repository rule that a `schema/v0.1/*.json` change moves with `protocol/`, the
validator, a fixture and `clients/ts/src/protocol.ts` has nothing to apply to
here, because this record adds no member. What the validator work carries is
the `+auth` payload rule and the `credential_in_trace` hoist, neither of which
is a schema change.

## What this unit does not admit

No token, code, secret or assertion travels in an auth envelope, including in
`extensions` — a namespaced extension is still the OAP wire. That is a rule
about `+auth` and not a property of OAP: `model-provider-core`'s `on_envelope`
grant tier deliberately carries a credential value, under the tier discipline
and the `credential_in_trace` diagnostic above. This unit declines that tier
rather than reinventing it.

No credential storage vocabulary. Where a token lives, how it is encrypted and
when it is evicted are endpoint concerns, and an endpoint that exposed them
would be describing its implementation rather than its boundary.

No refresh protocol. `expired` tells a caller that a refresh may help; it does
not give the caller a way to perform one, because performing one requires the
refresh token this unit forbids on the wire.

Device flow is the worked example, not the model. This unit does not assert
that every endpoint authenticates by RFC 8628, and an endpoint whose flow is an
API key typed into a prompt satisfies it with a single question.
