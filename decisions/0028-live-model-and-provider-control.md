# Decision 0028: Live Session Model and Provider Control

Status: proposed
Date: 2026-09-22
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0005](0005-run-controls.md),
[Decision 0006](0006-models-catalog.md), and
[Decision 0014](0014-provider-descriptors.md)
Supersedes: the proposed [Decision 0017](0017-provider-provisioning.md)

## Context

Per-submit `model_id` is an override for one run. It does not provide the
ordinary session operation "use this model from now on." Conversely,
attaching a provider is not the same as selecting a model: it makes an OAP
provider endpoint available to one session, after which a switch can choose a
model it offers. Both actions must be possible after `session.open` without
restarting the agent host. The original provider-provisioning proposal was
open-only and described attachments as vendor-wire destinations. That is the
wrong layer for `oapx`: its agent loop speaks `model-provider-core` downward.

## Core: `session.model.switch`

`session.model.switch.request` is a session-scoped core command with
`{session_id, model_id, allow_degraded_features?}` in its payload and the
same `session_id` on the envelope. Its correlated response carries
`{session_id, model_id, previous_model_id?}`; `model_id` is the effective new
default. The agent updates `current_model_id` atomically before answering and
emits `session.state.updated` for the changed canonical state. An unchanged
model is a successful idempotent switch, not a new run.

The operation is part of the core vocabulary and core capability set as
`session.model.switch`; it is not gated on `run.model_selection`, which is the
per-submit control. An endpoint that has a fixed model can accept a switch to
that same model and reject other ids with `model_not_found` and
`details.model_id`. An endpoint must not answer success while leaving the
session default unchanged. When a catalog is available, the target must be a
model in that session's effective catalog. A degraded capability requires
explicit opt-in through `allow_degraded_features` under the ordinary gate.

`model_not_found` is truthful only for an id absent from that session's
effective catalog. A fixed-model endpoint must expose only its fixed model in
that catalog; it must not list an unswitchable model and then call it missing.
Refusals for unrelated reasons, such as an evicted session, retain their own
codes even when the requested model is absent from a previously served catalog.
If the endpoint cannot enforce this boundary, it does not conform to core
model switching.

The switch affects run starts after its response, including promotion of
previously queued submissions that specified no `model_id`. A running run
keeps the model it started with. A queued submission with an explicit
`model_id` keeps that override, which is applied at promotion under Decision
0007. If the endpoint's `run.model_selection` mode is `session_mutation`, a
later promotion of such an explicit override may change the default again;
that later mutation wins in session order. Requests pipelined concurrently
with a switch have no implicit order; their admissions and effective models
must make the chosen order observable. A caller that needs a barrier waits for
the switch response before sending its next submit.

`models.response.current_model_id` and `session.state` reflect the accepted
switch. A models response may use `as_of_model_event.switch_request_id` to
anchor its snapshot to the switch, rather than inventing a run position for a
command that created no run. The existing `{run_id, sequence}` anchor remains
for model-affecting run events. The two forms are exclusive.

## Optional: `session.provider.attach`

An endpoint advertises `action.providers.attach` with the `session_live` mode
to accept `session.provider.attach.request`. The request is session-scoped and
has a `provider` object:

```
{ "id": "session-provider-alias", "provider_id": "upstream-provider", "service_id": "operator-service" }
```

`id` becomes the provider id in the agent session's `models.response` catalog;
`provider_id` names a provider in a `model-provider-core` service's
`provider.describe.response`; and optional `service_id` names an
operator-configured OAP service. Omission of `service_id` means the co-hosted
provider profile on this connection, if offered. A co-hosted provider may be
resolved in process without making a second stdio connection to itself. The
attachment does **not** carry a vendor `wire`, URL, command, headers, token,
environment values, or an opaque upstream-request map. Its remote stdio or
future HTTP destination is operator configuration behind `service_id`, not
client-supplied text in the attachment. The service must speak
`model-provider-core`; the agent loop does not call OpenAI or Anthropic wire
formats to satisfy the attachment.

An attachment is admitted atomically for one session or refused without
changing its catalog. Unknown services/providers, alias collisions, and
cross-session registry merges are refusals, never silent fallbacks. Repeating
the same alias and binding is idempotent; reusing an alias for a different
binding is `provider_conflict` with `details.provider_id`. An unsupported
operation is refused with `unsupported_feature` and
`details.feature: "action.providers.attach"`, with the normal degraded
opt-in rule when applicable. On acceptance the response carries the
session-local `provider_id` (`provider.id` in the request).

An accepted attachment invalidates only that session's previously served
model catalog, even if the endpoint-wide `capability_revision` is unchanged.
This is a narrow exception to Decision 0006's within-revision catalog
stability rule: it is announced by a correlated mutation whose effects the
caller requested. The next `models.request` returns the complete effective
session catalog, including attached providers and models; model ids must be
unique within it. A capability change independent of this operation still
uses `capabilities.updated` and a new capability revision. Attach does not
switch the session default and does not alter running inferences or runs.
The caller makes a separate `session.model.switch` request to choose a model
from the newly available provider.

Credential acquisition and renewal are the responsibility of the provider
service and its binding, with the agent's `+auth` obligations reflected at the
agent boundary. Attachment itself is not a credential channel. A provider
that permits anonymous inference may be used without a grant; the protocol
must not infer missing credentials from an attachment alone.

## Compatibility and scope

`session.open.request.providers[]`, proposed in Decision 0017 but never
graduated into the schema bundle, does not become an alternate path. Clients
open a session, attach as needed, then switch. The same operations work after
one or more runs. A connection exposing both profiles does not implicitly
attach the provider to a session.

Other OAP agent implementations may implement their loops in other ways. The
**`oapx` architecture** routes its provider inference through
`model-provider-core`; this decision does not require every agent-control
implementation to stop using a vendor SDK internally.

The provider profile's remote [HTTP binding](../drafts/provider-http.md) has
an `oapx` client implementation for a single operator-configured service at
startup. This decision defines live attachment identity and session behavior;
the HTTP server role and live attachment are not yet implemented.
