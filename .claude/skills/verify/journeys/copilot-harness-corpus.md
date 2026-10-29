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
The driver then runs the Rust Copilot native consumer against the manifest;
require exit 0 in `rust-native.txt`. This covers shared wire decoding. Rust
session projection and host integration remain separate work.

The pin is CLI 1.0.98 / protocol 3; source and artifact provenance come from the
existing ledger. This is synthetic schema/ledger-based data, not a new live
recording. It starts no Copilot process, uses no token, and performs no model
call. Go's native JSON-RPC decoder now consumes the same twenty-three cases, preserves
unknown event names and exact numeric source bytes, and rejects duplicate keys
and ambiguous message shapes. Inspect `go-native.txt` for its result and the
bounded Content-Length codec checks: fragmented/coalesced frames, UTF-8 byte
lengths, header/body limits, malformed or truncated input, and short writes.
The same script also checks RPC correlation across concurrent calls, preserved
notifications, refusal of reverse requests, delivered replies surviving EOF or
cancellation, and closure on malformed input or queue overflow. Call cancellation
unblocks an owned stalled pipe. These run with race detection and appear in
`go-native.txt`. Process ownership is exercised with an actual synthetic child
under temporary HOME and an explicit environment: an RPC reads that HOME, a
stalled call must return DeadlineExceeded, and close/cancellation must reap the
child. The script disables Go's test cache so this process proof really runs.
No real Copilot binary or authentication is used. The Go reducer additionally
matches fixed OAP for text, queue, unknown events, session errors and foreign
sessions and usage. The queued case also injects delayed delta/final text for the
completed interaction after its successor starts; neither may enter the next run.
Usage additionally pins measured zero, sparse calls, exact
counts above 2^53, uint64 maximum and sum overflow, native negative zero and
missing/partial/wide/non-finite costs. Missing counts stay unknown; neither
reducer infers totals with unknown cache terms. Go now matches all twenty-one
lifecycle projections, including tool success/denial, orphan/duplicate completions
and incomplete tools at run settlement. Its inventory test requires every
manifest case to be replayed or checked by the native codec. Public adapter and
session wiring follow separately. Unsupported host capabilities remain
unadvertised; the Rust lane follows separately.

