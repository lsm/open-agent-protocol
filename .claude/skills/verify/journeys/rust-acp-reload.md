# Rust ACP capability-gated native reload

Follow shared setup and the mkdir mutex in SKILL.md. After the baseline builds
ReleaseSafe `oapx`, run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-reload.py --output-dir "$EVIDENCE/reload"
python3 .claude/skills/verify/scripts/rust-acp-reload-ownership.py --output-dir "$EVIDENCE/reload-ownership"
(cd rust && cargo test -p oxap-adapter-acp --test reload)
```

The public Adapter/Session consumer opens an existing bound ACP conversation
through a synthetic stdio process with an empty environment and a one-entry
inbound queue. The agent sends 320 historical chunks and a reverse request
before its reload result. The adapter must drain them without inventing an OAP
run, sequence, transcript cursor or message, and answer the reverse request
method-not-supported. A fresh submitted turn starts at sequence 1 and reports
only the continued response. Reloaded cumulative turn counts remain in the ACP
extension, outside the new run's core usage.

The Go/Rust differential covers load precedence, resume fallback, malformed or
missing capabilities/binding, missing/null/changed ids, malformed results,
native refusal and opaque settings. Successful public states, catalogs,
admissions and events normalize only clocks and the capability revision. Refusals
compare typed feature/reason; full diagnostic detail is retained separately
because native parser diagnostics differ. All parsed native frames in both
directions must agree. Wrapped successful Rust exchanges must pass all three
validators. Recovery describes conversational attachment, never event replay.

Quiet reload, partial reverse-response cancellation and deadline ownership
checks must retain actual native requests/bytes and PIDs, prove nonblocking
polling, and observe child absence and joined consumer threads before exit.
The public draining operation returns ownership on success and joins its
collector/call workers on success, error and cancellation. It keeps no history
buffer beyond the existing bounded JSON-RPC queue.

Run the existing live-config, session, model-switch, operation, session-bridge,
submit, permission and cancellation journeys, complete baseline and Rust
all-features suite under the same mutex. Inspect the actual baseline PTY
fixture reply and `/quit` exit. Opening configuration/reasoning is covered by `rust-acp-opening-config.md`;
full byte corpus replay remains a later slice of #1439. No live provider/harness, port,
credential, tag/release or C8 served-parity claim is involved.
