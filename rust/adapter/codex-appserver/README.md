# oap-adapter-codex-appserver

The Rust port of `go/adapter/codex/appserver`: an OAP adapter over the Codex
app-server's JSON-RPC on stdio, pinned by `harnesses/codex-app-server.json` and
judged by the corpus in `fixtures/adapters/codex-appserver-0.163.0`.

So far it holds the native types, with the members Go's adapter reads and
writes, decoded as Go's `encoding/json` would, and the process launcher.
`process::Process` runs `<executable> app-server --listen stdio://` and speaks the
shared client in its unversioned dialect. It sends `initialize` with the host's
client info, then the `initialized` notification. Its first request id is
`0`, as Go's is. It reaps the child only after its reader exits, and closes by
releasing stdin, then killing the child at the shutdown timeout.

A forced shutdown kills the app-server, but a descendant that inherited its
stdout keeps the pipe open. Go closes the pipe to release its reader; Rust's
standard library cannot interrupt a blocked pipe read without unsafe code,
which this crate forbids, so the reader thread and the descriptor stay until
the last descendant exits. `close` still returns within the shutdown bound.
