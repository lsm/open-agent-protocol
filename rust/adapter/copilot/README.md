# oap-adapter-copilot

The Rust port of `go/adapter/copilot`: an OAP adapter over the GitHub Copilot
CLI's headless JSON-RPC, pinned by `harnesses/copilot-cli.json` and judged by
the corpus in `fixtures/adapters/copilot-cli-v1.0.98`.

So far it holds what Go has merged: `native::parse` and `decode_event`, which
read a frame and a `session.event` notification as Go's `native` package does,
and `rpc`'s Content-Length codec. The session, process and adapter follow as
Go's land.
