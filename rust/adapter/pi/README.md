# oap-adapter-pi

The Rust port of `go/adapter/pi`: an OAP adapter over Pi's RPC mode on stdio,
pinned by `harnesses/pi.json` and judged by the corpus in
`fixtures/adapters/pi-v1.3.0`.

So far it holds the native commands, responses and session state, decoded
strictly as Go's `internal/native` does.
