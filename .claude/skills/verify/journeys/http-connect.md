# Authenticated HTTPS CONNECT

Run the synthetic transport journey on the designated test host.

```sh
zig build --build-file zig/build.zig build-http-connect-probe -Doptimize=ReleaseSafe
python3 .claude/skills/verify/scripts/http-connect.py --binary zig/zig-out/bin/http-connect-probe --output-dir /tmp/oap-http-connect-evidence
```

Require 8/8 checks and exit 0. The probe uses the product HTTP compatibility
client, an isolated environment, temporary certificates and a loopback proxy.
The valid origin receives the expected body request through TLS. Proxy
credentials appear only on CONNECT. Bad proxy authentication, proxy denial,
an untrusted certificate, a trusted certificate for another hostname and a
plaintext origin must fail. Origin connection counts expose direct fallback.
The direct TLS control proves the fixture trust and hostname configuration.
A cached plaintext origin connection must not be reused for CONNECT.
