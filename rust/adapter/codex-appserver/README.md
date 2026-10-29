# oap-adapter-codex-appserver

The Rust port of `go/adapter/codex/appserver`: an OAP adapter over the Codex
app-server's JSON-RPC on stdio, pinned by `harnesses/codex-app-server.json` and
judged by the corpus in `fixtures/adapters/codex-appserver-0.163.0`.

So far it holds the native types, with the members Go's adapter reads and
writes, decoded as Go's `encoding/json` would.
