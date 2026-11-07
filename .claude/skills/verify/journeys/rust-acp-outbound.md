# Owned Rust ACP outbound operations

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
python3 .claude/skills/verify/scripts/rust-acp-outbound.py --output-dir /tmp/oap-outbound-<unique>
(cd rust && cargo test -p oap-adapter-acp --test outbound)
```

The public PromptBridge consumer uses owned notify, result-response and
error-response operations over real stdio. The native child receives an empty
environment. The verifier leaves the consumer alive after cleanup and requires
native PID absence and an exact return to the initial OS thread count.

Successful cases inspect actual notification/reply frames and repeated-response
refusal. Invalid and expired notifications must send no frame. Blocked-write
cases use object-shaped params and valid four-MiB raw payloads. Before asking the
consumer to cancel, the agent must record 64 actual outbound bytes. An empty
read fails this boundary. Future polling must be pending and nonblocking. Drop,
bridge close and final bridge drop must kill/reap a quiet child and join owned
workers. A notification deadline is also exercised, followed by owner cleanup.

The queue case holds one actual write blocked, starts two notifications against
write capacity one, and requires one queue-full result and one pending operation.
It then cancels the pending work and observes cleanup. The bridge uses the
shared JSON-RPC bounded FIFO writer. It adds no outbound payload queue. Focused
public tests also refuse a reverse request from another bridge while allowing
the actual native request with the same wire id to be answered normally.

Keep the signed failing API-consumer test before the implementation. Its red
result is a missing-method compile failure because this layer adds the owned
API. The native quiet-child proof runs against the implemented API. The script
retains native frames, partial-write evidence, public operation results,
consumer stderr, timings and OS thread inventories.

This layer exposes native operation ownership. Normal Session permissions are
covered by rust-acp-session-permissions.md; native cancellation is covered by rust-acp-cancel.md. Follow the existing ACP session, submit,
results, tools, model and bridge journeys too, then run the full baseline and
Rust all-features under the same mutex. Read the real PTY reply and `/quit`
exit. Shared process-helper grandchild-held-pipe limitations remain applicable.
