# Rust Hermes adapter

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oxap-adapter-hermes --test rpc)
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
