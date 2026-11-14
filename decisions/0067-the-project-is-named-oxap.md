# Decision 0067: The Project Is Named OXAP

Status: accepted 2026-11-03; implemented for `v0.1.0-alpha.33` except where
Implementation below says otherwise
Date: 2026-11-03
Protocol: `open-agent-protocol` version `0.1`
Profile: all existing profiles
Amends: project naming; no protocol semantics

## Context

The project is named OXAP. Existing documentation calls it Open Agent Protocol
or OAP, and the released CLI is `oapx`. A project rename must distinguish the
names people use from identifiers that existing peers, saved state and pinned
harness traces already understand.

This decision records the name and the compatibility boundaries before any
executable, configuration, repository or package migration. It changes no
runtime behavior. README commands continue to use the released `oapx` until
the new executable is available.

## Decision

### Project and CLI names

Use **OXAP** as the project name. The preferred CLI name will be `oxap`.
`oapx` remains a deprecated compatibility alias for at least two minor versions
after the first release providing `oxap`; it must invoke the same implementation
with the same arguments and exit behavior. Any deprecation notice goes to
stderr, never into protocol stdout.

### v0.1 wire identities retain their spelling

The project rename does not introduce alternate spellings for the following:

| Surface | Retained identity |
| --- | --- |
| Envelope `protocol` | `open-agent-protocol` |
| Envelope `profile` | All existing profile strings, including `open-agent-protocol.agent-control-core` and `open-agent-protocol.model-provider-core` |
| Hub transport events | `oap-subscribed`, `oap-overflow`, `oap-replay-gap`, `oap-session-closed`, `oap-frame-limit`, `oap-stream-failed` |
| Capability descriptors | The `capability_revision` member and existing revision values, including harness pins containing `-oap-` |

These are compatibility identities, not display labels. This naming decision
does not change envelope types, hub operations, feature keys, schema constants
or conformance claims. It does not add `oxap-*` hub event aliases.

A genuine capability or harness-version change may still publish a new
revision under the existing catalog and refresh rules. Branding alone does
not justify rewriting a pinned revision. A future wire rename requires its
own versioned compatibility decision; it is outside this v0.1 rename.

### Local names migrate with fallback compatibility

Later implementation changes must preserve these boundaries:

- User-facing `OXAP_*` variables take precedence over their legacy `OAPX_*`
  equivalents; read the old name only when the new name is absent. The SDK's
  `OAP_SDK_BINARY_PATH`, `OAP_SDK_BINARY_URL` and `OAP_SDK_BINARY_SHA256` follow
  the same rule with `OXAP_SDK_BINARY_*`. Internal test gates need no bulk rename.
- The new user-data location is `~/.oxap`. When it is absent, retain read
  access to existing `~/.oapx` state. Workspace `.oxap/` follows the same
  fallback rule for `.oapx/`. Do not automatically move or delete legacy state.
- Keep the Keychain service `ai.hyperneo.oap`. Do not copy credentials into
  a plain file or automatically move `auth.json` as part of renaming paths.
- The loop's namespaced metadata may later accept `oxap` alongside `oapx`.
  Readers must accept the new spelling before writers switch; reject
  conflicting values rather than silently choosing one. The current `oapx`
  spelling remains supported. This decision alone changes neither reader nor
  writer.

Each migration needs its own observable compatibility proof before examples
or installation instructions switch to the new names. No global replacement
of `OAP`, `oap` or `oapx` is authorized by this decision.

### Repository, publication and release matrix

The repository target is **`lsm/oxap`**: an in-place rename of this repository,
not a new organization. The Go module follows the repository URL as
`github.com/lsm/oxap` once the rename lands; no vanity domain is required.
GitHub redirects and immutable old tags keep older pinned Go versions working;
existing imports and published versions are not rewritten.

Publication names were re-checked against the registries on 2026-11-13 (all
unclaimed at that date; re-check before the first publish):

| Surface | Target name |
| --- | --- |
| GitHub repository | `lsm/oxap` |
| Go module and Go SDK | `github.com/lsm/oxap` (Git tag + Go proxy) |
| Zig package | `.oxap` in `zig/build.zig.zon` |
| Runtime, CLI and TUI | `oxap`, with `oapx` kept as a compatibility alias; release assets `oxap-<version>-<os>-<arch>` |
| npm CLI | `@oxap/cli`, resolving platform packages through optional dependencies |
| npm platform packages | `@oxap/cli-darwin-arm64`, `@oxap/cli-darwin-x64`, `@oxap/cli-linux-arm64`, `@oxap/cli-linux-x64`, `@oxap/cli-win32-arm64`, `@oxap/cli-win32-x64` |
| TypeScript SDK | `@oxap/sdk` |
| TypeScript daemon client | `@oxap/client` |
| Python SDK | distribution `oxap-sdk`, import `oxap_sdk` |
| Rust crates (17) | `oxap-protocol`, `oxap-validation`, `oxap-harness`, `oxap-provider-catalog`, `oxap-binding`, `oxap-adapter`, `oxap-adapter-acp`, `oxap-adapter-claude`, `oxap-adapter-codex-appserver`, `oxap-adapter-copilot`, `oxap-adapter-deepseek`, `oxap-adapter-hermes`, `oxap-adapter-opencode`, `oxap-adapter-pi`, `oxap-serve`, `oxap-conformance`, `oxap-sdk` |
| Homebrew | formula `oxap`, submitted to homebrew-core at the first stable release |

All npm names use the existing `@oxap` organization scope; the unscoped `oxap`
package is not claimed. The `nocomment` tool, `goap`, `oap-rust-hub` and
bench/probe binaries stay internal and unpublished. PyPI's existing `oap-sdk`
belongs to another project and is left alone; no compatibility `oap_sdk` import
is added.

The first OXAP release is `v0.1.0-alpha.33` (`0.1.0a33` for Python). Homebrew
waits for the first stable release; the alpha adds no tap.

Keep identifiers in `research/` ledgers and `fixtures/adapters/` corpora as
recorded. They describe pinned upstream vocabulary and wire evidence, not
project branding. Historical references remain valid under their old names.

## Consequences

This change adds the naming record and one README notice. Schemas, validators,
SDKs, harness catalogs, binaries, release assets and package manifests keep
their current behavior and names. CLI aliases, configuration fallback and
publication changes follow as separate, small changes with compatibility
evidence. The documentation site is also a separate implementation task.

## Implementation

The follow-up changes landed for `v0.1.0-alpha.33`: the `oxap` binary with its
`oapx` alias and `oxap-*` release archives, the `~/.oxap` state paths and
`OXAP_*` variables, the four SDK binary resolvers, the Go module
`github.com/lsm/oxap`, the `oxap-*` Rust crate names, and the `@oxap/cli`,
`@oxap/sdk`, `@oxap/client` and `oxap-sdk` package names.
[docs/install.md](../docs/install.md) lists them and how to move.

Still open after that release: the Zig package is still named `.makai`, and no
release job publishes the hub client to npm, the Python SDK to PyPI or the
Rust crates to crates.io.
