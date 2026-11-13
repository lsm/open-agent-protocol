# Rust Hermes adapter

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-hermes --test adapter)
(cd rust && cargo test -p oap-adapter-hermes --test rpc)
(cd rust && cargo test -p oap-adapter-hermes --test methods)
(cd rust && cargo test -p oap-adapter-hermes --test notifications)
(cd rust && cargo test -p oap-adapter-hermes --test process)
(cd rust && cargo test -p oap-adapter-hermes --test client)
(cd rust && cargo test -p oap-adapter-hermes --test open)
(cd rust && cargo test -p oap-adapter-hermes --test reopen)
(cd rust && cargo test -p oap-adapter-hermes --test reduce)
(cd rust && cargo test -p oap-adapter-hermes --test submit)
(cd rust && cargo test -p oap-adapter-hermes --test settle)
(cd rust && cargo test -p oap-adapter-hermes --test tools)
(cd rust && cargo test -p oap-adapter-hermes --test gates)
(cd rust && cargo test -p oap-adapter-hermes --test resolve)
(cd rust && cargo test -p oap-adapter-hermes --test controls)
(cd rust && cargo test -p oap-adapter-hermes --test native_list)
(cd rust && cargo test -p oap-adapter-hermes --test kept_list)
(cd rust && cargo test -p oap-adapter-hermes --test corpus)
(cd rust && cargo test -p oap-adapter-hermes --all-features)
```

`tests/rpc.rs` spells the text Go's `go/adapter/hermes/internal/rpc` prints
for each frame it parses, refuses, marshals or frames. Regenerate an
expectation by printing `ParseMessage`, `json.Marshal` and `Decoder.Decode`
from a throwaway test in that package. To see it bite, drop
`params_object_or_null` from `rpc::DIALECT`: the null-params and refusal tests
must fail. Drop `nonempty_string_ids`: the empty-id and marshal tests must
fail. Stop quoting a number id that is not an int64: the refusal test must
fail on `1.5`.

`tests/notifications.rs` is a table of frames Go's `DecodeNotification`
decoded or refused, with the text Go printed; three rows whose Go text names a
struct field or a trailing value are checked by prefix. Regenerate it by
printing `DecodeNotification` from a throwaway test in
`go/adapter/hermes/internal/native`. To see it bite, drop any one rule in
`native/events.rs`: the skin, epoch, sequencing, session-less ready, status,
delta, cancel, error and task checks, the decode of any typed payload, an
observed event or a run-scoped one. One test must fail for each.

`tests/process.rs` drives shell gateways through the handshake and teardown,
with each refusal's text printed from Go's `Start` over the same scripts. A
gateway that ignores stdin EOF traps the interrupt and exits 7, so the test
can tell an interrupt from a kill on any `/bin/sh`. To see it bite, drop the
interrupt, the kill, the stdin release, the cached close, the exit status,
the barrier, request or event-type refusal, the deadline or cancel arm, the
stderr in the refusal, abort's stderr grace, the notification check or
strict response ids: one test must fail for each.

`tests/go_capabilities.json` is Go's `Probe` capabilities for the catalog
revision, marshalled by Go's `encoding/json`. Regenerate it whenever Go's
Hermes descriptor changes by printing `json.MarshalIndent(descriptor.Capabilities, "", "  ")`
from a throwaway test in `go/adapter/hermes`, and require `tests/adapter.rs`
to compare equal to the new file.

`tests/methods.rs` holds what Go's `json.Marshal` prints for each method's
params and what its `json.Unmarshal` reads from each result. To see it bite,
drop an `omitempty`-matching `skip_serializing_if` from `SessionCreateParams`,
or deny unknown members on a result: one test must fail for each.

`tests/client.rs` drives shell gateways through `session.create`, with each
outcome printed from Go's process factory over the same scripts. To see it
bite, drop either id check, the model or the effort from the params, or report
the transport's error before the exit status: one test must fail for each.

`tests/open.rs` opens sessions over scripted factories, with every state and
refusal printed from Go's `Open` over the same fakes. To see it bite, send
Hermes's reasoning level as OAP spells it, keep the client open after a
refused level, stop trimming the stored id, ignore the reloaded model, close
the client twice, or answer `state` once closed: one test must fail for each.

`tests/reopen.rs` drives shell gateways through `session.resume`, with each
refusal printed from Go's process factory over the same scripts. To see it
bite, drop the not-found code, the null auto-continue exemption, the status
or running check, the empty-id check, or the stored id on the client: one test
must fail for each.

`tests/reduce.rs` drives the reducer over a fake gateway, with each outcome
printed from Go's reducer over the same messages. To see it bite, drop the
session-less, foreign-session, sequence or run-scoped check, or the non-string
id check: one test must fail for each.

`tests/submit.rs` compares every admission and envelope byte for byte with
Go's `Submit` over the same fake gateway, using Go's test ids and clock. To
see it bite, start the run without `message.start` or without the reply, drop
the transcript cursor, the transport-death deferral or the abort on a refused
reply, join text parts other than by a newline, or let a second run in:
one test must fail or hang for each.

`tests/settle.rs` compares the envelopes for every settlement status and
both delta kinds byte for byte with Go's reducer over the same frames. To
see it bite, drop the empty-delta skip, the reasoning part, the child-mirror
check, the negative-count refusal, the error-surface code, the
error-before-text order, the `turn <status>` fallback, the total-token count
or the reasoning delta: one test must fail for each.

`tests/tools.rs` compares every tool lifecycle Go distinguishes byte for
byte with Go's reducer over the same frames. To see it bite, drop the
duplicate check, the argument re-encoding, the `null` default for absent
arguments or result, the reply links, the open-tool filter on completion or
settlement, the settlement of open tools on completion or failure, the tool
call id on the envelope, or the requesting endpoint: one test must fail for
each.

`tests/gates.rs` compares every gate Go surfaces, expires or cancels byte
for byte with Go's reducer over the same requests, and checks no gate is
answered. To see it bite, drop the empty-choice check, the description
fallback, a question kind, the request-id or resolved filter on expiry, the
running update, the cancellation on settlement, the turn id, the reply link,
the pending input id, the participant or its default, `allow_cancel`, the
waiting update, or the replay of a buffered gate: one test must fail for
each.

`tests/resolve.rs` compares every resolution outcome, the answer Hermes
receives and the envelopes byte for byte with Go's `Resolve` over the same
gates, including a parked settlement, a withdrawn gate, a failed write and
two nested resolutions. To see it bite, drop the resolved filter, any of
the five ownership checks, the answer validation, the multi-choice encoding,
the deferral or the settling flag, the withdrawal, its refusal or its
cancelled status, the flush after a failed write, the submitted answers, or
the running update: one test must fail for each.

`tests/controls.rs` compares cancel, settings and resume with Go's
`Cancel`, `UpdateSettings` and `Resume` over the same gateway: the
interrupt and its failure, the interrupted settlement, a cancel or resume
before the turn opens, a level change and its refusal, a live, ended and
caught-up replay, a cursor past the run, a journal gap, and a closed or
dead session. To see it bite, drop the started or run-id check on cancel,
the interrupt failure, the live-settings refusal, the closed or unusable
check, the session check, the active-run check, the empty-level guard, the
timestamp, the closed check or started filter on resume, the cursor
arithmetic or comparison, or the gap refusal: one test must fail for each.

`tests/native_list.rs` compares every page a factory's `session.list`
answers with Go's `NativeList`: the limit, rows that are not objects or
name no id, a title falling back to the preview, duplicate and
case-folded keys, a page that is not a list, a failed listing, a factory
that cannot list, and the start time read as seconds, saturated or
refused. To see it bite, drop the limit, the id check, the preview
fallback, the long-s or case fold, the last-key rule on the page or on a
row, the integer path or its saturation, the float bound or its
arithmetic: one test must fail for each.

`tests/kept_list.rs` drives scripted gateways through Go's listing rules:
one gateway serves every listing and drains what it sends, an error it
answers keeps it, a broken one is replaced once only when it was reused, a
busy one leaves the listing a gateway of its own, a gateway that cannot
start fails the listing, and closing the adapter stops the kept one. To see
it bite, drop the drain, the busy branch, the remote-error check, the
reuse check, the retry's keeping, the kept gateway, the adapter's close or
the limit: one test must fail for each.

`tests/corpus.rs` replays every in-process case of the catalog's current
Hermes corpus the way Go's `TestHermesEvidenceCorpus` does, against the same
ids and clock, and each trace matches Go's `expected-oap.json` byte for
byte. It also checks the manifest and case pins, decodes every frame through
the production codec, checks each frame's classification, the prompts and
answers the adapter writes, each resume, and that no corpus file goes
unlisted. The three process cases and the two reopen cases are named and
not yet replayed. To see it bite, change one byte of any replayed case's
`expected-oap.json`: the test must fail on that case.
