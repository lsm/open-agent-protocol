# Cancellable Rust ACP process operations

From the repository root, run the public consumer over real synthetic stdio:

```sh
python3 .claude/skills/verify/scripts/rust-acp-operation.py --output-dir /tmp/oap-acp-operation-<unique>
(cd rust && cargo test -p oap-adapter-acp --test operation)
```

The consumer starts an empty-environment synthetic Python agent through
`Process::start_async`, and later owns a process through `call_async` for one
opening RPC. A quiet agent must leave `Future::poll` pending without blocking.
The verifier waits until the agent records the actual native request, tells
the consumer to drop the pending operation, and observes the child PID absent
when cancellation returns. The operation owns and joins its worker on both
completion and drop. A cancellation racing startup is repeated twenty times.
Successful startup and call must preserve process ownership through both future
completions, and close cleanly after the returned native result. A real quiet
startup deadline must abort and reap its child. Captured wire and consumer output
are retained for every scenario.

Dropping an operation cancels ownership of the native process, including an
opening call that has not returned its process yet. `call_async` is an owned
opening-operation bridge, and closes the process on an RPC error. These futures begin work on construction. Polling is
nonblocking, while drop joins cleanup before returning. The shared process
helper's documented grandchild-held pipe limitations still apply.

The adapter descriptor still reports session capabilities unavailable. The next
slice connects session/new, idle state, close and the model catalog through this
bridge. No live native harness, provider, credentials or network are used.
Run the complete SKILL.md baseline, Rust all-features tests and guardrails too,
and inspect the retained credential-isolated PTY reply and quit proof.
