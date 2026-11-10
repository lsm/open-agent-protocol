# HTTPS CONNECT transport

The HTTP compatibility client tunnels HTTPS targets through an HTTP proxy.
Proxy credentials are sent on CONNECT, and the tunnel negotiates TLS with the
target hostname and the configured certificate trust bundle. The CONNECT
socket uses the proxy's pool key, independently of origin connections. Proxy
initialization closes idle connections under the pool lock before publishing
the settings, so a cached direct TLS connection cannot bypass the proxy. Authentication,
certificate, hostname and plaintext failures close the tunnel. A proxy URL
using the HTTPS scheme is refused because nested TLS is unsupported.

The build generates a compatibility client from the pinned Zig 0.17.0 HTTP
client source, applying the CONNECT implementation in
`zig/src/compat/http_connect.zig`. The generator checks the upstream source
hash before applying it. Updating the compiler requires reviewing that source
and rerunning the authenticated CONNECT journey before updating the hash.
The client retains the standard TLS verifier and connection pool.

`no_proxy` or `NO_PROXY` supplies comma-separated destination exclusions,
checked before proxy selection. Lowercase takes precedence, including an
empty value. Entries match a hostname or dot-delimited subdomains without
case sensitivity, accept a leading dot and an optional destination port,
and ignore surrounding spaces. `*` bypasses every destination. Matching
requests connect directly with the usual target TLS validation. Exclusions
use destination names without DNS resolution and do not interpret CIDR ranges.

## Environment routing and attempt audit

The shared streaming client, remote provider service requests and one-shot
fetches initialize the standard proxy environment before making requests.
`http_proxy` or `HTTP_PROXY` selects HTTP forwarding; `https_proxy` or
`HTTPS_PROXY` selects the proxy for HTTPS targets. `all_proxy` or `ALL_PROXY`
is the fallback. Lowercase names take precedence. Unsupported proxy schemes
and initialization errors fail the request.

`OAP_ATTEMPT_AUDIT_ID` enables JSONL on stderr for model POST attempts in the
OpenAI/Codex Responses implementation (`openai-responses` and
`openai-codex-responses`).
Use a non-secret correlation label of 1–64 ASCII letters, digits, dots,
underscores or hyphens. Absent, empty or invalid labels disable the sink.
Each line contains only `audit_id`, a 1-based process-wide `attempt` number,
and `outcome`. Numbers are reserved when the model POST begins and remain
unique across concurrent inferences. Records are written as attempts finish,
so completion order can differ from attempt-number order.

The fixed outcomes are `success`, `http_error`, `transport_error` and
`stream_error`. Successful inference completion records success. HTTP failures
and send/receive failures record their respective outcomes; a later stream
failure records stream_error. Retries each receive a record. OAuth refresh,
CONNECT setup and remote OAP transport envelopes do not count as model POSTs.
The records exclude provider error text, credentials and model content.
