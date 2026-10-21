# Rust snapshot-listing consumer journey

After building ReleaseSafe `oapx`, run the snapshot journey through the public
Rust validator example, internal Go CLI and rebuilt Zig product:

```sh
python3 .claude/skills/verify/scripts/rust-snapshot-listing.py --output-dir /tmp/oap-rust-snapshot-<unique-name>
```

Every ported shared listing fixture must draw its manifest code set and phase
from all three. Each listing mutation must fail semantically at the affected
Rust pointer, while its repaired fixture passes: duplicate/foreign run,
terminal status, order, queue position, active run, missing/empty listing and
session status contradictions. The Decision 0057 pair must reject an omitted
interaction listing when queue delivery is advertised and accept the same
trace after withdrawing that advertisement. The script retains every input
and result and explicitly lists deferred claims still recorded as Rust gaps.
Read-window promotions and settlements in the shared positive fixtures must
remain valid. Advancing an entry to its observed start or terminal position,
or moving the read request past an already-published terminal, must reject
at the corresponding status, capture-position or run-id pointer. The baseline runner includes this journey.
