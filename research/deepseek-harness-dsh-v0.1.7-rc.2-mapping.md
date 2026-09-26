# DeepSeek Harness dsh-v0.1.7-rc.2 mapping ledger

This ledger records the move from `dsh-v0.1.6-alpha.2` to `dsh-v0.1.7-rc.2`.
It is a delta over [the 47f9438 ledger](deepseek-harness-47f9438-mapping.md),
which stays the base specification for everything not named here: boundary
selection, admission, settlement, capability matrix and the Zig port's gaps.

## Provenance

- Repository: `https://github.com/deepseek-ai/deepseek-harness`
- Implementation target: release `dsh-v0.1.7-rc.2`
- Tag commit: `477b4f420553e8a52c2fbccc464d7561b239c443`
  (`Merge pull request #5180 from deepseek-harness/rel/dsh-0.1.7-rc.2`)
- Tag date: 2026-09-24
- Tag commit tree: `e3e63253d1d35ad07f785273235c40813cb6c8bd`
- Previous pin: `dsh-v0.1.6-alpha.2`
  `ddefc45fbc7f8e46dd73185e68295696d1297887`, tree
  `5aca5ee6f8dfd110dc3ae199fbddf8a0f606625f`, 1963 commits earlier. It is
  retired: its tool-result shape is no longer admitted.
- The release publishes no binary assets, so there is no artifact digest to
  pin. The source commit and tree are the provenance.

```sh
git clone https://github.com/deepseek-ai/deepseek-harness.git dsh
git -C dsh checkout 477b4f420553e8a52c2fbccc464d7561b239c443
git -C dsh show -s --format='%H%n%s%n%T' HEAD
# 477b4f420553e8a52c2fbccc464d7561b239c443
# Merge pull request #5180 from deepseek-harness/rel/dsh-0.1.7-rc.2
# e3e63253d1d35ad07f785273235c40813cb6c8bd
```

Pinned blobs at `477b4f4`, against `ddefc45`:

| Source | Blob | Moved |
|---|---|---|
| `packages/sdk/protocol/src/types.ts` | `605b97cc6945397563e6351e03ffde7a9436b23f` | no |
| `packages/sdk/protocol/src/transport.ts` | `36574f46bf3e34738be045408e25bb79932ff609` | no |
| `packages/sdk/server/src/server.ts` | `50567c32e78067d7d82cd5c8429e945ca3c7d237` | yes |
| `packages/core/session/src/types.ts` | `593c86d52a5c26d531913ffaff9aac651732c2da` | yes |
| `packages/core/session/src/known-event-types.ts` | `adaca29255a181c793a73476baddc25324083cb2` | yes |
| `packages/core/agent/src/types.ts` | `5cbc81df38c81be4f5c4d85e6a2c8acd5108ed95` | yes, type-only |
| `packages/core/agent-loop/src/inbox.ts` | `ef00887a544c2dededb484ff7b9ce0efe83dea21` | no |
| `packages/core/agent/src/runtime-types.ts` | `2118507b8cc617a920e7ceae9f98d06daa2d4780` | no |
| `packages/core/agent-loop/src/agent.ts` | `bcd7ad17a82699c395c3a14855cdee6ed4a2b905` | yes |
| `packages/llm/llm/src/message.ts` | `d96a63edb6d78d64508875ebc1cc6a1cc5a824b6` | yes |
| `packages/llm/llm/src/types.ts` | `54453607f5c07f31baea3e6fd722f94cec71ad5d` | yes |
| `packages/llm/llm/src/assistant-stream.ts` | `7fca34c5d5bf432f7fad81bd48edfbdaf5083e83` | cast only |
| `packages/core/session/src/invariant.ts` | `154c95343862f5929c8b55e7df72d120b934ef13` | yes |
| `packages/core/session/src/surface.ts` | `eeedca77b9bafacaf5d2d358c2bdaa6915988d8b` | yes |

Added to the inspected set because the loopback gate depends on them:

| Source | Blob | Contract evidence |
|---|---|---|
| `packages/llm/llm-deepseek/src/adapter.ts` | `20e899788e58c79d9caf1ec4e3b6f4c6e39ba7eb` | stock provider posts to `<root>/v1/messages` |
| `packages/llm/llm-deepseek/src/messages-api.ts` | `2997753f2faa92a0259334537f3eb2e1912a0bdd` | API root and beta headers |
| `packages/llm/llm-deepseek-api-key/src/index.ts` | `cf039f9a0ab9f424796b2bc2c3c857641f8f3ba6` | `x-api-key` authentication |
| `packages/core/agent-loop/src/runtime-context.ts` | `cb959b055a691d7bb8f1e36dfbb721203999b347` | `runtime-context` message source |

## How the runtime was driven

