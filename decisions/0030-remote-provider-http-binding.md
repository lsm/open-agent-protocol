# Decision 0030: Remote Model Providers over HTTP

Status: proposed
Date: 2026-09-23
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.model-provider-core`

## Context

The provider stdio binding permits a spawned, local provider service. An agent
layer may also need a provider in another process, Pod, or machine. The OAP
envelopes already describe direct inference and model discovery independently
of transport, but they do not specify how an HTTP request relates to a
correlated answer and a stream of inference events. Decision 0028 reserves an
operator-configured `service_id` for this deployment without letting a client
send an arbitrary URL or credential in an attachment.

## Decision

[The HTTP provider binding](../drafts/provider-http.md) carries unchanged
`model-provider-core` envelopes. Each request is one HTTP POST. A unary
operation gets one JSON response envelope. An inference that emits events gets
one SSE response containing its correlated response and its inference events.
There is no additional OAP envelope type, global subscription, or mandatory
connection handshake. Concurrent inferences use concurrent HTTP requests and
keep independent inference IDs and event sequences.

The first version supports provider-managed credentials. It does not expose a
remote credential grant or manual authentication-input channel. A remote
provider advertises `credential_grant: none` and refuses
`provider.credential.grant.request` with a typed OAP error. It may resolve
credentials that its operator supplied within its own trust boundary. Neither
an agent attachment nor a transport error may carry a key, token, authorization
code, credential reference, arbitrary header map, or vendor endpoint URL.

The agent names a remote service through an operator-configured `service_id`.
The registry binds that ID to a base URL and transport security configuration.
An attachment uses the service and provider identities from Decision 0028; it
cannot override the registry's destination or security policy. An agent must
verify the service's OAP profile and provider descriptor before admitting the
attachment. Unavailable services and missing providers are explicit refusals,
not fallbacks to an in-process provider or vendor API.

## Connection security

The operator must choose one of these authenticated or local trust paths:

- A loopback HTTP address, used only within one host or Kubernetes Pod trust
  boundary.
- An HTTPS endpoint with validated server identity, optionally with mTLS.
- HTTP to a local, operator-managed proxy whose outbound leg is authenticated
  and encrypted, such as a service-mesh sidecar enforcing mTLS. This is an
  explicit operator assertion, not a property inferred from a Kubernetes
  Service DNS name or NetworkPolicy.

Plain HTTP directly to another Pod or remote host is not an accepted default.
Even without caller-held credentials, inference bodies and model output are
sensitive. TLS termination may be outside `oapx`; the binding does not require
the application itself to terminate TLS when an authenticated proxy does it.
Transport authentication is configured out of band and never serialized into
OAP envelopes or traces.

## Failure and lifetime

The SSE response is the lifetime of a streaming inference's observations.
Events are not replayable in this version. A broken stream before a terminal
event is a transport failure and an unknown inference outcome, never success.
The client may send a separately correlated cancellation request when it can
still reach the service, but cancellation intent is not settlement. A failed
cancel request does not justify inventing an `inference.failed` event. Retrying
an unsettled inference is a caller decision because the first request may have
executed. The binding sets finite framing and idle limits and keeps HTTP
status failures distinct from OAP `error` envelopes.

## Consequences

This is a new transport for the lower provider boundary, not a merger of the
agent and provider vocabularies. A combined stdio process from Decision 0027
still works. The same agent may attach a co-hosted or remote OAP provider; the
service registry and session catalog decide which models are available.
Remote caller-held credential grants and remote manual authentication input
remain separate security work.
