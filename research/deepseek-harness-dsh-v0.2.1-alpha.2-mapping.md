# DeepSeek Harness dsh-v0.2.1-alpha.2 mapping ledger

This ledger records the move from `dsh-v0.1.7-rc.2` to `dsh-v0.2.1-alpha.2`.
It is a delta over [the dsh-v0.1.7-rc.2 ledger](deepseek-harness-dsh-v0.1.7-rc.2-mapping.md),
itself a delta over [the 47f9438 ledger](deepseek-harness-47f9438-mapping.md),
which stays the base specification for everything neither names.

## Provenance

- Repository: `https://github.com/deepseek-ai/deepseek-harness`
- Implementation target: release `dsh-v0.2.1-alpha.2`, published
  2026-10-09, the newest release on 2026-10-10. Every release of the project
  is a prerelease; the newest release candidate is `dsh-v0.2.0-rc.2`, 935
  commits earlier.
- Tag commit: `d743267388641bc76f17c45ce8b4c231aed1d32c`
  (`Merge pull request #5946 from deepseek-harness/release-0.2.1-alpha.2`)
- Tag commit tree: `75a3d23c8b6db770a000081918c4b975912a9c39`
- Previous pin: `dsh-v0.1.7-rc.2` `477b4f420553e8a52c2fbccc464d7561b239c443`,
  1,383 commits earlier. It is retired.
- The release publishes no binary assets, so there is no artifact digest to
  pin. The source commit and tree are the provenance.

```sh
git clone https://github.com/deepseek-ai/deepseek-harness.git dsh
git -C dsh checkout d743267388641bc76f17c45ce8b4c231aed1d32c
git -C dsh show -s --format='%H%n%s%n%T' HEAD
# d743267388641bc76f17c45ce8b4c231aed1d32c
# Merge pull request #5946 from deepseek-harness/release-0.2.1-alpha.2
# 75a3d23c8b6db770a000081918c4b975912a9c39
```

Pinned blobs at `d743267`, against `477b4f4`:

| Source | Blob | Moved |
|---|---|---|
| `packages/sdk/protocol/src/types.ts` | `9c2fd6335d3db02fb7fabe9f0674e5a63647a09e` | yes, three requests added |
| `packages/sdk/protocol/src/transport.ts` | `36574f46bf3e34738be045408e25bb79932ff609` | no |
| `packages/sdk/server/src/server.ts` | `ab5084ffb723b327d6accc04547698d786f827a6` | yes, the three requests |
| `packages/core/session/src/types.ts` | `fbc9a9cccbe3f95d344f8e8078dfe5ddd3786a80` | yes, plugin-record types and prose |
| `packages/core/session/src/known-event-types.ts` | `9801de9222f0aee821fd7a33124b07b2ed833959` | yes, one event added |
| `packages/core/agent/src/types.ts` | `5cbc81df38c81be4f5c4d85e6a2c8acd5108ed95` | no |
| `packages/core/agent-loop/src/inbox.ts` | `ef00887a544c2dededb484ff7b9ce0efe83dea21` | no |
| `packages/core/agent/src/runtime-types.ts` | `0fa89afc054a819493206cef65dd7907092cdd7f` | prose only |
| `packages/core/agent-loop/src/agent.ts` | `adf2e33ddcc9b37e8e20e89203a276bcdf18ee5d` | yes |
| `packages/llm/llm/src/message.ts` | `d96a63edb6d78d64508875ebc1cc6a1cc5a824b6` | no |
| `packages/llm/llm/src/types.ts` | `794352dc837f0d7c0d9335e3da607e5eb9da91a8` | prose only |
| `packages/llm/llm/src/assistant-stream.ts` | `7fca34c5d5bf432f7fad81bd48edfbdaf5083e83` | no |
| `packages/core/session/src/invariant.ts` | none | deleted |
| `packages/core/session/src/surface.ts` | `0bb84c030ef595b3d9a17116e7dc7b9deb26369c` | prose only |
| `packages/llm/llm-deepseek/src/adapter.ts` | `8346abc29ab3f587cad9a74c449f1c0537032309` | abort cleanup |
| `packages/llm/llm-deepseek/src/messages-api.ts` | `2997753f2faa92a0259334537f3eb2e1912a0bdd` | no |
| `packages/llm/llm-deepseek-api-key/src/index.ts` | `fdbf6fba94e9b0cc85abd1683aef45045263dd67` | model discovery needs a key |
| `packages/core/agent-loop/src/runtime-context.ts` | `cb959b055a691d7bb8f1e36dfbb721203999b347` | no |

Added to the inspected set because they produce the new event:

| Source | Blob |
|---|---|
| `packages/session/working-directory/src/index.ts` | `7346feb2743ef976bfc5454384f210f906ea3e0d` |
| `packages/session/tool-working-directory/src/index.ts` | `f0aa8bc4f0854c6dff38feeb9475b303e5108cb6` |
| `packages/bundle/sdk-minimal/cordis.patch.yml` | `6c8dbce78af9de18c7241fd1b3001afb582f3ae0` |

## How the runtime was driven

