# Decision 0025: Authentication Is an Obligation, Not a Credential Channel

Status: proposed
Date: 2026-09-22
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`, with one member added to
`open-agent-protocol.model-provider-core`
Unit: `auth` (claim term `+auth`)
Extends: [Decision 0017](0017-provider-provisioning.md), applying its credential
rule to a channel it did not anticipate
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

This protocol has no authentication vocabulary. The executable core is
session, run, action, models, capabilities and user-input, plus `inference.*`
on the provider profile. The single exception is `auth_status` on a model
descriptor (`schema/v0.1/provider.schema.json:184`), which reports a state and
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

OAP carries the first two. It never carries the third. Completion happens
between the endpoint and its identity provider over a channel this protocol
does not describe and cannot observe, which is what makes the credential rule
enforceable rather than aspirational: there is no OAP envelope a token could
travel in, because no envelope has a member that would hold one.

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
`unknown` — moves from `provider.schema.json` to `common.schema.json`, and both
profiles reference it. `capabilities.response` gains an optional `auth_status`
using it.

It moves rather than being copied because a second enum is a second thing to
drift, which is the defect [#177](https://github.com/lsm/open-agent-protocol/issues/177)
already is one instance of: the provider profile's `ProtocolError` dropped
`retriable` because it had been modelled as its own struct rather than as the
common shape narrowed by `allOf`. One vocabulary with two references is the
pattern that held; two vocabularies naming the same states is the one that did
not.

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
  exactly one schema file, `provider.schema.json`, as `authStatus` (`:184`) and
  the `auth_status` member that references it (`:275`).
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

## Consequences

The SDK rewrite has somewhere to put a login. A control layer can drive one
without knowing whether the endpoint speaks device flow, an API key prompt, or
a browser redirect, because all three reduce to "show the human this, tell me
when they are done."

An endpoint whose auth genuinely cannot be expressed as an interaction — one
needing a token in the answer — declares `+auth` unavailable rather than
approximating it. That is the same discipline every other unit follows.

Schema, validator, diagnostic codes and fixtures move together, per the
repository rule that a `schema/v0.1/*.json` change requires matching edits to
`protocol/`, the validator, a fixture, and `clients/ts/src/protocol.ts`. Moving
`authStatus` into `common.schema.json` touches the provider profile, so the
provider corpus moves in the same commit.

## What this unit does not admit

No token, code, secret or assertion travels in an OAP envelope, including in
`extensions`. A namespaced extension is still the OAP wire.

No credential storage vocabulary. Where a token lives, how it is encrypted and
when it is evicted are endpoint concerns, and an endpoint that exposed them
would be describing its implementation rather than its boundary.

No refresh protocol. `expired` tells a caller that a refresh may help; it does
not give the caller a way to perform one, because performing one requires the
refresh token this unit forbids on the wire.

Device flow is the worked example, not the model. This unit does not assert
that every endpoint authenticates by RFC 8628, and an endpoint whose flow is an
API key typed into a prompt satisfies it with a single question.