No binary is published. The runtime was launched from source, the way the
SDK's own launcher does for a checkout (`packages/sdk/client/src/launch.ts`):
`node --import tsx/esm apps/cli/src/bin.ts --profile sdk --patch
apps/cli/src/sdk-source.cordis.patch.yml`, with `TSX_TSCONFIG_PATH` set to the
CLI's tsconfig. The checkout was installed with `pnpm 11.7.0 install
--frozen-lockfile --ignore-scripts` (`pnpm-lock.yaml` blob
`36dc0506d3f741747bd80ce6089eaf7f80206d5f`, source patch
`8545c2fd3a704bec264fa899ee813d93b6963297`) and the host `system.node` addon
built from `native/system` with its own script (`build.ts --host-addon-only`;
darwin-arm64 sha256
`9534cde445a865250eb7bc8cfe5e231f3f91d0fda3f4aaa694e81f3531ad1130`). Node was
v26.5.1. A source launch has no single artifact to digest, so
`OAP_DEEPSEEK_HARNESS_SHA256` was not set.

`initialize` answers `serverInfo {name: "deepseek-harness-sdk-runtime",
version: "0.0.1"}`, unchanged, so `admits` stays `["0.0.1"]`.

Both gates pass against that runtime:

- `OAP_DEEPSEEK_HARNESS_SMOKE=1`: PASS.
- `OAP_DEEPSEEK_HARNESS_INTEGRATION=1` (`TestDeepSeekProcessAgainstMessagesMock`):
  PASS after the changes below. Before them it failed twice over: the gate's
  mock served chat completions, and the adapter's codec refused the first
  `runtime-context` user message, closed the pipes, and the runtime died on
  `EPIPE` (the failure mode recorded at 0.1.5-rc.2).

## What moved on the wire, and how each move was absorbed

### The stock provider speaks the Messages API

The SDK server now imports `@deepseek-ai/dsh-llm-deepseek-api-key`, and the
`deepseek-official` route posts to `${messagesApiRoot(base)}/messages` with
`x-api-key` and `anthropic-version`, where 0.1.6 posted OpenAI chat
completions with a bearer token. `DEEPSEEK_BASE_URL` still redirects it. This
is provider-side, not adapter wire; only the integration gate changes: it now
serves `providertest.AnthropicMessages` at the mock root and asserts the
`x-api-key` header. The test was renamed from `...AgainstResponsesMock`, which
named an API it never served.

### `tool/result` carries a first-class tool-role message (breaking)

`ToolResultBlock` is gone from `ContentBlockMap`. A tool result is now
`{role: "tool", source: {kind: "tool", callId}, toolCallId, content,
isError?}`: the result blocks sit directly in `content`, and `isError` moved
from the block to the message. `invariant.ts` reads
`event.data.message.isError`.

- The Go codec decodes `message` as `ToolResultMessage` and requires
  `role: "tool"`, a `tool` source and `toolCallId` equal to `source.callId`.
  The old single-`tool-result`-block rule is gone.
- `tool-result` is no longer a valid content block anywhere. Go's
  `validBlock` and `validBlockRaw` and the Zig port's mirrors refuse it. The
  decode slots `toolCallId`, `content` and `isError` stay on the Go block
  struct so that a mistyped member is still refused with the same
  `encoding/json` text the Zig port mirrors.
- `action.call.completed.result` is the message `content` as before, but that
  is now the result blocks themselves rather than a one-element array holding
  the wrapper block. That changes what a consumer sees. It does not change the
  descriptor.
- Failure still keys on `error {name, code, reason?}`. Live, a missing file
  produced `isError: true` with `error {name: "FsError", code:
  "FS_NOT_FOUND"}` and no `reason`, so the reason mapping is untested live.

### Message sources are producer-declared

`MessageSourceMap` dropped `plugin: {kind: "plugin", plugin} & ContextFormed`
and added `system-prompt`. Every other producer now declares its own kind in its
own module. At this tree that is about thirty kinds, among them `runtime-context`,
`tool-registry`, `skill-invocation`, `agent-message`, `webhook` and `user-rpc`,
and upstream says "consumers fall through unknown kinds". Two of them are
awkward: `user-rpc` is `{kind: "user", rpcId, clientTimeZone?}`, so a `user`
kind no longer means a bare source.

- `MessageSource` now decodes by kind. `model` (`provider`, `model`,
  `replayState?`), `tool` (`callId`) and `system-prompt` are closed: another
  member is refused. Every other kind, `user` included, is open.
- A user-role message may not carry a `model`, `tool` or `system-prompt`
  source.
- The ownership proof counts a source as direct only when `kind` is the
  only member and is `user`. Before, the proof rejected a fixed list of named
  members. That list could not name members that producers now add.
- The observed-in-every-turn `runtime-context` snapshot, `{kind:
  "runtime-context", form: "snapshot", sections}`, would otherwise fail the
  first live turn at decode.

### `developer/message` joins the vocabulary

A surface event carrying tool additions and removals (`tool-addition`,
`tool-removal` blocks, plus `headerSeq` when there are additions), appended
when a request's tool set changes. It is observed-only in both trees, for the
reason `system/message` is: it changes what the model is shown, not what a run
settles to or its final assistant message. `image/offload` stays unknown-required
because it rewrites projected message content. Not seen live: the gate's tool
set never changes mid-session.

The Zig list also gains `workspace/changes`, which Go has carried since the
0.1.6 move and the port had missed.

### Smaller moves

- `turn/end` reason `forked` joins the map. Only fork seeds carry it, the loop
  never emits it. The codec accepts it and the reducer fails such a run as
  `deepseek_forked`, like any other non-`completed` end.
- `request/header` may carry `startsSeries: true` on any reason, and the
  `series` reason the adapter was already missing is now accepted. The header
  gains a reserved `system?: never`, which the codec refuses.
- `SESSION_FORMAT_VERSION` is 4. It is visible only in
  `session-log-deepseek/delivery-accepted`, which is observed-only.
- `subagent.finished.lastAssistantMessage` is copied before notify. The shape
  is unchanged.
- `permission/preset`, `sandbox/mode` and `approval/policy` now precede the
  prompt's splice in every session. All three were already observed-only.

### A defect the first multi-step recording found

Every earlier corpus turn had one step. The recorded tool turn has two, and the
Go corpus replayed it nondeterministically. When the whole turn was buffered
before the prompt reply, the retrospective admission replayed the candidate
events but skipped every `step/start`, not just the admitted one. `run.step`
stayed 1, and the step-2 `assistant/message` failed the run with
`deepseek_invalid_grammar`. Both trees now skip only the step/starts at or before
the admitted step. `TestRetrospectiveAdmissionReplaysLaterSteps` and the Zig
test "a retrospective admission replays the steps after the admitted one" pin
it, and a mutant that skips every step/start fails both. The Zig corpus replays
in wire order, so there the proof lands live and never reached the bug.

## Capability revision

`deepseek-harness-dsh-v0.1.7-rc.2-oap-v1`, replacing
`deepseek-harness-47f9438-oap-v2`; endpoint version `0.0.1`. There is no
separate `oapx` revision any more: the Zig backend serves the Go adapter's
descriptor under this revision, and the catalog entry carries no
`oapx_capability_revision`.
The descriptor's features are unchanged, but the pin moved and what
`action.call.completed.result` carries changed shape, so the revision names
the pin it describes, as every other harness's does. Every expectation in the
corpus carries the new revision.

## Evidence corpus

`fixtures/adapters/deepseek-harness-dsh-v0.1.7-rc.2/` replaces
`fixtures/adapters/deepseek-harness-47f9438/`. The manifest names this pin.
Each `case.json` names the release its native side came from:

| Case | Native side | Why |
|---|---|---|
| `tool-lifecycle` | re-recorded at `477b4f4` | the tool-result shape changed |
| `injected-origin` | re-recorded at `477b4f4` | the `plugin` source kind no longer exists |
| every other case | carried from `fb2c4b9`, byte for byte | see below |

The re-recordings drove the runtime above over stdio with a loopback Messages
mock that scripted the model's replies. Frames were written verbatim as the
runtime sent them, from the first `session/prompt` to the final `idle`. The
`initialize` exchange before that and the `shutdown` after it are not part of
the case. The only frame not on the wire is the `wait-submit` harness control
in `injected-origin`, placed where the old case put it. Mapping and omissions
were generated by frame type, and the expectations by
`OAP_UPDATE_DSH_CORPUS=1`. Each recording ran under a neutral root
(`/tmp/dsh-tools`, `/tmp/dsh-text`) holding its workspace, `HOME` and
`DSH_HOME`, so the paths in the frames name no user.

- `tool-lifecycle` (sha256
  `1be46b67fef0fd62701c86a13474552a3e589cdafbd5ff8c44c69363c28c529c`): two
  prompts. Each turn has a `read` tool call and then a text step. The first
  reads a file that exists. The second reads one that does not, and the result
  carries `error {FsError, FS_NOT_FOUND}`.
- `injected-origin` (sha256
  `40226cc69c29df31720695d6e5658ad7f69b1cb74cc8d94b8d21c13e81724b3a`): one
  text turn. Its injected message is the real `runtime-context` snapshot.

The carried cases were hand-authored at `fb2c4b9`, as the 47f9438 ledger
records. Their frames were checked against every change above. None has a
`tool/result`, a `tool-result` block, a `plugin` source, or an event or
member whose shape moved. Every source in them is a bare `user` or a `model`
source. They are therefore valid 0.1.7 traces. They are regression input for
the reducer, not evidence of anything 0.1.7 added. `corpus_from` is not set,
because the corpus is not wholly from one older release. The per-case
provenance is the record.

Not re-recorded, and why: the remaining cases exercise paths a loopback run
cannot produce on demand, such as foreign turns, malformed frames, process
loss, overlapping prompts and subagent settlement. Re-recording them would take
a scripted runtime rather than a scripted model.

## Go and Zig

The Zig port gets the same moves where it models them: the direct-user rule,
the `tool-result` block refusal, the `developer/message` and
`workspace/changes` observed-only entries, and the replay fix. It still does not
validate event payloads (#143), so the `tool/result`, source and
`request/header` codec rules above are Go-only, as the 47f9438 ledger's Zig
section already records. One divergence stays unported and unexercised: Go maps
`error.reason` to the failure message, and the Zig reducer still reports `name`.
No live frame carried a `reason`.
