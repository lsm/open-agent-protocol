# HTTPS CONNECT transport

The HTTP compatibility client tunnels HTTPS targets through an HTTP proxy.
Proxy credentials are sent on CONNECT, and the tunnel negotiates TLS with the
target hostname and the configured certificate trust bundle. Authentication,
certificate, hostname and plaintext failures close the tunnel. A proxy URL
using the HTTPS scheme is refused because nested TLS is unsupported.

The build generates a compatibility client from the pinned Zig 0.17.0 HTTP
client source, applying the CONNECT implementation in
`zig/src/compat/http_connect.zig`. The generator checks the upstream source
hash before applying it. Updating the compiler requires reviewing that source
and rerunning the authenticated CONNECT journey before updating the hash.
The client retains the standard TLS verifier and connection pool.
