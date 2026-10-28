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
and ambiguous message shapes. Inspect `go-native.txt` for its result. This does
not yet claim a runnable Go adapter. The Go checks now include bounded framing,
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
synthetic subprocess. Public submission and adapter/registry wiring follow separately. Unsupported host capabilities
remain unadvertised; the Rust lane follows separately.
