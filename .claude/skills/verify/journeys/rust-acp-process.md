# Rust ACP process and initialization

Use synthetic agents only. From the repository root, run the standalone public
consumer and retain its actual stdio wire, output and cleanup observations:

```sh
python3 .claude/skills/verify/scripts/rust-acp-process.py --output-dir /tmp/oap-rust-acp-process-<unique>
(cd rust && cargo test -p oap-adapter-acp)
(cd rust && cargo test -p oap-adapter --features testkit --test testkit --test jsonrpc_client --test jsonrpc_backpressure)
```

The public `process::Process` example starts a credential-free Python ACP agent
with an empty environment. It sends exactly one version-1 `initialize` request,
empty client capabilities, optional client info and Go-compatible escaped JSON.
It preserves returned capabilities, auth methods and agent info. Unsupported
versions, absent/null agent capabilities, a quiet agent and early exit fail the
handshake. Stderr secrets are redacted. Closing releases stdin and preserves
normal exit status, or kills and reaps an uncooperative child within its timeout.
Every synthetic PID must be gone before the script reports success. Drop also
closes the process, so callers retain no running child on early return.

The shared client's capacity-one inbound queue must preserve notification order
when its consumer resumes, and close with `notification queue is full` when the
consumer remains stalled. The tests wait for the reader to exit after cleanup.
This verifies C0's existing testkit/backpressure while adding C1's first slice.
Session open, prompt/event mapping, gates, reopen and byte-for-byte ACP corpus
replay remain later slices of #1439. No live harness/provider is exercised.

Also run the complete baseline in SKILL.md and the Rust all-features suite,
format, clippy and comment checks before opening the PR. Inspect the baseline's
retained PTY reply and quit evidence.
