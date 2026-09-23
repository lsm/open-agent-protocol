# OAP Provider Binding: Inference Envelopes over HTTP and SSE

Status: draft
Profile: `open-agent-protocol.model-provider-core`
Base protocol: `open-agent-protocol` version `0.1`
Decision: [0030](../decisions/0030-remote-provider-http-binding.md)

This binding is for an operator-configured model-provider service. It does not
change the provider profile's envelope schema or turn a provider into an agent.
All envelopes on this binding carry
`profile: "open-agent-protocol.model-provider-core"`.

## Endpoint and framing

The service exposes `POST /oap/v0.1/provider` with exactly one UTF-8 JSON OAP
request envelope in the body. The request uses
`Content-Type: application/json`; an unsupported media type is an HTTP 415.
An endpoint declares a maximum request-body size and refuses excess input with
HTTP 413 before decoding. It never silently truncates a body. HTTP transport
headers and status codes are binding metadata, not OAP payloads.

For a unary operation the response is HTTP 200,
`Content-Type: application/json`, and exactly one OAP response envelope. The
envelope has `in_reply_to` equal to the request `id`. An OAP refusal uses the
profile's `error` envelope or the operation's refused response in HTTP 200,
not an HTTP error with an invented OAP shape. A syntactically valid but
unsupported OAP envelope gets a correlated `error` envelope.

An `inference.create.request` that starts a streaming inference receives HTTP
200 and `Content-Type: text/event-stream`. Each SSE event has one `data:` field
whose value is one complete OAP response or event envelope; an optional SSE
comment heartbeat carries no OAP meaning. The first non-heartbeat SSE event is
the correlated `inference.create.response` or `error` envelope. Subsequent
events for that inference use the profile's `inference_id` and contiguous
per-inference `sequence`. The SSE stream ends only after the inference's one
terminal event has been flushed, or on transport failure. An immediately
refused create may instead return a unary rejected `inference.create.response`
or `error` envelope with HTTP 200.

`inference.cancel.request` and other controls are separate POSTs and receive
unary responses. The service must accept them while a streaming response is
open. Parallel inference streams are separate HTTP requests; no connection-
global event order is implied. An HTTP client must route on OAP `in_reply_to`
and `inference_id`, not an SSE `id`, TCP connection, or arrival order.

## Transport failures

HTTP statuses 400, 413, 415, 429, and 5xx report failures to accept or carry
the OAP exchange. Their bodies are diagnostics, not OAP envelopes. A missing
response envelope, wrong profile, mismatched `in_reply_to`, malformed SSE
data, repeated or skipped inference sequence, or EOF before a terminal is a
transport/protocol failure. None is an inference success. This version has no
event replay or `Last-Event-ID` resume. A client may retry only under its own
idempotency policy; the original inference's execution outcome can be unknown.

An HTTP connection may be reused, but reuse does not create a session. There
is no global initialize or implicit provider selection. Discovery uses
`provider.describe.request` and `provider.models.list.request` on the same
POST endpoint as inference. The service's descriptor is checked before an
agent admits an attachment by operator `service_id`.

## Credentials and deployment

This version has no credential-value channel. The remote service manages its
own credentials and advertises `credential_grant: none`. A credential grant
request is a typed refusal, never an invitation to put a secret into an OAP
envelope or HTTP request body. Connection authentication, if used, is
operator-configured transport metadata and is excluded from OAP traces and
logs. An agent attachment carries only the provider alias, provider identity,
and optional operator service identity defined by Decision 0028.

Loopback HTTP is permitted within one trusted host or Pod. Cross-Pod or remote
traffic requires HTTPS with validated peer identity or an explicitly configured
local proxy that authenticates and encrypts its outbound leg. A cluster-private
address or NetworkPolicy alone is not proof of encryption. The default for an
unprotected non-loopback HTTP address is refusal. An implementation must not
redirect a request to a different origin while carrying transport credentials
or inference data unless the operator explicitly authorized that destination.
