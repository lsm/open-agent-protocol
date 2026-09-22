# Decision 0029: Authentication Over Agent Control

Status: proposed
Date: 2026-09-22
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `auth` (claim term `+auth`)
Replaces: [Decision 0025](0025-authentication-is-an-obligation-not-a-credential-channel.md), which was proposed but not accepted
Related: [Decision 0017](0017-provider-provisioning.md) and [Decision 0028](0028-live-model-and-provider-control.md)

## Context

The four SDKs expose provider authentication status, a standalone interactive
`login(provider_id)` operation, and one-shot authentication retry for both
direct inference and agent runs. Authentication can be needed before any agent
session or run exists. An in-run `+user-input` interaction, as proposed by
Decision 0025, cannot preserve that operation: it has no run to suspend, and a
direct `model-provider-core` caller may have no agent run at all.

Authentication is distinct from attaching a provider. An attachment names a
service and an upstream provider; it never carries an API key or token. The
provider service owns credential acquisition, refresh, and storage. The
agent-control client may ask that service's co-hosted authentication manager
to begin a human flow, but does not receive a usable token.

## Decision

`+auth` adds ten envelope types to `agent-control-core`:

| Request or event | Response or effect |
| --- | --- |
| `auth.providers.request` | `auth.providers.response` with provider ID, name, status, and optional last error |
| `auth.login.start.request` with `provider_id` | `auth.login.start.response` with a new `flow_id` |
| `auth.login.event` | URL, prompt, or progress for one flow |
| `auth.login.reply.request` with `flow_id`, `prompt_id`, `answer` | `auth.login.reply.response` acknowledging that answer |
| `auth.login.cancel.request` with `flow_id` | `auth.login.cancel.response` acknowledging cancellation |
| `auth.login.completed` | Exactly one terminal with success, failure, or cancellation |

The endpoint advertises `auth.providers` and `auth.login` separately. The
second key includes prompt reply and cancellation, not just starting a flow.
When a key is not advertised, the request receives a correlated
`unsupported_feature` refusal. An implementation never accepts a login start
and then silently ignores a prompt reply or cancel.

The start response is emitted before any event for its flow. Events and the
terminal carry `flow_id` in the payload and a positive, contiguous `sequence`
for that flow. The terminal consumes the final sequence; no event follows it.
The flow is independent of session and run identity, so it can be used for
preflight login and direct inference. A concurrent run may still produce an
ordinary `+user-input` authentication obligation; that path does not replace
or rename these standalone envelopes.

`auth.providers.response` is a fresh status read, not a capability revision.
Its status vocabulary shares `common.authStatus` with provider model entries.
`refreshing` and `login_in_progress` are included so a caller can distinguish
an active transition from a missing credential. A successful login does not
return a token; a subsequent status or model-catalog read observes whether the
provider became usable.

### Credential boundary

An API key, access token, refresh token, or provider credential reference is
never a field in this unit. A manual OAuth authorization code or domain answer
may be needed to finish a browser flow. `auth.login.reply.request.answer` is
the one deliberately sensitive value: it is accepted only over a trusted
locally spawned stdio binding, is passed to the authentication manager once,
and is neither logged nor retained in a trace. Trace collectors must redact
that member before persistence or export; implementations must not echo it in
responses, events, errors, or diagnostics. An endpoint that cannot provide
those safeguards must not advertise prompt-capable `auth.login`.

This does not authorize credential-bearing provider attachments or general
secret passage in agent messages. Remote HTTP provider transport and remote
auth answer transport require their own security binding; neither is defined
by this decision.

### SDK behavior

An SDK's `listProviders()` maps to the providers pair. `login()` starts a flow,
delivers URL/prompt/progress callbacks, answers prompts, and waits for the
terminal. Auto-once retry may invoke that same flow after a typed
authentication-required failure, then retry the original call once. It must
not retry on an arbitrary provider error or continue after cancelled login.
No SDK silently falls back to the Makai v1 wire when `+auth` is unavailable.

The JSON Schema payloads live in `schema/v0.1/auth.schema.json`. `+auth` is
separate from `model-provider-core`'s caller-held credential grant: the grant
is for a caller that already holds a credential; login is for obtaining one
through the endpoint's authentication manager.
