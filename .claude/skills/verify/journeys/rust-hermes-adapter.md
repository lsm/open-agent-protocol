# Rust Hermes adapter

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oxap-adapter-hermes --test rpc)
(cd rust && cargo test -p oxap-adapter-hermes --test notifications)
(cd rust && cargo test -p oxap-adapter-hermes --all-features)
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
