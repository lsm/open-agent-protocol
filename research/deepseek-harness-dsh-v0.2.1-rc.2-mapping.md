# DeepSeek Harness dsh-v0.2.1-rc.2 mapping ledger

This ledger records the move from `dsh-v0.2.1-alpha.2` to `dsh-v0.2.1-rc.2`.
It is a delta over [the dsh-v0.2.1-alpha.2 ledger](deepseek-harness-dsh-v0.2.1-alpha.2-mapping.md),
which with the [dsh-v0.1.7-rc.2](deepseek-harness-dsh-v0.1.7-rc.2-mapping.md) and
[47f9438](deepseek-harness-47f9438-mapping.md) ledgers stays the specification
for everything not named here.

## Provenance

- Repository: `https://github.com/deepseek-ai/deepseek-harness`
- Implementation target: release `dsh-v0.2.1-rc.2`, published 2026-10-17, the
  newest release on that day. Every release of the project is a prerelease.
  Between the two pins the project published `alpha.3`, `alpha.4`, `alpha.5`
  and `rc.1`.
- Tag commit (a lightweight tag): `687f282e8625c6b8d11ff58211a90524be2e4317`
  (`Merge pull request #6408 from deepseek-harness/worktree/release-dsh-0.2.1-rc.2`)
- Tag commit tree: `f127f0f96a815eac4dea336a2ae0d9cd0f2aa82d`
- Previous pin: `dsh-v0.2.1-alpha.2` `d743267388641bc76f17c45ce8b4c231aed1d32c`,
  990 commits earlier. It is retired.
- The release publishes no binary assets, so there is no artifact digest to
  pin. The source commit and tree are the provenance.

Pinned blobs at `687f282`, against `d743267`:

| Source | Blob | Moved |
|---|---|---|
| `packages/sdk/protocol/src/types.ts` | `25891197e26f9b6360acf20bc070f2b4b8b8e222` | yes, `session/resume` added |
| `packages/sdk/protocol/src/transport.ts` | `36574f46bf3e34738be045408e25bb79932ff609` | no |
| `packages/sdk/server/src/server.ts` | `726f430f155190922bdce21807da26b6e1a7b127` | yes, `session/resume` served |
| `packages/core/session/src/types.ts` | `e0931f22405b25cf1e9090929fb9f5f45852d6bc` | yes, a typert resource declaration |
| `packages/core/session/src/known-event-types.ts` | `a0f5c17795be074e3c521c525a0eb59ee25248d6` | yes, two events added |
| `packages/core/agent/src/types.ts` | `5cbc81df38c81be4f5c4d85e6a2c8acd5108ed95` | no |
| `packages/core/agent-loop/src/inbox.ts` | `ef00887a544c2dededb484ff7b9ce0efe83dea21` | no |
| `packages/core/agent/src/runtime-types.ts` | `0fa89afc054a819493206cef65dd7907092cdd7f` | no |
| `packages/core/agent-loop/src/agent.ts` | `adf2e33ddcc9b37e8e20e89203a276bcdf18ee5d` | no |
| `packages/llm/llm/src/message.ts` | `d96a63edb6d78d64508875ebc1cc6a1cc5a824b6` | no |
| `packages/llm/llm/src/types.ts` | `8001ab81d8f4e159f1b5e02e12af4f7f6223f625` | yes, route endpoint and native-auth descriptors |
| `packages/llm/llm/src/assistant-stream.ts` | `7fca34c5d5bf432f7fad81bd48edfbdaf5083e83` | no |
| `packages/core/session/src/surface.ts` | `0bb84c030ef595b3d9a17116e7dc7b9deb26369c` | no |
| `packages/llm/llm-deepseek/src/adapter.ts` | `53b288f1f2d1594e2d2e11f20e8b23022b93252a` | yes, connection resolved per call, image projection |
| `packages/llm/llm-deepseek/src/messages-api.ts` | `2997753f2faa92a0259334537f3eb2e1912a0bdd` | no |
| `packages/llm/llm-deepseek-api-key/src/index.ts` | `fdbf6fba94e9b0cc85abd1683aef45045263dd67` | no |
| `packages/core/agent-loop/src/runtime-context.ts` | `cb959b055a691d7bb8f1e36dfbb721203999b347` | no |
| `packages/session/working-directory/src/index.ts` | `7346feb2743ef976bfc5454384f210f906ea3e0d` | no |
| `packages/session/tool-working-directory/src/index.ts` | `f0aa8bc4f0854c6dff38feeb9475b303e5108cb6` | no |
| `packages/bundle/sdk-minimal/cordis.patch.yml` | `79922e7113a607462c96d96137d843359ef345d2` | yes, five plugins mounted |
| `apps/cli/src/sdk-source.cordis.patch.yml` | `8545c2fd3a704bec264fa899ee813d93b6963297` | no |

## How the runtime was driven

As at `dsh-v0.2.1-alpha.2`, from source: `node --import tsx/esm
apps/cli/src/bin.ts --patch apps/cli/src/sdk-source.cordis.patch.yml` with
`TSX_TSCONFIG_PATH` set to the CLI's tsconfig, through a one-line launcher
that `OAP_DEEPSEEK_HARNESS_BIN` named; the adapter supplies `--profile sdk`
itself. The checkout was installed with `pnpm 11.28.5 install
--frozen-lockfile --ignore-scripts` (the version its `packageManager` names;
`pnpm-lock.yaml` blob `5f38bae3561846058531c3fcc1092df01b012024`), and the
host `system.node` addon was built with the checkout's `build:native-system`
script; its darwin-arm64 sha256 is unchanged at
`d14698a8dda5a8eb6cbc97b309d6c389f28c695f79192cb825c2197dce310220`. Node was
v26.12.0. `OAP_DEEPSEEK_HARNESS_SHA256` was not set, as before.

