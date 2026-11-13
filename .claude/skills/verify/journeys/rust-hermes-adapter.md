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