As at `dsh-v0.1.7-rc.2`, from source: `node --import tsx/esm
apps/cli/src/bin.ts --profile sdk --patch
apps/cli/src/sdk-source.cordis.patch.yml` with `TSX_TSCONFIG_PATH` set to
the CLI's tsconfig, through a one-line launcher that `OAP_DEEPSEEK_HARNESS_BIN`
named. The checkout was installed with `pnpm 11.28.5 install --frozen-lockfile
--ignore-scripts` (the version its `packageManager` names; `pnpm-lock.yaml`
blob `dfaa16532d4a9500a31a3995bd0da8d3af579585`, source patch unchanged at
`8545c2fd3a704bec264fa899ee813d93b6963297`), and the host `system.node` addon
was built with the checkout's `build:native-system` script (darwin-arm64
sha256 `d14698a8dda5a8eb6cbc97b309d6c389f28c695f79192cb825c2197dce310220`).
Node was v26.5.1. `OAP_DEEPSEEK_HARNESS_SHA256` was not set, as before.

`initialize` still answers `serverInfo {name: "deepseek-harness-sdk-runtime",
version: "0.0.1"}`, so `admits` stays `["0.0.1"]`.

## What moved on the wire

### `working-directory/change` joins the vocabulary

The session working-directory owner appends `working-directory/change
{cwd}` whenever a Session's directory changes, and, read from the source,
from `ensure` when no directory has been recorded yet; the recordings without
a `cd` carried none.
The `working_directory` tool, which `sdk-minimal` (the profile `sdk` boots)
mounts, changes it on a model's `cd` and then appends a `user/message` whose
source kind is `working-directory`, announcing the move to the model. The
event carries no `ignorable` marker. Both trees failed a run on it with
`deepseek_unknown_event`; it is now observed-only in both. The notice
message needs no change: Go accepts a source kind it does not close over,
and neither tree treats a non-user source as the user's own turn.

Recorded in the new `working-directory-change` case: a `working_directory`
call with `cd: "sub"`, the change event between `tool/call` and
`tool/result`, the notice and a fresh runtime-context snapshot, and a
completed turn.

### Additions the adapters do not use

- **Requests.** `session/wait`, `session/working-directory/get` and
  `session/working-directory/set`. The adapters send none of them.
- **Plugin records.** `packages/core/session/src/types.ts` declares
  `plugin:`-prefixed experimental record types. They never enter the session
  event map and none appeared on the wire.

### Deleted upstream

`packages/core/session/src/invariant.ts`, the optional companion that checked
turn, step and call relations in the session log, is gone. The adapters never
depended on it; their own reducers keep their grammar. The corpus test now
pins that blob only for the cases carried from `fb2c4b9`.

### Unchanged, by recording

`tool-lifecycle` and `injected-origin` were re-recorded against this runtime
with the same scripts as at `dsh-v0.1.7-rc.2`: same file contents, model
replies, usage and neutral roots (`/tmp/dsh-tools`, `/tmp/dsh-text`). Every
frame has the same type, order and payload shape as before, and the
regenerated expectations differ only in one string: the `read` tool's result
now renders its path as `/private/tmp/dsh-tools/workspace/a.txt`, the
resolved form of the same file. That is tool output the adapters pass
through.

## Capability revision

The capability descriptor is unchanged, so only the version prefix moves:
`deepseek-harness-dsh-v0.2.1-alpha.2-oap-v3`.

## Evidence corpus

`fixtures/adapters/deepseek-harness-dsh-v0.2.1-alpha.2/` replaces the
`dsh-v0.1.7-rc.2` corpus.

| Case | Native side | Why |
|---|---|---|
| `tool-lifecycle` | re-recorded at `d743267` | evidence that the tool-result shape did not move |
| `injected-origin` | re-recorded at `d743267` | evidence that the runtime-context source did not move |
| `working-directory-change` (new) | recorded at `d743267` | the new event |
| every other case | carried from `fb2c4b9`, byte for byte | as the dsh-v0.1.7-rc.2 ledger records |

The recordings drove the runtime over stdio with a loopback Messages mock
scripting the model, writing frames verbatim from the first `session/prompt`
to the final `idle`; `injected-origin` keeps its `wait-submit` control where
the old case put it. Mapping and omissions take each frame type's
classification from the old cases; `working-directory/change` is
observed-only. Expectations were generated with `OAP_UPDATE_DSH_CORPUS=1`.
`native.jsonl` digests: `tool-lifecycle`
`43701b126fe2c9aae825918278d87ae1f1d5b87061aad8aa2986db78e83b6592`,
`injected-origin`
`18e29a4dae495dc4e17fdb56708f958e9a645053d2dc21f6f05497653b507f18`,
`working-directory-change`
`b4dc2bde2f617b9a6e864864d27315bd68290b70fb263085281096fdf834f7fe`.

The carried cases use no frame type or member whose shape moved: the SDK
notification payloads are unchanged, and the only new event is the one
above (read from `types.ts` and `known-event-types.ts`).

## Process gates

Against the runtime above, three runs each: `OAP_DEEPSEEK_HARNESS_SMOKE=1`
(`TestDeepSeekProcessSmoke`) and `OAP_DEEPSEEK_HARNESS_INTEGRATION=1`
(`TestDeepSeekProcessAgainstMessagesMock`) pass. Both passed before any change
too: neither drives the working-directory tool.
