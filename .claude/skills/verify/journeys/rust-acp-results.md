# Rust ACP usage, stop outcomes and message identities

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root after building the ReleaseSafe validator.

```sh
python3 .claude/skills/verify/scripts/rust-acp-results.py --output-dir /tmp/oap-results-<unique>
python3 .claude/skills/verify/scripts/rust-acp-text.py --output-dir /tmp/oap-results-engine-<unique>
(cd rust && cargo test -p oap-adapter-acp)
```

The first script builds standalone public Rust and Go adapter/session consumers.
Each drives the same credential-free synthetic native agent over stdio at queue
capacity one. It retains both native directions, raw public results, admission,
state and actual Rust run events. Every native child must be reaped.

The public differential comparison normalizes wall-clock timestamps and the
declared staged capability revision. Other parsed JSON fields and native host
frames must agree. Original outputs remain intact. Byte-for-byte corpus replay
retains its approved final slice.

Cases cover two-turn usage scope, an initial RPC error, idle and between-turn
context observations, invalid required counts, optional-field fallback, measured
zeros, uint64 maximum, fractional/exponent/negative-zero refusal, case folding,
last duplicate wins, reported cost, multiple native message ids, refusal and
unknown stop reasons. The synthetic agent waits for an explicit public terminal
observation before sending between-turn metadata. The next submit waits for a
native reverse-RPC acknowledgement of those idle observations.

Core run usage is emitted only for the first completed native prompt call of a
fresh native session. A failed write admission consumes that scope and finishes
the unaccepted attempt at sequence zero. Turn counts remain in their ACP
extension on later prompts. Thought
and cache counts keep their native meaning in extensions. The ACP v1.12.0
mapping ledger records their unresolved accounting semantics. Input/output
counts remain lossy and unverified against Decision 0063. Context is replaced
by valid updates and preserved on invalid required fields. A bad cost drops only
that cost, and no price is calculated from tokens.

Each public Rust trace uses its actual returned admission and events with a
synthetic host request prefix. Go, Rust and Zig validators must all accept it.
Multiple native message ids retain separate accumulated text, repeat ids reuse
their OAP ids, and an omitted id continues the current message. The final
response contains the last current message's text.

Focused public engine tests also cover Go floating-point JSON spelling, Unicode
case-fold aliases, malformed optional fields and late answers after a run was
already failed by an observation. A shared opaque UsageScope keeps settled-call
attribution across those engine instances. Its first-call scope advances on
native prompt errors and failed admission too. The public failed-admission
regression follows it with another projected answer and requires core usage to
stay omitted. Normal stdio admission cleanup is exercised separately by the
public submit ownership journey. The normal session tests exercise idle
snapshots and success/error first-turn behavior over real stdio.

Run the remaining ACP journeys, full baseline and Rust all-features suite under
the same mutex. Observe the baseline PTY reply and `/quit` exit. Tool lifecycle
is covered by `rust-acp-tools.md`, normal permissions by
`rust-acp-session-permissions.md`, and native cancellation by rust-acp-cancel.md.
Reopen/live configuration,
full ACP corpus and C8 served parity retain their approved later slices.
Shared process-helper grandchild-held-pipe limitations remain applicable.
