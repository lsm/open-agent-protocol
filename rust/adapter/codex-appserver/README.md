# oap-adapter-codex-appserver

The Rust port of `go/adapter/codex/appserver`: an OAP adapter over the Codex
app-server's JSON-RPC on stdio, pinned by `harnesses/codex-app-server.json` and
judged by the corpus in `fixtures/adapters/codex-appserver-0.167.2`.

So far it holds the adapter's configuration, its descriptor, and the
`thread/start` config an open carries: `adapter::thread_config` sends the
session's reasoning level as `model_reasoning_effort`, with `off` as Codex's
`none`, and a token compaction limit as `model_auto_compact_token_limit`. It
ignores `auto` and refuses `off` and `share`, which Codex cannot honor at open.

`permissions::confirm` is the check a resume runs on Codex's answer: the
thread must keep the configured sandbox, by its policy `type`, the configured
approval policy, and the configured working directory. The directories are
compared as the strings `filepath::clean` returns, a port of Go's
`filepath.Clean` for Unix and Windows, so a drive letter keeps its case.

It also holds the native types, with the members Go's adapter reads and
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
