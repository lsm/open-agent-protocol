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

`Process` exchanges frames with a caller-configured `std::process::Command`.
The caller controls its environment, directory and stderr. Its Unix stdout
reader has a 64-frame queue and a cancellation descriptor. `close` and Drop
kill and reap the endpoint and join its reader, including when another process
holds stdout open or the host has stopped draining the queue. `next` has a soft
receive timeout. After stdout EOF, `wait_exit` kills an endpoint that outlives
its exit budget. Call `close_input` to send stdin EOF first.

`Client` routes replies by envelope ID, events in arrival order, and control
answers by transport ID. Each wait shares one deadline across unrelated frames
and checks buffered answers before expiring. A zero deadline selects five
minutes. Response, event and EOF-drain timeouts close the endpoint and retain a
`closed_by_runner` reason. EOF draining shares one budget so a chatty endpoint
cannot postpone termination. A control wait uses the smaller of the line
deadline and ten seconds, and its timeout leaves the endpoint usable.

The client records sent requests and received envelopes for `transcript()`.
`probe` omits the request and its answers. `close_input` followed by `wait`
drains stdout and observes the exit status, with an exit grace capped at thirty
seconds. Conformance checks follow in subsequent slices.

The public consumer journey is
`.claude/skills/verify/journeys/rust-conformance-wire.md`.
Process lifecycle coverage is in
`.claude/skills/verify/journeys/rust-conformance-process.md`.
Correlated-wait coverage is in
`.claude/skills/verify/journeys/rust-conformance-client.md`.
