# Rust DeepSeek adapter configuration

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-deepseek --test adapter)
(cd rust && cargo test -p oap-adapter-deepseek --all-features)
```

`tests/go_capabilities.json` is Go's `Probe` capabilities for the catalog
revision, marshalled by Go's `encoding/json`. Regenerate it whenever Go's
DeepSeek descriptor changes by printing `json.MarshalIndent(descriptor.Capabilities, "", "  ")`
from a throwaway test in `go/adapter/deepseek`, and require the Rust test to
compare equal to the new file. The other tests pin Go's construction
refusals, the initialize params and reasoning a launch carries, and each
compaction patch, including the token-count refusal.
