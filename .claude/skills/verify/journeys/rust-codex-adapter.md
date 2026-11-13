# Rust Codex app-server adapter configuration

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-codex-appserver --test adapter)
(cd rust && cargo test -p oap-adapter-codex-appserver --test permissions)
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
