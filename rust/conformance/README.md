# oap-conformance

The Rust port of `go/internal/conformance`, built in small steps under
Decision 0052 and #1438.

The first slice exposes `read_frame` and `write_frame` for newline-delimited
endpoint envelopes and replay control frames. Envelope decoding uses
`oap-protocol`; control frames keep an explicit `after: 0` and unsigned
64-bit cursors. Blank lines are skipped and a final unterminated frame is read.

`Transcript` excludes answers to registered probe IDs, deduplicates envelope
IDs, and places run events after the response admitting their run. Admission
can come from a submit response or an open response carrying an active run.
Unadmitted events remain at the tail in their first-observed run order, so
later validation can report them.

The process client, bounded waits and conformance checks follow in subsequent
slices. This crate currently provides framing and transcript assembly.

The public consumer journey is
`.claude/skills/verify/journeys/rust-conformance-wire.md`.
