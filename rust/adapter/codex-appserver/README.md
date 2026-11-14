# oxap-adapter-codex-appserver

The Rust port of `go/adapter/codex/appserver`: an OAP adapter over the Codex
app-server's JSON-RPC on stdio, pinned by `harnesses/codex-app-server.json` and
judged by the corpus in `fixtures/adapters/codex-appserver-0.167.2`.

So far it holds the adapter's configuration, its descriptor, and the
`thread/start` config an open carries: `adapter::thread_config` sends the
session's reasoning level as `model_reasoning_effort`, with `off` as Codex's
`none`, and a token compaction limit as `model_auto_compact_token_limit`. It
ignores `auto` and refuses `off` and `share`, which Codex cannot honor at open.

`Adapter::open` starts a Codex thread, or resumes the one the configuration
or a reopen names, and returns a `Session` that reports its state and closes
its client once. A resume keeps the model and effort Codex reports, and a
reopen reports itself recovered. The default factory launches the configured
executable with `open-agent-protocol` as its client name.

`Session::submit` starts a Codex turn for user text and answers with the run
it admitted. It refuses what Go refuses before any native call, and a
`turn/start` with no turn id closes the session. Codex's `turn/started`
becomes `run.started`, and `turn/completed` settles the run by the turn's
status: completed, interrupted as `run.cancelled`, failed with the turn's
error, or anything else as `invalid_native_terminal`. An undecodable start
or completion fails the active run, as an undecodable text delta does.
Each delta streams as `content.delta`, and the completion carries the joined
text. A closed transport fails the active run as inferred, with the client's
error. `Session::resume` replays a run from the bounded journal and reports
a gap for events it no longer holds. A session with an active run refuses to
close.

A run submitted with an `output_schema` completes with a structured result
read from the last `agentMessage` item Codex completes, trimmed, compacted
as Go's `json.Compact` compacts and checked against the schema. A missing,
non-JSON or nonconforming message fails the run as
`structured_output_failed`. An `item/started` or `item/completed` that
cannot be decoded fails the active run as `invalid_native_event`.

Command, file-change and MCP items map to `action.call.requested`, `started`
and a terminal action, with the arguments and result Go builds. An action
still open when the turn settles is cancelled on an interruption and fails
as `native_action_incomplete` otherwise, in the order its items started. A
completion with no matching start fails the run as `invalid_native_action`.

A command or file-change approval for an active action becomes
`action.permission.requested`, offering the decisions Codex lists, and
`Session::resolve` answers Codex with the chosen decision once, refusing
what Go refuses. An approval still pending when the run settles is answered
with `-32800` and resolved as failed, or cancelled on an interruption.

An `item/tool/requestUserInput` request for the active run becomes
`user.input.requested`, with each question's options as single-choice
options, and the run waits for input. `Session::resolve` answers Codex
once with each answer's text or chosen option's label, refusing what Go
refuses, and the run goes back to running. Input still pending when the
run settles is answered with `-32800` and resolved as cancelled.

A session reads what Codex sends on its own thread. It answers each reverse
request it does not handle with `-32601` and the method as data. While
`turn/start` is in flight, the reader sets aside what Codex sends and the
submit replays it once the turn is recorded, so the reader never waits on
the submit and those messages land as Go's do, after the turn is known.
Every other operation, including the reader's handling of a closed
transport, waits for the submit, as Go's does.

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
