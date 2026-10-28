## Shared Copilot CLI corpus

Use the shared build/tool setup in `SKILL.md`, then run from the root:

```sh
python3 .claude/skills/verify/scripts/copilot-harness-corpus.py --output-dir /tmp/oap-copilot-corpus-<unique-name>
```

Zig's production native decoder and reducer replay every manifest case against
fixed expected OAP: text, queued runs sharing one busy period, accumulated usage,
tool success/denial, session errors, foreign sessions and unknown events. Reasoning
is explicitly observed without a published projection. Malformed JSON-RPC and
malformed event bodies must be rejected by the native codec, with no OAP events.
The script wraps each event sequence with its checked-in admission exchange and
runs the public Go and oapx validators. Require exit 0 and `PASS`; inspect
`zig-replay.txt`, the full trace and both validator outputs. The replay also
refuses a mislabeled outbound response, so its direction is part of the contract.

The pin is CLI 1.0.98 / protocol 3; source and artifact provenance come from the
existing ledger. This is synthetic schema/ledger-based data, not a new live
recording. It starts no Copilot process, uses no token, and performs no model
call. Go's native JSON-RPC decoder now consumes the same twenty-three cases, preserves
unknown event names and exact numeric source bytes, and rejects duplicate keys
and ambiguous message shapes. Inspect `go-native.txt` for its result. The Go
checks also include bounded framing,
RPC dispatch and a synthetic child process under temporary HOME, with cancellation
and shutdown reaping. The Go reducer matches fixed OAP for text, queue, unknown
events, session errors, foreign sessions and usage. Usage additionally pins
measured zero, sparse calls, exact counts above 2^53, uint64 maximum and sum overflow,
native negative zero and missing/partial/wide/non-finite costs. Missing
counts stay unknown; neither reducer infers totals with unknown cache terms.
Go now matches all twenty-one lifecycle projections, including tool success/denial,
orphan/duplicate completions and incomplete tools at run settlement. Its inventory
test requires every manifest case to be replayed or checked by the native codec.
The Go session read loop additionally proves state snapshots, bounded cursor
replay and explicit gaps, process-exit settlement and close/reaping with a
synthetic subprocess. Public session submission now tests an early native event
before its reply, Unicode text across messages/parts, native refusal without a
ghost run, missing binding teardown, queue promotion and the nine-run bound.
Unsupported portable controls and non-text input are refused before admission.
The public Go `New`/`Probe`/`Open` path now starts the synthetic child with
enforced headless/stdio/no-update flags, verifies protocol 3 and the pinned CLI
version, giving each handshake RPC its own request deadline, then produces
validator-accepted events. Mismatched versions and
unsupported open controls are refused; each open owns a separate process.
The Go registry exposes this adapter as `copilot`; its dedicated example is
`examples/oap-serve-go-copilot.json`, because the shared registry must stay usable
by both trees. Unsupported host capabilities
remain unadvertised; the Rust lane follows separately.

Run the registered command-line surface with a token-free synthetic peer:

```sh
python3 .claude/skills/verify/scripts/copilot-hub.py --output-dir /tmp/oap-copilot-hub-<unique-name>
```

Require `PASS` after all four actual hub traces are accepted by Go and oapx.
The script builds the internal Go command, uses a temporary HOME and an explicit
child environment, and exercises multi-message text boundaries, Unicode, tool
lifecycles, exact usage above 2^53 and queued-run promotion through `hub --stdio`.
It retains the actual hub/native wire and validator results, removes the temporary
HOME and checks that every synthetic child exited. It performs no login, reads no
credentials and makes no network or provider call. This proves the registered
Go front door against the pinned synthetic wire; live CLI compatibility remains
unverified.

After registry/example changes, also run `zig build --build-file zig/build.zig test-unit-adapter`
and require exit 0. This includes the shared example inventory check and prevents
a Go-only entry from breaking the Zig front door.
