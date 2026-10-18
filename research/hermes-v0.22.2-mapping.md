# Hermes agent v0.22.2 mapping ledger

Status: the implementation contract for the Hermes adapter pinned to
`v0.22.2`. It records only what changed against
[`hermes-v0.21.6-mapping.md`](hermes-v0.21.6-mapping.md), which with the
[v2026.9.24](hermes-v2026.9.24-mapping.md) and
[v2026.8.31](hermes-v2026.8.31-mapping.md) ledgers stays the base for
everything not named here. Each claim below was checked by running the pinned
gateway, except where a paragraph says it was read from the source.

## Provenance

- Repository: `https://github.com/NousResearch/hermes-agent`
- Release: `v0.22.2`, published 2026-10-15 (annotated tag object
  `7b77e29eedd85a8497ce2821448dac7964dc46f6`, tagged by the project's release
  automation from release candidate `rc.2-v0.22.2`)
- Release commit: `d923a5eb487a17c0a68801bba9ef0f56c81040a8` ("Release 0.22.2
  from release/0.22.1 with the published v0.22.1 as a parent")
- Release commit tree: `5a064077e8d1de9fc7a4b27efc04f0caa2cb06c0`

Between the two pins the project published `v0.21.7`, `v0.22.0` and
`v0.22.1`. `tui_gateway/` differs in 63 paths.

Normative source blobs at the release commit:

- `tui_gateway/entry.py` — `6f7c2d23e990eb7c3593dec335e0c105840fcb06`
- `tui_gateway/transport.py` — `8e9a54946fe97f83baa2c5fcb6efb9feb00a3058`
- `tui_gateway/server.py` — `83b1ff52f4eab8b4ad3d938e0d2de71db09ddaea`
- `tui_gateway/event_replay.py` — `f0b6aa0f8cec280036d63245bf5d3f97a3fa01e8`
- `tui_gateway/_stdin_recovery.py` — `add5e841df4bead308a96bd8b0f0e3c5475bd167` (unchanged)
- `tui_gateway/ws.py` — `42dbd6164b8acb2fc257ba90d076122e5325ef77`
- `tui_gateway/methods_prompt.py` — `f4b0b80c94361e07b0634e0b844b5d5303888d8a`
- `tui_gateway/methods_session.py` — `c83b0ef4e4d2fb6d5bad5c2913acfb8a32c60735`
- `tui_gateway/server_requests.py` — `dfd39d842485ca081548e38baa3d81518bfc6e23`
- `tui_gateway/rpc_dispatch.py` — `b2a2587c42dab0b09a24b66e842910f36abfb539` (unchanged)
- `tui_gateway/prompt_turn.py` — `827d4f398e291b01fe23e705b4f021b1ecd4bbd0`
- `tui_gateway/contracts/` (tree) — `782f88736632b3c17dfcdf0bf7027d37c7eedc87`
- `hermes_state.py` — `4b67b8f46b47d3288a3cceadf9992f641e4ef7d7`
- `hermes_state_sessions.py` — `0755614a6e328faf686bc54268754f0090e5a3d3`
- `hermes_state_common.py` — `5a37d49eba37d17ab4e84fdef625071977bbef86`

The release ships no binary artifact, so the catalog records no digest. The
checkout still declares its dependencies only for Python 3.14 (all 46 pins in
`pyproject.toml` carry `python_version >= '3.14'`). The runs below used the
same CPython 3.14.6 as v0.21.6 (sha256
`0826ccaa8dd19bd84a5f0ffdbe84558f036e3b7d8ce7725704a7393995392129`) in a
virtualenv holding the release checkout installed editable.

## How the difference was established

1. **The contracts registry was dumped at both tags** (every method's params
   and result, every server→client request and every event payload, as JSON
   Schema from the pydantic models) and diffed, ignoring titles and
   descriptions. The v0.21.6 dump reproduces that ledger's counts (77 events,
   14 server requests, 257 methods); v0.22.2 has 77, 16 and 257.
2. **The v0.21.6 corpus was validated against both contracts**, frame by frame
   in both directions. The result is identical at both tags: every frame
   validates except three hand-authored `subagent.*` payloads in
   `subagent-frames`, which omit members the contracts have required since
   before v0.21.6, so nothing in the corpus depends on a changed contract.
3. **The Go process gates were run against the release checkout**
   (`OAP_HERMES_SMOKE=1`, `OAP_HERMES_INTEGRATION=1`), four times green.
4. **`oapx serve agent --backend hermes` was driven against it** three times,
   with a scripted loopback chat-completions provider, an isolated `HOME`,
   dead loopback proxies and no ambient credential: an open at reasoning
   level `low`, a turn whose tool call raises the approval gate, a `deny`
   answer, and `run.completed`. Each run served revision
   `hermes-v0.22.2-oap-v1`, the provider saw `reasoning_effort: "low"` on both
   requests, the denial reached the model in the tool result, and all three
   traces pass `goap validate` and `oapx validate`.

## Wire differences against v0.21.6

None of the events, server requests or methods the adapter sends or projects
changed. The differences, and what each means here:

| change | classification |
| --- | --- |
| event `onboarding.ended` added | observed-only: admitted in Go's closed event vocabulary (`native.observedEvents`); the Zig reducer ignores event types it does not project |
| event `layout.apply` removed | no effect: Go keeps the name admitted, since an older gateway may still send it |
| server requests `setup_beat`, `theme.apply`, `theme.pick` added; `setup_choose` removed | unmapped: both trees answer an unmapped server request with method-not-found, as before |
| `session.close` result gains `messages` (array of strings) | no effect: neither tree decodes the `session.close` result |
| `prompt.submit` gains optional `confirm_deep_truncate` | no effect: the gate applies only to a submit that truncates history (`_truncate_history_for_submit`, read from the source), which neither adapter sends; it is warn-only in this release (`DEEP_TRUNCATE_ENFORCED = False`) and refuses with `4033` from the next one |
| `session.resume` and `session.activate` results gain `ConnectionDisplay` members | no effect: both trees decode these results leniently |
| `connection.*`, `connectors.*`, `onboarding.*`, `plugins.*`, `machine.facts` method and event changes | not on the adapter's surface |

## The live gates start the user's `copilot` CLI

Two of the first three gate runs against v0.22.2 failed only in `t.TempDir`
cleanup (`directory not empty`): a process kept writing into the test's
isolated `HOME` after the gateway exited, under
`Library/Caches/copilot/pkg/darwin-arm64/`, which is the GitHub Copilot CLI's
own package cache. The gates pass `PATH` through, and the developer's `PATH`
held a `copilot` binary. With that directory removed from `PATH`, four runs
in a row passed. The same cleanup failure appeared once in three runs
against v0.21.6, so it is not new in this release. Which gateway code path
starts the CLI was not established. The consequence for anyone running the
gates: a `copilot` on `PATH` is started inside the isolated `HOME`, outlives
the gateway, and may reach whatever credentials it keeps outside `HOME`, so
run the gates with a `PATH` that does not contain it.

## Corpus

`fixtures/adapters/hermes-v0.22.2` carries the v0.21.6 cases forward
unchanged in what they replay: every case's provenance names the v0.22.2
release commit, tree and source blobs, and every expectation carries
`hermes-v0.22.2-oap-v1`. The native frames recorded at v0.21.6 are kept
verbatim (a recorded `session.info` still names that checkout's path and
branch), because step 2 above shows each of them is still valid against the
v0.22.2 contracts. No case was re-recorded, since no surface the adapter uses
changed.
