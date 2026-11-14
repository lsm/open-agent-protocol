# Installing OXAP and moving from the OAP names

OXAP is the new name of the project once called Open Agent Protocol (OAP).
`v0.1.0-alpha.33` is the first release under the new names. The wire protocol
does not change: an OXAP release speaks the same v0.1 envelopes as the
releases before it. [Decision 0067](../decisions/0067-the-project-is-named-oxap.md)
records the names and the compatibility rules.

## Install

| What | How |
| --- | --- |
| The `oxap` binary (CLI, terminal UI and endpoint) | Download the `oxap-<version>-<os>-<arch>` archive from the [releases page](https://github.com/lsm/oxap/releases) and check it against `SHA256SUMS.txt`. |
| The binary through npm | `npm install -g @oxap/cli`, or `npx @oxap/cli --version`. |
| The binary from source | Zig 0.17.0, then `make install` in a checkout. |
| TypeScript SDK | `npm install @oxap/sdk` |
| Go library and Go SDK | `go get github.com/lsm/oxap@v0.1.0-alpha.33` |
| TypeScript hub client | From a checkout, as a `file:` dependency on `clients/ts` ([its README](../clients/ts/README.md)). |
| Python SDK | From a checkout: `pip install ./sdk/python`, then `import oxap_sdk`. |
| Rust SDK | From the repository: `oxap-sdk = { git = "https://github.com/lsm/oxap" }`. |

A release publishes the GitHub archives and the npm packages `@oxap/cli` and
`@oxap/sdk`. The hub client, the Python SDK and the Rust crates already carry
their OXAP names, but no release publishes them to npm, PyPI or crates.io yet,
so they are used from the repository.

Release archives cover macOS, Linux and Windows on arm64 and x64. The npm
package `@oxap/cli` picks the matching `@oxap/cli-<os>-<arch>` package through
optional dependencies, so an install fetches only one binary. `@oxap/sdk` lists
`@oxap/cli` of the same version as an optional dependency, so the TypeScript
SDK finds a runtime without a separate download.

## Renamed packages

| Before | Now |
| --- | --- |
| Repository `lsm/open-agent-protocol` | `lsm/oxap` |
| Command `oapx` | `oxap` (`oapx` still works, see below) |
| Release archives `oapx-<version>-<os>-<arch>` | `oxap-<version>-<os>-<arch>` |
| Go module `github.com/lsm/open-agent-protocol` | `github.com/lsm/oxap` |
| npm `oap-sdk` | `@oxap/sdk` |
| npm `@oap-sdk/cli-<os>-<arch>` | `@oxap/cli`, over `@oxap/cli-<os>-<arch>` |
| `oap-client` | `@oxap/client` |
| Python `oap-sdk` / `oap_sdk` | `oxap-sdk` / `oxap_sdk` |
| Rust crates `oap-*` | `oxap-*`, for example `oxap-sdk` and `oxap-protocol` |

None of the old npm, PyPI or crates.io names was ever published by this
project, so there is no old registry package to replace. The `oap-sdk` already
on PyPI belongs to a different project and has nothing to do with this one.

## Moving existing code

**The command.** Use `oxap`. `oapx` is the same program under its old name: it
takes the same arguments and exits with the same codes, and its output on
stdout is the same. It also prints a deprecation note, on stderr only, so a
protocol stream on stdout is never touched. `oapx` stays for at least two minor
versions after this release. The release archives and a build from source
carry both names; the npm package installs only `oxap`.

**Go.** Change the module path in `go.mod` and in every import, for example
`github.com/lsm/open-agent-protocol/go/sdk` becomes `github.com/lsm/oxap/go/sdk`.
A program pinned to an older tag keeps building, because GitHub redirects the
old repository URL and old tags are never rewritten. Each new version needs the
new path.

**TypeScript.** Import from `@oxap/sdk` instead of `oap-sdk`, and from
`@oxap/client` instead of `oap-client`. Exported names such as `MakaiClient`
and the `Oap*` types do not change.

**Python.** Reinstall from `sdk/python` and change `import oap_sdk` to
`import oxap_sdk`. There is no `oap_sdk` compatibility module, because that
name would collide with the unrelated PyPI package. Class names such as
`MakaiClient` do not change.

**Rust.** Rename the dependency, for example `oap-sdk` to `oxap-sdk` in
`Cargo.toml`, and the crate paths in code with it (`oap_sdk::` to
`oxap_sdk::`).

## Environment variables

The runtime's own settings read an `OXAP_*` name first and fall back to the
old `OAPX_*` name when the new one is unset: for example `OXAP_BASE_URL`,
`OXAP_PROVIDER_SERVICE_URL`, `OXAP_PROVIDER_SERVICE_SECURITY`,
`OXAP_PROVIDER_SERVICE_TOKEN` and `OXAP_KEYCHAIN_SERVICE`. The SDKs read
`OXAP_SDK_BINARY_PATH`, `OXAP_SDK_BINARY_URL` and `OXAP_SDK_BINARY_SHA256`
first, and fall back to the old `OAP_SDK_BINARY_*` names. The old names are
still read, so nothing breaks today; move to the new ones when convenient.

Two old names have no new spelling yet. `OAPX_PROVIDER_API_KEY` is not a
setting: `oxap` sets it in the environment of a Codex child it starts. And the
Rust provider catalog library reads only `OAPX_BASE_URL`.

When an SDK looks for the binary on its own, it tries the name `oxap` before
`oapx`. The TypeScript SDK also finds it in the installed
`@oxap/cli-<os>-<arch>` package.

## Where state lives

New state goes to `~/.oxap`, and a workspace's state to `.oxap/` in that
workspace. If only the old `~/.oapx` (or `.oapx/`) exists, OXAP keeps reading
and writing it there. Nothing is moved or deleted for you. To switch, move the
directory yourself while no `oxap` process is running:

```sh
mv ~/.oapx ~/.oxap
```

On macOS, credentials stay in the Keychain under the service
`ai.hyperneo.oap`, so moving the directory does not touch them and no new
sign-in is needed.

## What keeps its old spelling

These are identifiers that other programs already depend on, not branding, so
v0.1 keeps them as they are:

- the envelope `protocol` value `open-agent-protocol` and every profile string;
- the hub events `oap-subscribed`, `oap-overflow` and the other `oap-*` events;
- capability revisions, including harness pins containing `-oap-`;
- the endpoint id `oapx.agent` and the backend name `--backend oapx`;
- the Keychain service `ai.hyperneo.oap`.

The runtime accepts session settings under `metadata.oxap` as well as
`metadata.oapx`, and refuses an open whose two copies disagree.
