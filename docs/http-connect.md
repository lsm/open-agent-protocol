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
