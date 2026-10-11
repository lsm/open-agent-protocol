# Hermes agent v0.21.6 mapping ledger

Status: the implementation contract for the Hermes adapter pinned to
`v0.21.6`. It records only what changed against
[`hermes-v2026.9.24-mapping.md`](hermes-v2026.9.24-mapping.md), which with
[`hermes-v2026.8.31-mapping.md`](hermes-v2026.8.31-mapping.md) stays the base
for everything not named here. Each claim below was checked by running the
pinned gateway, except where a paragraph says it was read from the source.

## Provenance

- Repository: `https://github.com/NousResearch/hermes-agent`
- Release: `v0.21.6`, published 2026-10-08 (annotated tag object
  `f27cdbce5ab63eafe8633b293a75151409da5769`)
- Release commit: `818c13be1dc4fd28987e1e881a9408224afd4535`
- Release commit tree: `f36c56dd4fe068b0768e50e1ab39909dee2ab51a`

The project moved from date tags to version tags: `v2026.9.24` was its
`v0.21.5` (that commit's subject is "chore: release v0.21.5 (2026.9.24)"), and
`v0.21.6` is the next release. Its notes put the window at 8,867 non-merge
commits and 2,106 merged pull requests; `tui_gateway/` differs in 190 files.

Normative source blobs at the release commit:

- `tui_gateway/entry.py` — `b09f0acb3e06c7acc00a9cf3c0fda84daffd6957`
- `tui_gateway/transport.py` — `689f5614299c77f3f6a4c779176938b537a8cb4d`
- `tui_gateway/server.py` — `d4934a257c97acd7b1ccc5ac943efe93b74b0f17`
- `tui_gateway/event_replay.py` — `c5dac701896ccb7febf7b44fb0e6b6f57340823a`
- `tui_gateway/_stdin_recovery.py` — `add5e841df4bead308a96bd8b0f0e3c5475bd167` (unchanged)
- `tui_gateway/ws.py` — `653c1c3b6a934e642b3e330b5f790d1d0a358e1d`
- `tui_gateway/methods_prompt.py` — `103184b70bda36cc6a42d4ed4d0872f11d3887f5`
- `tui_gateway/methods_session.py` — `30b6fdd24a6178795a4450378895a14caa571ff0`
- `tui_gateway/server_requests.py` — `c38d7ed5fca2339610c8df6314532026efc45da0`
- `tui_gateway/rpc_dispatch.py` — `b2a2587c42dab0b09a24b66e842910f36abfb539`
- `tui_gateway/prompt_turn.py` — `0cc90dee8a2bb548230fdee9c36a92e635a76898`
- `tui_gateway/contracts/` (tree) — `5bc7227aacba6bdcbfd505d3bd2ccd8381167a5b`
- `hermes_state.py` — `7e6483dd022d73db5ee4c7daf96a6a91826c5ec7`
- `hermes_state_sessions.py` — `488d2718775e3ebc320c66b02d7d0bf0a4e68cd1`
- `hermes_state_common.py` — `33a35606368e4c37a2970f43138b219a018ee834`

The release ships no binary artifact, so the catalog records no digest. The
checkout declares its dependencies only for Python 3.14 (every pin in
`pyproject.toml` carries `python_version >= '3.14'`), so an older interpreter
installs the package with none of them. The runs below used CPython 3.14.6
(sha256 `0826ccaa8dd19bd84a5f0ffdbe84558f036e3b7d8ce7725704a7393995392129`)
in a virtualenv holding the release checkout installed editable.

## How the difference was established

1. **The contracts registry was dumped at both tags** (every method's params
   and result, every server→client request and every event payload, as JSON
   Schema from the pydantic models) and diffed. Events go from 73 to 77,
   server requests from 13 to 14 and methods from 237 to 257; nothing the
   adapter sends or reads was removed.
2. **The v2026.9.24 corpus was validated against the v0.21.6 contracts**,
   frame by frame. Twenty of its twenty-two cases validate unchanged; the two
   that do not are `interaction-gates` and `interaction-expire`, whose
   single-question clarify is gone (below).
3. **The real gateway was driven** with a scripted loopback
   OpenAI-chat-completions provider, an isolated `HOME`, dead loopback
   proxies and no ambient credential, recording both directions verbatim:
   the four gates in one turn, a clarify withdrawn by interrupt, and an
   approval withdrawn by interrupt.
4. **Both adapters were run against it**: the Go process gates three times,
   and `oapx serve agent --backend hermes` three times through an approval and
   a clarify to `run.completed`, opened at reasoning level `low`.

## Wire differences against v2026.9.24

### Clarify is batch-only

The `clarify` server request lost its single-question form. Its params are
now `{session_id, questions, answers?}` with `questions` required, each entry
`{qid, question, choices?, multi_select}`, and `answers` (sent only on a
reconnect replay) maps a `qid` to a string or `null` for a skipped question.
The result lost `answer`: it is `{answers}` for the whole set, and a response
without `answers` is cancel-all. One question arrives as a one-entry batch
with `qid` `q0` (recorded). The clarify tool itself now takes only
`questions`: a model that calls it with a bare `question` gets
`questions must be a non-empty array` back in the tool result and no server
request is sent (recorded while preparing the corpus).

### Additions

- **Events.** `approval.cancelled`, `free_tier.challenge`, `projects.changed`
  and `voice.partial` are new. `approval.cancelled` is a session-less
  broadcast that names the pending approvals an interrupt, reap or teardown
  dropped (`session_lifecycle._announce_cancelled_gateway_approvals`, read
  from the source). Over stdio an interrupt during an approval still
  withdraws it with `request.cancel` (recorded in `approval-interrupt`), and
  no `approval.cancelled` frame arrived in that recording.
- **`message.complete`** gains `response_reused` and `response_transformed`,
  each a boolean or `null`.
- **`setup_choose`** is a new server request; the adapter does not map it, so
  it answers method-not-found as for any unmapped request.
- **Optional params** on methods the adapter calls: `session.create` gains
  `cwd_explicit`, `idempotency_key` and `service_tier`, `prompt.submit` gains
  `voice_turn`, and `session.resume` gains `inline_images`. The adapter sends
  none of them. `session.list` rows gain `_lineage_root_id` and
  `live_message_count`, and `session.resume` results gain catalog members;
  the adapter decodes both results leniently.

Unchanged: the `approval`, `sudo` and `secret` requests and every event
payload the adapter projects (`message.start`, the three deltas,
`message.interim`, `tool.start`, `tool.complete`, `session.info`,
`session.usage`, `error`, `request.cancel`, `subagent.complete`,
`btw.complete`, `background.complete`, `gateway.ready`), and the methods
`session.interrupt`, `session.steer`, `session.events.since`,
`session.close`, `prompt.btw`, `prompt.background` and `config.set`.

### The reasoning level set right after `session.create` is lost

Opening at a level and running a turn sent `reasoning_effort: "medium"` to
the provider, the config default, not the level asked for. The adapters set
the level with `config.set` straight after `session.create`. At v0.21.6 that
`config.set` starts the session's deferred agent build, and the build thread
reads the session's `create_reasoning_override` before `_set_reasoning`
stores it, while there is no agent yet for `_set_reasoning` to update
(`server._start_agent_build` and `_deferred_build_agent_kwargs`, read from
the source). A direct probe confirmed it: `config.set` right after create
lost the level, while the same `config.set` three seconds later, or
`session.create` carrying `reasoning_effort`, sent `low`.

## Findings the move did not cause

**The gateway sometimes hangs at exit after stdin EOF.** It logs
`[gateway-exit] stdin EOF (peer closed)`, returns from `main`, and then
blocks during interpreter finalization: `SessionDB.__del__` →
`hermes_state.close` runs an `import`, and the import lock is held by a
daemon thread that finalization has already frozen (faulthandler stack,
recorded). It happened in 3 of 14 runs of `TestHermesProcessSmoke`, in 2 of
11 probes that closed stdin right after `gateway.ready`, and in 1 of 4 that
closed it after `session.close`. The adapter's close is bounded: after
`ExitTimeout` it sends SIGINT, kills after two more seconds, and reports
`gateway did not exit on stdin EOF`, which is what the smoke gate saw.

## Adapter changes

Go (`go/adapter/hermes`) and Zig (`zig/src/adapter/hermes`), in parity:

- **Clarify.** The request decodes only the batch form; a single-question
  request, or one with no questions, is refused as an invalid gate. Each entry
  becomes a question with its own `qid`, and the answer is always
  `{"answers":{…}}`. Go decodes the replay `answers` map as strings or
  `null`.
- **Reasoning at open.** The adapter's own process factory sends the open's
  level as `session.create`'s `reasoning_effort` (`off` as `none`), and then
  sends the same `config.set` as before, which validates the level and
  refuses one Hermes rejects. A host that supplies its own Go
  `ClientFactory` keeps the old behaviour, since `ClientFactory.Start` takes
  no level; its signature is unchanged. The feature reason says so.
- **Vocabulary.** Go's closed event vocabulary admits the four new events as
  observed, and `message.complete` admits the two new members. The Zig
  reducer reads only the members it projects and needed neither.
- The capability revision is `hermes-v0.21.6-oap-v1`. Support levels are
  unchanged; only the `session.reasoning` reason changed.

## Corpus (`fixtures/adapters/hermes-v0.21.6`)

Recorded from the release gateway, frames kept as the exact wire lines, with
the host frames the adapter writes byte for byte:

| case | recording | what it pins |
|---|---|---|
| `interaction-gates` | approval, clarify, sudo, secret in one turn | the one-entry clarify batch and its `{"answers":{"q0":…}}` response |
| `interaction-expire` | clarify, then `session.interrupt` | `request.cancel` withdrawing a batch clarify |
| `approval-interrupt` (new) | approval, then `session.interrupt` | `request.cancel` withdrawing an approval, and the interrupted settlement |

Each recorded case now opens with the `session.info` (seq 1) that v0.21.6
emits straight after `session.create`, before the first `prompt.submit`.
v2026.9.24 sent it after the submit. Their mapping and omission entries reuse
the classifications the v2026.9.24 corpus gives each frame type. Their
expectations differ from v2026.9.24's only in the clarify question id (`q0`
rather than `answer`) and the revision.