Every run used an isolated `HOME` and XDG directories, dead loopback
proxies, and a loopback Anthropic Messages mock scripting the model with a
test-only key; no ambient credential reached the runtime. The `alpha.2`
checkout was installed the same way for side-by-side runs.

`initialize` still answers `serverInfo {name: "deepseek-harness-sdk-runtime",
version: "0.0.1"}`, so `admits` stays `["0.0.1"]`.

## What moved on the wire

### `job/registered` and `job/settled` join the vocabulary

`known-event-types.ts` adds both, written by `@deepseek-ai/dsh-jobs-local`
(read from the source). Recorded in the new `background-job` case: a `bash`
call whose command outlives its `timeoutMs` is moved to a background job,
and `job/registered {jobId, owner, kind, label, startedAt, toolCall}` arrives
between `tool/call` and `tool/result`, inside the owned run. The tool result
says the command moved to the background. When the job ends,
`job/settled {…, finishedAt, status, cause, producerReported, …}` arrives
after the run's `turn/end` and idle status.

Both trees list the two events as observed-only. Without that, the
`job/registered` inside the run is an unknown event, which fails the run
(`deepseek_unknown_event`); the corpus test fails in both trees when it is
removed from the list. The recorded `job/settled` arrives after the run, where
both trees drop it as external activity (below) without consulting the list,
so no test pins its entry. It is listed for a job that settles while a run is
still open, such as one the model waits on with `job_output`: read from the
source, not recorded.

### The runtime starts a turn of its own when a job settles

After `job/settled`, the runtime splices a notice into the inbox and runs a
second turn that no host prompt asked for (recorded). This is not new: the
`alpha.2` runtime, driven with the same script, runs the same unprompted
second turn, without the job events. Both adapters treat any notification
that arrives while no run is reserved as external activity
(`externalActivity` in Go's `session.go`, its twin in Zig) and mark the
session unusable, so the next submit answers `session_closed`; no envelope
reports it in between. Measured identically through `goap serve agent` and
`oapx serve agent` against `rc.2`. The classification is an impedance
mismatch with no projection: OAP has no run the host did not submit, and the
adapter has nothing to report it under. A command the model backgrounds
therefore ends the session once it settles, at both pins. Surfacing that
closure to the host, or projecting the runtime's own turns, is a protocol
decision rather than a pin change.

### `session/resume` is a new request

`session/resume {sessionId}` restores a persisted session without sending a
prompt and rejects one that is missing or already open (read from
`server.ts`). Neither adapter sends it; reopening stays as the alpha.2 ledger
describes.

### The `sdk` profile mounts compaction and image offload

`sdk-minimal`, which does not layer over `base`, now mounts
`attachment-local`, `tool-fs` (`readImage` only), `token-meter`,
`compaction-basic` and `compaction-image-offload`. At `alpha.2` none of the
five was in the `sdk` tree. What reaches the model is unchanged: the
provider saw the same 25 tools at both pins (recorded). Two consequences:

- **Compaction runs by default.** The adapter's compaction-policy patch now
  edits a mounted `compaction-basic`. Opens with a `share` policy and with
  `off` both completed a turn at `rc.2` (and at `alpha.2`).
- **`image/offload` is now reachable** when images are offloaded from the
  context. It stays unknown-required, as the dsh-v0.1.7-rc.2 ledger records:
  it rewrites the projected message, so it cannot be skipped as
  observed-only, and a run in which it arrives fails. Not recorded; read
  from the source.

### Additions the adapters do not use

- `packages/llm/llm/src/types.ts` gains route endpoint and native-auth
  descriptors for provider routes.
- The DeepSeek adapter resolves its connection per call and gains an image
  request target. The scripted turns below exercised the connection path
  unchanged.

## Capability revision

The capability descriptor is unchanged, so only the version prefix moves:
`deepseek-harness-dsh-v0.2.1-rc.2-oap-v3`.

## Evidence corpus

`fixtures/adapters/deepseek-harness-dsh-v0.2.1-rc.2/` replaces the
`dsh-v0.2.1-alpha.2` corpus.

| Case | Native side | Why |
|---|---|---|
| `background-job` (new) | recorded at `687f282` | the two new events |
| `tool-lifecycle`, `injected-origin`, `working-directory-change` | carried from `d743267`, provenance moved to `687f282` | driven again at `rc.2`, a `read` tool turn produced the same frame types in the same order |
| every other case | carried from `fb2c4b9`, byte for byte | as the earlier ledgers record |

`background-job` drives one `bash` call with `timeoutMs: 200` on
`sleep 2; echo job-finished`, from the `session/prompt` to the `job/settled`
after the idle status. Its labels follow the existing cases' rules (an
inserting splice and the direct user message are mapped; deletion splices
and injected messages are observed-only), and its expectations were
generated with `OAP_UPDATE_DSH_CORPUS=1`.

## Process gates

Against the runtime above, three runs each: `OAP_DEEPSEEK_HARNESS_SMOKE=1`
(`TestDeepSeekProcessSmoke`) and `OAP_DEEPSEEK_HARNESS_INTEGRATION=1`
(`TestDeepSeekProcessAgainstMessagesMock`) pass. `oapx serve agent --backend
deepseek` ran the `background-job` script three times to `run.completed`,
serving `deepseek-harness-dsh-v0.2.1-rc.2-oap-v3`; the traces pass `goap
validate` and `oapx validate`.
