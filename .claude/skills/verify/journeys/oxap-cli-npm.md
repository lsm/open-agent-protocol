# Journey: @oxap/cli installs and runs oxap

The npm CLI is `@oxap/cli`, an entry package whose `oxap` bin runs the binary
from one of six optional platform packages, `@oxap/cli-<platform>-<arch>`.
The TypeScript SDK resolves its default runtime from that platform package.
Nothing is published: this journey packs the tarballs and installs them
offline, which is what a registry install does.

Build the binary and the SDK first, from the repository root:

```sh
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe
npm ci --include=dev --ignore-scripts
npm run build:sdk
```

## The packed CLI runs the real binary

```sh
node scripts/check-npm-cli.mjs zig/zig-out/bin/oxap
```

It runs `scripts/package-npm.ts` with the host's binary and the other five
targets withheld, checks the manifests (entry bin `oxap`, optional
dependencies on the packaged platforms at the same version, no bin on a
platform package), installs both tarballs into a fresh project with
`--offline`, and compares `oxap --version` and `oxap serve ghost` through
`node_modules/.bin/oxap` against the binary run directly. Require one line per
command showing equal exit codes (0, then 1), the installed version line
starting `oxap-`, and `check-npm-cli: OK`. CI runs the same script in the
`TS SDK E2E` job.

## The SDK finds its runtime through the installed CLI

Pack `dist/npm/cli-<host>`, `dist/npm/cli` and the SDK package left by the
check above, install the three tarballs into a fresh project with
`--offline`, and with `OXAP_SDK_BINARY_PATH` and `OAP_SDK_BINARY_PATH` unset,
`HOME` a temporary directory and `PATH` limited to `/usr/bin:/bin` plus
`node`, call `resolveMakaiBinary()` and then `createMakaiClient()` and
`client.auth.listProviders()`. The resolved path must be
`node_modules/@oxap/cli-<host>/bin/oxap`, and the provider list must be
non-empty. `npx --no-install oxap --version` in that project must print the
same `oxap-<version>-<os>-<arch>` line as the binary.