The other twenty cases are carried forward: native frames, mappings and
omissions verbatim, provenance and revision moved, and each validates against
the v0.21.6 contracts. The Zig corpus driver now adopts a case's native
session id from its first `prompt.submit` before replaying any frame, as the
Go harness does, so a frame before the submit is judged against the right
session.

## Process gates

Against the release checkout, three runs each:
`TestHermesProcessAgainstChatMock`, `TestHermesProcessApprovalAgainstChatMock`,
`TestHermesProcessTakesALiveReasoningLevel`,
`TestHermesProcessReopensItsStoredSessionWithTheConversation`,
`TestHermesProcessListsTheSessionItStoredOnOneKeptGateway` and
`TestHermesHubReopensItsRecordedStoredSessionAfterRestart` pass. Before the
reasoning change, `TestHermesProcessTakesALiveReasoningLevel` failed with
`run 1 asked for reasoning_effort "medium", want "low"`.
`TestHermesProcessSmoke` passed in 11 of 14 runs, failing only on the exit
hang above.

`oapx serve agent --backend hermes`, opened at `low`, completed an approval
and a one-question clarify to `run.completed` three times; every chat
completions request asked for `reasoning_effort: "low"`, and each trace
passes `goap validate` and `oapx validate`. The backend entry needs `"args":
["-m", "tui_gateway.entry"]`: neither tree supplies the module.
