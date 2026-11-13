# Rust Codex app-server adapter configuration

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-codex-appserver --test adapter)
(cd rust && cargo test -p oap-adapter-codex-appserver --test permissions)
(cd rust && cargo test -p oap-adapter-codex-appserver --test filepath)
(cd rust && cargo test -p oap-adapter-codex-appserver --test open)
(cd rust && cargo test -p oap-adapter-codex-appserver --test submit)
(cd rust && cargo test -p oap-adapter-codex-appserver --test dispatch)
(cd rust && cargo test -p oap-adapter-codex-appserver --test events)
(cd rust && cargo test -p oap-adapter-codex-appserver --test structured)
(cd rust && cargo test -p oap-adapter-codex-appserver --test items)
(cd rust && cargo test -p oap-adapter-codex-appserver --test approvals)
(cd rust && cargo test -p oap-adapter-codex-appserver --all-features)
```

`tests/go_capabilities.json` is Go's `Probe` capabilities for the catalog
revision, marshalled by Go's `encoding/json`. Regenerate it whenever Go's
Codex descriptor changes by printing `json.MarshalIndent(descriptor.Capabilities, "", "  ")`
from a throwaway test in `go/adapter/codex/appserver`, and require the Rust
test to compare equal to the new file. The other tests pin Go's construction
refusal, the default journal capacity, the `thread/start` config an open
carries, and the mapping from a resumed effort back to an OAP level. To see
them bite, send `off` to Codex as `off`: the thread-config test must fail.
Accept `auto` as a refusal: the same test must fail. Read Codex's `off` as a
level: the effort-mapping test must fail.

`tests/permissions.rs` pins the check a resume runs on the thread Codex
answered with. Every case and message there was printed by Go's
`confirmsHostPermissions`, including the `null` Go shows for an explicit null
and the empty text for an absent member. To see it bite, stop folding `..` in
a working directory: the confirmed-resume test must fail. Compare the whole
sandbox instead of its `type`: the same test must fail. Show an explicit null
as empty: the sandbox and approval-policy tests must fail.
The sandbox's `type` is read as Go reads a struct member: any ASCII casing of
the key matches, the last match wins, a null leaves it unchanged, and other
members are skipped unconverted. Match the key exactly, or keep the first
match: the member test must fail.

The working directories are compared as Go compares them, as the strings
`filepath.Clean` returns, so a drive letter keeps its case. `filepath::clean`
is a port of Go's lexical cleaning for both platforms, and `tests/go_clean.json`
holds each path with Go's Unix and Windows results. Regenerate it on a Linux
host by printing `filepath.Clean` and, for Windows, Go's own `Clean`,
`volumeNameLen` and their helpers from `internal/filepathlite`, copied into a
throwaway package with `Separator` set to a backslash. To see it bite, match
the device prefix's `UNC` case-sensitively: the lower-case `unc` case must
fail. Skip `postClean`: the two cases that guard against forming a volume
must fail. Drop the drive-letter volume: six drive cases must fail.

`tests/open.rs` opens sessions against a scripted client and compares the
`thread/start` and `thread/resume` params byte for byte, so an empty config
must be absent from the wire, as Go's `omitempty` leaves it. It pins the
refusals for a missing participant, an unbound reopen, a reopen Codex cannot
load and a resume under other permissions, and checks the client is closed
after every failed open and once on close. A shell runtime checks the default
factory sends the host's client info. To see it bite, skip the close on a
failed open: four tests must fail. Rename the client: the factory test must
fail. Keep the configured model or level on a resume: the resume test must
fail. Refuse a reopen only on a closed transport: the unloadable-reopen test
must fail.

`tests/submit.rs` submits against the same scripted client, which lives in
`tests/support`. It compares the `turn/start` params byte for byte and checks
the ids are minted in Go's order (a missing message id, the run, the run's
message, then the submission), the response, the queued state, each refusal
Go makes before any native call, and what a refused or turn-less `turn/start`
leaves. To see it bite, read a null content as missing: the null-content test
must fail. Ignore a failed transport after a refused `turn/start`, drop the
active-run check, or leave the session queued after a refusal: one test each
must fail. Let close skip the active-run check: the close-refusal test must fail.

`tests/dispatch.rs` checks the session's own reader thread. It answers a
reverse request Go does not handle with `-32601`, `unsupported Codex server
request` and the method as data. Its fake holds each reply behind a
barrier once the reader is running, as the real client does, so a submit
returns only if the reader acknowledges without waiting for the operation
lock, including when Codex asks something ahead of its answer. A transport
that closes while `turn/start` is held at a gate must still fail the run
the submit admits, as Go printed from its fake. A client that panics in
`turn/start` closes the session, and `close` still returns. To see it bite,
answer with another code or data member, take the operation lock before
acknowledging a barrier, take it to answer a reverse request, let the
closed-transport path run during `turn/start`, or forget the start guard
before the call: one test each must fail.

`tests/events.rs` drives a run's notifications through the same client and
checks each trace against the shared run validator, with the submit the run
was admitted with. It pins Go's ids, sequences, timestamps and payloads for
a start and a completion, and the terminal each native status settles to.
An interruption no cancel asked for settles as `run.cancelled`, as Go's
does; that trace has no cancel evidence, so it is checked by type and
payload alone. A runtime with one counter for ids and the clock shows
Go's order: the payload's clock, the event id, then the stamp. To see it
bite, read the clock before minting the id: the order test must fail.
Accept another thread's turn: the ignored-notifications test must fail.
Treat an empty turn error as a message: the terminal-status test must fail.
What Codex sends ahead of the `turn/start` answer must produce the same
events and answers as when it comes after it. Handle such a message at once,
or skip its replay: the ahead-of-answer test must fail.

The same file pins each agent message delta as `content.delta`, the joined
text in the completion, and a closed transport or inbound queue failing the
active run as inferred with the client's error. A resume replays the run's
journal and reports a gap once a small journal has dropped the events it
asks for. To see it bite, return without failing when the inbound queue
closes: the transport test must fail. Skip the gap check: the gap test
must fail.

`tests/structured.rs` pins the result a run submitted with an
`output_schema` completes with: the last `agentMessage` item Codex completes
after the turn started, trimmed as Go's `strings.TrimSpace` trims and
compacted as `json.Compact` compacts, escapes and number spellings kept.
Each refusal is `structured_output_failed` with Go's message. Go printed the
compaction of every input there. To see it bite, skip the trim: the result
test must fail. Keep the first final message: the same test must fail. Read
an `item/started` message or another item kind: the started-turn test must
fail.

`tests/go_items.json` holds item scenarios with the envelopes Go's own
session emitted for each, and `tests/items.rs` replays them and compares
the bytes as Go marshals them. Regenerate it by running a throwaway test in
`go/adapter/codex/appserver` that opens the package's fake client with
`openFake`, submits `fakeSubmit`, sends each scenario's notifications, and
writes the drained envelopes with `json.MarshalIndent`. To see it bite,
drop the duplicate-start check, the pending-action sort, or the `failed`
status check: the replay test must fail.

`tests/go_approvals.json` is the same kind of record for approvals: each
scenario's steps (notifications, approval requests, waits and resolutions)
with Go's events, its answer to every request and the outcome of every
resolution, and `tests/approvals.rs` replays them. Its generator also
drives requests through the package's `request` helper and `Resolve`, and
fills an empty `interaction_id` from the latest request event. To see it
bite, accept a decision Codex did not offer, skip the responder check, or
settle pending actions before pending approvals: the replay test must fail.
