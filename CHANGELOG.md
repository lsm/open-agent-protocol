# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

## [0.1.0-alpha.9] - 2026-10-06

### Merged pull requests

- **hub: route a live settings update to the session's adapter** ([#898](https://github.com/lsm/open-agent-protocol/pull/898)): Both hubs now take a live settings update at `POST /sessions/{id}/settings`, and `goap hub --stdio` takes it as the `settings` op.
- **tui: follow a queued follow-up over --attach instead of refusing it** ([#897](https://github.com/lsm/open-agent-protocol/pull/897)): `oapx tui --attach` can now queue a follow-up with Tab: the hub admits it queued, and the terminal runs it once the current run ends.
- **decisions: accept 0040 session reattach on its graduated evidence** ([#896](https://github.com/lsm/open-agent-protocol/pull/896)): Accepts Decision 0040: the reattach wire, the memory references, both conformance runners and every pinned adapter's reopen (or refusal of one) are on main, and both hubs share one binding file.
- **codex: resume a thread under the host's sandbox, approval policy and directory** ([#895](https://github.com/lsm/open-agent-protocol/pull/895)): Closes the last open item of #446 (owner decision, 2026-10-05).
- **tui: steer and compact over --attach through the hub's submit route** ([#894](https://github.com/lsm/open-agent-protocol/pull/894)): Part of #442 (hub attach parity).
- **hub: keep session bindings in a file oapx reads at reopen** ([#893](https://github.com/lsm/open-agent-protocol/pull/893)): Part of #447 (epic #442).
- **deepseek: advertise reopen as unavailable and decline it before a runtime starts** ([#892](https://github.com/lsm/open-agent-protocol/pull/892)): Part of #448 (epic #442).
- **opencode: reopen the bound server session after its last stored event** ([#891](https://github.com/lsm/open-agent-protocol/pull/891)): Part of #448 (epic #442).
- **hermes: reopen the bound stored session through session.resume** ([#890](https://github.com/lsm/open-agent-protocol/pull/890)): Part of #448 (epic #442).
- **providers: let an override move the Anthropic and Codex rows, and refuse one on Copilot** ([#889](https://github.com/lsm/open-agent-protocol/pull/889)): Closes the last part of #360.
- **providers: hold a stored vendor API key to the origins its OAuth token may reach** ([#888](https://github.com/lsm/open-agent-protocol/pull/888)): Part of #360; flagged in #880's review.
- **Pi: Reopen bound session files in Go and Zig** ([#887](https://github.com/lsm/open-agent-protocol/pull/887)): Both adapter trees reopen Pi’s recorded UUID and session file through `switch_session`, report recovered settings, and advertise capability revision v5.
- **auth: name the host an override sends a provider to** ([#885](https://github.com/lsm/open-agent-protocol/pull/885)): Part of #360.
- **acp: Reopen bound sessions through advertised load operations** ([#884](https://github.com/lsm/open-agent-protocol/pull/884))
- **providers: apply an override's models as the row's allowlist** ([#883](https://github.com/lsm/open-agent-protocol/pull/883)): Part of #360.
- **tui: end a compaction over OAP only once its run settles** ([#882](https://github.com/lsm/open-agent-protocol/pull/882)): Fixes the `compaction over OAP ... ends on its summary` flake (issue #10).
- **claude: Reopen sessions through their native bindings** ([#881](https://github.com/lsm/open-agent-protocol/pull/881)): Refs #448, #442.
- **providers: point a catalogued row at its override, and keep the row's stored credential home** ([#880](https://github.com/lsm/open-agent-protocol/pull/880)): Part of #360, per the owner's decision: an override's endpoint gets no row credential unless it says `forwards_credential: true`; environment keys are not gated.
- **zig: detect the Z.AI coding plan as Z.AI, on live evidence of its thinking wire** ([#879](https://github.com/lsm/open-agent-protocol/pull/879)): Closes #580, per the owner's decision: widen the Z.AI detection once a live probe confirmed the thinking wire.
- **tools: deny a read or write whose workspace_root lies outside the session's root** ([#878](https://github.com/lsm/open-agent-protocol/pull/878)): Closes #587, per the owner's decision: a model-supplied `workspace_root` must lie inside the session's root.
- **tools: let an MCP server's declaration, not a tool's name, set its permission tier** ([#877](https://github.com/lsm/open-agent-protocol/pull/877)): Closes #630, per the owner's decision: an MCP tool's name no longer grants anything.
- **zig: count a 402 from a coding plan's models endpoint as not subscribed** ([#876](https://github.com/lsm/open-agent-protocol/pull/876)): Closes #352, per the owner's decision on it: a 402 from a coding plan's models endpoint now counts as "not subscribed", like 401 and 403, so the plan is dropped for that key.
- **tui: steer a running turn over OAP** ([#875](https://github.com/lsm/open-agent-protocol/pull/875)): Closes #615.
- **serve: write an accepted run.cancel.response before the run.cancelled it confirms** ([#874](https://github.com/lsm/open-agent-protocol/pull/874)): Closes #818.

## [0.1.0-alpha.8] - 2026-10-04

### Breaking changes

- **opencode: change the reasoning level on a live session (0045)** ([#860](https://github.com/lsm/open-agent-protocol/pull/860))

- `go/adapter/opencode`: the `Client` interface gains `Session` and `SwitchModel`, so a custom `Client` must implement both.

- **adapter: compact the memory references for overflow past the reference window (0044)** ([#855](https://github.com/lsm/open-agent-protocol/pull/855))

- `go/adapter`: `CapabilityRevision` (the memory reference) is now `reference-memory-v17`.

- **serve: route the live settings update through both endpoints and the memory references (0045)** ([#850](https://github.com/lsm/open-agent-protocol/pull/850))

- `go/adapter`: `CapabilityRevision` (the memory reference) is now `reference-memory-v16`.

- **adapter: compact the memory reference adapters on their own past a threshold (0044, 0045)** ([#840](https://github.com/lsm/open-agent-protocol/pull/840))

- `go/adapter`: the memory adapter's `CapabilityRevision` constant moves from `reference-memory-v14` to `reference-memory-v15`, since its descriptor now advertises `session.compaction.policy` and a changed `run.compaction`.

- **adapter: execute compaction in the memory reference adapters** ([#837](https://github.com/lsm/open-agent-protocol/pull/837))

- `go/adapter`: `CapabilityRevision` is now `reference-memory-v14`, since the memory descriptor gains `session.compact` and `run.compaction`. A caller that pins the old value must move with it.

- **serve: order a steer's publication behind its response** ([#813](https://github.com/lsm/open-agent-protocol/pull/813))

`go/serve`: `Hub.Published(protocol.SessionID)` is new, and a `steered` admission adopts no run. `go/client` and `clients/ts` are additive.

### Merged pull requests

- **decisions: accept 0045 session settings on their graduated evidence** ([#872](https://github.com/lsm/open-agent-protocol/pull/872)): Flips Decision 0045 (reasoning level and compaction policy as session settings) from proposed to accepted.
- **codex: report the reasoning level a reopened thread resumed under (0045)** ([#871](https://github.com/lsm/open-agent-protocol/pull/871)): Decision 0045 says a reopened session reports the settings it resumed under, naming Codex's `thread/resume` `reasoningEffort` as the case.
- **tools: cut the built-ins to Shell, Read, Edit and Write** ([#870](https://github.com/lsm/open-agent-protocol/pull/870)): The TUI's 12 built-in tools become 4, picked from usage across 42 recorded sessions (5,654 calls; `shell_execute` was 90%).
- **tui: carry /compact and /autocompact over OAP (0044, 0045)** ([#869](https://github.com/lsm/open-agent-protocol/pull/869)): Closes #866, by a different route than the issue proposed: no native-wire spec change was needed.
- **decisions: record that the hub-served oapx adapter compacts in 0044's status** ([#868](https://github.com/lsm/open-agent-protocol/pull/868)): #865 merged after #867 accepted 0044, so its status line was stale.
- **decisions: accept 0044 compaction on its graduated evidence** ([#867](https://github.com/lsm/open-agent-protocol/pull/867)): Flips Decision 0044 (compaction) from proposed to accepted.
- **oapx: compact on request and at the session's threshold in the hub-served adapter (0044)** ([#865](https://github.com/lsm/open-agent-protocol/pull/865)): The `--backend oapx` adapter now serves 0044 under `oapx-agent-v5`, using compaction the in-process runtime already has:
- **tui: change the thinking level on an open OAP session (0045)** ([#864](https://github.com/lsm/open-agent-protocol/pull/864)): The TUI over OAP refused every thinking-level change once the session opened.
- **opencode: open without waiting for event-stream headers in the Zig port** ([#863](https://github.com/lsm/open-agent-protocol/pull/863)): OpenCode 1.18.34 holds the `/event` stream's headers until the first event, so `oapx serve agent --backend opencode` never answered `session.open` against a real server.
- **oapx: refuse a reopen on the default agent endpoint** ([#862](https://github.com/lsm/open-agent-protocol/pull/862)): `oapx serve agent` doesn't advertise `session.open.reopen` but answered a reopen of a never-opened session with a fresh session, failing `goap conformance`.
- **sdk: have the fake host wait for each tool_result before ending the run** ([#861](https://github.com/lsm/open-agent-protocol/pull/861)): Fixes a flake in `TestAgentRunReportsUnknownTools` (and the other tool-call tests): the fake host ended the run 20ms after `tool_execute` without reading stdin, so `Agent.Run` could return before the `tool_result` was logged (CI run…
- **opencode: change the reasoning level on a live session (0045)** ([#860](https://github.com/lsm/open-agent-protocol/pull/860)): Both OpenCode adapters serve `session.settings.update.request` for the reasoning level: `POST /api/session/:id/model` with the session's recorded model and the new variant, then `GET /api/session/:id` to confirm.
- **hermes: change the reasoning level on a live session (0045)** ([#859](https://github.com/lsm/open-agent-protocol/pull/859)): Both Hermes adapters serve `session.settings.update.request` for the reasoning level by re-sending the open's `config.set reasoning` between runs.
- **codex: change the reasoning level on a live session (0045)** ([#858](https://github.com/lsm/open-agent-protocol/pull/858)): Both Codex adapters serve `session.settings.update.request` for the reasoning level: it rides the next `turn/start`'s `effort`, which Codex keeps for later turns.
- **pi: change the reasoning level and compaction policy on a live session (0045)** ([#857](https://github.com/lsm/open-agent-protocol/pull/857)): Both pi adapters serve `session.settings.update.request` with the commands they already send at open, so `session.reasoning` and `session.compaction.policy` gain `session_live` under `pi-v1.0.1-oap-v4`.
- **oapx: take the reasoning level at open and on a live session (0045)** ([#856](https://github.com/lsm/open-agent-protocol/pull/856)): Decision 0045 names `oapx`'s endpoint as one of its two graduating implementations (Claude Code is the other).
- **adapter: compact the memory references for overflow past the reference window (0044)** ([#855](https://github.com/lsm/open-agent-protocol/pull/855)): Makes the `overflow` compaction reason executable (it was fixtures-only).
- **claude: change the reasoning level and compaction policy on a live session (0045)** ([#854](https://github.com/lsm/open-agent-protocol/pull/854)): First real harness for the live settings update (after #849/#850).
- **pi: serve a compaction request through Pi's compact command (0044)** ([#853](https://github.com/lsm/open-agent-protocol/pull/853)): Completes Decision 0044's step 3 (pi native evidence).
- **tui: show the session title in the terminal tab, the cwd row and zen** ([#852](https://github.com/lsm/open-agent-protocol/pull/852)): The session title was only visible in `/status` and the resume picker.
- **pi: publish Pi's own compactions as the run's compaction events (0044)** ([#851](https://github.com/lsm/open-agent-protocol/pull/851)): First half of Decision 0044's step 3 (pi native evidence).
- **serve: route the live settings update through both endpoints and the memory references (0045)** ([#850](https://github.com/lsm/open-agent-protocol/pull/850)): Second half of the live settings update (#849 put it on the wire).
- **validation: put the live session settings update on the wire (0045)** ([#849](https://github.com/lsm/open-agent-protocol/pull/849)): Adds `session.settings.update.request`/`.response` from Decision 0045: change a live session's reasoning level or compaction policy.
- **acp: move the pin to v1.10.2 and cagent v1.145.0** ([#848](https://github.com/lsm/open-agent-protocol/pull/848)): Moves ACP to v1.10.2 (schema-v1.24.1) and the gate agent to cagent v1.145.0, all latest stable.
- **opencode: move the pin to v1.18.34** ([#847](https://github.com/lsm/open-agent-protocol/pull/847)): Moves OpenCode to v1.18.34, the latest stable.
- **claude: pin 2.1.288** ([#846](https://github.com/lsm/open-agent-protocol/pull/846)): Moves the Claude Code pin from 2.1.282 to 2.1.288, with the TypeScript Agent SDK at 0.3.288 and the Python SDK at 0.2.163 (the newest; it bundles 2.1.286).
- **tui: tell a dirty worktree apart from an unverified one** ([#845](https://github.com/lsm/open-agent-protocol/pull/845)): Follow-up to #842, covering the two issues left out of that PR.
- **codex: pin rust-v0.160.0** ([#844](https://github.com/lsm/open-agent-protocol/pull/844)): Moves the Codex app-server pin from `rust-v0.157.0` to `rust-v0.160.0` (`a956835d`).
- **pi: pin v1.0.1 and admit its new message and event members** ([#843](https://github.com/lsm/open-agent-protocol/pull/843)): Moves the Pi pin from v0.87.1 to v1.0.1.
- **tui: offer a force remove when a session worktree is dirty** ([#842](https://github.com/lsm/open-agent-protocol/pull/842)): Deleting a session whose worktree has uncommitted changes was refused outright with a single line: *"Cannot delete this session: its worktree is dirty or Git could not verify it safely."* The guard is correct, but the dead end was not —…
- **tui: let /autocompact take a token count beside a share, auto and off** ([#841](https://github.com/lsm/open-agent-protocol/pull/841)): `/autocompact` can now also compact after a fixed number of tokens, alongside `auto`, a share of the window, and `off`.
- **adapter: compact the memory reference adapters on their own past a threshold (0044, 0045)** ([#840](https://github.com/lsm/open-agent-protocol/pull/840)): Before this, the memory reference adapters only compacted when asked, so threshold compaction (0044) was covered by validator fixtures alone.
- **serve: admit a compaction on the hub's submit op (0044)** ([#839](https://github.com/lsm/open-agent-protocol/pull/839)): A hub client couldn't ask for a compaction.
- **tui: zen long-reply scrolling, centre-out activity swap with a looping light, and a numbered trail** ([#838](https://github.com/lsm/open-agent-protocol/pull/838)): Zen polish, driven by real use.
- **adapter: execute compaction in the memory reference adapters** ([#837](https://github.com/lsm/open-agent-protocol/pull/837)): Step 1 of Decision 0003's gate for compaction (Decision 0044): both memory reference adapters now execute it, in Go and Zig. #829 landed the wire and validator without this step.
- **tui: slide zen's activity line up as it changes, and time only a stuck step** ([#836](https://github.com/lsm/open-agent-protocol/pull/836)): In zen, the line naming what the agent is doing no longer swaps in place.
- **decisions: accept 0013 steer on its graduated evidence** ([#835](https://github.com/lsm/open-agent-protocol/pull/835)): Flips Decision 0013 (steer) from proposed to accepted.
- **tui: test the zen note on every send path, and the end-of-run fade** ([#834](https://github.com/lsm/open-agent-protocol/pull/834)): Tests only.
- **release: write the 0.1.0-alpha.7 changelog section** ([#833](https://github.com/lsm/open-agent-protocol/pull/833)): Cuts `v0.1.0-alpha.7`; per `docs/releasing.md` the section lands on `main` before the tag is pushed.
- **validation: graduate the compaction wire surface in Go and Zig** ([#829](https://github.com/lsm/open-agent-protocol/pull/829)): Graduates the `+compaction` wire surface Decision 0044 proposes, tracking #613: the two envelope pairs in `schema/v0.1/` and the envelope `oneOf`, the Go protocol types in `go/protocol/`, the Go validator (`go/validation/compaction.go` and…
- **adapter: execute steer in the pi adapters** ([#824](https://github.com/lsm/open-agent-protocol/pull/824)): The third T4 slice: pi executes `delivery: steer` in both trees.
- **serve: order a steer's publication behind its response** ([#813](https://github.com/lsm/open-agent-protocol/pull/813)): Step 2 of Decision 0003's gate for [Decision 0013](decisions/0013-steer.md): publication ordering and buffering, so the admission response is observable before the settlement everywhere the unit is served.

## [0.1.0-alpha.7] - 2026-10-03

### Breaking changes

- **adapter: reopen a closed session in the memory references and both hubs** ([#820](https://github.com/lsm/open-agent-protocol/pull/820))

- `go/serve`: `SubscribeGate` is renamed `ElectionGate` and now also judges `reopen`. `UnknownSessionError` is an alias of `adapter.UnknownSessionError`, and `ErrUnknownSession` is `adapter.ErrUnknownSession`.
- `go/adapter`: `Memory` keeps its closed sessions in a map behind a mutex, so it is no longer comparable or safely copyable; use it through its pointer, as `NewMemory` returns it.

- **adapter: execute steer in the memory reference adapters** ([#812](https://github.com/lsm/open-agent-protocol/pull/812))

`go/adapter`: additive only — `ErrInvalidSteerTarget`, the `SteerReason*` constants and `InvalidSteerTargetError` are new, and `CapabilityRevision` moves to `reference-memory-v12`. No existing declaration changes shape; a host that pins the memory revision must re-read it.

- **validation: add the steer schema and conformance rules** ([#804](https://github.com/lsm/open-agent-protocol/pull/804))

`go/protocol`: adds fields to public request, response and active-run structs (unkeyed literals must be updated).

- **adapter: tell every adapter the envelope id of the submit it answers** ([#798](https://github.com/lsm/open-agent-protocol/pull/798))

- `go/adapter` — `Session.Submit` takes `SubmitRequest { Request, EnvelopeID }`.
- `go/serve` — `Session.Submit` takes `adapter.SubmitRequest`.
- `go/client` — `Session.Submit` gains a variadic `SubmitOption`, so `WithEnvelopeID` can name the submit envelope.
- `go/adapter/claude`, `go/adapter/deepseek`, `go/adapter/hermes`, `go/adapter/pi` — their exported `Session.Submit` follows `adapter.Session`.

### Merged pull requests

- **tui: keep tool titles in quiet mode, and add /zen** ([#832](https://github.com/lsm/open-agent-protocol/pull/832)): Quiet mode now keeps each tool row's title (description, command or path) on the row and still drops the command block.
- **adapters: take the reasoning level and compaction policy at open (0045)** ([#831](https://github.com/lsm/open-agent-protocol/pull/831)): Every pinned adapter now applies Decision 0045's settings when a session opens, in both trees, and reports them in the session state.
- **validation: put the session settings on the wire at open (0045)** ([#830](https://github.com/lsm/open-agent-protocol/pull/830)): Wire slice for Decision 0045 (#828), covering open time only.
- **decisions: propose reasoning level and compaction policy as session settings (0045)** ([#828](https://github.com/lsm/open-agent-protocol/pull/828)): Proposes Decision 0045.
- **codex: reopen a closed session by resuming its bound thread** ([#827](https://github.com/lsm/open-agent-protocol/pull/827)): Implements Decision 0040's reopen for Codex (#448) and reports the model the session resumed under (#458).
- **conformance: check that a reopen of an unknown session fails closed** ([#826](https://github.com/lsm/open-agent-protocol/pull/826)): Step 5 of #446 (Decision 0040): both conformance runners now send a reopen naming a session the endpoint never had.
- **tui: attach oapx tui to a running oapx hub over HTTP and SSE** ([#825](https://github.com/lsm/open-agent-protocol/pull/825)): `oapx tui --attach URL [--adapter NAME]` runs the terminal UI against a running `oapx hub` instead of an in-process endpoint.
- **tui: queue follow-ups over oapx tui on the OAP queue** ([#823](https://github.com/lsm/open-agent-protocol/pull/823)): `oapx tui` stops refusing a follow-up typed during a turn.
- **hub: run the clients/ts suite against oapx hub as well as goap hub** ([#822](https://github.com/lsm/open-agent-protocol/pull/822)): This closes #388's acceptance condition: the clients/ts integration suite now runs against `oapx hub`, with its assertions unchanged.
- **oapx: pass whether an event is terminal into prepareEvent** ([#821](https://github.com/lsm/open-agent-protocol/pull/821)): main does not compile at 99fdd6019f: #811 split `emit` into `prepareEvent` and `publishEvent`, and #817 added a `terminal`-gated `context_tokens` extension inside the envelope build.
- **adapter: reopen a closed session in the memory references and both hubs** ([#820](https://github.com/lsm/open-agent-protocol/pull/820)): Step 4 of #446 (Decision 0040): the reference backends in both trees reopen a session they closed.
- **validation: add the session-reopen wire and judge it in both trees** ([#819](https://github.com/lsm/open-agent-protocol/pull/819)): Step 3 of #446 (Decision 0040, `session-reattach`): the wire and its rules, with the reference and the conformance check still to come.
- **tui: answer tool approvals and report usage over oapx tui** ([#817](https://github.com/lsm/open-agent-protocol/pull/817)): In ask mode, `oapx tui` now shows the adapter's `action.permission.requested` as the usual approval prompt and answers with `action.permission.resolve.request`.
- **hub: serve oapx's own agent loop as a registry adapter** ([#816](https://github.com/lsm/open-agent-protocol/pull/816)): Adds an `oapx` registry type to `oapx hub`, built exactly as `oapx serve agent --backend oapx` builds it.
- **hub: serve the twelve routes and their event streams over HTTP** ([#815](https://github.com/lsm/open-agent-protocol/pull/815)): `oapx hub --addr` now answers every route in `drafts/hub.md` instead of 404, and serves many connections at once, so an open SSE stream no longer blocks other clients.
- **tui: /status reports session, model, usage, run, settings and auth** ([#814](https://github.com/lsm/open-agent-protocol/pull/814)): `/status` now writes one grouped report:
- **adapter: execute steer in the memory reference adapters** ([#812](https://github.com/lsm/open-agent-protocol/pull/812)): Step 1 of Decision 0003's gate for [Decision 0013](decisions/0013-steer.md): the memory reference adapter executes steer deterministically, in both trees, at parity.
- **adapter: Queue oapx runs in admission order** ([#811](https://github.com/lsm/open-agent-protocol/pull/811)): Advertise eight queue reservations beside one executing run at `oapx-agent-v3`.
- **tui: steer /model and hold run-dependent commands until the run ends** ([#810](https://github.com/lsm/open-agent-protocol/pull/810)): A pending switch is dropped if the model list is replaced, so the agent never holds a model from a freed list.
- **tui: steer or queue /compact during a run** ([#809](https://github.com/lsm/open-agent-protocol/pull/809)): `/compact [focus]` no longer refuses during a turn.
- **tui: reprint the scrollback after a verbosity change, with /redraw and ctrl+o** ([#808](https://github.com/lsm/open-agent-protocol/pull/808)): The clear also drops what the terminal showed before oapx started; `docs/tui-rendering-model.md` says so.
- **tui: list the declared providers with /provider list** ([#807](https://github.com/lsm/open-agent-protocol/pull/807)): `/provider add` and `/provider del` write `~/.oapx/providers.json`, but nothing reads it back to the user, so the only way to see what is declared is to open the file.
- **tui: control how much the transcript and status bar show with /verbose** ([#806](https://github.com/lsm/open-agent-protocol/pull/806)): `/verbose [quiet|normal|verbose]` sets five parts at once; `/verbose <thinking|tools|output|notices|status> <level>` sets one (e.g.
- **adapter: Route oapx permission and user-input prompts** ([#805](https://github.com/lsm/open-agent-protocol/pull/805)): Route ask-mode permissions and `request_user_input` through the oapx adapter.
- **validation: add the steer schema and conformance rules** ([#804](https://github.com/lsm/open-agent-protocol/pull/804)): First 0013 slice: steer wire types in schema, Go and TS; matching Go/Zig admission, target/refusal, settlement, terminal and recovery/capture diagnostics; 49 positive/negative fixtures.
- **tui: run the terminal UI over OAP as oapx tui** ([#803](https://github.com/lsm/open-agent-protocol/pull/803)): Step 1b of #375: `oapx tui` is the terminal UI as an agent-control-core client, beside `oapx --tui`.
- **tui: resume a session on the current model when its own is gone** ([#802](https://github.com/lsm/open-agent-protocol/pull/802)): Resuming a session whose saved provider/model isn't in the current list (provider renamed, signed out, model retired) no longer fails with `ModelNotFound`.
- **tui: delete a custom provider with /provider del** ([#801](https://github.com/lsm/open-agent-protocol/pull/801)): `/provider del <id>` removes the entry from `~/.oapx/providers.json` (other entries and `overrides` kept as written, replaced by rename), deletes the key `/login <id>` saved for it, and refreshes models.
- **zig: keep a stored key with the row it was saved for** ([#800](https://github.com/lsm/open-agent-protocol/pull/800)): Drops stored-key sharing between catalog rows that read the same environment variable (`opencode-zen`/`opencode-go`, the four Xiaomi regions).
- **adapter: serve oapx's own agent loop as an adapter (--backend oapx)** ([#799](https://github.com/lsm/open-agent-protocol/pull/799)): Step 1a of #375.
- **adapter: tell every adapter the envelope id of the submit it answers** ([#798](https://github.com/lsm/open-agent-protocol/pull/798)): Lands the prerequisite named in `decisions/0013-steer.md` ("**The blocker.**") and nothing else: adapters are now told the envelope id of the submit they answer.
- **tui: refresh models in the background, during a turn or not** ([#797](https://github.com/lsm/open-agent-protocol/pull/797)): `/model refresh` (and the refresh after `/login`/`/logout`) now always runs on its own thread; the status bar shows `refreshing models`, and the new list is swapped in once no turn is running, before a queued message starts the next one.
- **tui: give the session a working directory a cd changes** ([#796](https://github.com/lsm/open-agent-protocol/pull/796)): Closes #586.
- **zig: name the OpenCode Zen provider opencode-zen** ([#795](https://github.com/lsm/open-agent-protocol/pull/795)): Renames the provider id `opencode` to `opencode-zen`, so it reads as the Zen subscription next to `opencode-go` rather than the parent of both.
- **zig: send the thinking level to opencode, and /think max to deepseek as max** ([#794](https://github.com/lsm/open-agent-protocol/pull/794))
- **decisions: propose compaction as an optional unit (0044)** ([#793](https://github.com/lsm/open-agent-protocol/pull/793)): Proposes `+compaction` (#613, seam gap G6), so #375 can move the TUI's compaction onto the endpoint.

## [0.1.0-alpha.6] - 2026-10-01

### Merged pull requests

- **tui: declare a custom provider with /provider add** ([#789](https://github.com/lsm/open-agent-protocol/pull/789)): Adds `/provider add <id> <base_url> [--api <api>] [--env <NAME> | --no-auth]`.
- **tui: send queued messages as a new turn after esc aborts the run** ([#788](https://github.com/lsm/open-agent-protocol/pull/788)): Pressing `Esc` during a run used to discard every message still waiting: steers (`Enter`) and follow-ups (`Tab`).
- **build: install oapx by renaming a fresh copy over the old binary** ([#786](https://github.com/lsm/open-agent-protocol/pull/786)): Adds `make install`: it builds oapx, then puts it in `$(PREFIX)/bin` (default `~/.local/bin`) by copying to a temporary name in that directory and renaming it over `oapx`.
- **provider: read a delta's content even when it carries tool_calls: null** ([#785](https://github.com/lsm/open-agent-protocol/pull/785)): opencode-go replies served from its DeepInfra endpoint (`x-opencode-endpoint-id: deepinfra-dsv4.1flash`) were failing as "the model returned an empty reply".
- **provider: say what came back when a completions stream ends with no reply** ([#784](https://github.com/lsm/open-agent-protocol/pull/784)): "the model returned an empty reply" (and "the stream ended before the model replied") kept nothing the provider sent, so six of them on OpenCode Go past ~500k context could not be diagnosed.
- **release: write the 0.1.0-alpha.5 changelog section** ([#783](https://github.com/lsm/open-agent-protocol/pull/783)): The `0.1.0-alpha.5` section of `CHANGELOG.md`, generated by `scripts/changelog-release.mjs` per `docs/releasing.md`.
- **zig: one deepseek level table, and identity that survives a proxy** ([#758](https://github.com/lsm/open-agent-protocol/pull/758)): Moves the DeepSeek thinking facts into one place, and recognises an explicitly named DeepSeek vendor behind a proxy.
- **hub: preserve three #656-only records, verified against current main** ([#757](https://github.com/lsm/open-agent-protocol/pull/757)): Documentation only: **51 added lines in `drafts/hub.md`**, no source file touched, no test added, no production change, no compiled source modified — so no Zig build is claimed or needed.
- **validation: a mixed pack proves one refusal takes the pack's contributions, and record the contract** ([#747](https://github.com/lsm/open-agent-protocol/pull/747)): A mixed pack proves one refusal takes the pack's contributions with it.
- **go: validate both members before any local filter can skip an entry** ([#733](https://github.com/lsm/open-agent-protocol/pull/733)): The catalog boundary audit found the same defect the Python review found, still live in Go.
- **tools: keep results inline through 32 KiB** ([#727](https://github.com/lsm/open-agent-protocol/pull/727)): User-requested tool-output behavior fix.
- **zig: exercise the two degraded operations over real traffic** ([#715](https://github.com/lsm/open-agent-protocol/pull/715)): The complementary half of #365 slice 7.
- **shell: report the directory a command ended in** ([#704](https://github.com/lsm/open-agent-protocol/pull/704)): Slice 3 of the #586 working-directory series: `shell_execute` learns where its command finished.

## [0.1.0-alpha.5] - 2026-10-01

### Fixed

- **The OAP endpoint client could not spawn an endpoint outside a test build.**
  `endpoint_client.Client.spawn` handed `std.process.spawn` the io from
  `std.Io.Threaded.global_single_threaded`, which has no thread to run a child's
  pipes on, so every spawn outside a test binary failed with `OutOfMemory` from
  `Threaded.spawnPosix` before a process existed. The client now owns its own
  `std.Io.Threaded`, as `adapter/process.zig` and `tools/process_runner.zig`
  already do, and spawns on that. Nothing caught it because the tests took the
  other branch: `defaultIo` returned `std.testing.io` under `is_test`, so the
  suite exercised a path the product never ran, and the branch is gone rather
  than inverted, so the tests now drive the same io a release build does.

### Added

- **The Zig semantic machine judges a published tool source that carries an
  attachment-only member**, `attachment_field_in_catalog`, part of #367. A
  source published in a catalog — a `capabilities.response`'s `sources` or any
  `layers.*.sources`, an `action.tools.list.response`, a `session.open.response`
  or a session state document — is a *description* of a tool source, and
  `command`, `args` and `environment` belong to the attachment that *serves* it.
  A catalog that names one is publishing the attachment as though it were part of
  the source, which is the leak `descriptor-leaks-attachment-fields` and
  `tools-catalog-leaks-attachment-env` are about: an environment entry carrying
  a secret into a document every session reads. Go has judged this since the
  rule landed; the Zig machine declared neither the code nor the check, so a
  source carrying one earned no semantic finding. **`oapx validate` on either
  fixture is unchanged by this, and was never passing it**: in strict mode the
  schema phase refuses the member first — `toolSourceDescriptor` is closed — and
  the semantic phase does not run at all. The rule is unreachable in strict mode
  by construction, which is why the two fixtures are `mode: tolerant` in the
  manifest and why the semantic gate skips them: only once #367's tolerant mode
  exists is there a path that reaches this check, and judging the three tolerant
  fixtures rather than skipping them is that step's work. A source list the
  check cannot read is left unjudged, matching Go's decode rather than
  judging the entries around a malformed one.

- **A design note for the Go tree's native agent loop (#370).**
  [`docs/go-agent-loop.md`](docs/go-agent-loop.md) maps `zig/src/agent/`'s loop
  — turns, tool execution, permissions, cancellation, compaction — onto what
  the Go tree needs and in what order, and names the first slice: text turns
  and client-executed tool calls over `go/internal/provider`, served by
  `goap serve agent` with no backend so the parity harness compares it with
  `oapx`'s own loop, driven at the loopback provider #528 adds. It records the
  two rules the Go loop inherits rather than rediscovers: a run ends with
  exactly one terminal event, and a provider that refuses is a normal
  `agent_end` — what it settles as on the wire is the endpoint's decision, not
  the loop's. The note now also records that the first slice lands as four
  PRs, one concern each, in dependency order.
- **`go/internal/agent` gains the rule that decides a turn's fate (#370, first
  of four).** `TurnOutcome` reads a reply and says which of three things
  happens next: the turn failed, the run is answered, or the reply's tool calls
  run. It carries `oapx`'s cut-off rule with it — a reply whose arguments were
  truncated by the output limit is retried, and after three in a row the run is
  answered rather than retried forever — because a Go loop that ended a run on
  a different condition would be a parity divergence the harness reports as an
  unexplained order difference. It reads `provider.AssistantContent` and does no
  I/O, so it is the one piece of the loop that can be right or wrong on its own.

### Changed

- **Zig stops escaping `<`, `>`, `&`, U+2028 and U+2029 the way
  `encoding/json` does**, the last step of #417 under Decision 0038's amended
  parity section, which compares the trees by parsed JSON. #319 and #330 made
  `zig/src/json/writer.zig` and the ACP frame writer escape all five, so each
  became six bytes on the wire -- in prompts and tool output that is mostly
  code, where `if (a < b && c > d)` was being written as five escape
  sequences. No ledger at any pin records a harness reading those bytes, so
  under the amendment nothing earns the escaping; Decision 0032 says a Go
  runtime quirk is not protocol behaviour until a decision says so, and none
  did. The writer's own test now asserts the round trip rather than Go's
  spelling, and a new one pins that a string carrying all five parses back to
  itself.

  **Those tests had never run.** `json_writer` was a module with no
  `addTest` and no test root pulling it in, so its ten existing tests were
  compiled by nothing -- the same class as `provider_caps` before #534, and
  the reason a leak in the new one went unnoticed here. It is wired into
  `test` and `test-unit-core` beside its sibling `json_encode`, so all
  eleven run in CI now; the ten that had never executed pass unchanged.

  **The ACP frame writer moves off `gomarshal` onto `json_encode`**, which is
  already the module the adapter uses for the events it emits, rather than
  having `gomarshal` itself stop escaping. That is the narrowest correct
  change: `gomarshal` is shared with the OpenCode adapter, whose
  `port-goldens.json` records what the Go adapter sends -- including a prompt
  written as `a\u003cb\u003e\u0026c` -- and dropping the escaping there would
  change bytes a recorded golden expects. ACP writes only what it builds
  itself: ids are a string or an integer, because `parseMessage` refuses
  `InvalidID` for anything else, and the parameters are assembled from strings
  and integers, so no float reaches a frame and the two encoders cannot
  differ on a number. `TestBackendsMatchOapx` and
  `TestMemoryBackendMatchesOapx` pass against a freshly built `oapx`.

- **Kimi is served by the generic catalog loader, and the catalog says which
  region it defaults to.** Kimi was the one row the loader could not take, so it
  kept a credential lookup, a models URL, a cache name, a parser and a base-URL
  function of its own. A row with one endpoint per region may now record
  `default_region`; `kimi` records `china`, which is what the old code assumed
  in a `if` it never explained. `goap check` refuses a default naming a region
  none of the row's endpoints serves.
  A region comes from `KIMI_REGION` first, then from the region chosen at login,
  then from the row's default — so a user who chose Global at login is
  discovered against, and sent to, `api.moonshot.ai`. `KIMI_REGION=moonshot`,
  `cn` and `coding` still mean what they meant, because those are the words
  people type and matching the catalog's own names would have dropped all three
  quietly. The region is now resolved **once**, in one place, and every path that
  needs one asks for it: it used to be read and normalised three times over, and
  `KIMI_REGION="global "` — with a trailing space, from a `.env` file — sent
  discovery to the China models endpoint while the models themselves carried the
  moonshot base. A region value is trimmed before it is matched, and a value
  written into `auth.json` by hand is read by the same rules as one typed into
  the environment, synonyms and all. A row that ships one endpoint per region now
  also caches each region's discovered models under its own name, so a
  `KIMI_REGION` switch cannot serve the models one region listed against the other
  region's base for a day.
  **Kimi's real limits, its display name and its offline fallback come back.**
  The generic loader stamped every discovered row with a 128000-token context, an
  8192-token output cap and the model id as its name, so a Kimi run in the TUI
  silently asked for half the context it had and half the output it was allowed.
  Kimi is 262144 and 16384, its model is called `Kimi K2.7 Code`, and when its
  models endpoint could not be reached it served nothing at all where it used to
  serve one known model. A catalog row may now record `context_window`,
  `max_tokens` and a `models` list, with the same meaning a custom provider's
  fields have had all along: the row's two numbers are the defaults for its
  models, a `models` entry overrides them for one model and names how to show it,
  and a row that lists models still serves them when its own listing answers with
  none. `goap check` refuses a row that declares one model id twice, since the two
  entries would then disagree about which name and limits win. No row but `kimi`
  uses any of this yet.
  **A provider's own listing speaks for its models, and the row's numbers are only
  the default** — which is what `docs/custom-endpoints.md` has always said about
  the same two fields on a custom provider. The generic loader did not ask, and
  stamped its own guess on every model of every row, so a model whose listing
  reports a 1048576-token context and a 32768 output cap was served a quarter of
  the context and half the output. A model object is now read for its display
  name, its context window, its output cap, whether it reasons, and whether it
  takes images — under the names Kimi's listing used (`context_length`,
  `supports_reasoning`, `supports_image_in`) and the ones this repository would
  use, with a row's `models` entry, then the row's figures, then the generic
  default behind them. Reasoning and image input are what a TUI shows and what a
  request sends; no row's listing is asked for them today, because only Kimi's
  parser read them and that parser went with the bespoke loader.
  **A listing that reports a zero is treated as saying nothing**, for a context
  window or an output cap: a model served a zero-token budget cannot be used, and
  the row's own figures are a better guess than a provider's empty field.
  **A row may now say which credential outranks which.** The environment has been
  read before a stored login since the generic loader began, and a request is
  signed with the stored credential first, so a user with both a Kimi login and
  `KIMI_API_KEY` had their models listed under one key and their requests signed
  with the other, at the region the other one chose. `kimi` records
  `["stored", "environment"]`, which is the order its own loader used before the
  generic one; every other row keeps the default. `goap check` refuses a source
  it does not know, or the same one twice.
  **A Kimi login made in the TUI keeps working, which it very nearly did not.**
  The TUI stores an API key with its region as an *oauth* entry, because the
  api-key entry has nowhere to put a region. The old Kimi code accepted either
  shape; the generic credential lookup refused an oauth entry for a row that
  takes only `api_key`, so a logged-in user would have found Kimi serving zero
  models and its model refs unresolvable. The lookup now answers such an entry
  with its `access` as the key, and only when the entry carries **no refresh
  token** — a real oauth credential always has one, and that is what keeps this
  from becoming a hole.
  A refresh that fails now leaves the caller's error alone when *any* loader
  served something. The old guard asked only about Kimi and Anthropic, and with
  Kimi on the catalog path it could have fired while a dozen models were in
  hand.
  `oapx -p` and the smoke demo ask the same catalog row for everything they used
  to repeat: the region, its 262144-token window and its 16384-token output cap
  were each written out again in the print path and in the request default, and
  `oapx -p` carried a second copy of the region rules, synonyms included. A
  Kimi row that declared a different region, or different limits, would have been
  described correctly by the model list and wrongly by the path that sends the
  request.

- **`make build` and `make tui` build ReleaseSafe.** They built Debug, where Zig's debug allocator records a stack trace for every allocation: resuming a 50 MB session left the TUI unresponsive for over a minute, and a message sent later took 14 seconds to answer a keystroke. A ReleaseSafe build resumes the same session in about a second. `OPTIMIZE=Debug` still gives a debug build.

- **The CI fixture auth provider is served only when a test asks for it by
  name.** `oapx auth providers` returns the catalog's rows and nothing else
  unless `OAPX_TEST_FIXTURE_PROVIDER` is set to `1` or `true`, so a user running
  the binary is no longer offered a row called `Test Fixture (CI)` that cannot
  log in against anything. `1` and `true` opt in; `0`, an empty value, any other
  string, and a variable that is merely similar in name do not.
  The gate covers the **login** as well as the listing, which the first cut of
  this did not: the fixture's flow was hardcoded into the auth server and never
  consulted the provider table, so `oapx auth login --provider test-fixture`
  still started the fixture's prompt with the variable unset. It now answers
  `UnknownProvider`, and the opt-in is visible in every call site rather than
  hidden in a list.
  The SDKs' real-binary tests set the variable where they log into the fixture —
  the Rust suite through a new `real_builder_with_fixture` beside the existing
  `real_builder`, so a test that wants a plain user keeps exercising what a plain
  user sees. **A test that asked for the fixture and did not get it now fails.**
  That is the point of the change: in #486 the fixture was removed from the
  served list and `sdk/go/binary_smoke_test.go:185` *skipped*, so the loss of
  coverage looked like a pass. A skip is only acceptable for a precondition the
  test did not choose; here the test chose it, so its absence is a failure.
  The Rust and TypeScript suites each gained the other half — a test that asks
  for nothing and asserts the fixture is *not* offered — so the default is
  pinned from the consumer's side as well as the runtime's. Python's real-binary
  test iterates whatever providers it is given and never named the fixture, so
  it needs no variable and loses nothing.
  The Go suite lands with the others now that #492 has moved it: its
  `requireFixtureAuthProvider` is a `t.Fatalf` rather than a skip, and
  `TestSmokeAuthListProviders` asserts the row too, so "asked and got it" is a
  claim in Go as well as in Rust. Each SDK that names the fixture sets the
  variable, and each was found by running the suite rather than by reading it —
  the TypeScript demo test drives the login through the demo server, which
  spawns the runtime with its own environment, so the variable had to go in
  *that* spawn rather than in the test process.
  The decision is recorded on #354, where the owner chose this shape over
  serving the row always or removing it and changing what four SDKs assert.
- **The fixture opt-in is read portably.** The gate read the environment
  through a POSIX-only accessor, so `zig build -Dtarget=x86_64-windows` and
  `aarch64-windows` failed to compile — caught by CI's cross-compile matrix,
  and by running the same two targets locally before pushing the fix. The read
  goes through the same cross-platform path the rest of the tree uses, taking
  the environment as a value, which is also what lets a test drive it with a
  literal rather than by mutating the process.

- **`ProtocolClient.waitResultFor` and `waitResult` return a result the caller
  owns.** Both handed back a shallow copy of the message the client holds in
  `stream_results`, and `removeStreamState` deinits that message -- so a result
  kept past cleanup, which is the order `DESIGN.md` section 5.1 prescribes,
  held freed slices. `CLAUDE.md` warned about it and told callers to
  `cloneAssistantMessage` first, which is a rule about a shape rather than a
  fix: the return type is now `ai_types.OwnedMessage`, a deep copy with
  `deinit` and `intoMessage`, and there is no borrowed spelling to forget.
  The one production caller, the provider protocol bridge, cloned the result by
  hand before handing it to `complete()`; it now hands the owned message on
  directly. Two tests read the result *after* the client's own copy is gone --
  one through `removeStreamState`, one through `reset` on the legacy
  `last_result` path, which had the same aliasing -- and both fail against the
  old shape under the debug allocator.

### Added

- **`/resume` lists sessions by title, in local time.** A session was labelled with its model, provider and a UTC time, so switching models mid-session made it look like a different session. The label is now `title · local date and time · model`. The title starts as the first line of the session's first message; after the first reply, the current model is asked once, in the background, for a title of at most six words, which replaces it. Older sessions take their first message from the file.

- **`go/internal/provider` gains the `anthropic-messages` client, part of #358
  step 3.** It reuses step 2's SSE parser, event types, json tree and
  pre-transform, and it is **a different client rather than a variant of the one
  beside it**, so the parts that differ are called out rather than smoothed over:

  - **The key goes out as `x-api-key`, not `Authorization`.** A bearer header
    appears only under an oauth key -- one containing `sk-ant-oat` -- which is the
    only case that also adds two extra `anthropic-beta` flags, a fixed `user-agent`
    and `x-app: cli`. An **empty** key still sends `anthropic-beta`, so "no
    credential" and "no auth header" are not the same thing. A model may not
    displace a header the client already set.
  - **The wire is block-indexed**, with a `content_block_start`, deltas and a
    `content_block_stop` per block, so this client emits the four
    `text_start`/`text_end`/`thinking_start`/`thinking_end` kinds the openai one
    does not -- all thirteen of the union, against nine there. A block's content
    index is assigned at **start** from the number of blocks that have already
    completed, while the **wire** index only keys the map, so the two numbers
    differ; a delta for an index the map has not seen is dropped, and a block type
    the client does not model is skipped rather than failing.
  - **Under an oauth key the system text is prepended, not replaced**: the Claude
    Code sentence, a blank line, and then the caller's own prompt. A port that
    replaces it drops the caller's instructions on every oauth request that has
    one; the bare sentence is written only when there is no prompt at all.
  - **An image part means one thing to both writers**: base64 `data` and a
    `media_type`, as the oracle's single `ImageContent` does. The openai writer
    builds `data:<media_type>;base64,<data>` from them and the anthropic writer
    writes the two members directly, so one context serves both clients. The
    earlier shape — a URL plus a fidelity knob — meant different things to each
    and sent `media_type: ""` with a URL where base64 belonged.
  - **A user message that has parts is always a block array**, never a joined
    string — the openai writer flattens text-only parts and this one does not,
    so a port that shares the rule sends a different shape than oapx. A message
    with no parts at all is still a plain string.
  - **An empty system prompt counts as absent**, so no empty text block is
    written, and under oauth the bare sentence goes out with no trailing blank
    line rather than one followed by nothing.
  - **A tool call is a block inside the content array**, not a sibling member as
    in the openai body, and a tool result is a **`user`** message -- a whole run of
    consecutive results in one message -- whose `content` is a plain string for a
    single text part, an array for several or an image, and `""` for none.
  - **A signed thinking block is the only one that is not text**: without a
    signature it is written as a text block, with one it carries `thinking` and
    `signature`.
  - **The default `max_tokens` is `min(model / 3, 32000)`** and the order is
    `model`, `max_tokens`, `stream`. There is no `stream_options`: usage arrives
    in `message_start`, and **its cache tokens are kept separate rather than
    subtracted from the input**, which is the opposite of the openai path. The
    `total_tokens` backfill adds the input and the output only.
  - **An empty response is an error, not an empty text block** -- the openai client
    emits one empty text part. The message is the raw body's own error text when it
    is one, the byte count when it is not, and a fixed sentence otherwise.
  - **After the loop a synthetic blank line is fed to the parser** so a final frame
    that arrived without its trailing separator is recognised, and **only an error
    in it is acted on** -- a trailing delta is parsed and discarded.
  - **The anonymous rule excludes one vendor, `anthropic`**, where the openai rule
    excludes four. The two lists are separate on purpose: reusing the openai one
    here would let an anthropic model through anonymously.
  - **A `ping` on this wire is not handled** and falls through to nothing;
    keepalives come from the client's own interval.
  - The `is_oauth` branch of the shared pre-transform, which step 2 added behind a
    flag because its own path never sets it, is what canonicalises a tool name
    against the declared tools here -- so **#514** and **#515** are inherited
    directly by this client.
  - **The cache ttl and the thinking branches are complete or absent**: a long
    retention gets `ttl:"1h"` when the host is anthropic **or** the model's own
    compat says so; an adaptive model declares `thinking:{"type":"adaptive"}`
    before its `output_config.effort`; and a thinking budget is guarded by
    `max_tokens > 1024`, defaulted to 1024, and clamped to
    `[1024, max_tokens - 1]`, so a budget the api would reject cannot go out.
- **`/think` is back in the TUI**, as `/think [off|low|medium|high|xhigh|max]`: with a level it sets it, and alone it shows the current one. Shift+Tab still cycles the levels, and the status line now shows `off` instead of hiding the level.

- **A `max` thinking level, above `xhigh`.** It sends Anthropic's `max` effort, the effort `xhigh` already sent there; OpenAI's highest level, `xhigh`, or `high` on a model without it; and the largest budget or level elsewhere. The OAP provider profile has no `max`, so a provider reached through it gets `xhigh`.

- **`go/internal/provider`: the `openai-completions` client, part of #358 step 2.**
  A Go program can now drive an OpenAI-compatible endpoint without a Zig binary in
  the path. The package is `internal` on purpose: it is not yet a public surface,
  and `goap check` refuses an importable package that is in neither the public set
  nor `go/internal`, because every exported name in one becomes public API the day
  a program imports the module. It moves out when the serving layer that maps its
  events to the profile's envelopes lands.

  It is a **transcription of `zig/src/providers/openai_completions_api.zig`**, and
  the awkward parts are transcribed rather than tidied, because the point is
  parity in step 5:

  - **The SSE parser** keeps four rules the obvious implementations get wrong. A
    blank line is the separator and a line feed alone is not; a carriage return
    followed by a line feed is **one** delimiter, including when the two bytes
    land in different chunks; repeated `data:` lines are joined with a newline;
    and an `event:` line with no `data` emits nothing **but still leaves its type
    bound**, so a later `data:` inherits it. The limits are a mebibyte per line
    and four per event, and the event limit counts the type, the data and the
    separators between data lines.
  - **`isOpenAINative` and `isOpenAIHost` are two different functions** and are
    ported as two. The first matches the substring `api.openai.com` anywhere in
    the base URL; the second parses the URL and compares the host to `openai.com`
    with a label boundary, case-insensitively. They disagree for
    `https://api.openai.com.evil.example` and for a proxy carrying the name in its
    query. **#511 records the divergence and it is not fixed here** — it is a
    behaviour change in Zig and therefore the provider agent's to decide. What the
    port does is keep them apart: six decisions ride on the pair, and the
    detection gate is what discards the native caps for a URL matching the
    substring but not the host.
  - **The thinking member is named `reasoning_content` unless a signature comes
    back**, in which case the signature becomes the member's name. That is a wire
    quirk — the field name is data from the previous turn — so it is spelled out
    and tested rather than derived.
  - **The credential order is the caller's key, then the environment, then the
    anonymous rule**, and an empty value at either of the first two steps falls
    through rather than winning. A variable that is set but empty is therefore not
    an anonymous grant. The client takes an explicit key; the wider stored and
    oauth lookup stays with the caller, which is what step 4's discovery needs.
  - **The request runs the pre-transform before writing anything.** An unanswered
    tool call grows a synthetic `"No result provided"` result marked as an error,
    flushed before the next turn; a tool-call id is stripped at its `|`, truncated
    and sanitized to 40 characters on an OpenAI host, or hashed to nine characters
    for a Mistral host, with the matching result remapped so the pair still lines
    up; an aborted or errored assistant is dropped; and a thinking block from a
    **different** model becomes text while one from the same model keeps its
    signature. Without this an unanswered call goes out dangling and a long id goes
    out unnormalized. It carried a defect of its own, **#514**: the pending
    calls were keyed by the normalized id and the answered ones by the original, so
    on a Mistral host — where every id is re-hashed — an answered call grew a
    second, spurious error result. It was transcribed rather than corrected, and
    pinned that way, so the port held the defect in place while the Zig side
    decided it. Both are now fixed: #514 landed in `pre_transform` and the
    transcription here keys its answered set the same way.
  - **The event stream's thirteen kinds are the union, and this client emits nine
    of them** — `start`, `text_delta`, `thinking_delta`, `toolcall_start`,
    `toolcall_delta`, `toolcall_end`, `done`, `error` and `keepalive`, each
    carrying a content index and a partial. The four `text_start`/`text_end`/
    `thinking_start`/`thinking_end` kinds are the **anthropic-messages** client's,
    which has a wire that carries them; this one's does not, so nothing here emits
    them. The union is thirteen because the Go SDK's six `ProviderEvent`s cannot
    express the starts and the ends, and a serving layer cannot map what is not
    there. The SDK types are untouched: they remain the client API over the
    profile's envelopes.
  - **A keepalive is emitted on the ping interval, and every event is stamped.**
    The first one always fires when pinging is on, because the last-ping time
    starts at zero. The terminal message's usage also carries a **cost**, computed
    from the model's own per-million rates.
  - **A `usage` member that is present but is not an object leaves the accumulated
    prompt and cache totals alone**, while `output` is still reassigned, because
    that is the asymmetry in the source. Reading `usage` as a fresh struct per
    chunk would drop the totals a server already reported.
  - **`toolcall_end` carries a different content index than its `toolcall_start`
    did.** The start counts tool calls alone; the end is the position in the final
    array, which also holds the thinking and the text. The partial on that event is
    the content accumulated *so far*, so it grows with each one.

  Four behaviours are transcribed because Zig has them, and all four are pinned by
  tests so a later reader knows they are deliberate rather than accidents. The orphan
  check on a tool result runs only on the **first** of a run, so an orphan following
  an answered one is still written. A malformed chunk is **swallowed** rather than
  failing the stream, which is why the partial-text rule has no reachable caller here.
  A `reasoning_details` blob is **escaped** where oapx splices it raw, which keeps
  the body valid JSON when a signature carries a quote or a backslash — recorded as
  **#515**, with what step 5's parity run has to do about it. And an answered tool
  call growing a duplicate error result once its id is normalized is **#514**,
  transcribed and pinned there rather than corrected here. The first two are filed
  as **#513**; none of the four is fixed here.
- `oapx hub --stdio` serves the two catalog operations, `models` and `tools`, over the
  same transport objects `goap hub --stdio` serves. Both take `session_id` and
  `allow_degraded_features`; both answer a `models.response` or
  `action.tools.list.response` envelope stamped with the revision the **lister**
  served the catalog under, and both mint a response id and an `oap-request-N`
  correlation in that order, as the other ops do.
  A catalog refused `unsupported_feature` now names the `feature` and `reason` in
  `details`, an unlabelled one is `internal` — the code both trees answer, since
  `catalog_unlabelled` is the core's name and has no wire code of its own — and a
  mis-scoped one takes that op's own fallback, because it is the lister's
  error and not the caller's.
  `request_cancelled` is recorded as **D8** rather than mapped: `contract` has no
  cancellation signal and `hub.Failure` has no error for one, so a cancelled lister
  call arrives as whatever the adapter chose. Mapping it would have been a mapping
  the hub cannot actually reach.
  `tools` falls back to `tools_failed` and `models` to `internal`, each as Go's
  `toolsError` and `modelsError` do, and each names the feature it asked for —
  `action.tools.list` or `models.list` — rather than one shared answer for both. A
  mis-scoped or unlabelled catalog takes the op's own fallback, because it is the
  lister's error and not the caller's.
  A session that closes under a catalog answers `session_closed` once and is gone
  after, as Go does: it marks the session closed and still propagates the error.
- **The contested settlement has a fixture: `pi-two-open-calls`.** A terminal
  event sweeping several open calls at once had no fixture, and #433 step 4 was
  parked waiting for a hub that could serve one. The memory backend structurally
  cannot supply it — `resolvePermission` ends in `requestInput` and both adapters
  hold a single pending interaction, so no memory fixture can open two — so this
  is a second `pi` fixture whose scenario starts `read b` while `read a` is still
  running and **neither** finishes. Both trees then emit
  `action.call.requested`/`started` for the two calls, a `progress` for the first,
  and settle both `action.call.failed`, the first-started one first.
  To let a fixture name a backend other than its own directory, a fixture
  directory may carry a `backend` file; without one the directory name is still
  the backend, so every existing fixture is unchanged, and the subtest is now
  named after the directory, which is what keeps two `pi` fixtures distinct.
  This supersedes the note above that no fixture opens two interactions at once:
  it was true of the tree, not of the protocol.
- **`go/providercatalog` resolves a row's credentials, base and wire, the way the
  Zig tree does.** The package joined a catalog row to its two URLs and stopped
  there, so a Go provider runtime had a base and a key that each tree had to
  find by its own rules. `LookupCredential(catalog, env, stored, id)` now answers
  the key a request carries and where it came from: the row's own
  `credential_env` variables first, **in the order the row records them** —
  `anthropic` answers from `ANTHROPIC_AUTH_TOKEN` before `ANTHROPIC_API_KEY` —
  a variable that is set and empty counting as unset, and the first value held
  for a name deciding that name. A stored key answers next, and only for a row
  whose `auth` lists `api_key`; a stored OAuth access only for a row that lists
  `oauth`, so `openai-codex` and `github-copilot` are not handed an API key and
  `openai` is not handed a token. `NeedsNoCredential` reports the row that
  records `none`, which is how `ollama` says it. The row accessors beside it
  answer what the row records: `Status`, which is `supported` for a row that
  records none **and for an id the catalog does not hold**, `Offering`, which
  for the same unknown id is nothing, `CredentialEnv`, `BaseURLEnv`, `RegionEnv`,
  `ModelsEndpoint`, `OAuthOriginFor`, `Wires`, `Endpoints`. `BaseURL` and
  `DefaultBaseURL` pick the row's own endpoint for a wire and a region, with the
  rule that a region answers only an endpoint declaring that region and no region
  answers only an endpoint declaring none, and the default answering nothing at
  all for a row that records no `base_url_source` — the gate Zig's
  `defaultBaseUrlOf` puts there, which no live row currently trips but which a
  future row would, silently and in one tree only. `WireForModel` sends a model
  only the responses wire serves — `gpt-5-pro`, `o1-pro`,
  `computer-use-preview` and the rest — to `openai-responses` while every other
  openai model takes the row's first implemented wire. The URL half was already pinned by
  `providers/resolved_urls.json` in both trees; these rules are code, so they are
  pinned here, each read out of the Zig function it mirrors, with the premise
  asserted before the behaviour.
- **`go/providercatalog` applies a base-URL override, so a run can be pointed
  somewhere that is not the vendor's.** The package could *name* a row's
  `base_url_env` but nothing read it, and `OAPX_BASE_URL` — the global redirect
  `zig/src/provider_base_url.zig:17` reads ahead of every per-provider variable —
  appeared nowhere in the Go tree. Every base URL a Go provider runtime used was
  therefore the catalog's, and the `base_url_source` gate that is correct on its
  own is exactly what prevents a redirect: a loopback is not `api.openai.com`, so
  a static row answers with the catalog's URL and the override is never consulted.
  `BaseURLWithOverrides(overrides, provider, wire)` now answers an override and
  `ResolveBaseURL` prefers it over the catalog, in the order
  `baseUrlWithOverrides` (`:92`) uses: the global variable wins outright; otherwise
  the provider's own variable applies, and only when the provider **and** the wire
  both match, so `ANTHROPIC_BASE_URL` does not reach `openai` and a matching wire
  on the wrong provider is not an override either. A variable that is set but
  empty is not an override, as everywhere else in this tree, and the Kimi region
  accepts only the two the catalog records.
  Two rules read backwards before they were read, both of which would have failed
  quietly: `normalizeVersionedBaseUrl` (`:79`) **strips** a trailing `/v1` rather
  than appending one, because the stored base is versionless and the versioned
  route adds the segment later, and `usesVersionedRoute` (`:85`) keys off the
  **wire** with `github-copilot` excluded, not off the provider. An override for a
  versioned route is normalised to be versionless; one for `github-copilot` keeps
  its path and loses only its trailing slashes, which is what `:94` does on that
  branch.
  The variable names are read out of the row through `FirstBaseURLEnv` and
  `RegionEnv` rather than spelled in Go, so a catalog rename moves both trees —
  Zig gets its names at compile time from `baseUrlEnv(...)[0]` and cannot drift.
  The `openai` variable reaches **both** of that row's wires, `openai-completions`
  and `openai-responses`, because a responses model is reachable through
  `WireForModel` and a redirect that missed it would send the credential to
  `api.openai.com` anyway. The Kimi region takes the aliases the catalog's own
  test names — `moonshot` for global, `cn` and `coding` for china — folded
  **ASCII**-only, the way `std.ascii.eqlIgnoreCase` is: `strings.EqualFold` also
  folds `moonſhot` onto `moonshot`, and a value one tree accepts and the other
  rejects points the run at a different endpoint, which is a credential sent
  somewhere else. It falls back to the row's own `default_region`, which
  `DefaultRegion(catalog, id)` answers and which #507 put in the catalog for
  exactly this, when neither the environment nor the caller's region is usable, so
  a blank or unrecognised region selects an endpoint instead of refusing to
  resolve at all — and a row recording no default is still answered, from the
  literal the source falls back to. The region is kimi's alone:
  every other pair looks its endpoint up without one, which is what
  `catalogTarget` (`model_catalog.zig:459`) does when it passes a null region.
- **`docs/parity-job.md` says what the parity job is for.** The job drives the same requests through both trees and fails when the bytes differ, which makes it the last check before a divergence has to be settled by hand against a decision, a draft or a corpus. The note says which divergences that is the last check on — a payload the two trees decode differently, an answer one refuses and the other admits, and above all **the order of a run's events and the settlement order of two open interactions** — the latter uncovered by any fixture, and added by the entry above: the memory backend holds one pending interaction at a time, so it cannot open two, and the contested settlement #475 was written for — a terminal event sweeping several open gates, as the claude adapter's `sweepRun` does — had no fixture until `pi-two-open-calls`. What the `memory` fixture contributes instead is the only run long enough to read as a stream, through `TestBackendsMatchOapx`; `TestMemoryBackendMatchesOapx` compares a *sorted* set and is order-blind by construction — and which are covered cheaper elsewhere, because the fixtures are the oracle for the wire, the corpora for each harness, and the per-adapter tests for each adapter's own error handling. It says that every fixture which submits streams its run's envelopes, because the submit handler subscribes and pumps the run itself rather than the session-open `subscribe` member, and that `memory` is neither unique in doing that nor identical in both trees by construction — the two memory adapters are separate implementations, which is why the comparison scrubs `id` and every `*_ms` member. It has a row for the one divergence the "covered cheaper elsewhere" rule cannot place — what each tree writes to the harness, which needs a second tree to see at all — and it says which test does what — seven fixtures drive a `child.sh`, `opencode` answers the in-test fake HTTP server, and only one of the two memory tests looks at order — and it runs both tests in its own command, because running one is running half the coverage, and names the three CI jobs that run them — `backend-parity`, `memory-conformance`, and the twenty-fold `pi-parity-repeat` determinism gate. It also says what the job is *not* for: it is not conformance, not the harness's coverage, and not a race detector — one deterministic script per fixture cannot schedule a race — though a parity flake that recurs is race evidence, which is how pi's cancel divergence was found and why `pi-parity-repeat` exists. Each fixture gets a row saying which divergence it is the last check on, and `CLAUDE.md` and the README's paragraph that already promised "identical output" link to it. It lives in `docs/` rather than beside the fixtures because the parity test globs that directory and would read a README as a tenth backend.


- **`auth.providers.response` says how each provider accepts a credential.** The
  row carried an id, a name, a status and an optional last error, which
  describes a provider's *state* and not its *means*: an API-key-only provider
  and an OAuth provider are both `login_required` before anything has been
  entered, so a caller had no way to know which kind of login to start. Each row
  now carries `auth_kinds`, a non-empty ordered list of `api_key`, `oauth` and
  `none`. The first entry a caller can perform is the one to drive, and `none`
  is how a provider that needs no credential — Ollama — says so rather than
  omitting itself.
  The values come from the catalog's own `auth` array, so a row's kinds cannot
  disagree with the row it came from; the runtime reads one and sends it.
  The field is **required** with a `minItems` of one, which reserves emptiness:
  a conforming runtime never sends an empty list, so an empty one can only mean
  a runtime predating the field, and never "needs no credential". All four SDKs
  read a missing or unrecognised list as empty rather than failing the listing,
  because V1 evolution is additive-only and a new client has to stay usable
  against an older runtime — which is also what lets the two cases be told apart.
  Decision 0029's `auth.providers.response` row is amended, with the reasoning in
  [Decision 0043](decisions/0043-auth-providers-carry-how-they-accept-a-credential.md).
  This is a wire change, so it is the kind that breaks a client paired with an
  older binary. Each SDK was checked rather than assumed: the new field is
  absent-tolerant on read, and the fixture manifest gained a schema-invalid
  case so a runtime that omits it is caught at the boundary rather than in a
  user's terminal.

- **The duplicate-key JSON walk exists once.** Six adapters had their own copy — four byte-identical, two differing only in local names and where a `default` arm sat — plus a seventh call site that applied a different rule under the same name by delegating to a strict decode into a map. The walk is now `go/internal/jsonwalk.RejectDuplicateKeys`, the copies and the delegate are gone, and the **ten** call sites across nine entry points call it: each adapter's `ParseMessage` or `parseObject`, pi's and deepseek's `DecodeStrict`, opencode's event decoder and its HTTP response decoder, and deepseek's `carriesIntoATrace` — the one entry point with no wiring test of its own, because it asks whether a recorded trace is a wire the client can carry rather than whether a frame decodes. The property is tested once, in the package that owns the function, and is now **seeded from all seven harness corpora** rather than one adapter's; what each package keeps is a wiring test that its own entry point still refuses a key written twice, because a copy-paste regression would live in the call site and nowhere else. opencode's `native.RejectDuplicateKeys` export is gone with the copy — its one caller is in the same module and now calls the shared walk — and the weekly matrix runs the two shared targets in place of the thirteen that were per-adapter.

- **The Go SDK's OAP path speaks `protocol.Envelope` instead of its own envelope struct.** The SDK had a private `frame` for both dialects, and the OAP half of it hand-rolled a vocabulary `go/protocol` already describes: `session.open.request`, `inference.create.request`, `models.request`, `auth.login.start.request` and the rest were built as a `frame` with string members. They are now built and read as `protocol.Envelope`, so the SDK's control-plane wire is the same type every other Go package uses and the validator can be pointed at it. **The legacy V1 wire keeps the frame**, which is the honest split rather than a compromise: a V1 envelope carries `inference_id`, `stream_id`, `message_id`, `protocol_version` and a numeric `version`, none of which `protocol.Envelope` has members for, and the SDK's streamed events read `inference_id` off the frame it receives. One request stays on the frame for the same reason — `inference.cancel.request` names its inference in a member the protocol envelope cannot carry — which is a gap in the protocol package's coverage of the provider profile rather than in the SDK. The transport's write path is now shared by both dialects, and the fake host in the wire tests emits frames while the SDK under test sends envelopes, so the asymmetry is what the suite exercises. The real-binary smoke tests pass against a freshly built `oapx`.

- **The parity job now watches a run settle.** A new fixture, `testdata/parity/memory`, subscribes at open and drives the reference script through both of its interactions — the permission gate and the user input — so the run reaches `run.completed` inside the compared output: twelve envelopes under one `run_id`, each with a `sequence`, which is exactly the group `TestBackendsMatchOapx` compares in order since #475. The content comparison is a multiset and stays; the ordered comparison now has a run to look at, and the fixture is the first thing in the suite that would notice the two trees settling a run differently. The decision #433 asked to be recorded: **the run is observed over the endpoint's stdio binding, not over the hub's op set** — and the reason is one tree, not both. `goap hub --stdio` serves the whole op set the draft specifies, `open`, `submit`, `events`, `resolve` and `cancel` included, and a streaming driver against it carries a run to `run.completed`. `oapx hub --stdio` serves `adapters`, `sessions`, `capabilities`, `state` and `close` and answers `unknown_op` for the other five, so the hub cannot carry a run in both trees until the Zig side grows them. The endpoint's binding serves `session.open.subscribe` for any adapter that advertises it, and the reference adapter does, so a fixed scenario is enough and no new driver is needed. `exchangeWithChild` now starts its fake OpenCode server only when a fixture's registry asks for one with `@URL@`, so a fixture with no child script does not get one it will never talk to.

- **Nothing a user reads suggests installing `goap` any more.** Every run instruction now goes through `go run ./go/cmd/goap` — the README's `hub`, `hub --stdio`, `validate` and `conformance` examples, its `fixtures/packs/` line, `examples/README.md`'s pack invocation and `docs/go-library.md`'s check-you-work line, and the eight runnable-looking commands in `docs/oap-system-map.html` — an orphan page nothing links to, which is how it drifted, and which I swept rather than deleted, because whether it should exist is the owner's call — and the daemon section says up front that `goap` is this repository's own command, run with `go run`, and what the released `oapx hub` carries today (the stdio transport; `unavailable` for `--addr` and `--config`, naming which). `drafts/cli.md` no longer frames two binaries: its title is *One Verb Set*, it states Decision 0038 inline in the decision's own words — one released binary, and a library for every language — and the `goap` column is labelled *repository tool* and records what the Go tree carries so the trees can be compared, rather than presenting a second product. `CLAUDE.md` says the Go command is internal to the repository.

- **Go has one library.** The Go SDK moves from its own module at `sdk/go` into the main module as `go/sdk`, so a Go program that wants a client for a running endpoint imports the same module as everything else rather than a second one with its own `go.mod` and its own wire types. The exported surface is unchanged — `Client` with its `Auth`, `Models`, `Provider` and `Agent` namespaces, the same request and response types, the same typed errors — and the package name is `sdk`, because `makai` is the runtime's old name and nothing outside this repository imported it. Every caller-visible literal follows (`oap sdk: `, in the errors, the resolver and the transport's log lines), and the binary cache directory is `…/oapx/bin` rather than `…/makai/bin`, so a package that says the old name is retired is retired in its messages and on disk. **The zero-comment rule now has no exceptions at all**: the 31 files of `sdk/go` were the entire Go allowlist, and folding them in took the last entry with it, so `go/tools/nocomment`'s allowlist and its stale-entry check are gone and the floor for Go is zero like Zig's and TypeScript's. The documentation those comments carried did not go with them: everything a caller of the API needs — the four namespaces, per-call cancellation versus `Client.Close`, opaque `ModelRef` and session ids, what an endpoint refuses with `unsupported_feature`, and the typed error set — is now a section in `docs/go-library.md`, which is prose the policy does not reach. CI's separate `go-sdk` job, which ran its own Go 1.23 toolchain against `sdk/go`, is replaced by a `go-sdk-smoke` job in the main workflow that builds `oapx` and runs the SDK's real-binary tests, the only place the Go SDK talks to a real runtime. The SDK's private `frame` is still the one place Go hand-rolls an envelope; replacing it with `protocol.Envelope` is deliberately a separate change.

- **Every Go decoder that reads bytes from outside the process now has a fuzz target, and a scheduled job runs them.** The class is the rule: not the two decoders an issue happens to name, but every function that turns bytes another program wrote into a value. That is `validation.Validator.Validate` (a whole envelope trace), the hand-rolled duplicate-key JSON walk in six places, the adapters' native entry points (`DecodeNotification` for hermes and deepseek, `DecodeServerRequest`, `DecodeObservation` and `DecodeControlRequest` for claude, `DecodeEvent` for opencode, `DecodeStrict` for pi, hermes and deepseek), and the limit-bounded frame reader in the codex app-server codec. Each target asserts a property rather than merely returning: a refusal carries no value, an admission implies the invariant it was admitted for (an opencode event with a valid id, a supported type and a durable position; a codex frame that is a request, a result or an error and nothing else), a method the decoder does not serve is refused whatever the data says, and a decoder never hands back bytes that are not one whole JSON document. **A scheduled leg that names no target, and a target nothing schedules, both fail a test** (`go/internal/fuzzseed/matrix_test.go`): the matrix and the tree are checked against each other in both directions, and a leg is named by its package as well as its target so a failing job is identifiable. It earned its place immediately -- it found that `go/validation` already carried `FuzzValidateNeverPanics` and `FuzzApplyEnvelopeNeverPanics` and nothing ran them, and it found that two targets this change had added were later edited out of the tree while the matrix still listed them. **Seeds come from the corpus, through the catalog**: `fuzzseed.Corpus(harnessID)` resolves the current corpus of a harness from `harnesses/*.json`, so no version pin is spelled in the tree (the `pin_literal` gate would refuse it) and a new corpus version re-seeds the targets without a code change; `fuzzseed.Manifest` does the same for the 573 conformance fixtures, spread evenly across the corpus rather than taken from its head. The seeds are bounded and deduplicated, because a seed corpus runs on every ordinary `go test`. `.github/workflows/fuzz.yml` runs each target for 45 s on a weekly schedule and on demand, six at a time inside a 20-minute ceiling, and uploads the input that failed so it can become a fixture.


- The catalog loader serves the five coding-plan rows and OpenAI, each reachable
  by exporting its own key and nothing else: `ZHIPU_API_KEY`,
  `ALIBABA_CODING_PLAN_API_KEY`, `MINIMAX_API_KEY`, `TENCENT_CODING_PLAN_API_KEY`,
  `ARK_CODING_PLAN_API_KEY` and `OPENAI_API_KEY`. Twelve of the sixteen rows the
  loader could serve now are, with the gateways and DeepSeek. MiniMax is served on
  `anthropic-messages` and the rest on `openai-completions`, each the wire its
  catalog row records rather than a shape the loader assumed.
  All five coding plans are the `carries_version` shape in the hardest form: the
  base ends in a version segment *and* the `models_endpoint` is `/models`, so the
  version sits in the middle of the path. A listing that appended a second `/v1`
  — as the override path does for a versioned base — would ask
  `…/api/coding/paas/v4/v1/models` and get a 404, so the override test now walks
  all ten versioned rows rather than the six it did, and a new one pins the
  versioned base under an override exactly.
- **OpenAI's wire is chosen per model, and the reason is that the row has two
  wires and the product already knew which one each model needs.** The catalog
  lists `openai-completions` first, so a loader that took the row's first wire
  would serve every OpenAI model as a chat completion — but the built-in OpenAI
  descriptor has always served OpenAI on `openai-responses`, and the
  responses-only models (`o1-pro`, `o3-pro`, `gpt-5-pro`, `gpt-5-codex`,
  `gpt-5.1-codex-max`, `deep-research`, `computer-use-preview`) exist only on
  that wire. Discovery sees the whole listing at once, so the wire is decided per
  model id: responses for the models that need it, chat completions for the rest.
  That list was a private function in `makai.zig`; it now lives in
  `provider_catalog.zig` as `isResponsesOnlyModel` and `makai` calls it, so there
  is one list of which models are responses-only rather than two that could
  drift. A test asserts a single-wire row keeps its wire whatever the model is
  called, and another asserts the loader and the per-model rule choose the same
  wire for every row the loader can serve.
- **MiniMax's coding plan could list its models but not use them.** Its row is
  `anthropic-messages`, and that wire resolved a key only for `anthropic` — it
  named `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` literally and returned
  null for every other provider — so a discovered MiniMax model reached
  `error.MissingApiKey` on every run. A row's credential is now read from the
  names that row records, on every wire, in one place: the resolution lives in
  `provider_catalog.zig` beside `credentialEnv`, so the two request paths that
  need it share one implementation instead of each holding a list of vendor
  names to drift. `anthropic`'s own order is unchanged, because the catalog
  records `ANTHROPIC_AUTH_TOKEN` ahead of `ANTHROPIC_API_KEY`.
- **The override test now reads the URLs the product computes, not ones it does
  not.** It joined the overridden request with the version fact hardcoded and
  asserted `…/api/chat/completions`, but a model discovered under an override
  resolves the fact to false — the override base matches no catalogued endpoint
  and carries no trailing `/v1` — so the product requests
  `…/api/v1/chat/completions`. The test had blessed a listing and request
  disagreeing with each other that does not happen, which would have hidden one
  that does. It drives the loader's own resolution now and reads both URLs back
  off the model it builds, and a second test pins that an override base which
  *does* arrive versioned is normalised before the listing sees it, which is
  what keeps the two in step.
- The catalog loader serves the five gateway rows beside DeepSeek: OpenRouter,
  OpenCode Zen, Vercel AI Gateway, ZenMux and Deep Infra. Each is reachable by
  exporting its own key — `OPENROUTER_API_KEY`, `OPENCODE_API_KEY`,
  `AI_GATEWAY_API_KEY`, `ZENMUX_API_KEY`, `DEEPINFRA_API_KEY` — and nothing
  else, so five of the sixteen rows the loader could serve are now served. They
  are the first rows whose base already carries the API version, so they are the
  first to exercise `carries_version` end to end rather than in a unit test: the
  row's recorded fact decides the request path, and under an override the models
  listing is built the way the request is, so `OAPX_BASE_URL=https://proxy.example`
  sends OpenCode Zen to `…/v1/models` and `…/v1/chat/completions` rather than one
  of each.
  A gateway that advertises hundreds of models now appears in `/model` and the
  TUI picker in full. That is the honest outcome of discovery — the runtime does
  not second-guess what a gateway says it serves — and `providers.json` remains
  the way to narrow it, through the allowlist the custom-provider path already
  has. No row is filtered here, because a curated catalog has no opinion about a
  user's aggregator.
  **A listed model also has to be requestable**, which listing alone did not
  prove. Discovery read the credential through the catalog, so the models
  appeared; the request path's own env fallback named DeepSeek, OpenAI and Kimi
  literally, so all five new rows reached `error.MissingApiKey` and only DeepSeek
  worked end to end — for the wrong reason. That fallback now reads the names the
  row records, which is the same lookup discovery uses, so "export this row's key
  and it works" is true of the request and not only of the listing.
  Each row has its own test rather than one shared assertion: the set the
  production loader enables, each row's wire and version fact, and one that
  drives `loadProductionModels` with two rows' fakes and reads both models back,
  so "the row is enabled" and "the row's models appear" are separate claims with
  separate failures. I checked that second claim by removing OpenRouter from the
  loader and confirming two tests fail — the first version of these tests called
  the row loader directly and passed with the row absent, which proved nothing
  about what the product actually serves.
  The coding plans, Xiaomi's rows and OpenAI are separate steps. Xiaomi waits for
  #352, because a pay-as-you-go key must not surface a token plan it cannot use.

- An opt-in live smoke gate per catalogued row, so a row can earn `status:
  current` against recorded evidence instead of against a probe that only
  checked a path exists. `zig build test-e2e-provider-smoke` runs the four cases
  the issue names — one completion, a streamed completion asserted to arrive as
  more than one delta, one tool call, and one unknown model that must be refused
  — for a single row named by `OAP_PROVIDER_SMOKE`. DeepSeek is the row wired up
  as the worked example; the gate's own table is the seven `current` rows, and a
  hermetic test pins that table against the catalog so a row's promotion or
  demotion moves the gate with it.
  **The gate is opt-in twice over, and neither opt-in is a credential.** A
  credential's presence alone does not run it, which is the rule the harness
  gates keep and which the existing provider E2E tests do *not* keep — they skip
  on the key and nothing else, so a developer with `OPENAI_API_KEY` or
  `ANTHROPIC_AUTH_TOKEN` exported runs them by accident. This one reads no key
  until `OAP_PROVIDER_SMOKE` names a
  row it knows, and it refuses outright when `CI` is set, so a misconfigured
  runner cannot start spending a key. Both properties are exercised by running
  the step with a key exported and no opt-in, and again with the opt-in under
  `CI=true`.
  Nothing about a key reaches the output. The credential is resolved through
  `provider_credential.lookup`, so the row's environment variable wins over a
  stored one and neither is printed; a failure reports the row, the case and the
  error name. The base URL and wire are read from the catalog, so the gate
  cannot drift onto a base the catalog no longer pins, and a regional row — Kimi
  is the one `current` row that is regional — resolves through its `region_env`
  rather than a default the gate invents.
  The step is wired into neither `test` nor any `test-unit-*` group for the four
  live cases — it runs only when a person names a row — while the module itself
  is wired into both `test` and `test-unit-providers`, so the two hermetic tests
  that pin the gate's table run in CI and in a full local run alike. The pin is
  two-way: every listed row must be `current`, and every `current` row must be
  listed, so a promotion cannot reach the catalog without reaching the gate.
  **Redirect `HOME` as well as the keychain service.** `OAPX_KEYCHAIN_SERVICE`
  redirects the store but not `~/.oapx/auth.json`, so a run with only the
  service overridden falls back to the real file and spends a real key. I did
  exactly that while checking this and four live calls went out against a
  DeepSeek key; with `HOME` redirected the same invocation skips, which is the
  fourth gap in the keychain notes and the reason the command on the issue
  redirects both.
  No evidence is recorded here and no row is promoted: the live runs need the
  owner's keys, so the ledger, the per-row script results and the `goap check`
  rule requiring a ledger reference for every `current` row all land in the
  change that records the first reading.

- `auth.providers` is answered from `providers/catalog.json` rather than from a
  four-row literal. `zig/src/auth/providers.zig` hardcoded Anthropic, GitHub
  Copilot, OpenAI Codex and a CI fixture, so `listProviders()` never showed the
  other nineteen rows — OpenRouter, DeepSeek, Vercel, the coding plans, the
  gateways — and a caller could not learn they existed without reading the
  catalog by hand. The definitions are now the catalog's rows in catalog order,
  each carrying the row's id, its display name and its `auth` kinds, so an
  SDK can tell an API-key-only provider from one that also offers OAuth, and can
  tell a row needing no credential (Ollama) from one that needs a key. The
  values are read through `provider_catalog`, so a row that is added, renamed or
  re-authed appears without a second edit and `goap check`'s literal rule keeps
  a catalogued name out of code.
  A row the runtime cannot load yet is listed rather than hidden. Google and
  Azure have no models listing the loader can discover and no endpoint the
  catalog resolves, and Ollama's base is a local default; all three appear with
  whatever status their credential has, which is `login_required` until one is
  set. Listing a provider the runtime cannot yet serve is a fact an SDK needs —
  hiding it would make the list look complete when it is not.
  The CI fixture row keeps its place, last, after the catalog's rows rather than
  among them. I first removed it from the served list, on the grounds that
  `oapx auth providers` should not advertise "Test Fixture (CI)" to anyone
  running the binary — and that silently disabled real coverage: the Go SDK's
  binary smoke test skips when the fixture is absent, so the only
  CI-exercisable interactive login across the four SDKs would have stopped
  running without failing anywhere, and the Rust SDK's end-to-end login test
  failed outright. A fixture is a test affordance, but the served list is also
  the only channel a test has for reading a login back, so removing the row
  removes the test rather than the fixture. It stays a constant that is not a
  catalog row and has no credential path of its own.
  The auth kinds are carried on the definition here and travel on the wire in the
  next step: Decision 0029's `auth.providers.response` entry has only `id`,
  `name`, `auth_status` and `last_error`, so expressing an API-key-only provider
  needs that payload amended rather than approximated.

- `providers/catalog.json` records, per endpoint, whether its `base_url` already
  carries the API version, so a request path stops depending on a guess about a
  path segment. Today the join reads the base URL looking for any segment shaped
  `v<digits>` and, if one is there, drops the wire's own leading `/v1`. That is
  right for all 22 catalogued endpoints, which is why nothing here moves a URL —
  `providers/resolved_urls.json` regenerates byte-identical — and wrong for any
  base a user names, since only the endpoint's owner knows which it is. A proxy
  mirroring Anthropic under `https://proxy.example/api/v1/anthropic` serves
  `…/anthropic/v1/messages`, and the guess sends it `…/anthropic/messages`.
  A wire fixes its tail — `/chat/completions`, `/messages` — and across the
  catalog the only thing that varies is whether the base already includes the
  version, so that is the one bit recorded. The path stays in `wire_paths`, which
  is where it is one entry per wire rather than twenty-two; `models_endpoint`
  stays a literal because no wire defines a models path.
  The 14 endpoints that deduplicate today carry `carries_version: true`: the ten
  whose base ends in `/v1` — OpenRouter, OpenCode Zen, Alibaba's, MiniMax's, the
  three Xiaomi plans and Xiaomi, Vercel and ZenMux — plus three that end in a
  version other than `v1`, Z.AI's `/paas/v4` and Tencent's and Volcengine's
  `/coding/v3`, and Deep Infra's `/v1/openai`, where the version is not the last
  segment. The other 8 say nothing, which means the wire's full path is appended.
  Absent means absent: no endpoint is asked to repeat a default, so adding a
  wire with a versioned path needs no catalog edit.
  Two loaders carry the member, `Endpoint.CarriesVersion` in `go/providercatalog`
  and the generated `Endpoint.carries_version` in the Zig tree, and two refuse it
  where the fact cannot mean anything: `goap check` reports
  `provider_carries_version_without_versioned_path`, and the Zig generator panics
  naming the row and the wire, both for an endpoint on `openai-codex-responses`,
  `ollama` or `google-generative-ai`, whose wires append no leading `/v1`. Those
  two hold the generator's own list of versioned wires, so a Zig test re-checks
  every recorded fact against `wire_paths` as well: a fact the generator let
  through is still caught where the wire table lives. Nothing moves a URL —
  `providers/resolved_urls.json` regenerates byte-identical.
  At request time the fact reaches the join as `Model.carries_version: ?bool`,
  null meaning not stated, and a null resolves with one exact lookup of provider
  plus base URL against the catalog's endpoints, both sides trimmed the way the
  join trims. Nothing sets the field: the catalog loader, the Kimi, Anthropic,
  Codex and Copilot special cases, custom providers and every static model on
  the provider server all pin a base, and none of them can get the fact wrong,
  and neither wire carries a member — so the agent protocol's model keeps its
  deduplication through a decoder that was never taught a new field. A base that
  is not a catalog endpoint's resolves false, so an override never inherits the
  vendor's fact, and only `providers.json` states one.
  Two things change for a user. A custom base or a `*_BASE_URL` whose version is
  not trailing — `…/paas/v4`, `…/v1/openai` — now gets the wire's full path
  appended where it used to be deduplicated; the remedy is in
  `docs/custom-endpoints.md`, and it is `providers.json` with `carries_version`,
  because an environment variable cannot state a fact about a path. And a models
  listing under an override is built the way its request is, so a
  carries-version endpoint's listing gains the version its request gains:
  `OAPX_BASE_URL=https://proxy.example` sends opencode to `…/v1/models` and
  `…/v1/chat/completions` rather than one of each.
  A base that ends in `/v1` and states nothing is unchanged, and that is the
  documented convention rather than the guess this removes: a base the catalog
  does not hold resolves from a trailing `/v1` alone, so an override naming
  `https://proxy.example/v1` and a provider-protocol client sending
  `http://host:8000/v1` both still reach `/v1/chat/completions`. Only a *stated*
  fact skips that convention, and stating it also turns the read-time strip off
  in `providers.json`, so the strip and the join cannot both drop the same
  version. What is gone is the wider guess — a version segment anywhere in the
  path, `…/v4`, `…/v1/openai` — which is what mis-served a proxy under the
  vendor's own path shape.
  `Wire.dedup_version` and the version-segment scan are gone from both trees.
  `copilot_wire` keeps its suffix and loses only the flag, which had never done
  anything: its path is `/chat/completions`, with no leading `/v1` to drop.

- The TUI composer grows with the draft up to `min(12, height/3)` content rows
  instead of windowing a long draft with `…`; past that the window follows the cursor
  and muted `▲ N` / `▼ N` markers in the panel border count the hidden rows. Up/Down
  move the cursor one visual row inside the draft (keeping the goal column) and only
  walk history at the first/last row, while a recalled entry showing unedited still
  walks on any press. Home/End and Ctrl+A/E jump within the current line. Pastes
  normalise CRLF to LF, tabs render as `→`, and other control bytes render as caret
  notation so pasted escape sequences can never reach the terminal raw.
- [Decision 0038](decisions/0038-one-released-binary-and-a-library-for-every-language.md)'s parity section is amended: the two trees are compared by **parsed JSON**, not by bytes, and an exact byte comparison stays only where a harness's ledger records that the harness reads those bytes. No ledger at any pin records it — the two that discuss byte-exactness say the opposite, that a gate "must be structural, never byte-exact" — so the differential suite compares parsed data throughout, and a case that earns byte equality is named in the record and in its test. Byte equality is what made Zig copy `encoding/json`'s escaping of `<`, `>`, `&`, U+2028 and U+2029, which Decision 0032 does not make protocol behaviour. No code changes with the record.
- `go/binding` records what a host needs to reopen a session it opened, and `goap hub --bindings <path>` writes one record per open. A binding says which harness ran which session, under which pin, with which model and tool sources, in which home and — the directory the **adapter entry was configured with**, omitted when the hub does not know one, never the daemon's own — and it never holds a credential or a resolved environment value; a hub test opens a session and asserts the written bytes carry neither. The store is an interface the host supplies, with a file implementation beside it: an append-only log created `0o600` where **history is appended rather than replaced**, and **a torn write is detected and never read** — a partial line or a record whose bytes do not match its checksum is refused as `binding.ErrTorn` rather than half believed, because half a binding would reopen the wrong session. The repair keeps the longest prefix of records that decode, so a crash — or an edit of the host-owned file — cannot leave the log permanently unreadable, and it walks only the bytes it has not already validated, so appends stay linear in what they read; because a validated prefix cannot be re-checked for free, **a read that meets a record it cannot decode forgets the cached prefix**, so an edit the walk missed is noticed by the first read of that record and the next append repairs the log from zero rather than writing after it **Every record is written in the same critical section as the registry transition it records, and none is written for a transition that did not happen**: a registration records its open, a deregistration its close, a rollback records the close even when the adapter's close failed before `markClosed`, and a duplicate that never ran records **one `refusal` and nothing else** — so `binding.State`, which reads the last entry that claims to be a state, never reports a running session as closed. The store is read at reopen; the wire member that asks for one arrives with the reopen unit, so the read side is the store's own `Latest`/`History` for now.

- `zig/src/model_catalog.zig` grows a generic catalog loader, so a row in
  `providers/catalog.json` reaches `/model` and the TUI picker by being
  discovered rather than by being spelled out in code. The four rows the runtime
  loaded by hand — Codex, Kimi, Anthropic, Copilot — were every row it loaded.
  Sixteen other rows had an implemented wire, an endpoint and a models listing,
  and were still unreachable, so setting `DEEPSEEK_API_KEY` bought nothing. The
  loader is
  modelled on the custom-provider path in the same file, and a row is served
  when it has three things: a credential `provider_credential.lookup` resolves
  (an environment variable first, then the Keychain, per the order the epic
  decided), a wire one of the built-in wire modules claims, and a models URL the
  catalog composes. Discovery reads the models listing that URL names, caches it
  at `~/.oapx/model_catalog/catalog-<id>.json` with the same 24-hour preference
  and the same fetch timeout the custom path uses, and falls back to that cache
  however old when the fetch fails, so an outage cannot empty the picker.
  The cache name is deliberately not `custom-<id>.json`: a catalogued row and a
  custom row can share an id, and one row's listing must never be served as the
  other's. The built models carry the row's own id, wire and base URL, all read
  through `provider_catalog` — the row's id and the wire id are the two facts
  that stay literals, and everything else is data, so no base URL is written
  twice. A row with no credential, no claimed wire, no endpoint or no models
  listing is skipped rather than half-loaded, which is what keeps Ollama, Azure
  and the two OAuth subscriptions out until their own shape is decided. A
  catalogued row has no declared `models` list to fall back on, so a row whose
  discovery yields nothing contributes nothing instead of inventing entries. One
  row is enabled in this change, DeepSeek; the rest follow as their own steps.
  Both the DeepSeek row and the six values it needs are asserted from the real
  catalog, so a schema or a wire change that would quietly drop the row fails a
  test rather than a user's `/model`.
  A discovered model resolves its base URL through `provider_base_url`, so
  `DEEPSEEK_BASE_URL` still points a discovered row at a proxy. What shipped is
  the order `provider_base_url` already documents: `OAPX_BASE_URL` first, then
  the row's `base_url_env`, then the catalog. Pinning the catalog base on the
  model would have silently bypassed all three, which is the one thing a
  catalog-first precedence cannot do: the model carries a base, so nothing
  downstream consults the operator's environment again. The models URL follows
  the resolved base, because discovery must go where the operator pointed the
  row — otherwise a redirected row would send its key to the vendor for the
  listing and to the proxy for the request.
  A credential handed to a catalog row's discovery is copied once and zeroed
  when the copy is released, as every other credential path in the tree does.
  The bearer sent with the listing request is owned at function scope so it
  outlives the header list it is stored in.

- [Decision 0041](decisions/0041-cancel-acceptance-is-judged-when-the-cancel-is-checked.md) (proposed) amends 0001's "Cancellation intent is not settlement" on two clauses, and both validators change with it. **Acceptance is judged when the cancel is checked**, which a trace shows as the request's position against the run's terminal: an accepted `run.cancel.response` is illegal only when the request it answers (`in_reply_to`) arrived after the run's non-`run.cancelled` terminal, and a request that arrived while the run was live may be answered accepted after a natural completion. **A cancel response is unordered against the run's stream** — it carries no `sequence` and 0001 makes it not a terminal — so its position is never what makes it legal. A late cancel is a *legal request* with 0001's typed answer, so neither validator refuses the request itself; Go used to, and does no longer, which is what lets the two trees judge the same trace the same way. Three fixtures carry it: `core-cancel-accepted-after-natural-completion` (valid), `cancel-accepted-after-late-request` (semantic-invalid, `illegal_run_transition`) and `core-cancel-late-request-refused` (valid, the answer is `run_already_terminal`). The pi announcement order is explicitly *not* settled by this record and is not policed by it: both orders are legal, and the two trees differ in output order only.

- [Decision 0040](decisions/0040-a-session-reopens-through-its-own-binding.md) (proposed) answers T7's two open questions for `session-reattach`. A reopen is `reopen: true` on `session.open.request` — the unused `recovery` object is not reused — and it answers the session's state document with `recovery.recovered: true` and the model and settings the session actually runs under, which is the harness's recorded configuration rather than the loader's: Codex's `ThreadResumeResponse` requires six such members and the Go adapter dropped all six (#458). A create naming a bound id is `session_exists`, a reopen with no binding is `unknown_session`, and a harness that cannot load is `unsupported_feature`. A store the harness can no longer honour answers `unsupported_feature`, as 0039 already rules, and **no new code is proposed**: the code has to come from the binding rather than the harness's reply, because two of the ledgers record a harness creating a missing store before looking in it (Hermes mode `0o600` plus the schema, OpenCode's migration runner) and pi's discovery answers `null` either way, so for those a deleted database, a moved home and a session that never existed are one answer on the wire. Of the seven harnesses read at their pins, three type their own absence — Hermes `4007`, OpenCode `SessionNotFoundError`, DeepSeek `SessionPersistenceNotFoundError` — and four do not: pi's `null`, ACP's silence, Codex's `-32602 invalid_request` (its own ledger calls that "not a not-found code"), and Claude Code, whose answer is unrecorded. The binding is the host's record — never a credential, never a resolved environment value — written atomically, with a torn write detected and never read, and appended rather than replaced. No wire changes with a proposed record.


- `oapx hub` is the multi-session hub in the Zig binary, and `--stdio` serves it
  over the same transport objects `goap hub --stdio` serves. The hub and its wire
  are in the `oapx` binary for the first time, which is what the cross-compile
  check in the previous release could not reach.
  One loop owns the hub and waits on the readiest of everything it holds — the
  host's request stream and every open session's child — so a session and a
  request compete for the same cycle rather than for a share of it, per §8.6. A
  cycle that read a request drives the hub afterwards, so a child that went quiet
  during the read is not held back by the next one. On Windows, where there is no
  `poll` to wait on, the loop falls back to a blocking read of the request stream;
  that is a wait per input rather than a wait on readiness, and #460 replaces it.
  `--addr` and `--config` answer `unavailable` and exit non-zero, naming what is
  missing, because a host that asked for a transport this build does not serve is
  better told than handed a pipe that answers the requests that fit it.
  A host that closes the request stream ends the serve, and the serve *succeeded* —
  treating end-of-stream as an error would make every well-behaved host look like
  a broken one.
  A differential test drives one script through `goap hub --stdio` and
  `oapx hub --stdio` and compares the answers as parsed JSON, keyed by the
  request's `id` rather than by position, normalising only the envelope ids and
  timestamps the daemon mints. Four scenarios cover the five ops this wire serves,
  the refusals and their wording, a null parameter against a wrongly typed one,
  and a framing defect stopping the wire. It found four divergences the day it
  was written, all now fixed: the refusal for an undeclared parameter named
  neither the op nor the parameter, an unknown op said so without naming it, a
  missing `adapter` was refused as an undefined parameter rather than as
  `adapter is required`, and a missing `session_id` was refused at all where Go
  looks the empty id up and answers `unknown_session`.
- The TUI shows the working directory on a muted, right-aligned row under the status
  line. The path is sanitised, collapsed to `~` on a home-directory component
  boundary, left-truncated with `…` when it is wider than the terminal, and hidden on
  terminals shorter than 12 rows.
- `/help` now lists the full key map (send, newline, history, word and line edits,
  scrolling, abort and quit gestures) after the command list, and the empty-session
  welcome names what `!` does and points at `/help`.
- `zig/src/hub/stdio.zig` is the hub's stdio wire: strict newline-delimited framing,
  and the five operations it serves today — `adapters`, `sessions`, `capabilities`,
  `close` and `state` — over the transport objects the draft specifies. Framing is
  strict rather than lenient on purpose: a carriage return, invalid UTF-8, an empty
  line, a line over the frame limit, or a parameter an op does not define are all
  refused with a code, and a line over the limit is a defect rather than something to
  grow into. An op the frontend does not serve is a correlated refusal, not a
  defect, so a host learns which ops this build has without being told the pipe
  broke. Thirteen tests cover the framing, the five ops, the in-flight bound, and a
  refusal whose message is bounded so a long one still frames.
  `close` answers a bare `null`, and a repeated `close` is `unknown_session` rather
  than a second success — Decision 0039's close releases the session, so there is
  nothing left to close. The envelopes it mints carry the ids Go's stdio frontend
  mints, `oap-response-N` and an `oap-request-N` in reply to, spending the counter
  in Go's per-op order, so the two trees number the same answers the same way.
  They still differ in the order the members are written: Go's envelope struct
  puts `payload` straight after `id` and this tree's serializer writes it last,
  which the hub draft's divergence ledger records.
  A session's `created_at` is now RFC 3339 in UTC at whole-second precision with
  a trailing `Z` and no fractional part, which is what Go's `time.RFC3339` writes.
  It carried milliseconds, so the two trees wrote the same session differently in
  every listing and the difference was not in the divergence ledger. A sub-second
  remainder is truncated rather than rounded, as Go does, and the format is stated
  in the hub draft's `sessions` row.
  The module is now compiled for the Windows cross-compile targets, which the
  cross-compile never reached while it was outside the `oapx` binary.
- The TUI's `/compact [focus]` has the model summarize the conversation and replaces
  the history with that summary. The replaced messages are kept as JSONL transcripts,
  and every summary names all of them, so the agent can read back what a summary
  dropped. A resumed session starts from its latest summary. The unused compactor that
  truncated each message to 800 characters is removed.
- `contract.Session` grows an optional `readable` slot, so the hub's loop waits on
  every session's child at once instead of giving each a share of the wait in turn.
  The loop gave each open session at least 1 ms of blocking wait, one after
  another, so an idle session added its share to every cycle and a silent child added
  its to every other session's events. `claude`, `codex`, `deepseek`, `pi`, `hermes`,
  `acp` and `opencode` report the handle they already hold — a spawned child's stdout
  and a connected socket — and the hub polls them in one `std.posix.poll` and hands
  each a zero wait, so a cycle's only blocking wait is that one. A session with no
  handle keeps the timed pump, and the memory adapter has none to report because it
  has nothing to wait on. This is the model `DESIGN.md` §8.6 states, and it is the
  one contract change that model requires; `oapx hub` and the HTTP transport build on
  it. `Endpoint` in `zig/src/adapter/endpoint.zig` runs its own round-robin over the
  adapters it wraps and reports no handle, so the `serve` path is unchanged.
  Two properties of that loop are pinned, both on the hub's own accounting rather
  than on a clock: six idle sessions that report a handle are each handed a zero
  wait, so a cycle's only blocking wait is that one poll however many sessions
  there are; and a session with events queued in the same cycle as a silent one
  receives them, because the loop waits on the readiest rather than on each in turn.
  Where a platform cannot wait on a handle — Windows, whose only wait is a socket
  poll and a child's output is a pipe — no handle is used at all and every session
  keeps its share, which is the old loop and is correct if slower.
- `go/cmd/apidiffcheck`, and a CI job that runs it: an incompatible change to a public package fails unless the Unreleased section of `CHANGELOG.md` names that package's path in backticks — a delimited reference, so a word in prose or a longer path such as `go/serve/servehttp` cannot record a break in `go/serve`. The baseline is the newest `v*` tag, so it is whatever the last release was; additions never fail, because a compatible change is not a break. It counts every package outside `go/internal`, so a package *deleted* at head still needs a record — deleting a public package is the most incompatible change there is, and filtering on the head's own set would have let it through silently. The classification lives in `go/internal/publicset`, which `goap check` enforces the set with, so the two commands agree on what is internal. It reports the packages it could not account for rather than suggesting a version, because the version is the owner's to choose and the record is the obligation.
- `go/adapter` grows the loop a Go program writes to drive a harness: `RunToTerminal(ctx, session, request, options)` submits one prompt, reads the event stream, answers every `user.input.requested` and `action.permission.requested` through a policy function, resumes after an overflow, notices a stalled stream, and returns the run's final response, its text, its usage, its run id and the number of tool calls it started. An interaction policy answers the three things a run can ask of a client — `user.input.requested`, `action.permission.requested`, and the `action.call.requested` of a tool the *client* provides, which is resolved through the session's `CallResolver` and refused with a typed error when the session is not one — and is given the payload and, when the ask names a `tool_call_id`, the tool's name and arguments from the run's own `action.call.requested`, so a consumer decides by tool rather than by parsing the question's prose. Every answer is checked with `ValidateInputAnswer` before `Resolve`, an envelope at or below the last sequence is skipped, a `ReplayGap` or a session that offers no resumed stream ends the call with that error, and a failed, cancelled or silent run each returns its own typed error, as does a session that refuses an answer — the refusal's reason and response are on the returned error rather than lost behind a stall. This is library API: Zig has no counterpart (Decision 0038).
- `drafts/hub.md` decides [#53](https://github.com/lsm/open-agent-protocol/issues/53):
  stdio has no `unsubscribe` op, in either tree. The pipe carries every
  subscription at once and has no per-stream hangup, and the honest HTTP form of
  "drop the connection" is not expressible there — so an `unsubscribe` op would be
  the one verb the parity job could never check, which is the thing the stdio
  transport exists to prevent. A subscription ends at its run's terminal envelope,
  so a host that subscribes per run, which is the shape that fills a ceiling, has
  each subscription end on its own and without asking the hub for anything. Go also
  stops counting it the moment its run's reader exits; the Zig core still counts an
  ended subscription until a later event fans out to it or its consumer closes it,
  which is queued in [#399](https://github.com/lsm/open-agent-protocol/issues/399).
  `close`, with its `POST /sessions/{id}/close` counterpart, stays the one verb that
  ends a session's subscriptions, and a port may not add an `unsubscribe` op alone.
- `zig/src/provider_credential.zig` answers one catalog row's credential without loading anything. The row's `credential_env` variables are tried in the order the row records them, then a key in `AuthStorage`, then an OAuth access token, and a row that declares only `api_key` is never answered with an OAuth credential or the other way round. An environment variable wins over a stored key, an empty variable counts as unset, and `needsNoCredential` answers for a row whose `auth` accepts none, which is Ollama today. The environment is passed in as values rather than read here, so the function is pure and its tests touch no process state, no network and no credential.
- `providers/resolved_urls.json` is the table both trees check their URL resolvers against: every endpoint's base, its models listing and its request URL, in catalog order, with an absent member recording that the catalog resolves no URL there. `zig/build.zig` reads it at build time and `go/providercatalog` from the embedded file, so a tree that resolves a row differently fails on that row rather than on a user's machine, and `goap check` fails when the file no longer matches the catalog. `go run ./go/cmd/goap providers catalog-urls --format=json > providers/resolved_urls.json` rewrites it. The two trees each pinned the same twenty-two rows in a literal before this; one file replaces both.
- `go/providercatalog` resolves a row's URLs the way the Zig tree does. `ModelsURL(catalog, id, region)` appends the row's recorded `models_endpoint` to the endpoint serving that region, `RequestURL(catalog, id, wire, region)` appends each wire's path — dropping a version the base's path already spells, and treating a base that is already the full path as done — and `Resolve` returns every endpoint's base beside both URLs. Each answers `(url, found)`, so a caller can branch on the catalog not holding the row instead of comparing a URL with `""`. What the catalog does not hold: a row with no static endpoint, a wire this package does not join, a model-scoped wire, or a region no endpoint serves. A table over all twenty-two endpoints pins both URLs in this tree as the Zig table does, and the two agree value for value.
- `zig/src/hub/hub.zig`: the multi-session hub core in Zig, the layer
  [`drafts/hub.md`](drafts/hub.md) specifies and the piece `oapx hub` will serve
  from. It is a registry of adapters built from a `--config` document, many
  sessions per process each with its own bounded journal, fan-out from a run's
  stream to any number of subscribers with a bounded mailbox per subscriber — which bounds a
  resumed replay as well as a live stream — and no subscriber ceiling unless a transport
  sets one, cursor replay with `ReplayGap`
  rather than fake continuity, compound open with a subscribing registration that
  cannot lose the race with its own message, and the held subscription a transport
  whose response carries no stream adopts on the next request. A subscriber that
  falls behind is detached and told the run and position it last read rather than
  waited on; a subscription ends once it has been handed a run's terminal envelope, live or replayed, or once its cursor already sits at one;
  and the shutdown sweep cancels each session's live runs before closing it, so a
  child agent process is never left running. No transport yet: the operations are
  in-process methods, and #387 and #388 add the stdio and HTTP wires over them.
  `contract.refuseUnadvertisedOpen` is split so a hub can judge an open's own
  elections without the refusal that stops an endpoint from accepting a compound
  open's `message` — the hub submits that itself, above the adapter, as
  `serve.OpenCompound` does in Go — and `journal_capacity` from the registry
  document is now read rather than decoded and ignored. Six places where the Zig
  adapter contract cannot carry what the draft specifies are recorded as D2 to D6 in
  the draft, beside D1, which is Go's to fix. Forty-nine unit tests over a scripted
  backend, and `checkAllAllocationFailures` over every function that allocates and
  hands off ownership — which is what found three places swallowing `OutOfMemory`,
  three use-after-frees on a closed session or a borrowed run id, a stream-failed
  session that was never destroyed, an unbounded subscriber list, and a `pump` that
  gave adapters no wait, so no non-memory backend could make progress.

- [`drafts/hub.md`](drafts/hub.md) writes the multi-session hub's wire down as prose, so a Zig `oapx hub` has something to be built against that is not Go source. Under [Decision 0032](decisions/0032-go-and-zig-are-peers.md) the specification decides between the trees, and until this document the hub's HTTP routes, SSE framing, stdio transport objects, cursor rules, fan-out bounds and trust model were defined only by `go/serve` code, the README's daemon sections and `clients/ts`. Every rule names the Go test that pins it today, and the nine no test pins are listed as gaps rather than left for a port to discover. The draft carries [#53](https://github.com/lsm/open-agent-protocol/issues/53) — stdio has no host-initiated way to end one subscription — as a gap in both trees, and records one divergence for the Go side to fix: the `Host` allowlist refusal answers a bare `{"error": …}` with no error code, where every other refusal on both transports is a typed `error.response`. No wire, code or test changes.

- `zig/src/provider_catalog.zig` resolves a row's URLs from the catalog instead of leaving each one spelled where it is used. `modelsUrl(id, region)` appends the row's recorded `models_endpoint` to the endpoint serving that region, and `requestUrl(id, wire, region)` appends each wire's path the way its module in `zig/src/providers/` joins it — trimming a trailing slash, dropping a version the base's path already spells, and treating a base that is already the full path as done, each only where that module does. Both read comptime tables, so a request path is not spelled twice, and a row with no static endpoint resolves no URL at all. Google's wire is model-scoped (`{base}/v1beta/models/{model}:streamGenerateContent?alt=sse`), so it resolves no fixed request URL. A test pins all twenty-two resolved endpoints in a table, so a catalog edit that moves a base, a path or a region lands as a diff in review rather than as a failure on a user's machine.
- [Decision 0039](decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md) (accepted) gives a session an identity of its own that outlives every process, and makes the harness it runs on a recorded binding rather than its identity. Close detaches: it ends the harness process and releases what the endpoint held in memory, keeping only the binding record, so neither hub keeps a closed session listed. An open that asks to reopen a session loads it through the harness's own mechanism — Claude Code's `--resume`, Codex's `thread/resume`, ACP's `session/load`, Pi's session file — and is refused rather than answered with an empty session when it cannot. Persistence returns as three staged units, `session-reattach`, `transcript-load` (whose entries and cursor are OAP's vocabulary) and `session-list`, with the harness as the store of record until a later decision gives OAP a native store; continuing a session on another harness is refused until then. `drafts/hub.md` specifies that close releases a session; both hubs now do (#452, #455), so the record is accepted. A reopen is asked for with a `reopen: true` member on `session.open.request`, and the reopened state document reports the model and settings the session actually runs under.
- [Decision 0038](decisions/0038-one-released-binary-and-a-library-for-every-language.md) (accepted) makes `oapx` the only released binary, carrying the TUI, its agent loop, providers and login, the harness backends, and the `validate` and `conformance` tools another implementation needs; `goap` stays in the repository as an internal tool CI runs, never released. Parity between the trees becomes protocol behaviour rather than the command line. Each language gets a library in one of two shapes: TypeScript and Python stay thin SDKs over `oapx`, while Go, Rust and, when someone needs it, Java become native libraries that delegate to `oapx` only what they have not implemented yet and prove themselves against the shared schemas, fixtures, corpora and conformance runner. Go goes first: `sdk/go` merges into the main Go module on the shared `protocol` types. The hub stays a Go library and `oapx` gains it as `oapx hub`, both held to [`drafts/hub.md`](drafts/hub.md), which answers 0019's open question on whether `serve/` survives; [`drafts/cli.md`](drafts/cli.md) lists `hub` as an `oapx` verb not yet built. No code changes with the record.
- The curated provider catalog grows from ten rows to twenty-three, and a row now records what it *offers* and where it sits in the list: `offering` is `coding_plan`, `subscription` (GitHub Copilot and OpenAI Codex, reached by OAuth rather than a key) or `api_key`, and `status` is `current` (the providers the list names first: OpenAI, Anthropic, OpenCode, OpenRouter, DeepSeek, Z.AI, Kimi), `supported` (everything else it offers) or `withheld` (recorded, not offered — no row carries it yet). A row is a vendor's offering rather than the vendor, because a coding plan is a different endpoint reached under the same credential — `zai-coding-plan` and `xiaomi-token-plan-{cn,sgp,ams}` each read the same key as their pay-as-you-go sibling and serve it from their own host. The new rows are the plans Z.AI, Alibaba, MiniMax (the first plan endpoint on the `anthropic-messages` wire), Tencent and Volcengine, Xiaomi's three plan regions and its metered endpoint, and the multi-vendor gateways OpenRouter, opencode Zen, Vercel AI Gateway, ZenMux and Deep Infra. Google's single row answers both of its APIs, so the separate `google-gemini-cli` row and the `alias_of` member that stood for it are gone. A new row's `models_endpoint` is the path appended to its own base URL, so a base already ending in a version segment takes `/models`: probed per vendor, the doubled `/v1/v1/models` is a 404 on the six hosts that answer unauthenticated, while `base + /models` answers 200 or 401 everywhere. `goap check` fails an `offering` or `status` that is not a known value; a test pins that every `current` row precedes every other, that no two rows name one host, that a subscription is reached without a credential name, that a composed models listing spells its version once, and that an origin policy belongs to an oauth row.
- A curated model provider catalog, `providers/catalog.json`, validated by `providers/catalog.schema.json` and read by both trees. A row records a provider id, its display name, the credential kinds it accepts and the *names* of the environment variables they arrive in, the wires it speaks, the base URLs it defaults to (with a region where a vendor ships one per region, as Kimi does), where its models listing lives, and the origins an OAuth credential may be sent to. Every member is optional: an absent member records that the fact is unknown, not that it is empty, and no credential is ever catalogued. Go embeds the file and `goap check` fails when a row is malformed, when one id is catalogued twice, or when Go or Zig source outside test code spells a catalogued base URL as a literal; Zig does not parse it at run time, because `build.zig` turns the same file into typed rows, so a base URL, credential variable name or origin policy the catalog does not hold does not compile. The ten ids `custom_providers` reserved, the base URLs in `provider_base_url`, `model_catalog`, the provider server's static catalog, the OAP built-in provider table, `makai -p` and the TUI's default model, the Anthropic and Kimi catalog fetches, and the `/login` environment scan all read it now. No wire, request or response changes; the values are the ones the source already spelled.

- [Decision 0036](decisions/0036-a-presentation-layer-is-not-evidence-for-its-own-profile.md) (proposed) holds that a presentation layer is not evidence for its own profile. `zig/src/tui/` embeds the layers below the presentation boundary in-process and never speaks the `presentation-control` profile, so it cannot be the profile's reference implementation, conformance evidence or proof that the wire is learnable; that proof is a consumer this project does not control. The record gives the profile a four-step gate, lands its first two steps together so a schema no producer has emitted cannot be called executable, keeps *executable* (the wire) apart from *graduated* (the profile), puts the reference projection in both memory backends with Zig first, and moves the TUI onto the agent control core before it moves onto this profile, so retiring the Makai v1 wire does not wait for the profile.
- [Decision 0037](decisions/0037-presentation-state-is-versioned-and-every-intent-is-idempotent.md) (proposed) settles the `presentation-control` wire rules that 0036's gate makes executable. An `epoch` names one target's revision numbering, so revisions stay sound across a control-layer restart without mandating persistence. Same epoch and revision mean same state: an update applies whole or not at all, every piece of minimum session state can change without a snapshot (`session.replace`, `composer.replace` and `timeline.item.remove` join the minimum kinds), and a control layer that drops a timeline item says so. A snapshot request subscribes its connection to the target's updates, in order, and names any change kinds the receiver applies beyond the minimum ones every receiver applies. `pending_prompts` is the single source for prompt content, identified by the core's `interaction_id`, and both resolve intents are minimum profile. Every intent is idempotent while control remembers its `intent_id`, a degraded feature needs the user's `allow_degraded` opt-in, and the minimum profile assumes one trusted user behind every attached presentation. The draft states each rule; no schema, validator or implementation change accompanies either record.
- [Decision 0035](decisions/0035-a-model-entry-publishes-its-facts-and-absence-means-unknown.md) (proposed) would let a `provider.models.list.response` entry carry what discovery learned and cannot say today — `cost`, `input_modalities` / `output_modalities`, `reasoning_levels`, `release_date`, `family` — and would give the response an optional `catalog` member publishing `complete` and `observed_at_ms`, so a provider can say its listing is a subset and a `fallback` source can be told from a fresh one. The `+models` binding is judged on `models.response` in the other profile and is left untouched, as is any change to it. An absent member means the fact is unknown, never a default; a catalog source URL, credential hints, per-mode body overrides and a refresh request member are all refused. No schema, type, validator, fixture or client change accompanies the proposal. The evidence is a new ledger, [OpenCode provider breadth and its model catalog](research/opencode-provider-catalog-mapping.md), which reads that project's provider layer at `dev` commit `696f41bc` and its model catalog (223 providers, 8179 models, one dated digest) and maps each fact to a place this profile already has, does not have, or refuses to have.
- `oapx serve agent --backend pi` accepts inline image parts and forwards them in Pi's native prompt (`images`), as `goap` does; the OAP content-part union in Zig now carries `image` with `url`, `data` and `media_type`, and the Pi parity fixture submits a text-plus-image message.
- `oapx serve agent --backend <acp entry>` passes attached `process` tool sources to `session/new` as MCP servers, refuses the attachments Go refuses with Go's wording, lists them in session state with the last run sequence as `transcript_cursor`, and now serves the Go descriptor under its revision `acp-v1.7.0-schema-v1.21.0-oap-v3`. The parity test gains an ACP fixture and now fails when goap answers fewer lines than the scenario sends, rather than passing two backends that both refused to start.
- A parity test drives `goap serve agent` and `oapx serve agent` against the same scripted fake child for each served backend and requires identical answers and identical bytes written to the child; CI runs it over every directory under `go/cmd/goap/testdata/parity/`: Codex, DeepSeek, Hermes, OpenCode and Pi. `oapx` session state now carries `transcript_cursor` for Codex, DeepSeek, Hermes and OpenCode, and for OpenCode also `active_runs` and `as_of`, as `goap` reports them. Both trees now mint a Pi run's `submission_id` when the run starts rather than when the submit returns, so its number no longer races the dialog ids minted beside it. Endpoint refusals now carry `goap`'s wording (`adapter: ...`, `no session "<id>"`), carry the `tool`, `source` and `detail` a backend's refusal names, and for DeepSeek `oapx` now accepts several messages and text parts as `goap` does, answers with the harness receipt as `submission_id` and the request's own message ids, and refuses a submit addressed to another session `run_not_found`.
- `oapx serve agent --backend pi` reads Pi's `get_state` on every state request, as the Go adapter does, and closes the session when it names another native session. Its state reports the last run sequence as `transcript_cursor`. Like Go, it refuses a `get_state` with no session, a negative count, an unknown queue mode or an unknown thinking level. Its `session.state` and `run.reconciliation` reasons now match Go's.
- The TUI queues a follow-up with Tab while a turn runs; Enter still steers the running turn. A queued message waits above the composer and is sent when the turn stops, and the hint line and placeholder name both keys. Shell tool rows show the command they run under the description, highlighted and wrapped to the width, and the model picker filters as you type.
- `oapx serve agent --backend claude` mints ids in `goap`'s order (turn, message, run) with the message id as `submission_id`, suffixes control request ids with four random bytes as `goap` does, and reports `claude_native_session_id` metadata and the last run sequence as `transcript_cursor` in session state. The Claude parity fixture covers a permission gate answered allow.

### Changed

- The TUI status line drops the `perm:` and `think:` labels in favour of bare values
  (`bypass`, `low`, …), hides the thinking segment while thinking is `off`, and moves
  the idle/streaming state to the tail. The context gauge and its percentage are
  coloured by usage band (green below 60%, yellow 60–75, orange 75–85, red 85+); when
  the row overflows, the context segment shrinks to the coloured percentage first,
  then segments drop whole by priority (turns, thinking, cost, hint, `ask`
  permission, queue, drops, model, backpressure, context) with the state segment —
  and `bypass`/`pending` — never dropped. The post-backpressure drop counter no
  longer renders as `drops:drops:N`.
- The inline flush budget counts the composer at its one-row minimum (like a modal),
  so a growing composer covers transcript rows instead of flushing them and a
  shrinking composer no longer leaves blank rows behind.

### Fixed
- **Resuming a long TUI session no longer reads every streamed chunk, and no longer stops at 64 MB.** A session file held every text, tool-call and provider chunk, each line repeating the session's details: one 50 MB session was 84% chunks, a resume parsed all of it, and a file over 64 MB refused to load. Chunks now go to `<session>.stream.jsonl`, which a resume does not read; the details are written only when they change; and `<session>.meta.json` records the last completed compaction, so a resume starts there, with up to 256 KB before it for the screen, and reads the whole file when that compaction's record does not load. Older files still load, skipping their provider events, tool-call deltas and tool progress unparsed, and nothing that was written before is dropped.

- **A shell command's output under 10 KB now reaches the model whole.** With compact output on, which the TUI turns on by default, `shell_execute` stored every output as an artifact whatever its size and returned only a summary: about 430 bytes of retrieval instructions, then the first and last 512 bytes. Output under 1 KB came back about three times its size, output between 1 and 10 KB lost its middle, and the model often had to call `artifact_retrieve` next to read it. The shell tool no longer takes `compact_output`, and only output over its 10 KB limit is stored as an artifact.

- `contract.Session`'s `models` and `tools` now report the revision the lister served
  the catalog under, beside the catalog itself, as Go's `base.Catalog` and
  `base.ToolCatalog` do. The hub stamped the answer with the **adapter descriptor's**
  revision and never learned what the lister thought, so a lister serving a catalog
  under one revision while its descriptor claimed another was silently restamped
  with the descriptor's. An empty revision is refused, which is Go's own rule: an
  adapter that serves a catalog with no capability revision is a backend failure.
  Both trees answer `internal` for it — `catalog_unlabelled` is the core's name
  for the condition and has no wire code of its own.
  `endpoint` now stamps the lister's revision on the envelopes it forwards rather
  than the descriptor's, and refuses an unlabelled catalog rather than passing one
  through, so a session mediated by an endpoint is checked the same way a direct one is.
  A lister that reports the session closed now releases the session and answers
  `session_closed`, as Go does: it marks the session closed and still propagates the
  error, so the answer is that once and `unknown_session` only on the next operation.
- A session open's `metadata` now reaches the adapter. `contract.OpenRequest` and
  `hub.OpenRequest` carry it and the hub forwards it, as a parsed value, which is
  what Go hands an adapter. It could not be forwarded at all: the core had no
  member to carry it, so a metadata-carrying open had nowhere to put it between
  the wire and the adapter. Accepting one and dropping it is the endpoint's
  behaviour, in both trees, and is recorded as D9.
- **`docs/oap-system-map.html` is kept and made true.** The page was an orphan with nothing linking to it, and everything countable on it had drifted: it said 546 fixtures where `fixtures/manifest.json` lists 576, 53 diagnostic codes where `go/validation/diagnostic.go` declares 57, twelve schema documents where `schema/v0.1/` holds thirteen, and it showed eleven conformance units where `drafts/conformance.md` lists twelve — `+provider-attach` was missing from the chips. The status ladder was behind the tree: **semantic is complete** (`nothing_outstanding` in `zig/src/validation/semantic_gate.zig` is empty) and **all seven adapters are reachable behind `oapx serve agent --backend`**, so the page now carries a hub row instead — `oapx hub --stdio` serves `adapters`, `sessions`, `capabilities`, `state` and `close` and answers `unknown_op` for the rest — and names #496 for the differential. The commands table was wrong about the released binary: `oapx serve agent provider` is `oapx serve agent,provider --stdio`, `oapx run` only enters the agent loop with `--agent`, and `oapx hub --stdio` and `oapx validate` were missing; the hero now says `oapx` is the released binary and that `goap` is run with `go run ./go/cmd/goap` and never installed. The SVG's harnesses are pinned to catalogued versions rather than to one commit (Decision 0033), and the vendor box says it shows four of the catalog's 23 rows. The trace section and the `goap validate --format=json` transcript were checked against `fixtures/valid/tools-permission-completed.json` and the command's real output and are unchanged — every key and value the page shows is in the fixture, field for field. The README links the page and says to open it in a browser, and its SDK line now names three SDKs in `sdk/` and the Go one at `go/sdk/`.
- **A run no longer stops by itself after 100 turns.** The agent loop capped a run at 100 model turns when its caller set no limit, and the TUI never sets one, so a long task ended right after a tool call, with no reply and no message. A run now goes on until the model answers without calling a tool or you cancel it, as pi-mono's loop does. `max_iterations` still sets a limit for a caller that wants one.
- **The agent loop runs the tool calls a reply carries, whatever stop reason it reports.** It used to act on the stop reason alone, and the OpenAI Responses and Google providers never report `tool_use`, so a reply calling a tool through them ended the run without running it. A reply reporting `tool_use` with no tool call now ends the run instead of asking again. A tool call cut off at the output token limit is not run: the model gets an error asking it to call the tool again with complete arguments, and the run goes on. A fourth cut-off reply in a row ends the run, and the TUI now says when a run ends on a reply cut off at the output token limit.
- **The duplicate-key JSON walk no longer refuses a valid message because of a number it cannot hold.** The walk exists to catch a wire that disagrees with itself, and `encoding/json`'s token reader turns a number into a `float64` by default — so a well-formed integer literal longer than a `float64` can represent (`1e999`, or 400 digits) made the walk return a range error and the adapter reject a message that is perfectly valid. The deepseek copies already asked for `UseNumber()`; acp, claude, codex, hermes, pi and opencode did not, so six adapters refused valid traffic in six slightly different ways, and opencode's copy is exported as `native.RejectDuplicateKeys`. All of them ask now, and the copies that remain are [#489](https://github.com/lsm/open-agent-protocol/issues/489)'s to collapse.

- The Zig endpoint now **drains a session's events before it answers a `run.cancel`**, so everything an adapter emitted while handling the cancel — the `run.status.updated {status: cancelling}` announcement the Go adapters emit, and any terminal a harness settles inside the round-trip — reaches the stream before the acknowledgement, which is Go's order. The two trees disagreed here: goap announced `cancelling` and then answered, while oapx answered and then announced, which the order-aware parity comparison found as soon as it stopped sorting its input. Both orders are legal under [Decision 0041](decisions/0041-cancel-acceptance-is-judged-when-the-cancel-is-checked.md) — a cancel response is unordered against a run's stream — so nothing was wrong with either; aligning them is what makes the parity output comparable, and it is a change in `zig/src/adapter/endpoint.zig` rather than in an adapter, because the adapters' only flush point is the drain the host calls. The pre-drain defers a frame failure to the read loop that already owns it, so a cancel cannot turn a serialisation error into a serving error.


- `oapx hub --stdio` now passes the hub a clock in **nanoseconds**, which is the
  unit the hub's contract is in and the one it divides to build a session's
  `created_at`. It was being passed milliseconds, so every session opened through
  the verb was stamped 1970 — a plausible timestamp rather than an obvious failure.
- The hub's stdio serve loop no longer reads the host's pipe when a *session* woke
  it. The wait collapsed the whole polled set into "did anything wake", so a
  session producing output also woke the cycle into a blocking read of an idle
  host's pipe, which is the stall the loop exists to avoid. It now answers one
  question — is the input ready — and drives the hub either way.
- The serve loop's per-cycle poll arrays are freed again. The scratch arena was
  handed to each cycle and never reset, so a daemon accumulated a poll's worth of
  arrays every cycle for the life of the process.
- A framing defect exits **non-zero** and names the line that caused it, bounded to
  512 bytes and marked as cut beyond that: a defect report is not the place to
  reproduce the defect at whatever length the host chose. A final line the host
  never terminated is reported as a framing defect rather than dropped, so a host
  that sent half a request is told rather than left wondering.
- A caller that cannot give the loop a handle to wait on now gets a read of its
  input per cycle. It used to be told nothing was ready, and with no sessions open
  that was forever: a loop that waits for input it never reads.
- `oapx`'s usage text now names the `hub` verb.
- `compat.stdio.pipe` now refuses on Windows instead of failing to compile there.
  It has never been compiled for a Windows target until the hub's stdio module
  imported `compat`, and the `compile-hub` step has been reporting it since — I read
  my own measurement of that step as clean and carried on, which is the failure the
  step was built to prevent.
- The hub's serve-loop tests read a scripted request stream rather than a real pipe.
  A blocking read of an OS pipe under the test runner's I/O passes on macOS and
  hangs on Linux, so `Unit Tests - hub` was killed after six minutes — a test that
  only ever *polls* a pipe in this job had never read one, which is why nothing
  had hit it before. The double is scripted, so the loop, the framing, the defect
  path and end of stream are all still covered and the job terminates.
- A request line that never arrives no longer buffers without bound. The frame
  limit was only consulted once a newline turned up, so a host that never sent one
  could grow the daemon's buffer until it ran out of memory; the buffered bytes are
  now bounded by the limit and a line over it is a framing defect naming what was
  buffered.
- `oapx hub --stdio` now notices a host that closed its pipe on Linux. An empty
  pipe whose writer has closed polls as `POLLHUP` without `POLLIN`, so the wait saw
  "no input" and the daemon never learned the request stream had ended — it ran
  until it was killed. A hangup means a read will return end-of-stream, so it
  counts as ready. This one only shows on the platform whose `poll` says it that
  way, which is why the differential job that would have caught it is not wired
  into CI yet: it needs an `oapx` binary, and that is #390.
- The hub's differential test normalises what the daemon mints wherever it appears,
  rather than at the paths it appeared when the test was written. It reached into
  `result` and `result.payload`, and the sessions listing puts a timestamp inside an
  array instead, so a listing compared the two trees' clocks against each other.
- A host that stopped reading now ends the serve. A failed write propagated out of
  the op but was caught as if it were an unreadable request, so the loop kept
  reading a host that was no longer there, once per cycle, for as long as the
  process ran.
- The three reasons a hub's stdio serve can stop are reported as themselves: a
  framing defect names the line and says so, a failed read says the request stream
  failed, and a failed write says the host stopped reading. All three exit
  non-zero. A read failure was reported as a framing defect, which is a thing that
  did not happen.
- The hub's stdio serve loop no longer stops driving the hub after the first
  request. It read until end-of-stream inside one cycle, so a host that stayed
  connected and had nothing more to say left every session undriven for as long
  as it held the pipe open — the sessions' children produced nothing, the journal
  did not move, and nothing timed out. One readiness, one read, then drive the
  hub, so a session's output is never held back by a request stream that is idle.
  End of stream is still the end of the serve, and a framing defect still stops
  it, so the two are still told apart.
- A refusal for parameters an op does not define now names all of them, in the
  order the wire declares them, rather than the first one found. A host that sent
  three it should not have is told about three.
- An agent run now always ends with exactly one event that ends it. A run that
  failed — at any point, including the two paths that returned before the run
  started — used to simply stop, so a consumer waiting for the run to end waited
  forever and had a second channel to remember to check for why. `AgentEvent`
  gains a `run_failed` variant and an `isTerminal` that names the two events that
  end a run; `runLoopThread` emits exactly one of them from its `defer`, so a
  failure after the run has already ended does not end it twice, and a second run
  on the same agent is not silenced by the first run's terminal. A provider that
  refuses is unchanged: it is a run that got far enough to end, so it ends with a
  normal `agent_end`.
  A run that failed also ends the TUI's stream as an error rather than as a
  completion. That reason was inferred from the last turn's stop reason, and a run
  that fails before a turn ends never set one, so the inference read a failure as a
  success — and a success is what drains the user's queued follow-ups, so a failed
  run discarded them. The reason is passed by the caller that knows it now, the same
  way compaction takes it from the outcome.
- Five adapters across both trees answer a `run.cancel` accepted on a live run with `cancelling` and no longer re-read the run's terminal after the native round-trip, so the answer depends on the acceptance rather than on how far the reader goroutine has got: `go/adapter/pi`, `go/adapter/codex/appserver` and `go/adapter/acp`, and Zig's `codex` and `pi`. Decision 0001 is the rule, and the pi parity scenario had flaked on the alternative at least five times (#10); it now runs twenty times over in CI. A run that is already terminal when the cancel is checked is still refused `run_already_terminal` — except a run that already settled `cancelled`, which is idempotent and still answers accepted `cancelled` where it did before (ACP and OpenCode in both trees, and codex in both trees by its own path) — and a settled run's recorded status is still not rewritten, so a run that settles *during* the round-trip no longer changes what the cancel answers. Zig ACP, Claude Code, Hermes, OpenCode and DeepSeek were read in both trees and needed no change: Zig ACP's status branch is the *pre-check*, so an already-terminal run is still refused before anything is sent, and DeepSeek's `run.cancel` is `unavailable` because its selected SDK wire has no cancel request at all.
- A long reply in the TUI is no longer cut off at two minutes with `Provider protocol
  stream timed out`. The in-process provider bridge counted its 120-second limit from
  the request, so a response still streaming at two minutes was ended mid-sentence. The
  limit is now an idle window that every event resets, as `oapx serve provider --http`
  already does, and the window is ten minutes on both paths, up from two, because a
  thinking model can stay silent for minutes before its first token. A provider silent
  for longer still fails.
- A closed session now leaves the Zig hub entirely, and every operation naming it is
  refused `unknown_session`. The core kept a closed session's entry, its journal and
  a cursor for every run it had, listed it as closed, and answered later operations
  with `session_closed` — so an id stayed taken, a `sessions` listing grew for the
  life of the process, and the memory did too. This is Decision 0039: close detaches,
  ending the session's subscriptions with a `session_closed` ending and releasing its
  journal, cursors, holds and state. `close` releases, so the id is free again, and
  `sessions` lists live sessions only. A session whose adapter reports itself closed
  is released the same way when it is next observed, and a `state` in flight at that
  moment answers the closed document the adapter reported rather than `unknown_session`
  — the host learns how the session ended instead of only that it is not there. The
  next `state` is `unknown_session`, the same as Go's, which ignores
  `ErrSessionClosed` and answers what the session last said.
  A run's stream failing is not a close and no longer acts like one. The hub ends
  the subscriptions and nothing else: it does not close the child, because
  `contract.Session.close` destroys the session, so a hub that closed the child of a
  session it meant to keep would answer the next request from freed memory. The
  session stays open and answerable until it is closed or released, and the close
  lands on that release. Scoping a stream failure to its own readers rather than the
  whole session is still outstanding, and that is D7.
- The Zig hub's subscriber ceiling no longer counts subscriptions that have
  already ended. `max_subscriptions` bounds a session's subscriber list, and an
  ended subscription stayed on that list until the next event happened to fan out
  to it or its consumer closed it — so a host that subscribes per run and reads
  each run to its terminal, which is the shape that fills a ceiling, filled it with
  finished subscriptions and was refused `busy` for a session nothing was watching.
  A subscription now leaves the list at the moment it ends: when its consumer reads
  the terminal envelope, and, for a replay that already overflowed, before it is
  ever added. Neither needs a `close()` from anyone, so the ceiling of 64 bounds
  live streams, as Go's already did when its last reader exits. An event that
  arrives for a settled run still reaches a live subscription, so a late loss is
  still reported against the run the subscriber is following. The handle itself is
  the holder's until it calls `close()`: an ended subscription costs no slot, but
  the hub keeps the handle rather than freeing one its holder may still close.
- An `anthropic-messages` `base_url` ending in `/` no longer requests
  `//v1/messages`. The anthropic builder trimmed the base only to test whether the
  path was already present and then appended the untrimmed base, so a user who wrote
  the trailing slash got a doubled one; the `ollama`, `openai-responses` and
  `openai-codex-responses` builders concatenated with no check at all, so the same
  base produced `//api/chat` and `//v1/responses` and those wires also doubled a
  base that already ended with their path. A trailing `/` is now ignored on every
  wire, and a base that already ends with its wire's path is used as it is. The
  four builders had become copies of the catalog's own join with the three flags
  each had drifted on, so they now call it: one rule, no per-wire `trim` or
  `idempotent`, with `dedup_version` and `model_scoped` left as descriptor data.
  `go/providercatalog` gets the same rule, and `providers/resolved_urls.json` is
  unchanged — no catalogued base ends in `/` or already carries its wire's path, so
  nothing a catalogued row could observe moved.
- An expired hub hold is now freed by the hub. `hold()` returned the live
  `*Subscription`, and `release` never marked it detached, so `reclaim()` skipped it
  forever: the only thing that could finish an abandoned hold was the holder calling
  `close()` on a pointer it was still expected to own, and reading that pointer
  after the hub freed it was a use-after-free. `hold()` now returns a small `Hold`
  value that names only when it lapses — nothing to dangle on, and nothing for the
  holder to do — and releasing a subscription marks it detached, which is what makes
  it reclaimable. An expired hold with events queued now leaves the hub's hold list,
  its subscriber list and its subscription list all empty, asserted on those counters
  rather than on the allocator. Adoption is unchanged: the request that follows still
  receives the live subscription with the events that arrived while it was held.
- A config document naming a negative `journal_capacity` for an adapter is refused instead of loaded with the default. Every Go constructor treats `<= 0` as "unspecified", so a value no operator would write reported success and silently retained a different depth; the Zig loader already refused one with `ConfigRefused`. A document naming `0` still keeps the default in both trees, because that is a request for the default rather than a malformed value.
- The `Host` allowlist refusal on the HTTP transport is an `error.response` carrying the code `unrecognized_host`, keeping the 403 and its wording, where it answered a bare `{"error": …}` with no code — a shape a client cannot branch on, and the one divergence that failed a differential comparison on its first request. `TestHostAllowlist` asserts the body, which is why the shape had drifted unpinned.
- The `go/serve/servehttp` body gate admits any `charset` and reads the body as UTF-8, where a `charset` other than UTF-8 was refused `415 unsupported_media_type` naming it. RFC 8259 §11 defines no `charset` parameter for `application/json` at all, so the parameter is not part of the grammar the decoder reads: refusing it turned a sender's redundant parameter into an error the schema never described, for a request that parses. A media type that is not `application/json` is still refused, because that is a different grammar rather than a different spelling of this one. `TestAnyCharsetIsAdmittedAndTheBodyIsReadAsUTF8` pins the admission by naming a session whose id carries non-ASCII text under every charset, including `latin1` and a name that is not a charset, and requires the answer to echo those characters unchanged; `TestABodyThatIsNotUTF8IsReadWithTheReplacementCharacter` pins the other half, that a body which is not valid UTF-8 is read with U+FFFD rather than transcoded per the header or refused.
- A Claude Code permission ask now names the tool call it is for. When `can_use_tool` arrives for a `tool_use_id` the run already announced, both trees put `tool_call_id` on the ask's envelope and in its payload; an ask for a call the run never announced leaves both empty, as before. A client that decides by tool could otherwise only learn which tool an ask was for by parsing the question's prompt text. The envelope's `tool_call_id` follows `turn_id` where the schema orders them, so the two trees serialise an ask that names a tool call identically.
- The claude, acp, hermes, deepseek and pi adapters now cancel a settling run's pending tool calls, and resolve its open prompts, in the order they started rather than in Go's randomised map order. #343 fixed this in the codex and opencode adapters; with one pending call there was nothing to order, so the order was never observable, and a fixture that opens two would have found the trees disagreeing — the Zig reducers range an `ArrayList` and always keep start order.
- `zig/src/hub/hub.zig` frees a subscription's queued events when it is closed. A client that disconnected mid-stream left its subscription, both list capacities and up to `stream_queue` event lines behind, because `reclaim()` only frees a subscription whose queues are already empty and nothing drains a closed one — the fan-out no longer reaches it and only the client called `next()`. A closed subscription is unreadable from then on, so its events are released there and the next pump reclaims the subscription itself.
- `zig/src/hub/hub.zig` names the run a queue overflow lost, the way Go's
  `lossRun` does, and points its cursor where the client actually stopped. The
  candidate set is Go's: the event that overflowed, every run with a queued event,
  the run the subscription is reading, and the session's current run, preferring a
  run the session has not finished. The current run is the one that was missing —
  without it a cancel-then-resubmit loss named the run being read while Go named
  the newer live run, and a client resumed onto a settled run. The cursor is the
  position the client stopped at *after* draining, not where the queue filled, so a
  resume neither skips the events already handed over nor repeats them.
  `drafts/hub.md` records the rest of the overflow table as **D7**: a run's stream
  failure cannot be attributed to that run or scoped to the subscribers exposed to
  it, because `contract.Session.drain` reports a failure rather than whose stream
  failed, so a stream failure ends the whole session where Go confines it to the
  readers of that run.

  A replay that outgrows the mailbox names the run being replayed at the
  position it reached, as Go's `nextReplay` does. The loss candidate set answers a
  different question — which run a *live* stream's loss belongs to — and applying
  it to a replay named the session's current run and orphaned the replayed suffix.

- A wire whose path carries a version now drops that version when the base's path already spells one, rather than only when the base ends in `/v1`. Five catalogued rows were composing an address no vendor serves: `zai-coding-plan` reached `/api/coding/paas/v4/v1/chat/completions`, `tencent-coding-plan` and `volcengine-coding-plan` reached `/coding/v3/v1/chat/completions`, `deepinfra` reached `/v1/openai/v1/chat/completions`, and on the anthropic wire `minimax-coding-plan` reached `/anthropic/v1/v1/messages`. Probed unauthenticated, the doubled form is a hard 404 for `deepinfra` and `minimax-coding-plan` while the deduplicated form is served, and the other three hosts answer 401 either way; `go/provider/zai.go` already paired that base with a versionless path. A base like `/v1/openai` or `/api/coding/paas/v4` is a versioned root, so the wire's own version must not be appended to it a second time.
- `goap serve agent --backend codex` closes open interactions and actions in the order they opened, and `goap serve agent --backend opencode` settles unfinished tools in start order, as `oapx` does, instead of Go map iteration order. Neither parity fixture opens two at once, so the corpus cannot see it; the same unordered-map pattern still exists in the other Go adapters' settlement sweeps.
- `oapx serve agent --backend hermes` decodes `gateway.ready`'s payload against the pinned type, as `goap` does: a member outside `skin`/`change_events`/`replay_epoch` now refuses the open instead of being ignored. The `skin`, `change_events` and 32-hex `replay_epoch` checks were already enforced.

- The Zig JSON writer escapes `<`, `>`, `&`, U+2028 and U+2029 as `encoding/json` does (`\u003c`, `\u003e`, `\u0026`, `\u2028`, `\u2029`), so OAP envelopes written through `zig/src/json/writer.zig` match Go's default HTML-safe escaping. No ledger recorded the gap.
- `goap` always emits the required member of a `text` or `reasoning` content part, even when it is empty, matching `schema/v0.1/common.schema.json` (which requires `text` and `reasoning` respectively) and `oapx`. `protocol.ContentPart` had tagged both `omitempty`, so an empty `text_delta` or `thinking_delta` produced `{"type":"text"}` or `{"type":"reasoning"}` with no member.
- The OpenCode adapter converts a native token count through an explicit saturation (`0` for a non-positive, non-finite or non-number float; `MaxUint64` at or beyond `2^64`), as `oapx` does, instead of a platform-dependent `uint64(float64)`.
- `goap` refuses a `--config` member whose name differs from `oap-serve.json` only by case, as `oapx` does, naming the container the way `oapx` does (`config: adapter "memory": unknown field "Type"`); `encoding/json` had matched members case-insensitively, so `"Type"` loaded where the Zig reader refuses it. It also refuses a duplicated member (`config: not one JSON object: DuplicateField`) and names the first unknown member in sorted order, so the refusal no longer depends on map iteration.
- `oapx serve agent --backend <acp entry>` writes a permission answer to the agent before recording it, as `goap` does: `action.permission.resolved` follows a successful write, and a write the agent cannot take settles the gate cancelled and fails the run `acp_permission_response_failed`, where oapx emitted the resolution first and failed the transport.
- `oapx serve agent --backend codex` rebuilds a settled session's arena once it has grown 256 KiB past its last compaction, keeping the thread, the session state, each run's id, turn, status and sequence, and a stub per item and interaction, so a reused native item id is still ignored and its completion fails the run, and a late answer still reads as already resolved, as in `goap`. A Codex session held every frame and envelope until it closed.
- The Hermes adapter no longer projects the gateway's `thinking.delta` as reasoning, in Go or Zig. The event carries the activity spinner (`"{face} {verb}..."`, or `""` to clear it), not model reasoning, so it is now observed-only; `reasoning.delta` is still projected. Recorded in [`research/hermes-thinking-delta-note.md`](research/hermes-thinking-delta-note.md).
- **A tool result whose call is gone is now dropped wherever it falls, not only when it opens a run.** The `openai-completions` request builder drops such a result in its outer loop over messages, but that loop then hands a whole run of consecutive results to an inner loop that never repeated the check, so only the result that opened the run was ever examined. An orphan after an answered result was written into the body, where a `tool` message naming a call the request never made is an error the provider rejects — so the repair that exists was applied half the time, and the half that failed is the one a conversation of parallel tool calls answered out of order produces. The inner loop now applies the same guard. Dropping can never make a request worse, because the result is unusable either way, and the rule reads as absolute in the function that carries it; nothing in the code said the rule was positional. A result whose call is present is untouched, and a conversation with no tool call in it at all still keeps every result, since the guard stands down when there is nothing to match against. #513
- **The Go `openai-completions` request builder drops an orphan wherever it falls in a run, as Zig does.** The port checked for a tool result whose call is not in the conversation in its outer loop over messages, and then collected a whole run of consecutive results into a slice and handed it to `toolResultRun` without repeating the check, so only the result that opened a run was ever examined. The inner loop now applies the same guard, which is what Zig's `openai_completions_api.zig` has done since #530. A run of nothing but orphans writes no tool message at all, and a conversation with no tool call anywhere still keeps every result.

  One of the port's own tests was leaning on the gap without saying so: `TestAnEmptyAssistantIsWrittenAroundToolResultsOnlyWhenAsked` needed two consecutive written tool messages to observe the `requires_assistant_after_tool_result` filler, and got its second one from a result for a call nothing made. Its fixture now calls both tools, so it exercises the rule it is named for rather than the orphan. A test that passes because of a defect is the one kind of test that gets read as evidence for the defect.

  **The Anthropic wire carried the same gap in both trees, and it is closed in both.** `anthropic_messages_api.zig` and `anthropic_request.go` each check for an orphan in the outer loop over messages and then collect a whole run of consecutive results to batch into one user message, without repeating the check — so an orphan sitting after an answered result in the same run went out as a `tool_result` block naming a `tool_use_id` the request never produced. Both inner loops now apply the same guard, so the two trees stay in step.

  A run of **nothing but** orphans needed no code at all: the outer guard drops each of them one message at a time, so the run is never entered and no empty `content: []` is written. That outcome was already the tree's, and the first draft of this change added an empty-run check to each wire anyway; the tests showed the check was unreachable, so it is not in the diff. The test for that case stays regardless, because an all-orphan run writing an empty content array is the shape worth pinning, whichever loop turns out to enforce it.
- **A chunk the runtime cannot read is still ignored rather than ending the response, and that is now pinned rather than assumed.** `parseChunk` returns on a chunk that does not parse, which leaves `canCompletePartialTextOnStreamError` reachable only from an exhausted allocator — the sole remaining way `parseChunk` can return an error. That was worth deciding rather than reading either way, so: the swallow is the policy, and it is the tree's, not this file's. `azure_openai_responses`, `openai_responses` (twice), `anthropic_messages` and `google_generative` each return on an unreadable chunk the same way, and only this wire ever wrote the partial-text rule, so the rule is the residue of one error path rather than evidence of a plan the others share. The rule stays as the allocator policy it is — on an OOM mid-stream, text already accumulated is finished with `length` rather than thrown away, but only while no tool call is open — and two tests now hold the swallow in place: one that a malformed chunk, a truncated one, and a valid non-object all leave every accumulator untouched, and one that a good chunk after a malformed one is still read.
- **One predicate decides whether a base URL is an OpenAI host, and the two that disagreed are gone.** `provider_caps.isOpenAINative` looked for `api.openai.com` anywhere in the URL; `isOpenAIHost` parsed the URL and matched `openai.com` or a `.`-delimited suffix of it, case-insensitively. They were near-synonyms by name and were consulted at different depths: detection in `detectCapabilities` produced the native caps, and then `mergeCompat` discarded every one of them whenever its own predicate said no, so a URL carrying the vendor's name in a path or a query was detected native and then silently downgraded. There is now one `isOpenAIHost`, in `provider_caps`, holding the parse-the-host rule the request builders already shipped; `detectProviderType` and both `openai_completions` and `openai_responses` call it, and the verbatim copy at `openai_responses_api.zig` is gone.

  **One behaviour change, and it is the one that was going the wrong way.** An `openai.com` host — `eu.openai.com`, or the bare `openai.com` — is detected native now, and three of the six decisions these predicates feed move with it: the system prompt is written as `developer` rather than `system`, `reasoning_effort` is written at all, and the token limit is `max_completion_tokens` rather than `max_tokens`. Those three were reaching for the native caps before and getting the compatible ones, which came out right only because the compatible caps happened to match. The other three — `store`, strict mode and the 40-byte tool-id limit — were already keyed on the host predicate and are unchanged.

  A URL naming `api.openai.com` only in its path or a query moves **nothing observable**: it already landed on `store: false`, no strict mode, a zero tool-id limit and the compatible defaults, and it still does. What changed there is the mechanism rather than the wire — detection and use can no longer disagree, because there is one answer between them instead of two that were consulted at different depths.

  `provider_caps` also has **no `addTest` in `build.zig`**, so its nine existing tests had never run; it is wired into `test` and `test-unit-utils` now, and the sixteen-URL table that decided this is a test over the one function. The file's other ten substring predicates are filed as #533. #511

  **The Go port now carries the same one predicate**, as #534 does in Zig: `IsOpenAINativeURL`, which read the substring `api.openai.com` out of the whole URL, is gone, and `DetectProviderType` calls the host-parsing `IsOpenAIHost` that the request builders already used. It is the same change with the same one observable difference — an `openai.com` host such as `proxy.openai.com` or `eu.openai.com` is detected native and merges to the native developer role, `reasoning_effort` and `max_completion_tokens` — and the two tests that pinned the old split, one asserting the gate discarded what detection found, are rewritten because there is no longer a gate and a split to discard across. #511
- **A `reasoning_details` blob no longer splices the provider's own bytes into the request JSON.** The `openai-completions` read path rebuilt each `reasoning.encrypted` detail by formatting the id and the data straight into a JSON string with `{s}`, so a `"` in either one closed the string early and a `\` began an escape the parser then read as part of the surrounding document. The detail is now written through `zig/src/json/writer.zig`, which is the writer the rest of the tree's escaping already comes from, so the blob is valid JSON whatever the provider put in it — and it matches Go's `encoding/json` down to the HTML-safe escapes, which the writer was brought to parity for. The consequence was confined to the wire: a request whose detail carried a quote or a backslash was not parseable, so it could not be sent as a well-formed body. A detail with nothing to escape is byte-for-byte what it was. This is the only place the tree built a JSON object by hand; the Anthropic wire writes its signature as an ordinary string field. #515

### Changed

- TUI tool rows no longer cut a tool's description at 48 characters; the row fits it to the terminal width. Up and Down move the slash-command palette's selection, which Tab completes and Enter runs, and the composer border no longer pulses while a turn streams.
- `go/serve` releases a session once it stops being open, so a close is a detach in both trees (Decision 0039). The hub keeps no entry for it: every later operation naming the session answers `unknown_session` over both transports, a second `close` included, and `sessions` lists live sessions only. Both paths release — a successful close, and the adapter reporting `ErrSessionClosed` from any operation that calls it, which today are `State`, `Models`, `SwitchModel`, `Tools` and `Submit` — and a session that is already closed when an `open` probes it is never added, so its id is free at once. `session_closed` now means one thing only: a session that stopped being open while a request was in flight. Subscriptions still end with the `session_closed` signal. The `sessions` listing is where a session the adapter has already closed is noticed — it reads each session's state through the hub — so the listing no longer carries a session that is not open, and reading it releases the entry.
- The Go packages a program should import are now a named public set: `protocol`, `adapter` and every package under it, `client`, `harness`, `providercatalog`, `serve` with its three bindings (`servehttp`, `serveendpoint`, `servestdio`) and `validation`, plus the three embedded-data packages `harnesses`, `providers` and `schema`, which every adapter and the validator read. `goap check` walks the module root and fails on a package that is in neither the set nor an `internal` directory, so a new package cannot become public API by being added, and `go/provider` and `go/conformance` moved to `go/internal` — only `goap` read either. A nested module is not part of this module's surface and is skipped. This is what makes `go/adapter`, `go/adapter/<harness>` and `go/protocol` the surface a Go program depends on (hyperneo-review, the first one).

- `providers/catalog.schema.json` now states how `models_endpoint` joins a row's base URL: it is appended literally to the base the request would use, including that base's own path, never joined against the origin and never relative to the host, and the same path is appended to every endpoint the row has. `goap check` fails a `models_endpoint` that is not an absolute path. No row changes: all twenty composed listings answer 200 or 401 unauthenticated, and a Zig test keeps every recorded path absolute, every base free of a trailing slash, and a composed listing from spelling its version segment twice.
- [Decision 0035](decisions/0035-a-model-entry-publishes-its-facts-and-absence-means-unknown.md) (proposed) carries the owner's answers to the five questions it was opened with, dated 2026-09-26. `vision`, `audio_input` and `audio_output` stay in `modelCapability` for v0.1 as deprecated aliases; `cost` is carried as a published, unjudged fact, flat at four numbers, with a tier left to the row that needs one; the modality is `document` and `video` is in v0.1, a provider in the evidence publishing it; and the agent side takes no completeness member for now, so `models.response` and the `+models` rule are untouched. The record stays `proposed` — what accepting it requires has not moved, and no schema, type, validator, fixture or client change accompanies the amendment.
- `oapx serve agent --backend pi` handles Pi dialogs as the Go adapter does: one raised before `agent_start` surfaces once the run starts, one outside a run is ignored, and one still open at settlement resolves `cancelled` without an `extension_ui_response`. Its `run.started` now names the model, as Go's does.
- `oapx serve agent` words its `unknown_session`, `tool_catalog_unavailable` and unadvertised or degraded `unsupported_feature` refusals as `goap serve agent` does. A codex parity fixture for `TestBackendsMatchOapx` records the remaining differences.
- The Claude Code pin moves to 2.1.282 (Agent SDK 0.3.282; Python SDK 0.2.159, the newest, which bundles 2.1.281). Captures of all seventeen probes against 2.1.280 and 2.1.282 differ only in `system/init` (new optional `view_mode`, `per_turn_effort_active`, a built-in plugin, the `focus` command), so no adapter code changes; the capability revision becomes `claude-code-2.1.282-oap-v1` for the new endpoint version, in both trees. The corpus moves to `fixtures/adapters/claude-code-2.1.282`; 2.1.280 is retired and its corpus removed. Recorded in [`research/claude-code-agent-sdk-2.1.282-mapping.md`](research/claude-code-agent-sdk-2.1.282-mapping.md).
- The DeepSeek Harness adapter moves to `dsh-v0.1.7-rc.2` (`477b4f4`) in Go and Zig ([ledger](research/deepseek-harness-dsh-v0.1.7-rc.2-mapping.md)). A `tool/result` now carries a tool-role message, so `action.call.completed.result` is the result blocks themselves instead of a wrapping `tool-result` block. Message sources are producer-declared kinds, and only a bare `{kind: "user"}` proves ownership. `developer/message` is observed-only. The corpus moves to `fixtures/adapters/deepseek-harness-dsh-v0.1.7-rc.2`, with `tool-lifecycle` and `injected-origin` re-recorded against the runtime. The capability revision becomes `deepseek-harness-dsh-v0.1.7-rc.2-oap-v1`, which the Zig backend serves too. `dsh-v0.1.6-alpha.2` is retired.

- The Hermes adapter pin moves from `v2026.8.31` to `v2026.9.24` in both trees (capability revision `hermes-v2026.9.24-oap-v1`; support levels unchanged). Gates now arrive as server→client JSON-RPC requests (`approval`, `clarify`, `sudo`, `secret`). The adapter answers each with a response frame carrying the request's id, settles a gate `cancelled` on `request.cancel`, and refuses server requests it does not map with `-32601`. The retired `*.request`/`*.expire` events and `*.respond` methods are gone. Empty deltas, which the gateway sends on every turn, no longer project as schema-invalid content parts. A background delegation's result re-enters the session as a turn the adapter did not submit, and the session is closed as foreign activity. The corpus is `fixtures/adapters/hermes-v2026.9.24`: four cases re-recorded and two new ones recorded against the release, the rest carried forward. A new opt-in process gate answers a real approval. Details and every wire difference are in [`research/hermes-v2026.9.24-mapping.md`](research/hermes-v2026.9.24-mapping.md).
- Harness pins are written once, in `harnesses/<id>.json`. The Go adapters read them from the embedded catalog and the Zig adapters from a module `build.zig` generates from it, and both trees serve the one revision each version records. `goap check` now fails when Go or Zig source spells a pin value. The Zig served backends for Pi and DeepSeek now report the catalog's endpoint version (`v0.85.1`, `0.0.1`), as the Go adapters do, instead of `0.85.1` and `47f9438`.
- Pi is pinned to `v0.87.1` (capability revision `pi-v0.87.1-oap-v1`, which the Zig port serves too); `v0.85.1` is retired with its corpus. Pi now emits `system` role messages carrying the system prompt and tool declarations, which both adapters refused as an unknown role, failing the first run of every session. The Go adapter and the Zig port accept them and project nothing from them. The advertised surface is unchanged. The new corpus carries ten cases forward and adds a `system-message` case recorded from the released binary; the wire differences are in [`research/pi-v0.87.1-mapping.md`](research/pi-v0.87.1-mapping.md).
- The OpenCode pin moves from `v1.18.29` to `v1.18.32` (revision `opencode-v1.18.32-oap-v2`, which both trees serve); v1.18.29 is retired. The session API, its SSE events and every corpus-cited source blob are unchanged between the tags, so the fifteen corpus cases carry forward with their native lines untouched and neither adapter changes. See [`research/opencode-v1.18.32-mapping.md`](research/opencode-v1.18.32-mapping.md).

- The ACP adapters are pinned to agent-client-protocol v1.9.1 (schema v1.23.0) and gated against docker/cagent v1.143.0. The only stable wire change is the optional tool `name`; `action.call.*` now carries it when present, falling back to the ACP `title`. The corpus moves to `fixtures/adapters/acp-v1.9.1` with its native frames unchanged.

- The Codex app-server pin moves from commit `8d7cc24` to release `rust-v0.157.0` (`00c972ed`). No adapter code changes: every frame the adapters read or write has the same schema at both pins. The capability revision becomes `codex-appserver-0.157.0-oap-v1`, which `oapx` serves too, the corpus moves to `fixtures/adapters/codex-appserver-0.157.0` with its native lines carried forward, and `8d7cc24` is retired. See [`research/codex-app-server-0.157.0-mapping.md`](research/codex-app-server-0.157.0-mapping.md).

### Removed

- The Claude Code 2.1.263 floor: its corpus and catalog version. Its ledger stays as the base the 2.1.280 ledger builds on.

### Fixed

- The TUI showed no Kimi model when `KIMI_API_KEY` was exported and no `/login kimi` had run: the catalog required a stored credential, `openai-completions` resolved no key from the environment for kimi, and `/login` scanned no environment variable for it, so the picker stayed empty although `makai -p` already honoured the key. The catalog now accepts the exported key (a stored login still wins), fetches `GET {region base}/v1/models` with it, caches the body under `~/.oapx/model_catalog/kimi.json` or `kimi-global.json` for the 24-hour window Anthropic's catalog uses, and falls back to the stale copy and then the static `kimi-k2.7-code`; each entry takes its display name, context window, reasoning flag and text/image input from the response's `display_name`, `context_length`, `supports_reasoning` and `supports_image_in` instead of one hard-coded model's specs. A kimi request resolves the key when no credential is stored, and `/login` reports `✓ env key` for kimi.
- `oapx serve agent --backend <acp entry>` rebuilds a settled session's arena once it has grown 256 KiB past its last compaction, keeping the native session id, the attached sources, each settled run's status, the transcript cursor, and a stub per tool and permission gate so a reused tool id and a late answer are still refused. An ACP session held every frame it had ever read until it closed.
- `oapx serve agent --backend <acp entry>` writes its JSON-RPC frames with Go's escaping of `<`, `>`, `&` and U+2028/2029, so the bytes an ACP agent reads match `goap`'s; the parity scenario now sends a prompt carrying all of them.
- The in-memory reference adapter, in Go and in `oapx`, refuses a resolution addressed to a queued run that has not started, which could otherwise complete it with no `run.started`.
- `goap serve agent` honours an open's elections instead of dropping them: `tool_sources` are gated and resolved as the hub resolves them, `subscribe` is gated, and a compound `message` is refused `unsupported_feature` (`field: message`), as `oapx` refuses it. Before, all three were accepted and silently ignored.

### Added

- `oapx serve agent --backend memory` serves the deterministic in-memory reference script instead of answering `unavailable`, and passes `goap conformance`.
- `oapx serve agent --backend memory` now answers exactly as `goap serve agent` does: provided tools called through `action.call.resolve`, attached tool sources, the full session state (`active_runs`, `transcript_cursor`, `sources`, `as_of`), model context windows and providers, and the Go descriptor under its revision `reference-memory-v11`. The endpoint resolves `tool_sources` against the `--config` registry as the hub does, and error messages carry `goap`'s wording. A CI parity test replays two scenarios through both binaries and requires identical output.
- The Zig OAP types carry these members `goap` serves: `active_runs`, `transcript_cursor`, `metadata`, `sources` and `as_of` on session state; `context_window` and `providers` on models; `modes`, `constraints` and `limits` on a feature; `tools` on capabilities. The adapter contract forwards open-time `tools` and `tool_sources` and lets a backend declare feature details and a tool catalog.
- `oapx serve agent` keeps a bounded journal of 256 events per session and answers the replay control from it for every backend, as `goap` does; a frame lost to the line bound can now be replayed. Every backend advertises `run.resume` and `run.replay` `degraded`, and codex, hermes, deepseek, opencode, pi and claude now serve exactly the Go adapter's descriptor under its revision; acp moves to `acp-v1.7.0-schema-v1.21.0-oapx-v2` until it passes attached sources.
- `oapx serve agent --backend claude` enforces `run.tool_selection` as `goap` does: it registers the `oap_tool_selection` PreToolUse hook at `initialize`, denies a hook callback or permission ask for a tool the run's `tool_choice` excludes, and settles that call `refused_by_policy`.
- `goap serve agent` names its stdio binding (`{"kind":"stdio","serialization":"jsonl"}`) in `capabilities.response`, as `oapx` already does.
- On Windows, `oapx serve agent` and `serve provider` treat a console Ctrl+C, Ctrl+Break or close as end of input, as SIGINT and SIGTERM are treated elsewhere; a close gets up to 4.5 s to settle before Windows ends the process.
- `oapx serve agent` and `oapx serve provider` without `--backend` treat SIGINT and SIGTERM as end of input, as both stdio drafts require: they stop reading, settle what they admitted, flush and exit. Before, a signal killed the built-in loop outright.
- `oapx serve agent` without `--backend` now enforces the same two-minute output-stall bound: it writes one line to stderr and exits non-zero. `serve provider` keeps unbounded writes, since its binding sets no bound.
- `oapx serve agent --backend` stops when its stdout makes no progress for two minutes, the bound [`drafts/endpoint-stdio.md`](drafts/endpoint-stdio.md) sets and `goap` uses: it writes one line to stderr and exits non-zero. Before, a host that stopped reading left the endpoint blocked in a write forever. A piped or socket stdout is made non-blocking, and writes go out after `poll` reports it writable, in chunks no larger than `PIPE_BUF`, so no write can block past the bound. On Windows a watchdog thread cancels a write blocked past the bound with `CancelSynchronousIo`; its tests run on a `windows-latest` CI job.
- `oapx serve agent --backend` treats SIGINT and SIGTERM as end of input, as [`drafts/endpoint-stdio.md`](drafts/endpoint-stdio.md) requires and `goap` already does: it settles what it admitted, flushes, closes each session so its child is stopped, and exits 0. Before, a signal killed `oapx` outright and could leave a backend child running.

- The Claude Code adapter advertises `run.tool_selection` as `emulated` for the run (capability revision `claude-code-2.1.280-oap-v3`) and accepts `tool_choice` on submit (#122). Open registers a `PreToolUse` hook, which Claude Code 2.1.280 raises for every tool call, including ones `AllowTools` pre-approves and read-only or safe commands that never ask. The adapter denies a call the admitted policy excludes there, or at the `can_use_tool` gate if one arrives, without opening an interaction, and the call settles `action.call.failed` with `refused_by_policy` under Decision 0031. The descriptor publishes no tool catalog, so under Decision 0034 a policy is judged by the names it lists: `disallowed: ["Bash"]` excludes only `Bash`, and `allowed: ["Read"]` permits only `Read`. Verified against the pinned binary: an excluded `Bash` under both postures is refused, does not run, and the run completes. The Zig port carries the new revision without the control. Recorded in [`research/claude-code-agent-sdk-2.1.280-mapping.md`](research/claude-code-agent-sdk-2.1.280-mapping.md).
- **`oapx serve agent --backend <name> --config <path>`** also serves an OpenCode server (pinned `v1.18.29`) named by an `opencode` registry entry's `endpoint`. `zig/src/adapter/opencode/adapter.zig` drives the corpus reducer over a small HTTP/1.1 client (`opencode/client.zig`) with a pollable SSE subscription, and settles a run by polling the active set with Go's 10–500 ms backoff. `auto` and `queue` delivery are served; `run.resume` and `run.replay` are `unavailable`, under `opencode-v1.18.29-oapx-v1`. Only plain `http` endpoints are accepted. Against a scripted server, `goap conformance` fails only the model-switch checks, as it does against `goap serve agent`. Differences are in [`research/opencode-v1.18.29-mapping.md`](research/opencode-v1.18.29-mapping.md).
- The Zig endpoint's `capabilities.response` now carries the descriptor's `limits`, which a backend advertising native `queue` delivery must disclose, and `models.request` carries `allow_degraded_features`, so a degraded catalog is served only to a caller that opts in.

- **`oapx serve agent --backend pi [--config <path>]`** serves a Pi child (pinned 0.85.1) in `--mode rpc --no-extensions` through the Zig port, refusing explicit extension arguments as Go does. `zig/src/adapter/pi/adapter.zig` reads `get_state` at open, refuses a child already streaming, admits a run on `agent_start`, closes the session when Pi refuses a prompt, and cancels with one `abort`. It advertises Go's descriptor with `run.resume` and `run.replay` `unavailable`, under its own revision. Differences from Go are in [`research/pi-v0.85.1-mapping.md`](research/pi-v0.85.1-mapping.md).
- **`oapx serve agent --backend <name> --config <path>`** also serves a DeepSeek harness (pinned `47f9438`) named by a `deepseek` registry entry with its `executable`, `provider` and `model`. `zig/src/adapter/deepseek/adapter.zig` runs the pinned `initialize` handshake, refusing a harness that speaks first or names another server, admits a prompt once the harness enters it as a direct user message, and sends `shutdown` before stopping the child. `run.cancel` stays `unavailable`, as the wire has no cancel request; `run.resume` and `run.replay` are `unavailable` under `deepseek-harness-47f9438-oapx-v1`. Against a scripted harness, `goap conformance` fails only the model-switch checks, as it does against `goap serve agent`. Differences are in [`research/deepseek-harness-47f9438-mapping.md`](research/deepseek-harness-47f9438-mapping.md).

- **`oapx serve agent --backend <name> --config <path>`** also serves a Hermes gateway (pinned `v2026.8.31`) named by a `hermes` registry entry. `zig/src/adapter/hermes/adapter.zig` requires a valid `gateway.ready` as the first frame, then drives the reducer over `session.create`, `prompt.submit`, the four `*.respond` methods and `session.interrupt`, holding gateway events while an answer is in flight so a settlement that races it cannot cancel it. `run.resume` and `run.replay` are `unavailable`, under `hermes-v2026.8.31-oapx-v1`. Against a scripted gateway, `goap conformance` fails only the model-switch checks, as it does against `goap serve agent`. Differences are in [`research/hermes-v2026.8.31-mapping.md`](research/hermes-v2026.8.31-mapping.md).

- **`oapx serve agent --backend <name> --config <path>`** serves an ACP v1 agent named by an `acp` registry entry through the Zig port. `zig/src/adapter/acp/adapter.zig` writes the handshake, prompt, cancel and permission frames Go's client writes around the observing reducer, and advertises Go's descriptor with `run.resume` and `run.replay` `unavailable` and no tool-source attach, under `acp-v1.7.0-schema-v1.21.0-oapx-v1`. Against a scripted agent, `goap conformance` fails only the two model-switch checks, as it does against `goap serve agent`. Differences are recorded in [`research/acp-v1.7.0-mapping.md`](research/acp-v1.7.0-mapping.md).

- **`oapx serve agent --backend codex [--config <path>]`** serves a Codex app-server child (pinned `8d7cc24`) through the Zig port, the counterpart of `goap serve agent --backend codex`. `zig/src/adapter/codex/adapter.zig` runs the pinned `initialize` handshake, opens a thread, and drives the reducer: approvals arrive as `action.permission` interactions, option questions as `user.input`, and cancel sends `turn/interrupt` for the exact turn. It advertises Go's descriptor with `run.resume` and `run.replay` `unavailable`, under its own revision. Against a scripted child, `goap conformance` fails only the two model-switch checks, as it does against the Go adapter. The differences from Go are recorded in [`research/codex-app-server-8d7cc24-mapping.md`](research/codex-app-server-8d7cc24-mapping.md).

- A Zig port of the OpenCode v1.18.29 adapter under `zig/src/adapter/opencode/`: pinned native types with strict decoding, the SSE framing and request/response codec, the session reducer, and a corpus driver. Every one of the fifteen corpus cases reproduces `expected-oap.json` byte for byte and passes the Zig validator. The requests the port encodes, and its descriptor and capability revision (`opencode-v1.18.29-oap-v2`), are checked against goldens the Go adapter records in `go/adapter/opencode/testdata/port-goldens.json`. Fourteen multi-run scenarios the corpus does not cover, recorded from the Go session in `port-scenarios.json`, also replay byte for byte. Nothing wires the port into `oapx serve agent` yet. Divergences are recorded in [`research/opencode-v1.18.29-mapping.md`](research/opencode-v1.18.29-mapping.md).

- A Zig port of the Codex app-server adapter, `zig/src/adapter/codex/`: the line codec, the reducer and a corpus driver. All thirteen corpus cases reproduce `expected-oap.json` byte for byte and pass the Zig schema check and semantic validator. The frames the adapter writes to Codex are pinned by `fixtures/adapters/codex-appserver-writes/conversation.json`, which the Go adapter records against a scripted app-server; `TestCodexProcessWritesTheRecordedConversation` fails if Go stops writing those bytes, and the Zig driver requires the same bytes and the same capability descriptor. The port is not yet served by `oapx serve agent`. Divergences are recorded in [`research/codex-app-server-8d7cc24-mapping.md`](research/codex-app-server-8d7cc24-mapping.md).

- **`oapx serve agent --backend claude [--config <path>]`** serves a Claude Code child over the agent-control-core stdio binding, the Zig counterpart of `goap endpoint --config <path> --adapter claude` ([Decision 0019](decisions/0019-one-binary.md)). A new adapter contract (`zig/src/adapter/contract.zig`) and a single-adapter endpoint (`zig/src/adapter/endpoint.zig`) carry initialize, capabilities, open, state, submit with events streamed after its answer, cancel, the three resolve requests, models and the tool catalog, with stale-revision refusal and typed errors. The claude backend runs the `initialize` exchange at open, answers `can_use_tool` asks raised as `user.input` gates, and cancels with `interrupt`. `--config` reads an `oap-serve.json` entry with unknown members refused and `environment` as an explicit allowlist; without it, `claude` is found on `PATH` and given only `HOME` and `PATH`. Every other backend name answers each request `unavailable`, naming itself. An explicit `queue`, `steer` or `btw` delivery the backend does not advertise is refused `unsupported_feature` naming its key. `goap conformance` passes against it with a scripted child except for the two model-switch checks, which claude does not support in either tree; SIGINT, SIGTERM and the output-stall bound are not yet handled, as in the built-in loop. The differences from the Go adapter are recorded in [`research/claude-code-agent-sdk-2.1.280-mapping.md`](research/claude-code-agent-sdk-2.1.280-mapping.md).

- The harness catalog ([Decision 0033](decisions/0033-harness-pins-are-data.md)): `harnesses/<id>.json` for acp, claude-code, codex-app-server, deepseek-harness, hermes, opencode and pi, validated by `harnesses/harness.schema.json` and embedded in Go as package `harnesses`. Each version records its status, endpoint version, capability revision, admitted runtime versions, ledgers, corpus, components, per-platform artifact digests and source commits, all taken from the ledgers and corpus manifests. Claude Code 2.1.280 is `current` with 2.1.263 as its `floor`; DeepSeek's `current` is `dsh-v0.1.6-alpha.2` with `corpus_from` `dsh-v0.1.5-rc.2`, the release its corpus was recorded at. `goap check` gains an offline `harnesses` phase that fails on a harness without exactly one `current` version, a capability revision repeated within a harness, a corpus whose expectations carry another revision, a missing ledger or corpus, a digest or commit that none of its version's ledgers records, and a Go adapter whose `CapabilityRevision` or new `CorpusDirectory` constant differs from the current version.

- A Claude Code 2.1.280 evidence corpus, `fixtures/adapters/claude-code-2.1.280`, recorded against the pinned darwin-arm64 binary. It carries the same thirteen cases as the 2.1.263 corpus, which stays as a floor; Go and Zig run both. Frames a hermetic probe can produce come from a capture of the pinned binary, and the rest are carried over with 2.1.280's shape changes. Against the 2.1.263 expectations, four envelopes differ, each in text 2.1.280 writes itself. `TestClaudeProcessRecordsCorpusProbes`, gated on `OAP_CLAUDE_CAPTURE_DIR`, is the capture path. Recorded in [`research/claude-code-agent-sdk-2.1.280-mapping.md`](research/claude-code-agent-sdk-2.1.280-mapping.md).

- [Decision 0032](decisions/0032-go-and-zig-are-peers.md): Go and Zig are peer implementations and the specification decides between them. It supersedes the parts of Decision 0019 that made the Go tree the oracle and scheduled its deletion, says a Go runtime quirk is not protocol behaviour until a decision specifies it, and names the Go binary `goap`, answering the question left open when the Go CLI kept the name `oap`. The rename itself follows separately.

- `oapx serve provider --http 127.0.0.1:<port>` serves the same `model-provider-core` catalog and inference operations over HTTP/SSE, including concurrent inference streams and separately correlated cancellation. The listener binds only to loopback; cross-Pod exposure requires an operator-managed TLS/mTLS proxy. It advertises provider-managed credentials and does not accept credential grants over HTTP.

- The HTTP provider endpoint returns the provider profile's JSON error envelope with HTTP 200 when an OAP envelope fails decoding, preserving its correlation id, error code, and message instead of replacing it with an empty HTTP 400 response.

- Drafted the `model-provider-core` HTTP/SSE binding and added client-side remote-provider support to `oapx serve agent`. An operator can set `OAPX_PROVIDER_SERVICE_URL` and `OAPX_PROVIDER_SERVICE_SECURITY` (`loopback`, `tls`, or `mesh_proxy`); startup validates the service profile, managed-credential posture, and model catalog before the agent routes inference over HTTP/SSE. Caller-held credential grants are refused. Live `session.provider.attach` is not yet implemented.

- Bounded remote-provider HTTP operations: unary calls and the first streamed envelope have a 30-second deadline, and an active SSE stream has a 120-second idle deadline that SSE traffic, including comment heartbeats, refreshes. A timed-out connection is shut down, repeated or skipped inference sequences are rejected, and a cancel acknowledgment is consumed outside the bounded inference-event queue so cancellation cannot wait on that queue when it is full. SSE events are delivered as bytes arrive, rather than waiting for a 4 KiB read or connection close.

### Changed

- `adaptertest` refuses to synthesize a submit request when the descriptor offers any submit control (`run.instructions`, `run.model_selection`, `run.structured_output`, `run.tool_selection`) at a level other than `unavailable`. The synthesized request carried no controls, so a validity assertion on a control-bearing run judged a trace without the control it meant to test, which is what hid #122. Such tests now pass the request the adapter received, through `AssertProtocolValidWithSubmit`, the new `AssertProtocolValidWithSubmitAndCancellation` and `AssertProtocolValidWithSubmitAndCatalog`, and `ProtocolTrace` and `ProtocolTraceWithCancellation`, which now take the request. The guard surfaced a memory-adapter test whose `tool_choice`, `output_schema` and `instructions` had been dropped from its own validity check.

- A `capabilities.response` without a `tools` member, at the top level or in any layer, now leaves the tool catalog unknown rather than known and empty ([Decision 0034](decisions/0034-an-unpublished-catalog-is-unknown.md), now accepted). A `tool_choice` is then judged by its lists alone: `allowed` may name any tool, and `disallowed: ["Bash"]` excludes only `Bash` instead of every tool. `"tools": []` still publishes an empty catalog. Go and Zig validators both; five new `controls-tool-choice` fixtures.

- A call to a tool the admitted `tool_choice` excludes is judged at its settlement, not at `action.call.requested` ([Decision 0031](decisions/0031-a-policy-refusal-is-a-settlement.md), now accepted). Settled `action.call.failed` with `error.code` `refused_by_policy` it is valid; settled any other way, or unsettled at the run terminal, it is `unapplied_control` at the settling envelope or the terminal. A permitted call settled `refused_by_policy` is `unapplied_control` too. An endpoint that can only refuse a call after the model makes it can now advertise `run.tool_selection` `emulated` and project the attempt honestly. Go and Zig validators both; five new `controls-tool-choice` fixtures (#122).

- `goap serve` is now `goap hub` (same `--config`, `--addr`, `--stdio`), with no alias; `goap serve` without a role prints usage naming `hub`. `goap serve agent [--backend A]` serves one agent loop, replacing `goap endpoint --adapter A`, which stays as an alias. `goap serve provider` and `goap serve agent,provider` answer `unavailable`. This is the `goap` half of the CLI contract in `drafts/cli.md`.

- The claude, hermes and deepseek adapters deliver every event stream as a follower of the session journal: a consumer reads at its own pace with backpressure, and `ErrEventStreamOverflow` now means only that it fell behind what the journal retains, after which `Resume` from its cursor reports a `ReplayGap`. Previously a stream had 64 slots of headroom beyond any replayed backlog, so a resumed consumer draining a large backlog overflowed again while the run kept streaming (#237).

- The Hermes adapter's `Session.Resume` replays the adapter's own bounded journal instead of answering `unavailable`, so a consumer that overflows its event stream can resume from its last sequence, as `ErrEventStreamOverflow` tells it to. A cursor inside the journal replays the suffix and then follows the live run, and a run that has ended replays its retained tail, even after the gateway process has exited. A cursor older than the journal returns `*adapter.ReplayGap`, one past the run returns `ErrReplayCursorFuture`, and a run the session never admitted returns `ErrRunNotFound`. The gateway's native `session.events.since` ring is not used: it replays native frames under a per-session `seq`, not the envelopes a run's stream delivered, and dies with the gateway. `run.resume` and `run.replay` are now `degraded` (capability revision `hermes-v2026.8.31-oap-v2`), and `hermes.Config.JournalCapacity` should cover the events of the longest stall. The Zig port follows the revision; it keeps no journal yet, and its corpus driver skips the resume ops by declared count (#237). Recorded in [`research/hermes-v2026.8.31-mapping.md`](research/hermes-v2026.8.31-mapping.md).

- **The Go binary is `goap`, not `oap`** (Decision 0032). `go/cmd/oap` moves to `go/cmd/goap`, its log prefix and usage text follow, and CI, `clients/ts`, the README, STABILITY, CLAUDE.md, the drafts that invoke it and the system map name it. `oapx` is unchanged, and both still install side by side.

- The Claude Code adapter's `Session.Resume` replays the adapter's own bounded journal instead of answering `unavailable`, so a consumer that overflows its event stream can resume from its last sequence, as `ErrEventStreamOverflow` tells it to. A cursor inside the journal replays the suffix and then follows the live run, and a run that has ended replays its retained tail. A cursor older than the journal returns `*adapter.ReplayGap`, and one past the run returns `ErrReplayCursorFuture`. `run.resume` and `run.replay` are now `degraded` (capability revision `claude-code-2.1.280-oap-v2`). Against the pinned 2.1.280, a consumer stalled past the 64-slot stream now completes the run where it failed with `operation unavailable` before (#238). `claude.Config.JournalCapacity` should cover the events of the longest stall. The daemon's `?after=` reconnect drives the same `Resume`. Recorded in [`research/claude-code-agent-sdk-2.1.280-mapping.md`](research/claude-code-agent-sdk-2.1.280-mapping.md).

- The DeepSeek Harness adapter's `Session.Resume` replays the adapter's own bounded journal instead of answering `unavailable`, so a consumer that overflows its event stream can resume from its last sequence, as `ErrEventStreamOverflow` tells it to (#237). A cursor inside the journal replays the suffix and then follows the live run, and a run that has ended, including one whose process died, replays its retained tail. A cursor older than the journal returns `*adapter.ReplayGap`, one past the run returns `ErrReplayCursorFuture`, and a prompt receipt is not a run id. `run.resume` and `run.replay` are now `degraded` (capability revision `deepseek-harness-47f9438-oap-v2`), as the mapping ledger always classified them; `deepseek.Config.JournalCapacity` should cover the events of the longest stall. Cancel and resolve stay `unavailable`. The Zig port keeps no journal, so its corpus harness skips the resume steps of the new `journal-replay` case and only its capability revision follows. Recorded in [`research/deepseek-harness-47f9438-mapping.md`](research/deepseek-harness-47f9438-mapping.md).

- A prerelease tag (one with a hyphen, such as `v0.1.0-alpha.2`) publishes its npm packages under the `next` dist-tag and marks its GitHub release as a prerelease. Before, the release workflow published every tag as npm `latest` and as a full GitHub release, so an alpha would have become the version `npm install oap-sdk` resolves.

- The Claude Code adapter delivers submitted text verbatim: every user turn it writes carries `client_composed`, so Claude Code no longer expands `@path` mentions, dispatches slash commands or runs its turn-start attachment pass on text an OAP client submits. Against the pinned 2.1.280, an `@` mention of a file outside the working directory was read with no permission ask and its contents sent to the provider, and `/compact` compacted the session with no model turn. Neither is visible in the OAP trace, which records only the text. The environment and model reminders the attachment pass carried now arrive with the first tool result instead. `claude.Config.ExpandPrompts` restores the CLI's handling for a host that submits only its own text; the Zig port's `expand_prompts` matches. Recorded in [`research/claude-code-agent-sdk-2.1.280-mapping.md`](research/claude-code-agent-sdk-2.1.280-mapping.md).

- The Claude Code adapter is pinned to Claude Code 2.1.280 (capability revision `claude-code-2.1.280-oap-v1`), and `claude.AllowTools(...)` now bounds the tools the child has as well as pre-approving them: it passes `--tools` with the tools its rules name ahead of `--allowedTools`. Verified against the pinned binary, `--allowedTools` alone only pre-approves, so every unlisted built-in stayed available and an ask-gated call opened a permission gate that a host which never answers gates waited on forever. The same unbounded ask is what aborted the review reported in #232, which ran CLI 2.1.241, outside the pin. A rule that names no tool, such as `(git *)`, is refused. `UnrestrictedTools()` is unchanged, and `Config.Args` still follows the posture. The mapping is recorded in [`research/claude-code-agent-sdk-2.1.280-mapping.md`](research/claude-code-agent-sdk-2.1.280-mapping.md), which settles the tool-flag reading the 2.1.263 ledger left open.

- `oapx` is now the only executable name the TypeScript, Go, Python and Rust SDKs resolve automatically. The temporary fallback to a pre-rename `makai` executable has been removed from local-build discovery, `PATH` lookup and npm platform-package resolution; explicit binary paths and verified download URLs remain supported. CI, release archives, npm platform packages, conformance tooling and the Zig build already ship `oapx`, so this completes the executable-name cut without changing the legacy stdio wire protocol the SDKs currently speak.

### Added

- `oap_sdk` re-exports the four type names that were listed in `oap_sdk.types.__all__` and nowhere else: `ToolExecutor`, `AuthEventHandler`, `AuthPromptHandler` and `Role`. They are the names a *typed* consumer reaches for and nothing else needs — the signature of a `ToolDefinition.execute` callback, the two auth handler signatures, and the message-role union — so `from oap_sdk import ToolExecutor` raised `ImportError` while the other forty-five of the forty-nine names `types` publishes imported fine. That asymmetry is what makes it an omission rather than a decision, and the README had been reaching past the package surface to `oap_sdk.types` for one name as a result. A test now asserts the package re-exports every name `types` publishes, so the two lists cannot drift apart again silently.

- `makai --oap-provider --specimens` answers a specimen control frame, so a conformance harness can reach every envelope the endpoint emits without a provider, a credential or a live inference. A scripted exchange only provokes the frames a request draws — five of the twelve types this endpoint emits; the other seven need a real inference. Sending `{"control":"specimen","id":"s1"}` draws one `specimen.accepted` carrying `types` (what follows, in emission order) and `excluded` (what is supported and deliberately withheld — both grant answers, since a specimen of either is a fabricated credential exchange), then one well-formed instance of each type from the real serializer, then `specimen.complete`. Without the flag the same frame draws `specimen.error`, because an unanswered control frame is indistinguishable from a stall, and because a shipped binary should not emit frames outside a real exchange unless someone started it to. Two rules exist because a specimen frame and a real frame are indistinguishable line by line: specimens are refused while any inference is active, so they cannot interleave with real frames at all, and the specimen scope is `specimen-inference`, which the real generator cannot produce — ids are 32 lowercase hex characters, so a non-hex byte is structurally impossible rather than conventionally avoided.

- The `model-provider-core` carry round-trips, so an extended-thinking conversation survives more than one turn. A provider's reasoning signature — Anthropic's `signature`, Google's `thoughtSignature` — was already emitted on `inference.part.ended`, but nothing accepted one back, so a caller got a signed part on turn one and had no way to replay it on turn two: exactly the broken chain the member exists to prevent, and the reason every descriptor honestly advertised `round_trips_carry = false`. A request still naming the removed `reasoning.encrypted_carry` is refused, and the refusal names the new placement, because members inside a payload object are not policed the way payload members are and it would otherwise have decoded away silently. The carry now rides the `reasoning` and `tool_call` content parts in `messages[]`, symmetric with the part it arrived on, so a conversation with several reasoning blocks carries a signature on each and no placement rule has to be invented for a single request-level value. `anthropic` claims the round trip; the other built-ins do not and refuse a carry at create rather than dropping it. Two further gaps closed with it: `ContentPart.reasoning` parts were decoded and then silently discarded when a request was translated for the provider, so replayed reasoning never reached the model at all, and the terminal `inference.completed` assembly rebuilt its content parts without the signatures, so a caller replaying the terminal message — the obvious one to replay, being the complete one — handed back a history with every signature stripped. **`makai --oap-provider` now forwards reasoning and tool-call history upstream, where it previously forwarded text only.** That is a deliberate behaviour change rather than a side effect: a create naming a `reasoning` or `tool_call` part in `messages[]` was refused outright, so the inbound half was unreachable and the descriptor's claim was an overclaim. The refusal is now scoped by role: a `reasoning` or `tool_call` part belongs to an assistant message, a `tool_result` to a user or tool message, and a tool-role message must carry a tool result. Every other placement is refused at create rather than reshaped on the way through — a `tool_result` on a system message would otherwise have been forwarded as a tool turn with the system instruction itself demoted to a user turn, which is the silent rewrite this refusal exists to prevent. Replayed assistant messages are also stamped with the target model's identity, because `pre_transform` downgrades a thinking block to plain text when the recorded identity does not match the request's, which silently destroyed every replayed signature one layer below the protocol code; a live run against the endpoint is what surfaced it, after the unit tests had passed.

- `AuthStorage` can hold a credential that cannot reach durable storage. A caller-supplied key — bring-your-own-key, a per-tenant key, the per-call key Vertex documents — goes in with `putEphemeral` and lives in a map no writer can see: both persistence paths, `saveToFile` and `saveToKeychain`, serialize from `providers` alone, and refreshing an ephemeral OAuth credential writes the new token back to the ephemeral map without calling `persist`. The property is structural rather than a flag consulted at each write, because a flag is only as good as the call site that remembers it and a missed one puts someone else's key on disk with nothing observable to say so. Resolution prefers an ephemeral credential over a configured one for the same provider id, `hasRefreshableCredentials` and `credentialsExpired` route on it so a granted OAuth credential reaches the refresh path rather than failing as an unknown provider, and `releaseEphemeral` drops the lot so a connection ending takes its credentials with it. Nothing acquires ephemeral credentials yet; this is the representation the Open Agent Protocol `model-provider-core` credential grant needs before makai can advertise anything other than `credential_grant: none`.

- The macOS Keychain item makai stores credentials in is now `ai.hyperneo.oap` (account `auth.shared.json`), renamed from `com.makai.auth`. There is no fallback to the old service: an existing `com.makai.auth` item is left where it is and ignored, so the first run after upgrading finds no keychain credentials and falls back to `~/.oapx/auth.json` or a fresh login. Clearing the old item is a manual step (`security delete-generic-password -s com.makai.auth`). A fallback was considered and dropped: the old item's access list names the binary that wrote it, so a build signed with a different identity — every build that picks up Developer ID signing — is refused with `errSecAuthFailed` and the migration could not have run for the people who would need it.

- Released macOS binaries are signed with Developer ID, built with the hardened runtime and notarized, under the stable code identifier `ai.hyperneo.oap`. Signing runs before both the release archive and the npm platform binary are staged, so `@oap-sdk/cli-darwin-*` ships the signed binary too, and a tag build fails rather than publishing unsigned macOS artifacts (`workflow_dispatch` without the secrets warns and continues). A stable identity also fixes the local Keychain prompt-per-rebuild: the Keychain access list binds to the signing certificate instead of the code hash. `make build OAPX_CODESIGN_IDENTITY=<sha1>` signs a local build with the same identifier, and a self-signed certificate is enough for that — no Apple account required. The identity is named by the certificate's SHA-1 hash rather than its `Developer ID Application: ...` string, because a keychain holding the certificate twice makes the name ambiguous and `codesign` refuses it — though in CI either form works, since the throwaway keychain holds one certificate. The release secrets reuse `lsm/hyperneo`'s names and formats so one set of values serves both repos: `APPLE_CERTIFICATE` (base64 `.p12`), `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY`, `APPLE_API_PRIVATE_KEY` (raw `.p8` PEM, not base64), `APPLE_API_KEY` (the key id, which also names the `.p8` file notarytool is handed) and `APPLE_API_ISSUER`. Note that a bare CLI cannot be stapled (`stapler` only attaches tickets to `.app`, `.dmg` and `.pkg`), so first-run validation on a machine with no network still consults Apple online.

- `makai --oap-provider` serves the Open Agent Protocol `model-provider-core` profile: one vocabulary over every registered inference API, so a caller speaks one protocol and reaches Anthropic, OpenAI, Azure, Codex, Google or Ollama behind it without embedding a vendor SDK per vendor. It is a second front door on the same binary beside `makai --oap`, not a replacement for anything — each mode refuses the other's profile at decode and says which it serves. The endpoint answers `provider.describe` and `provider.models.list`, accepts `inference.create`, and drives the real provider stream, translating our assistant event union into the profile's `started`/`delta`/`ended` part triples with a single terminal per accepted inference. Eight registered APIs are describable: five earn a named wire, and both Google APIs plus Ollama say `other` with an opaque `wire_id`, because no second implementer speaks their shape. Eleven of the twelve compatibility facts carry across from `OpenAICompatOptions` unchanged; `usage_in_streaming` is left unstated when ours is `false`, because "do not send `include_usage`" cannot distinguish an endpoint that never reports usage from one that reports it in the terminal chunk. Credential grants are served at the out-of-band tier with the `static` kind, over a per-grant unix socket created under an owner-only directory and destroyed when the grant settles; a build whose toolchain has no unix sockets advertises `none` rather than a tier it cannot open. A grant is honoured only until the `expires_at_ms` its own grant response stated: an `inference.create` naming it after that instant is refused with `credential_expired` rather than served, and the grant is burned, so the expiry the endpoint advertises is the expiry it enforces. A **refreshable** grant is still refused, because `AuthStorage.persist` has no non-writing branch and the profile requires a granted credential to be unable to reach durable storage. The profile can be conformance-tested against this endpoint and cannot yet be compatibility-tested: a harness can drive every envelope against a local anonymous provider with no credentials, and no compatibility fact has been checked against the vendor it describes.

- A custom endpoint can declare `"auth": "none"` in `~/.oapx/providers.json` and have its requests go out with no credential, which is what a local llama.cpp, vLLM or LM Studio server needs ([`docs/custom-endpoints.md`](docs/custom-endpoints.md)). Previously every wire format available to custom providers raised `MissingApiKey` before sending anything, so a keyless server could only be reached by naming an environment variable holding a dummy value. It is an explicit opt-in rather than an inference from an absent key, so forgetting to log in still fails loudly instead of quietly sending an unauthenticated request, and a provider that declares it still prefers a real key when one resolves. The flag travels to the provider as `Model.allows_anonymous` and is serialized on the provider protocol, where an absent field means the previous behaviour. Each provider refuses to honour it for the vendor ids it serves, so no vendor path can start sending unauthenticated requests; a declared provider cannot hold one of those ids in any case, since they are reserved. An `auth` block that is present must now be one of the two valid shapes, and anything else is rejected at load time with `InvalidAuthMode` instead of being silently ignored: an object with no `env`, a non-string or empty `env`, a misspelled key such as `{ "environment": "KEY" }`, any other string, and any non-object non-string value. Each of those previously parsed as "no key" and surfaced much later as a confusing `MissingApiKey`. A load error disables all custom providers until the file parses, which the TUI reports as a startup row naming the file and the error.

- `scripts/check-zig-patterns.sh` now rejects a struct literal that allocates more than once — the defect class #331 fixed nineteen instances of by hand across four review rounds. The check is deliberately narrow so it has no false positives to argue with: it flags only a literal whose fields include two or more `dupe`/`dupeZ`/`allocSentinel`/`allocPrint`/`owned(` calls, which is unambiguous, because an `errdefer` cannot be written inside a literal and one written after it never runs when the assignment it guards was never reached. Brace tracking reuses the existing `strip_noncode` helper, so quoted strings and multiline `\\` literals containing braces cannot desynchronise it, and `test` blocks are skipped since a leak there already fails under `std.testing.allocator`. `known_multi_alloc_literals` declares the 41 pre-existing sites and is a shrinking backlog rather than an approved list: a new site fails and is expected to be fixed, and a declared site that disappears also fails, so a fix must remove its line and the count can only ratchet down. Entries are keyed by file plus source text rather than line number, matching `expected_ordinary_entropy_sites`, so moving code does not churn the list. The check adds about 3.6s to a 3.0s script; a prefilter would have saved roughly a second at the cost of a second pattern that has to stay in sync with the scanner, which is how this kind of guardrail usually breaks. Two sibling shapes are out of scope and documented as such in CLAUDE.md: an errdefer that frees a container without its contents, and a fully-built value dropped in a hand-off like `try list.append(allocator, try build(allocator))`.

- Added a Python SDK under `python/` (package `makai`, Python 3.11+, no third-party runtime dependencies), mirroring the TypeScript SDK's four namespaces over the same `makai --stdio` protocol: `auth` (`list_providers`, interactive `login` with event/prompt callbacks), `models` (`list`, `resolve` — `model_ref` stays opaque), `provider` (`complete`, `stream`), and `agent` (`run`, `stream`, with tools executed in client code through `ToolDefinition.execute`). The SDK is async-first: `async with makai.connect() as client`, `async for event in client.provider.stream(...)`, and a delegating blocking wrapper (`makai.connect_sync()`) for scripts. Errors mirror the TS classes with `kind`/`code` populated — `MakaiError` base plus `MakaiStreamError`, `MakaiAuthRequiredError`, `MakaiProtocolError`, and `MakaiAuthError` — and timeouts carry structured diagnostics. Binary resolution mirrors `typescript/src/binary_resolver.ts` (explicit path / `OAP_SDK_BINARY_PATH`, then `OAP_SDK_BINARY_URL` with a mandatory SHA-256, then `./zig-out/bin/makai`, `./zig/zig-out/bin/makai`, `PATH`), deliberately omitting the npm platform-package step, which has no published Python equivalent. The transport routes frames with a single reader task feeding per-`stream_id`/per-`session_id` queues registered before the request is sent, so it needs none of the TS client's TTL-buffered orphan queues; closing a client terminates the child (stdin close, `terminate`, `kill`) and fails in-flight requests with a typed transport error instead of hanging. Per spec §13.1 agent sequencing is per session and starts at 1 (`agent_start` 1, `agent_message` 2, `agent_stop` 3), `agent_start` carries both `session_id` and the legacy `resume_session_id` alias, and `session_id` is treated strictly as a correlation key — no resume path is implemented. A new `python-sdk-e2e` CI job runs `mypy --strict` and the test suite with `OAP_SDK_BINARY_PATH` pointed at a freshly built runtime; the Python SDK is not wired into the Zig build.
- Custom OpenAI- and Anthropic-compatible endpoints can be declared in `~/.oapx/providers.json` and appear in `/model`, the status bar and print mode ([`docs/custom-endpoints.md`](docs/custom-endpoints.md)). An entry carries an id, a wire format, a base URL, and optional headers, model list and capability overrides. Models are discovered from the endpoint's `/v1/models` and cached under `~/.oapx/model_catalog/`, with a declared list acting as an allowlist over the result so an aggregator serving hundreds stays usable, and as the fallback when discovery fails. Discovery never runs on the startup path: loading the catalog reads the cache and falls through to the declared list, and the fetch happens on the refresh that follows a successful `/login`, because `compat.http` sets no connect or read timeout and a declared endpoint that stalls would otherwise hold up `makai --tui` for as long as the peer cared to wait. A request naming a vendor wire format with a different `provider` is refused outright when that `provider` is itself a vendor id, and otherwise resolves its credential under an api-key-only rule that skips OAuth entries, so the `anthropic` and `openai-codex` OAuth tokens can no longer be resolved under a borrowed identity. This bounds identity, not destination: a provider's own token still travels to whatever `base_url` the request supplies, which is what proxy overrides rely on, and `github-copilot` cannot be added to the refused set because its models legitimately run on `openai-completions` and refusing it resolves no credential at all. The same api-key-only rule covers the six registered APIs that declare no `auth_provider_id`, where the credential is resolved under whatever id the request claims: naming `openai-completions` with `provider: "anthropic"` previously resolved the stored Anthropic OAuth token and sent it to the request's `base_url`. The Anthropic provider's own environment fallback is scoped to match: it reads `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` only when the model's provider is `anthropic`, as the OpenAI providers already did, so a custom endpoint can no longer be handed whichever vendor key happens to be in the environment. A custom provider that resolves no key of its own now fails with `MissingApiKey`; declaring a genuinely keyless endpoint is not supported yet. Keys never live in the file: `/login <id>` stores one in the keychain under the provider id, or an entry names an environment variable to read; an endpoint is listed and discoverable either way, though it cannot complete a turn without a key, since both providers raise `MissingApiKey` before sending a request. Capability fallback is per key rather than per block: undeclared keys are seeded with the generic values detection produces for an unrecognised endpoint, so declaring `cache_ttl` on a gateway no longer drags OpenAI-native defaults along with it and starts sending `max_completion_tokens` and `strict` to an endpoint implementing neither. A `capabilities` block populates `Model.compat`, which is what finally lets an Anthropic-compatible gateway on a private domain keep long prompt-cache TTL — capability detection is otherwise a hostname match that such a host can never satisfy. Base URLs are normalised to the origin, so pasting a vendor's documented URL ending in `/v1` no longer produces `/v1/v1/chat/completions` and a 404.

- Native Open Agent Protocol mode: `makai --oap [--model <model-ref>]` (also `OAPX_OAP_MODEL`) makes the binary speak OAP `0.1` `open-agent-protocol.agent-control-core` directly as JSONL over stdio, instead of requiring an external adapter over the native agent protocol. The endpoint lives in a new `zig/src/protocol/oap/` (types, envelope, server, bridge), parallel to `protocol/agent/`; the agent layer is untouched and stays protocol-agnostic. It serves the full minimum core surface (`protocol.initialize`, `capabilities`, `session.open`, `session.state`, `session.message.submit`, `run.cancel`, plus `session.state.updated`, `run.started`, `run.status.updated`, `content.delta` and the three terminals) with a revisioned capability descriptor, `stale_capabilities` precondition checking, and the `+run-controls` fail-closed gate — `run.model_selection` advertised `native`/`per_run`, the other three controls refused before any identity is allocated. It owns a reducer and terminal arbiter rather than renaming native frames: OAP run sequences are generated from receive order (makai's own frame sequence is not a portable per-run ordering domain), `agent_result` is retained as evidence until the terminal signal so makai's result-before-`agent_end` ordering cannot produce two terminals, cancellation is accepted as intent and settles only on authoritative evidence, and OAP session ids are mapped to freshly allocated native session ids rather than aliased. Three traces produced by the real binary — completed, provider-failed, and cancelled — validate against the OAP repository's own `oap validate`, and their frame-by-frame shape is pinned in CI by golden-trace tests in `zig build test-unit-protocol`. Tools, permissions, user input, persistence, model listing, `queue`/`steer`/`btw` delivery, dynamic capability updates and extension packs are unadvertised and refused with typed errors rather than ignored. Semantics, mapping decisions, and the four unresolved OAP/makai conflicts are recorded in [`docs/oap-alignment.md`](docs/oap-alignment.md).

- Added a Go SDK over the stdio protocol ([`go/`](go/README.md)): a dependency-free module (`github.com/lsm/makai/go`) that spawns `makai --stdio` and mirrors the TypeScript SDK's four namespaces — `Auth` (provider listing, interactive login with event and prompt callbacks), `Models` (list and resolve, with `ModelRef` kept opaque), `Provider` (`Complete` and `Stream`), and `Agent` (`Run` and `Stream`, with tools executed in client code through a callback). Idiomatic Go throughout: `context.Context` first on every I/O call, `bufio.Scanner`-shaped stream iterators (`Next`/`Event`/`Err`/`Close`), and typed errors reachable through `errors.As` (`*StreamError`, `*AuthRequiredError` which unwraps to it, `*ProtocolError`, `*AuthError`) whose cancellation cases wrap the context error so `errors.Is(err, context.Canceled)` works. The transport runs one reader goroutine that dispatches frames by the spec's §13.3 routing rules (`in_reply_to` correlation first, then the stream or session route) into per-call routes registered before the request is written, so no frame can be lost to a late subscriber; `Close` closes the runtime's stdin, waits out a grace period, then kills and always reaps the child. Binary resolution mirrors `typescript/src/binary_resolver.ts` minus its npm-only platform-package step, which has no Go equivalent. Sessions are not resumable and the SDK does not invent resume semantics; `RunOptions.SessionID` is documented as a correlation key only. Protocol tests run against a fake host built into the test binary (no runtime, no keys) and a `OAP_SDK_BINARY_PATH`-gated suite exercises a real `makai --stdio`. A `go-sdk` CI job runs `gofmt`, `go vet` and `go test -race` against a freshly built runtime. The module is deliberately not wired into the Zig build.

- Added `scripts/stdio-conformance.py`, a black-box conformance driver for `makai --stdio` that spawns the real binary and checks its observable behavior against DESIGN.md §4–5 and spec §13. Nine groups (`envelope`, `framing`, `sequencing`, `lifecycle`, `eviction`, `ids`, `routing`, `shutdown`, `resources`) cover 102 checks — envelope validation, per-session sequence scopes, session lifecycle and idle eviction, 100-way multiplexing, NDJSON framing and backpressure, EOF/disconnect, and ID formats. It needs no API keys or network: every check is answered before any provider call. Exit status is non-zero when a check fails, so it doubles as a regression gate.

- Added a Rust SDK under `rust/` (crate `makai`), mirroring the TypeScript SDK over the same `makai --stdio` protocol: `auth` (list providers, interactive login with event/prompt callbacks), `models` (list, resolve), `provider` (`complete`, `stream`), and `agent` (`run`, `stream`) with tools executing in the caller's process. Async on tokio; streams are `futures_core::Stream`s whose drop cancels the work (`abort_request` for a provider stream, `agent_stop` for an agent run) and whose transport reaps the child process on close or drop. The transport is a push router rather than the TypeScript pull-and-requeue model: callers register their routes before sending, and dispatch follows spec §13.3 — `in_reply_to` to the waiter that owns that `message_id`, otherwise the `stream_id`/`session_id` route — with one subscription holding several route keys so correlated replies and session-scoped run output arrive on one ordered queue. Failures are one `thiserror` enum mirroring the TS error classes (`Stream`/`AuthRequired`/`Protocol`/`Auth`, plus typed `Transport` and `InvalidRequest` for cases TypeScript leaves untyped), and no library path uses `unwrap`/`expect`/`panic`. Binary resolution mirrors `typescript/src/binary_resolver.ts` except for the npm platform-package step, which has no Rust counterpart and is documented as deliberately skipped; URL downloads are behind the non-default `download` feature, with an already-cached file still checksum-verified and used without it. Agent teardown follows spec §6.1's ownership rule (a caller-supplied session id is stopped only after a reply correlated to this attempt's own `agent_start`; an `agent_busy` session is never stopped) and resolves the stop sequence when the `agent_message` is still unresolved: the terminal path probes it against the server, and a dropped stream, which cannot await a reply from inside `Drop`, sends both candidates so the session is torn down whichever way admission went. The crate is not wired into the Zig build; a `rust-sdk` CI job runs `cargo fmt --check`, `cargo clippy --all-targets --all-features -- -D warnings`, and `cargo test` against a freshly built runtime. Tests need no credentials: the fake-server suite drives `makai-protocol-fake`, a scriptable `makai --stdio` stand-in that ships with the crate, and the binary-backed suite uses the runtime's `test-fixture` auth provider plus the unauthenticated `auth_required` failure paths. See [`rust/README.md`](rust/README.md).

### Fixed

- A DeepSeek run whose whole multi-step turn arrived before the prompt reply failed with `deepseek_invalid_grammar`: the retrospective admission skipped every `step/start` when replaying, not only the admitted one. Fixed in Go and Zig.

- A stdin reader still blocked when its handle was torn down no longer touches freed memory once input arrives: the handle now leaves the stream to the reader thread, which frees it when its read ends.
- The Zig semantic validator now judges tool sources: `unmatched_tool_source` (a descriptor or served catalog naming an undeclared source, a listed tool with none, a call attributed elsewhere or to nothing declared, a source changing mid-lifecycle, a provided tool naming a source a refresh removed), `unattributed_call`, `catalog_mismatch` for attached sources and provided tools, and `undisclosed_attach_limit`. An open response's reported sources are adopted as the attached descriptions, as Go does. `oapx validate` now fails 193 of the 306 semantic-invalid fixtures that need no pack, up from 178.

- `oapx serve agent` no longer aborts when a client request nests JSON more than 256 levels deep, for example a provided tool's `input_schema` at `session.open`: the envelope decoder re-encoded such members with `std.json.Stringify`, which checks nesting against a fixed 256-level stack in safety builds. The same encoder, `zig/src/json/encode.zig`, now also re-encodes model tool arguments in the agent loop, MCP schemas, arguments and results, content-part arguments in `oapx`, the TUI approval view, adapter configuration and the OpenCode port's history items.

- The Zig SSE parser (`zig/src/providers/sse_parser.zig`) no longer leaks an event's data when queueing the event fails to allocate. `checkAllAllocationFailures` over the OpenCode port's SSE decoder found it.

- The Go adapters refuse an explicit `queue`, `steer` or `btw` delivery they do not advertise with `unsupported_feature` naming `session.message.delivery.<mode>`, as `drafts/conformance.md` requires, instead of `invalid_submission` (or `internal` for OpenCode's `steer`). `adapter.RefuseUnadvertisedControls` now judges the delivery mode after the run controls, as the Zig contract does; OpenCode advertises `queue` through it. The Claude, Hermes and DeepSeek adapters answer a resolution naming no open interaction, such as any permission resolution, with `resolution_rejected` instead of `internal`.

- `oapx serve agent --backend claude` no longer panics on a tool input nested more than 256 levels deep. `std.json.Stringify` checks nesting against a fixed 256-level stack in safety builds, which releases are, while the Claude line decoder admits 10000. The emitted envelopes, the permission prompt and the input echoed back on allow now go through an iterative encoder, `zig/src/json/encode.zig`, whose output is byte-identical to Stringify's minified form.

- A Zig adapter corpus driver now fails when its case list and its corpus `manifest.json` disagree in either direction — a manifest case neither replayed nor excluded by name, a replayed case the manifest lacks, a case read from another path, or a stale exclusion. Before, dropping a case from a driver's list left the build green. Claude's `process-exit` is the one named exclusion: its own test replays it and allows the single Go-runtime message it quotes.

- The Claude Code adapter no longer fails a run when a `Bash` call outlives about three seconds. Claude Code 2.1.280 reports such a call as a foreground `local_bash` task whose `task_notification` carries `"output_file": ""`, and the Go and Zig decoders required that member non-empty, so the run failed with `claude_process_exit`. `output_file` must still be present, but may be empty.

- **`oapx validate` enforces the tool-call and interaction rules.** The Zig semantic machine now raises `unmatched_tool`, `illegal_tool_transition`, `pending_tool_at_terminal`, `duplicate_interaction`, `unmatched_interaction`, `wrong_interaction_responder`, `pending_interaction_at_terminal`, `resolution_payload_mismatch`, `wrong_tool_owner` and `undisclosed_provide_limit`: a call's lifecycle, permission and user-input resolution (answers included), the `action.call.resolve` ladder for control-owned calls, and the owner a call or a provided tool may claim. Of the 306 semantic-invalid fixtures that need no pack or tolerant mode, 178 now fail and 128 still pass on unported codes, up from 150. Two cases follow the drafts where the Go validator does not: a `capabilities.updated` retires the descriptor owners a call is judged against, and a permission or user-input resolution request is found by its payload's `run_id` rather than the envelope's.

- **`oapx --version` reports the version it was built as.** It printed a hard-coded `0.0.1` whatever the release. The version now comes from `zig/build.zig.zon`, a `-Dversion` build option overrides it, and the release workflow passes the tag, so a `v0.1.0-alpha.3` binary says `0.1.0-alpha.3`. The MCP bridge reads the same value instead of its own literal. The provider descriptor's `capability_revision` and `endpoint_version`, which reused the constant, follow it.

- The Zig DeepSeek line codec refuses a frame nested past 10000 containers, counting the frame object, where the Go codec refuses it (`exceeded max depth`), instead of parsing any depth (#247).

- **`oapx validate` no longer passes a trace it never checked.** It now runs the decode and schema phases before the semantic machine, in Go's order and with Go's codes (`malformed_json`, `duplicate_key` with the envelope's index, `schema_invalid`), and runs the semantic rules only when both are clean, as Go does. It reads a trace the way Go does too: blank input is an empty trace, a lone object is a one-envelope trace, and anything else that is not an array is read as newline-delimited JSON, with each finding carrying its line. Over the manifest's fixtures that need no pack or tolerant mode, every schema-invalid fixture now fails and no positive one does. The semantic port is still partial — 156 semantic-invalid fixtures still pass — so a pass says so, `--format json` reports `"complete": false`, and a trace the schema interpreter cannot judge is reported `UNJUDGED` and fails rather than passing.

- Every HTTPS provider response read as empty after #240, so the TUI showed no reply: a turn "completed" with an empty assistant message and no error, on kimi and openai-codex alike. `compat.readResponse` switched to `Reader.readVec`, which may return 0 after only refilling the reader's buffer, as TLS does after a decrypt; callers took that 0 for end of stream. It now reads again until bytes arrive or the stream ends. Plain-HTTP endpoints were unaffected, which is why loopback tests kept passing.

- The Zig DeepSeek adapter validates `subagent.finished` `lastAssistantMessage` the way the Go oracle does: each content block's member set per kind, Go's absent-versus-null rules, and the oracle's error text for mistyped or unknown block members. It previously checked only that the member was an array, so `[{"type":"bogus"}]` was admitted (#143).

- OAP model switching now retains the previous session model until its response is queued, so an allocation failure cannot silently switch the model or leak the old value. Allocation-failure probes cover model switching and listing, plus auth provider listing, login, cancellation, and disconnect. The auth adapter now propagates parsing OOM and releases native auth replies with their owning server allocator.

- Aligned core model-switch validation across Go and Zig, including degraded opt-in, switch-anchored model snapshots, unrelated refusals, and idempotent same-model updates. Agent-profile Go, Python, and Rust SDK requests now cite the discovered capability revision; successful auth responses are held to the same revision-repeat rule as other responses.

- The Zig DeepSeek adapter port refuses a notification's params the way the pinned Go codec decodes them, with the codec's own message. A member of the wrong JSON kind, params that are an array, params that are absent, and a `session.event` whose `event` is `null` or not an object all ended the session in the Go adapter and were mapped, or refused with a different message, in the port — a mistyped `sessionId`, for one, said `session.event sessionId is required` where Go says `json: cannot unmarshal number into Go struct field SessionEventNotification.sessionId of type string`. Where several members are wrong, the port now reports the first in document order, as `encoding/json` does. Member names are matched the way `encoding/json` matches a tag — case-insensitively, so `sessionid` is `sessionId` — where the port had refused every spelling but the exact one. The inside of `event` is still not checked, and `research/deepseek-harness-47f9438-mapping.md` records what that leaves open.

- `inference.create.response` is decoded with the same coherence rule its sibling grant response already had: an acceptance carrying a typed error, a refusal carrying none, and a refusal reporting an honoured snapshot policy are all refused rather than handed to a caller. The envelope half of this rule was already enforced — an acceptance without an `inference_id` scope, or a refusal with one, is an `AcceptanceScopeMismatch` — so acceptance was policed in the envelope and unpoliced one level down in the payload it describes, and a frame saying both things at once decoded cleanly. `makai --oap-provider` never emitted one; this is about what it accepts from a peer.

- The Anthropic provider's stream thread marks itself done when it is done, not when it decides to return. `markThreadDone()` was called on all 37 return paths inside `runThread`, ahead of the function's own `defer`s, so `waitForThread()` reported true while the thread was still freeing its header set, parser, HTTP client and delta buffers. It is one deferred call at the top of the function now, which runs last, matching what Ollama and Azure OpenAI Responses already did. The other four providers still mark early; [`docs/zig-stream-memory-ownership.md`](docs/zig-stream-memory-ownership.md) now says which do which, because a leak-checked test driving one of them can see an allocation that is about to be freed and call it a leak.

- The Anthropic provider no longer leaks the id and name of every streamed tool call. `parseAnthropicEventType` dupes both out of the `content_block_start` frame and the stream thread handed them straight to a `toolcall_start` event without ever freeing them, so one leak per tool call survived the request — invisible to the test suite, which has no seam for driving that thread with canned SSE, and found by counting allocations in a live run instead. They are freed the way the delta strings are now freed, which depends on how the stream was built: when it clones events on push — `requires_owned_stream_events`, which both production hosts ask for — the provider frees each string as soon as the push returns, because the queued event holds a copy; otherwise it defers to thread exit, because the queued event holds the string itself. The deferred branch is the only one that can dangle for a consumer still draining at thread exit ([#192](https://github.com/lsm/makai/issues/192)), and freeing on push also stops a long generation from holding every delta it ever emitted until the stream ends. The tail flush that drains a trailing partial SSE event had the same hole twice over: it parsed each event and dropped it, freeing nothing, so a response ending mid-event leaked whatever that event had duped. Every owning variant is now freed through one `ParseResult.deinit`, whose switch is exhaustive, rather than a hand-maintained list that had already missed two variants. Unchanged and still open: on a stream built without `requires_owned_stream_events`, those delta strings are freed when the producer thread exits and a consumer still draining the queue reads freed memory ([#192](https://github.com/lsm/makai/issues/192)); both production hosts ask for owned events and so are not exposed to it.

- The TypeScript SDK's stdio client no longer discards a correlated reply when two waits on one session are in flight. `waitForRoutedFrame` raced its own read loop against a poke from whichever waiter holds the read lock, and the poke branch dequeued from `replyFrameQueues` unconditionally — including after the race had already been settled by the read loop, in which case the dequeued frame was dropped on the floor. The poke now only signals, and the dequeue happens after the race, so a reply parked while the owner's own read wins stays queued for its next wait. The observable failure was two concurrent `agent.run` calls on one session id: the established run lost its `agent_started`, never sent `agent_message`, and timed out after `responseTimeoutMs`; when the duplicate lost its `agent_error` the same way it then waited out the established run's read lock before burning its own budget, so it settled at twice the timeout. It reproduced in roughly 0.6% of attempts and is the source of the intermittent 10s failures in `typescript/test/execution_client.test.ts`. The regression test drives the interleaving deterministically by having the fixture write two replies in a single chunk, which is what forces the lock to change hands inside one event-loop turn; it fails on every run without the fix. In the same function, `correlateDeliveries` was keyed by correlate and held one waiter, so two concurrent waits sharing a correlate overwrote each other's registration and the loser was never signalled — it then depended on acquiring the read lock, which a foreign waiter can hold for its whole timeout. The queue kept that case correct (the next wait dequeues the parked reply) but not prompt. It now holds every waiter, and each delivered frame signals the first waiter that is neither settled nor already signalled — an entry's settled flag is only set once its wait resumes, so two frames arriving in one synchronous pass would otherwise both re-signal the same waiter and leave the second one stalled behind the read lock.

- The inline TUI frame is padded to the full viewport height, so the composer and status line sit on the bottom row from the first paint. Previously the frame was only as tall as its own content and grew downward from wherever the shell left the cursor, leaving the rest of the screen blank below the status line until the transcript grew tall enough to fill it — most visible right after launch, where typing `/m` drew the command palette with a large void beneath it. The PTY harness's steer echo assertion now reads the rendered screen instead of the raw byte stream, since a bottom-anchored frame repaints the rows above an insertion too.

- Reserve stream-registration map capacity before starting a provider stream, so an allocation failure can no longer strand a running stream, its `PartialState` and its cancel flag (`zig/src/protocol/provider/server.zig`).

- `makai --oap` now answers the stdio binding's transport control frames instead of ignoring them, and treats a malformed line as a framing fault rather than a protocol error. A control frame is a JSON object with a `control` member and no `protocol` member; the binding requires an endpoint that does not implement one to answer `{"control":"<name>.error","code":"unsupported_control"}` and keep running. The endpoint previously produced nothing at all, so a host asking for a cursor replay blocked until its own timeout — which in the OAP conformance harness tore down the pipe and failed the five checks that ran after it, none of which had reached the endpoint. Separately, a line that is not JSON, not a JSON object, or neither envelope nor control frame now writes one bounded diagnostic to stderr and exits non-zero, writing nothing to stdout; it previously wrote an `error.response` envelope that carried no `in_reply_to` (an unparseable line has no id to reply to) and exited 0, reporting a clean end for a stream whose framing is in doubt. A JSON object that declares `protocol` and carries an `id` is still treated as an envelope, so an undecodable or unknown-type envelope keeps answering with a typed `error.response`, now correlated by `in_reply_to` instead of uncorrelated as before. One that declares `protocol` and carries no `id` is fatal for a different reason: every response this binding defines is correlated, so no refusal could be addressed to it, and answering something uncorrelated would corrupt a stream the host reads by correlation while dropping it silently would leave the host waiting forever. The two fatal cases carry distinct stderr diagnostics. `oap conformance` against the endpoint goes from 8 passed / 9 failed to all checks passing, with cursor replay recorded as a skip the binding permits. The two new paths are pinned against the allocation-failure defect family of #331 by a `checkAllAllocationFailures` probe over the control answer: the answer buffer is released by one `defer` on every path (an `errdefer` that stayed armed past an explicit `deinit` would double-free when queueing the answer failed), and a `parseFromSlice` that runs out of memory now propagates `OutOfMemory` instead of reporting a framing defect.

- `OpenAICompatOptions` can now express "not declared" on all twelve of its fields. Four of them could not: `max_tokens_field` and `thinking_format` were non-optional enums defaulting to `.max_completion_tokens` and `.openai`, and `supports_strict_mode` and `supports_usage_in_streaming` were optionals defaulting to `true`, so the `orelse` their consumers wrote could never fire. Every other field defaults to null and falls back to URL-based detection in `utils/provider_caps.zig`; these four silently forced OpenAI-native behaviour on whatever the author of a partially-populated block had not set. #318 fixed the one caller by having `parseCapabilities` in `custom_providers.zig` seed its result with the generic detection values, which repaired custom endpoints but left the struct itself a trap for the next construction site, and cost recognised hosts their detection: a declared endpoint on a `zai` or `qwen` host got the generic seed's `.openai` thinking format the moment it declared any unrelated capability. All four are now `?`-typed and default to null, `mergeCompat` in `openai_completions_api.zig` treats all twelve uniformly with `orelse`, and the seed is gone — an undeclared key is answered by detection on its own, recognised host or not. The OpenAI Responses path picked up the same fallback for strict tool schemas, where an unset field had been reading as `true` for any model carrying a compat block at all.

  This crosses the provider protocol, which is why it was not done in #318. `serializeOpenAICompatOptions` wrote both enums unconditionally with `@tagName`, so every frame carrying a compat block asserted a value for them whether or not one was ever declared; they are now written through a `serializeOptionalTagField` helper that omits null, matching what `serializeOptionalBoolField` already did for the booleans, and `deserializeOpenAICompatOptions` maps an absent field to null rather than to a default. An absent capability field on the wire therefore means *unset, detect*, consistently for all twelve. This is compatible in both directions for first-party peers: no SDK constructs or reads a compat block — it is built inside the runtime from `~/.oapx/providers.json`, the catalog and the proxy-override flags — and a frame from an older peer still carries explicit values for the two enums, which decode as declared. Documented in [`docs/custom-endpoints.md`](docs/custom-endpoints.md); the spec does not describe the compat block, so nothing there needed amending.

- `downloadToCache` in `typescript/src/binary_resolver.ts` left its temp file on disk whenever a download could not be finalized. The function wrote `<cachePath>.tmp` and then ran `chmod` and `rename` with no `try`/`catch` anywhere, so a failure in either — `EXDEV` when the cache directory is on a different filesystem from the rename target, a filesystem that rejects `chmod`, `ENOSPC` part-way through the write, or the target path being occupied — abandoned a file that nothing ever cleans up: `resolveMakaiBinary` only ever checks the target path, never the temp. Since `tempPath` is derived deterministically from `targetPath`, the stale file is overwritten on the next attempt rather than accumulating, but a failed download of a release binary still strands tens of megabytes indefinitely. The write, chmod and rename now run inside a `try` that removes the temp file before rethrowing, so the error still propagates unchanged and only the debris is cleaned up. This is the same partial-construction shape as the Zig allocation-failure leaks in #331, in the one form it can take in a garbage-collected language: the resource that leaks is a file, not memory. Covered by a regression test that occupies the cache path mid-download to force the rename to fail and then asserts the temp file is gone; it fails on the previous code.

- Intermittent SIGSEGV in provider stream teardown: a stream could be freed while its provider thread was still alive, and the thread then faulted on its next `push`/`pushBlocking`. `EventStream.deinit` joined the producer under `wait_for_thread_on_deinit` but discarded the result of `cancelAndJoinThread` and poisoned and freed regardless, so a bounded join that expired handed the allocation back to the allocator with the thread still holding the pointer; `std.heap.DebugAllocator` unmaps an allocation that large, so the next touch is a hard fault rather than a silent read. The join expires in practice because `compat.http` sets no connect or read timeout and providers only test their `CancelToken` between reads, so a stalled upstream parks a provider thread indefinitely — and `ProtocolServer.deinit` never set the cancel flag at all before joining (`handleAbortRequest` and `handleCompleteRequest` both did), leaving even a cooperative provider nothing to observe. The observed CI signature was the three symptoms of one stalled connection: the streaming test failing its own assertions once the 60s e2e deadline passed, that test's leak check reporting the zombie thread's allocations, and the *next* test in the binary dying with `@atomicLoad` on `self.completed.raw` inside `pushBlocking`, reached from the provider's first `.start` push — the first stream access after the wedged HTTP phase it had been parked in. `deinit` now abandons rather than finishing when the join fails: it leaves the stream unpoisoned and its queued events, result and error message unfreed, and reports `wasAbandoned()`. A new `deinitAndDestroy()` pairs the join with the `destroy` that every heap-allocated stream needs, freeing only on a successful join and returning false otherwise, and `ProtocolServer` routes all nine of its free sites — teardown, `cleanupCompletedStreams` for both active and `pending_cleanup` streams, the abort path's append-failure fallback, the borrowed-events rejection, the complete path and both `streamWithRefresh` auth retries — through one `releaseStream`/`releaseProviderStream` pair that signals the cancel flag first, joins under `provider_join_timeout_ms`, and holds back the `CancelToken` allocation too when the thread outlives it. The same `deinit`-then-`destroy` pair was live in `agent_loop`'s per-turn provider stream, `Agent`'s loop stream, `stream.zig`'s `complete`/`completeSimple` facades and four paths in `tools/makai.zig` (the stdio host's `ActiveAgentRun` and both print modes); all now use `deinitAndDestroy`. Separately, `markThreadDone` published `thread_done` *before* bumping and waking the futex, so a waiter could return from `waitForThread` and free the stream while the producer was still executing those two writes — a narrower instance of the same class, always present. It now publishes `thread_done` last, and `waitForThread` polls on a bounded `THREAD_DONE_POLL_INTERVAL_MS` so the reordering costs no latency. Trading a bounded leak for a use-after-free matches the policy the complete path already carried (`a provider that ignores cancellation is abandoned, not freed underneath`); [`docs/zig-stream-memory-ownership.md`](docs/zig-stream-memory-ownership.md) records the contract.

- HTTP requests that read a whole response are now bounded by a timeout. Nothing in the tree had one: `compat/http.zig` wraps `std.http.Client` and passed no deadline, so an endpoint that was down, dropped packets, or accepted the connection and then went silent blocked the calling thread for as long as the peer cared to wait. `compat.http.fetch` runs the request on its own thread, which owns its client, its allocations and a copy of every input, and waits on a `std.Io.Event` with a deadline; on expiry the caller abandons the thread and returns `error.Timeout`, and the thread frees what it owns once the peer answers or the connection drops. Converted: the Anthropic, Codex and custom-provider model catalog fetches (20s), and the GitHub device-code, token-poll, Copilot-token, model-policy and Anthropic and Codex token-exchange OAuth calls (30s). Proxy environment variables are still honoured, on the worker's own copy of the environment. Provider streaming is deliberately left unbounded: there is no whole-body read to bound, and a read deadline there would kill a turn whenever a model thinks for longer than the timeout. Two mechanisms that look right do not work and are recorded in [`docs/custom-endpoints.md`](docs/custom-endpoints.md): `std.http.Client.ConnectTcpOptions.timeout` is declared but never read anywhere under `std/http/`, and setting `SO_RCVTIMEO` makes `std.Io.Threaded` panic on the resulting `EAGAIN` in debug builds. Connect itself remains bounded only by the operating system, since the connection is established inside `Client.request` before the wrapper sees it.

- A stored OAuth credential is now bound to the origins its provider is expected to serve, so a stream request can no longer aim one at an endpoint of its choosing. #318 closed the identity-borrowing routes — a request can no longer resolve the `anthropic` or `openai-codex` token under another provider's name — but deliberately left the destination unbounded, because `base_url` arrives in the request and proxy overrides depend on it. That left two live routes: `provider: "anthropic"` on `anthropic-messages`, and `provider: "github-copilot"` on `openai-completions`, each reaching its own token with any `base_url` the caller supplied. Before the token is handed to a provider, `model.base_url` is now compared against an allowed set for that provider and the request is refused with `auth_required` when it points elsewhere. Origin means scheme, host and port; the path is not compared, so a proxy route under an allowed host stays usable, and an empty `base_url` is still allowed because the server fills the endpoint in itself from the same defaults. The allowed set is the vendor's own origin (`api.anthropic.com`, `chatgpt.com`, and any host under `githubcopilot.com`, which covers an `api.<tenant>.githubcopilot.com` enterprise tenant), plus whatever the environment names for that provider — `OAPX_BASE_URL` and the existing per-provider `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL` and `DEEPSEEK_BASE_URL` — plus the endpoint GitHub Copilot recorded in the credential's `provider_data` at login, so an enterprise deployment off that domain keeps working with no configuration. The asymmetry is the whole rule: an environment variable is set by whoever runs the process, while `base_url` can come from a remote protocol client, so an operator-configured proxy is allowed and an origin that merely arrived in a request is not, and no new variable was added to relax the check. A provider id with no entry — today only `test-fixture`, tomorrow any OAuth provider added without one — has no allowed origin and its token is withheld from every non-empty `base_url`, so a new OAuth provider fails loudly rather than silently reopening the gap. Not every `.oauth` entry is an OAuth credential, though: a login carrying `provider_data` is persisted as `.oauth` with an empty refresh and `expires` at maxInt rather than as `.api_key`, which is how `/login kimi` stores its key alongside a region, and how the legacy `region` field migrates. An entry with no refresh token is therefore exempt when its provider id has no policy, so Kimi keeps streaming; an id that does have a policy stays bound whatever its refresh looks like, so the exemption cannot unbind a vendor token. Plain `.api_key` entries and keys from a declared provider's environment variable are not checked at all, since constraining that pairing would regress custom endpoints for no gain. Enforcement sits at the two points where a credential is resolved, `streamWithRefresh` and `streamWithResolvedKey` in `protocol/provider/server.zig`, so it covers the refresh, retry and resolve paths alike rather than each call site. `github-copilot` is why widening the refused set could not have fixed this: Copilot is stored as OAuth and its models genuinely run on `openai-completions`, which declares no `auth_provider_id`, so the legitimate request and the exfiltrating one differ only in `base_url`. The test pinning Copilot resolving its stored token on `openai-completions` still pins exactly that, now against a `githubcopilot.com` base URL instead of an arbitrary one, with a sibling test pinning the refusal elsewhere. See [`docs/custom-endpoints.md`](docs/custom-endpoints.md).

- Fixed three further sites of this family flagged in a third review round of #331. `assistantTextMessageWithMeta` and `assistantToolCallMessage` (`tui/session_store.zig`) each held an `errdefer allocator.free(content)` that released the one-element `AssistantContent` array while stranding the one-to-three strings duped into it, so the guard also failed to cover the later `assistantMessage` call, whose own three dupes can fail after the content is fully built. `nextExecuteEnvelope` (`protocol/tool/local_runtime.zig`) packed three unguarded dupes into its returned envelope literal; its sequence increment also moved below the allocations, so an OOM no longer burns a tool-protocol sequence number for an envelope that was never produced. Pinned by two more `checkAllAllocationFailures` probes, each reporting 15 bytes (`"call-0123456789"`, the tool call id) on the reverted code. This closes the sites reported against this PR, but not the family: a tree-wide scan for the pattern returns roughly 200 candidate sites across `zig/src`, and spot-checking suggests a substantial fraction are genuine — among them `status_bar.zig`'s `pushOwnedValue` hand-off, `utils/oauth/anthropic.zig`'s token literals and `agent_loop.zig`'s `createToolResultMessage`. Closing the rest is tracked separately rather than folded in here.

- Closed the last three sites of this defect family, found in a second review round of #331. `TuiEvent.clone` (`tui/session.zig`) duped straight into the copy's fields across five arms — `message_end` alone performs eight — so a later failure left every earlier dupe owned by a local nothing frees. `parseAssistantContent` and `parseUserContentPart` (`tui/session_store.zig`) had unguarded multi-dupe literals in their `text`, `thinking`, `tool_call` and `image` arms; the caller's prefix errdefer cannot help there, because the block that leaks is the one still being built and never reaches the array. The third is the complement of the `toolMetadataFromAgentTool` fix rather than a repeat of it: the metadata was built correctly and then leaked in the hand-off, since `try metas.append(allocator, try toolMetadataFromAgentTool(...))` evaluates the inner call first and an `append` that fails while growing drops a fully-built value the list errdefer never covers — guarding a function's interior does not guard what the caller does with its result. Pinned by four more `checkAllAllocationFailures` probes; reverting each reports the expected bytes — 17 (`"final answer text"`) for the clone arm, 15 (`"call-0123456789"`, the tool call id) for `parseAssistantContent`, and 120 across four allocations for the hand-off, which is exactly the name, description, schema and version of one whole `ToolMetadata`.

- Extended the same allocation-failure sweep to eight more multi-allocation struct literals found in review of #331. `toolMetadataFromAgentTool` (`protocol/tool/local_runtime.zig`) is the production-reachable one: `tool_list_response` calls it once per tool, and its returned literal packed four unguarded `allocator.dupe` calls, so a mid-literal failure leaked every earlier field — the caller's errdefer covers only metadata already appended to the list, never the entry still being built. In `tui/session_store.zig`, five `parseEvent` arms (`message_end` with eight allocations, `tool_approval_requested`, `tool_execution_start`, `tool_execution_update` and `tool_execution_end`) had the same shape, as did `toolResultFromFields` and the `tool_result` arm of `parseMessage`. Those last two also show why freeing the container is not enough: each held an `errdefer allocator.free(parts)` that released the one-element `UserContentPart` array while stranding the text duped into it, so the guard that existed freed 40 bytes and leaked 47. Every site now builds its fields into locals with their own errdefers ahead of the literal. Pinned by four `checkAllAllocationFailures` probes; reverting each reports the expected bytes — 13 (`"shell_execute"`, the tool name) when the description dupe fails, 17 (`"final answer text"`) when `message_end`'s `content_json` dupe fails, and 47 (the tool result text) alongside the 40 bytes the old errdefer did free.

- Fixed a fifth allocation-failure leak of the same family in `parseToolResultFromPayload` (`tui/session_store.zig`), which builds the whole `ToolResultMessage` as one returned struct literal containing five fallible calls and carried no errdefer at all. Zig evaluates literal fields in order, so a failure in any of them leaked everything the earlier ones had already produced: the `tool_call_id` and `tool_name` dupes, the `content` array from `parseUserParts`, and the `details_json` dupe. It is not the loop-indexed shape the sweep targeted — there is no counter and no prefix errdefer — but it fails for the same underlying reason, a literal that allocates more than once with no place to unwind to. Two of the five values are compound and need deep cleanup rather than a plain free, so the `content` errdefer walks the parts and deinits each before freeing the array; freeing only the array would strand every part's own allocation. Pinned by `std.testing.checkAllAllocationFailures`: on the original code the probe reports 15 bytes (`tool_call_id`) when the `tool_name` dupe fails, and with only the `content` errdefer removed it reports 60 bytes across two allocations — the one-element `UserContentPart` array and the text inside it — when the `details_json` dupe fails, which is what pins the deep walk rather than a bare free.

- Fixed the same allocation-failure leak `ai_types.cloneModel` carried (#328) at four more sites, each found by sweeping for a struct literal assigned into a loop-indexed slot whose `filled`/`initialized` counter is incremented only after the assignment: `parseArtifacts` in `tui/session_store.zig`, both `cloneArtifactsToTool` and `cloneArtifactsToAgent` in `protocol/tool/local_runtime.zig`, and the `modelsTestDelegate` test-support handler in `protocol/agent/server.zig`. In each, a literal packed five `try allocator.dupe` calls together, so an earlier dupe had already succeeded when a later one failed; the literal never completed, the slot was never written, the counter never advanced, and the aggregate errdefer over the `[0..counter]` prefix excluded the entry, leaking every field duped for it. Each field is now built into a local with its own errdefer before the literal, leaving the literal itself infallible — the shape `deserializeHeaderPairs` in `protocol/provider/envelope.zig` already used. `modelsTestDelegate` also allocated its `capabilities` slice before the literal with no errdefer, so that leaked too on any literal failure. The four functions were checked for the second, after-the-loop gap `cloneModel` had — an errdefer scoped inside the loop body covering a failure during an iteration but nothing once the loop completes — and none has it: all four already declare the aggregate errdefer at function scope and do nothing fallible after the loop. Pinned by `std.testing.checkAllAllocationFailures`; reverting each fix while keeping its test reports the expected bytes — 7 (`artifact_id`, `"art-one"`) at all three artifact sites when the `uri` dupe fails, 1 (the unguarded one-element `capabilities` slice) and 46 (`model_ref`, `"anthropic/anthropic-messages@claude-sonnet-4-5"`) at the descriptor site.

- Made the provider decoder require the envelope `version` field, so all four protocols now agree on it. #319 hardened every envelope decoder against malformed frames but left the four inconsistent on this one field: agent, auth and tool took `fields.requiredInt(u8, root, "version")` while provider kept `fields.optionalInt(u8, obj, "version", 1)`, because a long-standing e2e test pinned the lenient behavior. The same envelope missing the same field was therefore rejected by three decoders and silently defaulted by the fourth — and `version` is exactly the field you would reach for to evolve the envelope, so a third-party client omitting it got different answers depending on which protocol it was speaking. Strict matches the spec, whose normative envelope examples all carry `"version": 1` (§7) and which fixes the protocol version at `1` (§863), so this removes drift rather than adding a rule. No first-party client is affected: the Go, Python and TypeScript SDKs all set the field on every outbound frame, verified by running all three suites against a binary built from this change. A provider frame without `version` now draws a correlated `nack` carrying `invalid_request` and "envelope is missing a required field" instead of being accepted as version 1.

- Fixed two allocation-failure leaks in `ai_types.cloneModel`. The header loop built each `HeaderPair` as one struct literal, so the `name` dupe was evaluated and succeeded before the `value` dupe could fail; the literal never completed, the slot was never written, and the name bytes leaked. The errdefer that would have freed them was declared *after* the enclosing `if`, so it was not yet registered when the loop ran — and it iterated the whole allocated slice rather than a filled prefix, which would have freed uninitialized entries had it ever fired. Separately, the `input` array's per-iteration errdefer was scoped to the loop body, so it covered a failure *during* the loop but nothing afterwards: any later failure in the function freed the slice and leaked every string already duped into it. Both loops now track a filled count with a single errdefer covering that prefix plus the buffer, and the header entry's fields are built into locals before the literal. Pinned by `std.testing.checkAllAllocationFailures`, which fails on the old code at the value dupe (5 bytes, the header name) and at the `HeaderPair` allocation (4 bytes, the input string).


- Streamed provider events were dropped whenever the consumer fell behind. `EventStream.push` returns `error.QueueFull` on a full ring (1023 usable slots) rather than blocking, and the Anthropic, Google Generative, Google Vertex and Ollama providers discarded that with `catch {}`, so a slow consumer silently lost events. Those providers now use `pushBlocking`, which retries instead of dropping. On its own that would have traded the drop for a hang: `pushBlocking` spins until the ring drains or the stream completes, and the two waiters that should have been draining it — `stream.zig`'s completion wait and `handleCompleteRequest` in `protocol/provider/server.zig` — waited on the result without polling events. OpenAI Completions and Responses already used `pushBlocking`, so for those two the hang was live before this change rather than introduced by it. Both waits now drain while they wait, against a monotonic deadline checked inside the drain loop rather than only around it. Owned events popped during a drain go back through `releaseEvent`, so draining an owned-event stream no longer leaks every event it discards. The Anthropic provider propagates OOM instead of silently dropping the final content blocks (#303), and its four new OOM exits publish the stream error before signalling `markThreadDone`, the order every other exit in that file already used — teardown gates on `thread_done`, so signalling first lets a waiter free the stream while the thread is still writing the error into it. The Google Generative, Google Vertex and Ollama providers stop leaking the duplicated `toolcall_end` event the same pattern produced, and Anthropic stops duplicating it at all: it had copied the id, name, arguments and signature for the event and skipped the push entirely if any of those four allocations failed, so an OOM silently dropped a tool call that `content_blocks` still carried — a new way to lose a streamed event, in the change meant to stop losing them. Its `toolcall_end` now aliases the `content_blocks` entry, as the sibling providers already do and as this file already does for `text_end` and `thinking_end`, which removes the allocation and the failure mode with it. On the complete path the provider thread is now cancelled and joined before the stream is freed, bounded by a configurable `provider_join_timeout_ms` (default 30s); a thread that outlasts the budget has its stream abandoned rather than freed underneath it, which trades a bounded leak for a use-after-free. `injectCompleteOptions` names the complete path's option builder and deliberately omits `requires_owned_stream_events`, a divergence from the streaming path that is pinned by tests on both sides. `check-zig-patterns.sh` gained a guard so the drain-while-waiting contract cannot be silently removed.
- Provider failures now say what the provider said. The OpenAI Completions, OpenAI Responses and Azure paths all read the upstream error body and then handed it to `std.debug.print`, which the TUI redirects into `~/.oapx/tui-stderr.log`, so a user saw `openai request failed: status=403` or, on the Responses path, the bare string `responses request failed` with no status at all. The reason was on disk in a file nothing points at. All three now build the error from the status and the upstream message, through a shared `providers/error_detail.zig` that understands the `{"error":{"type","message"}}` envelope, a bare string `error`, and a top-level `detail`. A Kimi quota stop now reads `kimi request failed: HTTP 403 (access_terminated_error: You've reached your 5-hour usage limit…)` instead of a status code, and the message names the model's own provider rather than always saying `openai`. The debug prints are gone, which also removes three writes to stderr from paths the TUI owns. Anthropic already did this and is unchanged.

- GitHub Copilot models never appeared in `/model`. `model_catalog.zig` merged Codex, Kimi, Anthropic and custom providers and had no Copilot source at all, so logging in succeeded, `model catalog refreshed` was printed, and nothing was added. The login flow already asks Copilot to enable every known model and gets the list back, but that list was freed when login finished. It is now persisted in the credential's `provider_data` alongside the resolved API base URL, and a new loader turns it into models on `openai-completions`. A login that predates this, or one whose list could not be read, falls back to the built-in known-model list and the default base URL, except for an enterprise login with no stored base URL, which contributes nothing rather than offering models on the individual endpoint its token cannot reach; a refresh or a fresh login repopulates it. The Copilot login path also stopped duplicating its resolved API base URL out of memory it had just freed, a read-after-free on every successful login whose token carried a proxy endpoint; the refresh path is written the same way. Classifying a failure as an auth failure now reads the status out of the message when one is present, so a non-auth status whose upstream text happens to mention `403` or `unauthorized` no longer triggers a token refresh and retry. The three Copilot entries in `STATIC_MODEL_CATALOG` are unrelated: that array only serves the SDK's `models.list`.

- macOS credential reads no longer block on an invisible Keychain prompt, and lock contention is no longer reported as a Keychain refusal. `AuthStorage.loadDefault` reads the `com.makai.auth` item under `SecKeychainSetUserInteractionAllowed(0)`, so a binary the item's access list does not authorize gets `errSecInteractionNotAllowed` and falls back to `~/.oapx/auth.json` instead of hanging on a prompt that never surfaces from a non-interactive shell — the symptom was `makai auth providers --json` printing `ready`, going silent on the first credential-touching request, and having to be killed. That fallback uses `loadFromFile`, not `loadFromFileWithSaveFn`, so a background refresh cannot re-save and re-arm the prompt; writes still prompt, and `OAPX_KEYCHAIN_SERVICE` still isolates items for local runs. Reads also take the keychain mutex with a bounded retry instead of a bare `tryLock`, and a holder that outlasts the budget now yields a distinct `busy` result rather than being mislabelled `needs_interaction`: two concurrent reads previously made one of them degrade to the file and report "no credentials" even though the keychain held them, with nothing distinguishing that from a real refusal. `std.Io.Mutex.tryLock` takes no `io` parameter in Zig 0.16.0 (`std/Io.zig`), unlike `lock`/`lockUncancelable`/`unlock`. The lock policy now lives in cross-platform code, so the Linux unit matrix analyses and exercises it rather than leaving it to macOS-target builds alone, and the macOS implementation and the non-macOS stub now declare identical explicit error sets — `KeychainError` for the non-allocating entry points and `KeychainAllocError` (that plus `Allocator.Error`) for the four that allocate. They had diverging inferred contracts in both directions, with nothing checking either: the stub could not produce the `KeychainBusy`/`KeychainNeedsInteraction` that callers matched on, and the macOS side could return an `OutOfMemory` the stub could not, so a caller handling it would have compiled on macOS and failed to compile on Linux.

- `zig/src/utils/oauth/storage.zig` had a module in `build.zig` but no test artifact, so its thirteen tests had never run in any build step or CI job, and had rotted past compiling under Zig 0.16 — `std.posix.setenv`, `std.fs.cwd()`, `Dir.createFile`, `Dir.rename`, `File.stat()` and `File.readToEndAlloc` all carried pre-0.16 signatures. `oauth_storage_test` is now wired into both `test` and `test-unit-utils`, restoring the invariant that every artifact in the global `test` step also sits in a group the CI matrix actually invokes. The rotted call sites moved onto the repo's own `compat.fs` wrappers, and the `HOME` override swaps `std.testing.environ` instead of mutating the real process environment: `test "AuthStorage - load non-existent file"` set no override at all and asserted an empty store against the developer's real `~/.oapx/auth.json`, so it would have failed on any machine with credentials on disk. `oauth_storage_saveToFile_does_not_require_directory_iteration` was the one test of the thirteen that could not pass, and was left skipped behind a constant rather than deleted because it encodes a design goal; it now runs (see the following entry).
- Saving credentials no longer requires read permission on `~/.oapx`. `atomicSaveCredentials` opened a handle on the auth directory and did its `createFile`, `deleteFile` and `rename` relative to that handle, and opening a directory handle requires read permission — so a `~/.oapx` set to `0o300` (write plus search, no read) failed the whole save with `error.AccessDenied` before writing a byte, even though every operation the save actually performs needs only search and write. The write path now builds the temp path with `std.fs.path.join` and issues those three calls against `std.Io.Dir.cwd()` by path, which is what `prepareAuthDirectory` (`createDirPath`) and the post-rename re-harden (`compat.fs.openFile`) already did, and matches `cleanupExistingAuthDirectory`, which has always tolerated an unreadable directory with `catch return`. The atomic same-directory temp-file-plus-rename sequence, the `0o600` file permissions, the `sync` before rename and the cleanup of the temp file on a failed rename are unchanged and still covered by their own tests. This unskips `oauth_storage_saveToFile_does_not_require_directory_iteration`, so all sixteen tests in the file now run: the constant gating it is flipped to `false`, and reverting the write-path change alone reproduces the original `error.AccessDenied`. The test's own setup had to stop using a directory handle too: it opened `~/.oapx` and called `Dir.setPermissions` on the handle, which is `fchmod`, and on Linux `openDir` without `.iterate` sets `O_PATH` (`if (@hasField(posix.O, "PATH") and !options.iterate) flags.PATH = true;`), whose descriptor `fchmod` rejects with `EBADF` — a panic, since std treats that errno as a programmer bug. macOS has no `O_PATH`, so the setup worked there and crashed on the Linux runner the first time the unskipped test ran. It now chmods by path through `Dir.setFilePermissions` (`fchmodat`), which needs no handle on either platform and matches what the test is about.
- Provider streaming threads signalled completion before they had finished touching the stream, so a consumer could free it mid-write. `EventStream.deinit` sets `completed` itself and then waits on `thread_done` alone, which makes that flag the only synchronisation point between a provider thread and teardown. Azure signalled it at each exit while freeing its `ThreadCtx` through a trailing `defer`, so a waiter observed "done" while the thread still owned the API key, base URL, body and cloned model — the leak abort that failed `provider_cancellation_azure_cancel_before_request` on CI while passing locally. The Anthropic, Google Generative and Google Vertex providers went further and called `markThreadDone` *before* `complete`/`completeWithError` at all 71 of their exits, so teardown could drain and free the stream while the thread was still writing its terminal state into it. Every provider now publishes the result or error first and signals done last, and Azure does both from one `defer` so no early return can skip it.

- Every HTTP response the runtime parses as JSON could arrive gzip-compressed and fail to parse. `compat.http.openRequest` left `accept_encoding` at the Zig client default, which advertises `gzip, deflate, zstd`, while `compat.http.responseReader` returns the raw transfer reader and never decompresses — so any server that chose to compress handed gzip bytes to `std.json.parseFromSlice`. The visible symptom was GitHub Copilot login: `/login`, pick Copilot, press Enter at the `GitHub domain (press Enter for github.com):` prompt, and the flow died with `login failed: SyntaxError`, because GitHub compresses the device-code response. Nine of the fifteen `openRequest` call sites had no encoding handling at all and were exposed, including the Copilot device-code and token-poll requests and the OpenAI, Azure, Ollama and Google provider requests. The remaining six touched `accept_encoding` by hand, but only the Anthropic provider actually sent `identity`: the two model-catalog requests and the Anthropic and Codex OAuth requests set `identity` in the options and then overrode it back to `.omit` on the opened request, while the Copilot token request never set the option at all and applied `.omit` on its own, omitting the header outright. An absent `Accept-Encoding` still lets a server or intermediary choose a coding (RFC 9110 §12.5.3). The wrapper now sends `Accept-Encoding: identity` explicitly unless a caller overrides it, and those five `.omit` overrides are gone, so bodies arrive identity-encoded, which is what every reader in the tree already assumed.

- Stopped the `makai --stdio` protocol host from aborting (SIGABRT) on a malformed inbound envelope. All four envelope deserializers (`protocol/{provider,agent,tool,auth}/envelope.zig`) and the shared `transport.zig` frame parser read required JSON fields with an unchecked `.?` plus an unchecked union field access, so any peer could kill the process with one line: a missing `timestamp`, `sequence`, `message_id`, `payload` or `type`; a `sequence`/`timestamp` sent as a string; a `message_id` sent as a number; a `payload` sent as an array; a `complete_request` with no `model`, or `model` sent as a string or null; a negative `sequence` or an oversized `version` (both unchecked `@intCast`); a non-array `context.messages`; or a message missing `role` or `content`. `protocol/provider/runtime.zig` wrapped the call in `catch |err|`, but a Zig panic is not a catchable error, so that handler never ran. Because the stdio host multiplexes auth, provider and agent for every session in one process, one bad line from any peer took down all concurrent sessions and every in-flight SDK call saw `MakaiStreamError: stdio process exited`. Field reads now go through a shared `protocol/envelope_fields.zig` that returns typed `MissingField` / `InvalidFieldType` / `FieldOutOfRange` errors (numeric narrowing via `std.math.cast`), and each runtime answers a rejected line with a correlated rejection frame instead of dropping it silently — a `nack` with `invalid_request` on the provider and auth protocols, an `agent_error` with `sequence: 0` per spec §13 on the agent protocol, and a `tool_error` on the tool protocol — so the host stays up and serves the next envelope. Envelopes with a syntactically invalid ULID now nack immediately instead of being dropped. A rejection frame is sent only for an envelope that could not be *decoded*; a well-formed envelope carrying an unrecognized payload `type` keeps its existing silence, since that path never panicked and both the Python and TypeScript SDKs treat it as the forward-compatibility contract. `typescript/test/makai_binary_smoke.test.ts` had pinned the old silence as the contract — it sent a deliberately malformed `stream_request` and asserted `nextFrame` *timed out* — so that case is now split in two: one test still covers the SDK's frame-timeout path by reading with nothing queued, and a new one asserts the malformed envelope comes back as an `invalid_request` nack and that a second malformed envelope on the same connection is answered too. The new boundary tests also uncovered and fixed eleven pre-existing leaks on deserialization error paths (partially built message, content-part, tool, artifact and provider-descriptor arrays, and multi-`dupe` payload structs in all four families), plus a double free in the tool `tool_result` cleanup. In `transport.zig`, `parseAssistantMessage` had its decoded `content` array covered by an errdefer scoped to the `if` block that built it, so a peer-controlled `"usage":{"input":-1}` leaked the whole array on every frame; `parseAssistantContent` and the `done`/`error` events leaked earlier allocations when a later required field was missing. The same block-scoped-errdefer shape leaked the decoded `metadata` map in both copies of `deserializeModelDescriptor` (provider and agent) whenever a trailing `auth_status`, `lifecycle`, `source`, `context_window` or `reasoning_default` read failed.

- Stopped the model-descriptor deserializers leaking a metadata key when the matching value is rejected. `deserializeModelDescriptor` in both `protocol/provider/envelope.zig` and `protocol/agent/envelope.zig` built each entry as one struct literal, so the key `dupe` was evaluated and succeeded before `asString` failed on a non-string value; the literal never completed, the `initialized` counter was never incremented, and the aggregate errdefer over `[0..count]` excluded the entry. A `models_response` descriptor carrying `metadata` like `{"tier": 5}` leaked the key bytes once per frame, reachable from stdin. The fallible check now runs before either allocation, so the mistyped case allocates nothing, and the key carries an errdefer covering the window until the entry is counted — the shape `deserializeHeaderPairs` already uses. `cloneModelDescriptor` in `protocol/model_catalog_types.zig` had the same literal, where both slices come from memory that already exists, so only an allocation failure on the value reaches it; fixed the same way and covered by allocation-failure injection.

- Made the stdio host report a failed stdin stream instead of exiting 0 in silence. The reader thread ends the stream permanently when its bounded queue fills (`Stream queue full`) or a line exceeds the 1 MiB limit (`stdio line too large`), but `runStdioMode` never read `getError()`, so the host dropped every remaining frame, abandoned any in-flight run, and exited 0 with nothing on stdout or stderr — indistinguishable from a normal shutdown. Both are reachable from a conforming client: a single `agent_message` carrying ~1.2 MiB of conversation history is deterministically fatal, and a burst of roughly 1.4k (Debug) or 3.5k (ReleaseSafe) frames trips the queue. The host now emits a runtime `error` frame with the new `input_stream_error` code before it drains and exits.

- Made `makai --stdio` exit 0 when the client closes its read side, instead of propagating `error.BrokenPipe` out of `main` and exiting 1 with a Zig stack trace on stderr.
- Stopped the TypeScript SDK from orphaning a runtime process when the stdio handshake fails. `MakaiStdioClient.connect()` rejected on a handshake timeout, a protocol `version_mismatch`, or an `error` handshake frame while leaving the spawned `makai --stdio` child running and `this.child` set; `createMakaiClient()` and `createMakaiAuthClient()` call `connect()` internally and reject before returning a handle, so the caller had no way to reach `close()` and every failed attempt leaked one process (three failed `createMakaiClient()` calls left three live children, and the leftover pipes kept the parent's event loop alive). `connect()` now terminates the child, closes the line reader, and clears the handle on any handshake failure.

- Stopped the TypeScript SDK from leaving a provider stream running when the consumer stops reading it. `for await (… of client.provider.stream(…))` followed by a `break`/`return` disposed the async generator without telling the runtime, so the provider kept streaming (and kept burning provider tokens) until it finished on its own; the abort-signal path already sent `abort_request`, and `client.agent.stream(…)` already sent `agent_stop` from a `finally`, so only the provider path was missing it. `provider.stream` now best-effort-cancels and drains on early disposal, exactly once (a completed or aborted stream still sends no extra `abort_request`).

- Typed the TypeScript SDK's closed-transport failures on the `provider` and `agent` namespaces. Both namespaces sent their first envelope outside the surrounding `try`, so calling them after `close()` rejected with a bare `Error("client is not connected")` instead of the `MakaiStreamError` with `kind: "transport_error"` the README promises — a consumer following the documented `instanceof` chain fell through to the rethrow. `provider.complete`, `provider.stream`, `agent.run`, and `agent.stream` now wrap that send. `client.models` still rejects with a plain `Error` on a closed transport; that asymmetry is documented and pinned by a test.

- Made the queued-steer state visible at narrow terminal widths: while a turn is streaming, the composer placeholder now reads `1 queued · type to steer more…` instead of `type to steer the running turn…`, and while a tool-approval prompt is showing it reads `1 queued · y / a / n to decide` (the count leads in both variants so prefix-keeping truncation cannot cut it), because the placeholder sits inside the composer box and survives any width, whereas the only previous indicator (`queue:1`) is a status-bar segment that whole-segment truncation (#268) drops below ~100 columns — at 60 columns a queued steer echoed like a sent message with nothing showing it was queued, including for the whole duration of an approval prompt. The PTY harness's `steer-abort` scenario now waits for any width-robust streaming indicator (`streaming` status word, the `waiting for` row, or a tool row's `running` status) instead of the literal status-bar word, so the scenario suite passes at 60×20, 80×24 and 100×24 instead of timing out at the first non-default width.

- Rewrote the TUI's inline terminal renderer (vendored `zigzag` `Program`, `zig/src/tui/app.zig`): the live frame is now a cursor-relative region painted with print-above history, replacing the bottom-anchored frame plus scroll-region insertion that left stale copies of the composer and status bar in the terminal whenever the frame shrank (closing the model picker or finishing a turn) and overwrote visible history when it grew. Full-width panel borders no longer lose their last column, `/clear` clears the screen and scrollback, quitting erases the live widgets and leaves only the transcript, streamed reasoning renders before the answer instead of after it, and window resizes are debounced and answered with a relayout (stale screen scrolled into scrollback, visible history tail reprinted at the new width) instead of a cursor-relative repaint over reflowed rows. The inline frame is now a bottom-anchored window over the transcript row stream: opening the model, permissions, session or approval panel covers the history tail instead of scrolling it away, and closing it puts the composer back on the bottom row without leaving blank rows or duplicating scrollback. Paints rewrite only the rows that changed, so a spinner tick costs one or two rows instead of a screen. The PTY harness gained a VT screen model and judges tool rows, steer echoes and approval prompts by what is on screen rather than by repaint bytes. Long system rows such as the OAuth login prompt wrap instead of being cut with an ellipsis, and URLs in them are OSC 8 hyperlinks that stay one link across wrapped rows. While the TUI runs, stderr is redirected to `~/.oapx/tui-stderr.log` so stray library prints (for example the OAuth proxy warning during login) can no longer shift the cursor and leave stale rows on screen. the macOS keychain item is now labeled "makai credentials" and the `auth.json` item migrates to `auth.shared.json` on first read (unsigned dev builds still get one keychain prompt per new binary because access lists bind to the code hash; signed releases are keyed by team ID and keep "Always Allow"); `OAPX_KEYCHAIN_SERVICE` isolates keychain items for tests; `/login` shows which providers are logged in, hold an API key, or have expired; and the model catalog lists Anthropic models (live `/v1/models` fetch with cache and static fallback) whenever Anthropic credentials exist, with the status bar hiding the cost for models without a known price. Anthropic model definitions now carry the API origin as `base_url` (the provider appends `/v1/messages` itself; the TUI's default model previously doubled the path and 404'd), the provider no longer doubles a suffix already present, and non-2xx responses include the API's error type and message in the error card. The cached Anthropic catalog is reused at startup for 24 hours and re-fetched after that (stale copy kept only as a fallback), folding a catalog entry into the default model keeps that entry's output limit and context window instead of the fallback's conservative values, the catalog merge frees each model exactly once when an allocation fails midway, tool rows take their status from the linked tool entry rather than from status words that may appear in the tool's label, `/quit` inside the resize debounce erases the reflowed live region completely, `Cmd.println` still writes immediately for full-screen (non-inline) programs, `PgUp`/`PgDn` (and the mouse wheel) now scroll the inline window over the whole transcript with a `↑ SCROLL n%` row instead of doing nothing in a real terminal, and `/login` still reports `✓ env key` when the stored auth cannot be loaded. See [`docs/tui-rendering-model.md`](docs/tui-rendering-model.md).

- Fixed a cross-binary race in the tool artifact-store tests, and removed the `build.zig` run-step chain that had been containing it. `tools/common.zig`'s `storeArtifact`/`retrieveArtifact`/`cleanupArtifacts` resolved `.oapx/tool-artifacts` against the process cwd, so every tool test binary shared one directory; `zig build` runs those binaries in parallel, and a `cleanupArtifacts()` `deleteTree` in one deleted files another was mid-read, failing `artifact_retrieve supports range grep and full context modes` with `error.FileNotFound`. #293 contained this by chaining five run steps (`common -> artifact -> shell -> search -> file`) so they could never overlap, and named real isolation as the better fix; this is that follow-up, so the chain is gone and every tool test gets a plain `b.addRunArtifact` again. The artifact root is now injectable: production still resolves against cwd (the override is typed `void` outside test builds, so the branch is comptime-dead and the shipped binary is unchanged), while tests take a per-test `std.testing.tmpDir` root via `common.TestArtifactRoot`. Tests in `common.zig`, `artifact.zig`, `file.zig`, `shell.zig`, and `search.zig` are isolated — `search.zig` by #293's static-reachability rule, since it writes nothing today but holds a live `makeTextResultWithArtifact` call site. In test builds an unisolated access panics rather than silently sharing the real store, so a forgotten isolation fails deterministically on first run instead of flaking. Removing the chain is close to wall-time neutral (measured 132s chained vs 129s parallel for `test-unit-tools`): `tools_file_test` alone is ~114s and is the critical path either way.

- Made the Zig DeepSeek port refuse a `tool/call` whose `name` or `callId` is empty, missing or null, and an `assistant/message` whose `usage` carries a negative token count, with the Go oracle's `invalid tool/call` / `invalid assistant/message` text as a `deepseek_process_exit` failure. The port previously emitted `action.call.*` with `"name": ""` and `run.completed` with negative token counts, envelopes the shared validator refuses. The rest of `Event.Validate` remains unported under #143.

### Changed

- **The SDK packages, the environment variables and the state directory are renamed, leaving `oap` to the protocol.** Nothing has ever been published — the one tag cut so far shipped no assets — so every name was still free, and the cost of this goes from nothing to permanent the moment a tag succeeds.

  | | before | after |
  | --- | --- | --- |
  | npm | `makai` | `oap-sdk` |
  | PyPI / import | `makai` | `oap-sdk` / `oap_sdk` |
  | crate | `makai` | `oap-sdk` |
  | Go module | `github.com/lsm/makai/go` | `github.com/lsm/open-agent-protocol/sdk/go` |

  The Go module path named a repository that does not hold the code, which was wrong before this change and is now fixed alongside it.

  **The SDK package no longer carries a `bin`.** It shipped an executable and six platform binaries as optional dependencies, so importing a library pulled native artifacts into `node_modules`. An SDK is something you import; the binary is installed separately. The CLI needs its own package, which this change does not create.

  **Environment variables split by who reads them.** `OAPX_*` for the thirteen the Zig binary reads, `OAP_SDK_*` for the fifty-six the SDKs and their test fakes read. The prefix is not `OAP_*` because the Go tree already uses that for its real-process gates, and not `OAPX_*` throughout because `oapx` names the binary rather than the project. `~/.makai/` becomes `~/.oapx/`, with no migration: nothing in it is published state and the move is cheap now and expensive later.

  The Go CLI keeps the name `oap` for now. It is not a temporary artifact of the port — it gives Go users the adapters natively with no Zig in the way — so what to call it is a product question rather than a cleanup, and it is being decided separately.


- **The shipping binary is now `oapx`, not `makai`.** [Decision 0019](decisions/0019-one-binary.md) names one binary for the product and makes it this one renamed; the Go `oap` keeps its name until the Go tree retires, because differential execution needs both on one `PATH`. `zig build` installs `zig-out/bin/oapx`, the release workflow archives `oapx-<version>-<os>-<arch>`, and the `@oap-sdk/cli-*` packages ship `bin/oapx`. The npm and PyPI **package** names are unchanged, since a registry rename is a publishing decision rather than a build one. All four SDK binary resolvers — TypeScript, Go, Python and Rust — try `oapx` everywhere before `makai` anywhere, including the PATH lookup, and require an executable regular file rather than merely a name that exists — TypeScript, Python and Rust gained that check, Go already had it in `checkExecutable` — so an existing install keeps resolving while the rename propagates. The root npm launcher does the same, since the platform packages it dispatches to no longer ship a `makai`.

- **`serve` takes a role as an argument, because a role is a noun.** `oapx serve agent` and `oapx serve provider` replace `--oap` and `--oap-provider`, `oapx run` replaces `-p`, and a bare `oapx` starts the TUI, which is what makes the name work without explanation — the usage a bare invocation used to print now lives behind `--help`. The superseded flags all still work. `--stdio` keeps its spelling: it names the SDK transport rather than an OAP role, and renaming it would break every SDK for no gain.

- The `sdk/rust/target/` build cache is ignored and untracked. `.gitignore` carried `rust/target/` twice, written for a layout that has not existed since the SDKs moved under `sdk/`, so neither pattern matched anything and 717 files of cargo output — fingerprints, lock files, and a `.rustc_info.json` naming local paths — were tracked. Any `cargo` run in `sdk/rust` rewrote them, leaving the tree permanently dirty.

- **A prerelease tag withholds macOS rather than failing the build or shipping it unsigned.** A stable tag still refuses outright when `APPLE_CERTIFICATE` is unset, because a release must ship signed macOS binaries. A prerelease tag — any version carrying a `-`, such as `v0.1.0-alpha.2` — now warns, skips the macOS archive and its npm binary, and lets Linux and Windows publish. Both macOS legs withhold — the missing certificate and the missing notary key — since a signed but unnotarized Developer ID binary is blocked by Gatekeeper and is no more shippable than an unsigned one. The npm side derives what to skip from which binaries the build actually uploaded rather than from the tag shape, so a prerelease built *with* the secrets ships the darwin packages alongside the darwin archives. The macOS rule is unchanged in substance: signed or absent, never unsigned. This exists because the first tag cut on this repo failed both macOS jobs and took the whole release with it, so nothing shipped at all.

- **`oapx validate <trace.json>...` runs the semantic validator in process.** It reports each diagnostic as `code at index` and exits nonzero if any trace draws one, so an endpoint can check its own emitted trace without a second install — the capability [decision 0019](decisions/0019-one-binary.md) asks the validator to carry. It routes by the profile the trace declares: a trace naming `open-agent-protocol.model-provider-core` is judged by the provider state machine, everything else by the agent-control one, which is what "either profile" in the record asks for. It still runs the **core** of whichever profile it picked; a trace exercising a graduated unit needs the packs the fixture gate loads, which this command does not take yet. Still unbuilt from that record's command list: `serve agent provider` in one process, `--backend <name>`, `check`, `conformance`, and `specimens` as a top-level command rather than `serve provider --specimens`.

- Declared the agent-protocol tool catalogue session-scoped, as a `[planned]` rule (`docs/v1-sdk-agent-provider-spec.md` §13.2 rule 8) with the current behaviour unchanged until the SDKs follow. The host resolves tools message-first today — `parseAgentTools` reads `tools` from the `agent_message` payload and falls back to the `agent_start` `config_json` only when the message omits the key — but no consumer exercises that override: every makai client writes the same list into both payloads from one request object and always emits the key (an empty array when there are none), so the message value unconditionally shadows the config and the session-scoped field has never been read. All three SDKs in tree — TypeScript, Go and Python — are one-shot (`agent_start`, one `agent_message`, `agent_stop`) and expose no session handle, as does the unmerged Rust client (#309); the native OAP bridge is the only multi-message consumer and declares no tools at all. The override's only observable effect is a silent failure: a caller that declares tools on `agent_start` and sends `"tools": []` on `agent_message` receives no tools and no error. Under the planned rule clients declare the catalogue on `agent_start` only, and a host receiving `tools` on `agent_message` rejects the message at admission when the value differs from the session's catalogue while accepting an identical restatement — which turns the silent failure into a correlated validation `agent_error` without breaking any client that exists today. `docs/oap-alignment.md` gains the two tool rows the ledger never had: the provisioning narrowing, and the fact that makai's `tool_execute`/`tool_result` round trip has no counterpart in the shipped OAP core (OAP Decision 0011 proposes the missing resolve pair but is not accepted, so client-hosted tools stay unavailable over OAP). Both records carry the reversal condition: session-scoped tools are sufficient only while makai owns every consumer, since the host already accepts repeated `agent_message` and a persistent-session API with steering messages would reopen it.

- Collapsed the four hand-inlined copies of `ToolMetadata`'s free sequence into a single `ToolMetadata.deinit` on the type. `Payload.deinit` in `protocol/tool/types.zig` spelled out the same six frees twice (`.tool_register` and `.tool_list_response`), `protocol/tool/local_runtime.zig` carried a third as `deinitToolMetadata` behind its `tool_list` errdefer, and #319 added a fourth as `freeToolMetadata` in `protocol/tool/envelope.zig` for its new errdefers. A field added to `ToolMetadata` had to be freed in four places across three files or it leaked on some paths and not others. No behavior change: the frees are identical and in the same order, #331's new allocation-failure probes for `toolMetadataFromAgentTool` and the `tool_list` handoff exercise the consolidated path, and the method takes `*const ToolMetadata` because `ToolListResponse.tools` is `[]const ToolMetadata`.

- Corrected SDK README drift (both `README.md` and `typescript/README.md`): the binary resolution order now lists the `@oap-sdk/cli-<platform>-<arch>` optional dependency, which outranks both local `zig-out` build paths; a new Cancellation section documents `options.signal`, `isAbortError`, and early-`break` cancellation (`RunOptions.signal` was missing from the documented type, and aborts reject with a plain `AbortError`, not the `MakaiStreamError` the error table claimed); the error table gains `StdioProtocolError` and the `client.models` closed-transport exception; and the timeout paragraph records that `responseTimeoutMs` does not reach `client.auth`, which is governed by `frameTimeoutMs` and otherwise stays on its 30s default. `typescript/README.md` also picks up the client-side tool-execution story (`ToolDefinition.execute`) that `README.md` already documented.

- Redesigned the TUI surface: welcome banner, role-glyph transcript entries with soft user blocks, styled assistant prose (bold/italic/code spans, bullets, numbered lists, headings, quotes, fenced code blocks with language tags) applied on top of the existing sanitizer and wrapper, one-line tool rows with right-aligned live status (`running`, `awaiting approval`, `✓ bytes · tokens`, `✗ failed`, `■ interrupted`) and capped `⎿` result previews, streaming spinner headers with a caret, titled pickers with a highlighted selection row, a key/value approval panel, a state-coloured composer border, a `/` command palette with `Tab` completion, and a compact status line with a context gauge, elapsed streaming time, model-priced context cost, and a right-aligned key hint.
- TUI keys: `Esc` clears the draft, then aborts a running turn; `Ctrl+C` aborts or clears first and quits on a second press (immediately when idle with an empty composer); `Ctrl+D` quits on an empty idle composer; `Ctrl+A/E/U/K/W`, `Alt+Backspace`, `Ctrl`/`Alt`+arrows and `Alt+B/F` edit and move by word; `Delete` removes the character under the caret.
- `scripts/tui-pty-driver.py` assertions follow the new rendering (raw-stream row breaks for the Shift+Enter draft, `✓`/`✗` tool glyphs, optional status-bar cut marker, double `Ctrl+C` semantics); the fixture provider accepts `<think>…</think>` in `text:` steps.

### Added

- **An open that subscribes or attaches is gated on the revision the host asked for.**
  `hub.OpenRequest` carries a `capability_revision`, and an open that set `subscribe` or
  carried tool sources compares it against the registered adapter's revision, answering
  `error.StaleCapabilities` on a disagreement — the refusal the draft names, with
  `expected_revision` and `current_revision` for the frontend to report. Go splits this
  across two gates, `AttachmentGate` and `SubscribeGate`, and the draft's own line calls
  the second "the same comparison", so one check covers both; what differs between them
  is the support feature each then checks, and that half already exists as the open's
  election check. `Failure.StaleCapabilities` had been declared since the hub was
  written and **never returned by anything**: no code compared a revision, and the
  request had no member to carry one. So a host that gated
  its open on a revision got a session opened against whatever the adapter happened to
  be serving — a silent disagreement where the draft specifies a 409 — and the answer's
  `capability_revision` had no checked value to report, only the request's own, which is
  the number the gate exists to verify. The comparison runs before the `session_exists`
  lookup, so the gate wins over a name collision in the order Go's wire has it, and a
  request that states no revision is not gated, which is Go's own `revision != ""`
  guard rather than a hole. The gate runs before the election check as well, so an open
  that both cites a stale revision and asks for an unadvertised feature answers
  `stale_capabilities`, as Go's wire order has it. The gate and the open's two
  attach elections key on whether the request **carries** entries, not on whether the
  member is present: Go gates on `len(request.ToolSources) == 0`, so an open sending
  `"tool_sources": []` attaches nothing and is admitted, where keying on the member's
  mere presence called it an attachment and refused it as unadvertised.

### Fixed

- **An unrecognised argument to `oapx validate` is no longer read as a path.**
  `runValidate` appended any argument it did not recognise to the path list, so
  `oapx validate --mode json manifest.json` reported `--mode: unreadable` and then
  `json: unreadable` — two files that do not exist — and carried on. The verdict was
  false about what had happened, and the user's intent was dropped without a word.
  A flag oapx does not carry is now refused by name:
  `oapx validate: --mode: unavailable: tolerant mode lands with the validator's own
  mode, in #367`. The flag's value is never consumed, so one mistyped flag no longer
  costs two phantom files, and the three flags `goap validate` has but oapx does not —
  `--mode`, `--pack` and `--provider` — each name themselves rather than falling
  through to a generic refusal. `unavailable` grew a `surface` parameter, because it
  hardcoded `oapx hub:` into its message and would have answered
  `oapx hub: --mode: unavailable` for a `validate` refusal; its `arena` parameter went
  with that, having been discarded on entry (`_ = arena`) and the only reason `runHub`
  still allocated an arena at all.

- **An answered tool call no longer grows a second, synthetic `"No result
  provided"` result when its id is rewritten.** `pre_transform` keys the set of
  unanswered calls by the id the call is written out under and the set of answered
  calls by the id the result arrived with, so the two only lined up when
  normalization left the id alone. Every exchange that rewrites an id therefore put
  one result on the wire per call and a duplicate error result beside it: **every
  call against a Mistral endpoint**, whose ids are re-hashed to nine characters, and
  any id over 40 bytes, carrying a `|`, or holding a byte that is not
  alphanumeric, `_` or `-` on any other host. The answered set is now keyed the way
  the pending set is, by the rewritten id, so a call that was answered is answered
  once and an unanswered one still grows exactly one synthetic result — carrying the
  rewritten id, so it still names the call it stands in for. The wire is unchanged
  for every id normalization leaves alone, which is every id on a non-OpenAI,
  non-Mistral host and every clean short id elsewhere. The Go transcription in
  `go/internal/provider` keyed its answered set by the arrival id and now does
  the same as here. #514

### Breaking changes

- **go: a missing lifecycle reads as unknown instead of being fabricated** ([#710](https://github.com/lsm/open-agent-protocol/pull/710))

- `go/sdk`: `ModelDescriptor.Lifecycle` changes from `ModelLifecycle` to
  `*ModelLifecycle`. A listing that does not state a lifecycle now reads as
  `nil` (unknown) instead of being reported as an empty `ModelLifecycle`
  that no provider stated. Callers must handle the pointer and must no
  longer treat a non-nil `Lifecycle` as guaranteed. Stated values are
  unchanged.
- `go/sdk`: a `models.list` or `models.resolve` response carrying a
  `lifecycle` that is not one of `stable`, `preview` or `deprecated`, an
  empty string, a number, or an explicit `null` is now a
  `malformed_response` rather than decoding as unknown. Previously the OAP
  path read any such value as the empty string and the shared path rejected
  an absent member outright.

- **go: a missing source reads as unknown in the shared catalog result** ([#690](https://github.com/lsm/open-agent-protocol/pull/690))

- `go/sdk`: `ModelDescriptor.Source` changes from `ModelSource` to
  `*ModelSource`. A listing that does not state a source now reads as
  `nil` (unknown) instead of being reported as `dynamic`. Callers must
  handle the pointer and must no longer treat a non-nil `Source` as
  guaranteed. Stated values are unchanged.
- `go/sdk`: a `provider.models.list.response` carrying a `source` that is
  not one of `discovered` or `fallback`, an empty string, or a non-string,
  is now rejected as `malformed_response` rather than silently read as
  absent. This is stricter than before and is the point of the change.

### Merged pull requests

- **tui: restore a session's thinking level on /resume** ([#781](https://github.com/lsm/open-agent-protocol/pull/781)): `/resume` kept whatever thinking level the previous session left, because `<session>.meta.json` recorded the model but not the level.
- **provider: fail a completions stream that ends with no reply** ([#780](https://github.com/lsm/open-agent-protocol/pull/780)): On the OpenAI-compatible wire, a reply with no thinking, text or tool call was settled as a normal `stop`.
- **tui: skip the automatic continue after a 402 or a spent balance** ([#779](https://github.com/lsm/open-agent-protocol/pull/779)): A 402 (or a provider saying the balance or quota is spent) now skips the TUI's one automatic `continue`, like a 401/403 does: the account has to be funded, and a replay is just another refused request.
- **tui: refresh models on demand and log out of one provider** ([#778](https://github.com/lsm/open-agent-protocol/pull/778)): Adds `/model refresh` and `/logout <provider>`.
- **tui: rename a session with /rename** ([#777](https://github.com/lsm/open-agent-protocol/pull/777)): Adds `/rename <title>`.
- **zig: fill a discovered model's missing limits from models.dev** ([#776](https://github.com/lsm/open-agent-protocol/pull/776)): OpenCode's `/models` lists ids only, so every OpenCode Zen and Go model got the generic 128k window and 8k output (DeepSeek V4 Flash has 1M / 384k).
- **zig: time a reply's tokens per second from when its events were queued** ([#775](https://github.com/lsm/open-agent-protocol/pull/775)): The status bar showed `1199000 tok/s`.
- **zig: add OpenCode Go as a provider, identifying this client and the conversation** ([#774](https://github.com/lsm/open-agent-protocol/pull/774)): OpenCode **Go** (subscription) has its own endpoint, `https://opencode.ai/zen/go/v1`, billed apart from the **Zen** balance the `opencode` row uses.
- **zig: deliver buffered frames and explain a spent recovery budget** ([#773](https://github.com/lsm/open-agent-protocol/pull/773)): The conformance reader delivers what it has and explains a spent budget.
- **zig: re-vendor zigzag from the fork at upstream v0.1.6** ([#772](https://github.com/lsm/open-agent-protocol/pull/772)): Re-vendors zigzag from the fork, lsm/zigzag at `7950b51`, which now carries our local edits (lsm/zigzag#2), upstream v0.1.6 (lsm/zigzag#3), and fixes for this PR's review findings (lsm/zigzag#4–#8).
- **zig: keep literal private-use glyphs and hyperlinks through inline code** ([#771](https://github.com/lsm/open-agent-protocol/pull/771)): The transcript keeps literal private-use glyphs and every link target.
- **zig: run allocation-failure sweeps on a fast allocator, and give TUI tests their own HOME** ([#770](https://github.com/lsm/open-agent-protocol/pull/770)): `test-unit-tui` took 187s locally, most of it on one core.
- **zig: shed streaming chunks, not messages, when the TUI queue is full** ([#769](https://github.com/lsm/open-agent-protocol/pull/769)): A DeepSeek session logged `1197 events dropped due to backpressure`.
- **zig: compact between turns inside a run once the context passes the autocompact point** ([#768](https://github.com/lsm/open-agent-protocol/pull/768)): A run could only compact before the next user turn, so a long tool loop could fill the window by itself.
- **zig: ask for a bounded reply by default, and raise it only when a reply is cut off** ([#767](https://github.com/lsm/open-agent-protocol/pull/767)): Every turn asked for the model's full output maximum (393,216 on deepseek-flash).
- **zig: compact on its own by default, at a point set by the model's window** ([#766](https://github.com/lsm/open-agent-protocol/pull/766)): Auto-compaction was opt-in with a fixed share.
- **zig: keep headroom below the context window when capping output** ([#765](https://github.com/lsm/open-agent-protocol/pull/765)): #751 caps output at exactly the room the estimated prompt leaves, so any undercount is a 400.
- **provider: keep a catalogued base from reading as an operator override** ([#764](https://github.com/lsm/open-agent-protocol/pull/764)): DeepSeek on `anthropic-messages` had no catalogued fallback, so `defaultBaseUrlForRef` resolved no base at all and the request path had nothing to send to.
- **fix: send deepseek thinking effort where this vendor reads it** ([#763](https://github.com/lsm/open-agent-protocol/pull/763)): DeepSeek's Anthropic-compatible endpoint documents that `budget_tokens` is ignored and `output_config.effort` is supported.
- **fix: keep a reasoning-only reply's reasoning when the request carries tools** ([#762](https://github.com/lsm/open-agent-protocol/pull/762)): #752 made any reply holding only reasoning send that reasoning as content and drop `reasoning_content`, because DeepSeek refuses an assistant message with neither content nor tool calls.
- **zig: make the cleanup-gate handshake a real one** ([#761](https://github.com/lsm/open-agent-protocol/pull/761)): Replaces a bounded yield-spin with a real handshake in the test that pins the provider cleanup gate's wait order.
- **fix: give a configured deepseek identity its effort mapping, and map effort to the documented table** ([#760](https://github.com/lsm/open-agent-protocol/pull/760)): DeepSeek was recognised only by host, and that one `deepseek.com` subdomain test gated both the reasoning-effort capability and the effort mapping, so a model configured with the `deepseek` provider behind a non-vendor proxy lost both and…
- **go: the shutdown test claimed sessions close and never looked** ([#759](https://github.com/lsm/open-agent-protocol/pull/759)): Test-only plus one ledger record: **59 changed lines** (`go/cmd/goap/serve_test.go` +16/−6, `drafts/hub.md` +37).
- **zig: assert the payloads the supported operations actually carry** ([#756](https://github.com/lsm/open-agent-protocol/pull/756)): First strengthening step for #365 slice 7.
- **zig: an abort stops the run, not just its tools** ([#755](https://github.com/lsm/open-agent-protocol/pull/755)): Pressing Esc or Ctrl+C set the run's cancel flag, but only tools honoured it.
- **zig: render markdown tables, rules and links in the TUI transcript** ([#754](https://github.com/lsm/open-agent-protocol/pull/754)): Markdown tables, horizontal rules and links in assistant replies showed up in the TUI as raw text.
- **zig: serve DeepSeek on its Anthropic endpoint** ([#753](https://github.com/lsm/open-agent-protocol/pull/753)): DeepSeek now runs on `api.deepseek.com/anthropic`.
- **zig,go: send a reply that holds only reasoning back with its reasoning as content** ([#752](https://github.com/lsm/open-agent-protocol/pull/752)): Since #743, a past assistant reply with only reasoning (no text, no tool calls) was sent as `content: null` plus `reasoning_content`.
- **zig: ask for no more output than the context window leaves** ([#751](https://github.com/lsm/open-agent-protocol/pull/751)): Every request asked for the model's full output limit, however large the prompt was.
- **tools: give hashline a budget of its own instead of the file limit** ([#750](https://github.com/lsm/open-agent-protocol/pull/750)): hashline's read default and its edit preview both read the file tool's inline limit, so the 20 KiB hashline budget was the same number by accident rather than by decision.
- **ts: correct the auth_status coverage, the filter control, and the record** ([#749](https://github.com/lsm/open-agent-protocol/pull/749)): Post-merge corrections to #746, raised on its own review.
- **tools: store the whole result behind an artifact reference** ([#748](https://github.com/lsm/open-agent-protocol/pull/748)): An artifact-backed text result stored only stdout, so a result carrying stderr lost those bytes on disk while `byte_size`, `raw_bytes` and the summary's `bytes:` line counted them, and the summary inlined the entire stderr instead of a…
- **ts: judge a present OAP auth_status, and read an absent one as unknown** ([#746](https://github.com/lsm/open-agent-protocol/pull/746)): `sdk/typescript/src/oap_client.ts:445` read the member as:
- **test: keep the acp stay-alive helper past stdin EOF, and prove it** ([#745](https://github.com/lsm/open-agent-protocol/pull/745)): `stay-alive` reached `select {}` **only after `reader.Decode()` had already returned an error**, which for a stdin reader is the parent's own close.
- **hub: pin the drain's round cap and poll cycle, the two bounds nothing held** ([#744](https://github.com/lsm/open-agent-protocol/pull/744)): A read-only audit of the hub HTTP path against fresh `main` (`bbb1d37244d7`) found two already-implied bounds the ledger never names and no test asserted.
- **zig: send DeepSeek its reasoning back as reasoning, and ask once for a lost answer** ([#743](https://github.com/lsm/open-agent-protocol/pull/743)): DeepSeek runs in the TUI were ending with only a reasoning block and no answer.
- **agent: release a failed turn's message on every way out** ([#742](https://github.com/lsm/open-agent-protocol/pull/742)): The #720 repair at a size inside the cap, on a current `main` that includes #723. **358 lines**, one file changed plus the two small fixtures from the earlier branch, **4 deletions**.
- **test: keep the codex stay-alive helper past stdin EOF, and prove it** ([#741](https://github.com/lsm/open-agent-protocol/pull/741)): `stay-alive` reached `select {}` **only after `reader.Decode()` had already returned an error**, which for a stdin reader is the parent's own close.
- **rust: an omitted OAP auth_status reads as the existing unknown** ([#740](https://github.com/lsm/open-agent-protocol/pull/740)): The OAP entry path deserialised the whole `ModelDescriptor`, and `auth_status` is a required `AuthStatus`.
- **ts: refuse a non-object catalog entry instead of dropping it** ([#738](https://github.com/lsm/open-agent-protocol/pull/738)): `models.filter(isRecord)` in the OAP catalog reader ran **before** any validation, so a published entry that was not an object was silently discarded.
- **test: keep the pi process helpers alive on real blocking IO** ([#736](https://github.com/lsm/open-agent-protocol/pull/736)): Both fixtures meant to stay running until the parent kills them ended in `select {}`.
- **zig: responses and generative publish done after their own cleanup** ([#735](https://github.com/lsm/open-agent-protocol/pull/735)): Applies the same teardown-ordering repair to the OpenAIResponses and GoogleGenerative provider threads, found by auditing every provider rather than assuming the house shape.
- **hub: the D23 and D26 rows cited a function that had moved under them** ([#734](https://github.com/lsm/open-agent-protocol/pull/734)): A docs-only anchor repair, read from current `main`.
- **zig: openai completions publishes done after its own cleanup** ([#732](https://github.com/lsm/open-agent-protocol/pull/732)): Publishes the OpenAICompletions producer's completion flag only after its own cleanup has finished, so a caller that observes the flag can safely destroy the stream.
- **docs: make every reader reference resolvable after merge** ([#731](https://github.com/lsm/open-agent-protocol/pull/731)): Every reader reference in the two absence documents is now resolvable by a later reader.
- **hub: the total-time guard's presence, through the callback that already exists** ([#730](https://github.com/lsm/open-agent-protocol/pull/730)): Pins the **presence** of `drain`'s total-time guard at `http.zig:218`, which the elapsed-vs-uptime test at `:1412` deliberately does not: that test's bytes are already buffered when `drain` starts, so deleting the guard leaves it green.
- **hub: the media gate, answered by the daemon with nothing else wrong** ([#729](https://github.com/lsm/open-agent-protocol/pull/729)): The third gate in `answer()`, and the only refusal no `go/cmd/goap` test named — so the media parity D26 records was pinned **in-process only**. **Two files, +121/-0, one production change: none.** Independent of #728 and relying on none…
- **hub: the refusal order is decided outside the process, not only in a Pipe** ([#728](https://github.com/lsm/open-agent-protocol/pull/728)): `answer()` at `http.zig:378-383` checks **Origin, then Host, then the media type**, and the **first** of those three had no real-process proof. **Two files, +195/-0, one production change: none.**
- **fix: one wait gets one budget, not one per line** ([#726](https://github.com/lsm/open-agent-protocol/pull/726)): `pull` handed `c.deadline` to **every line read**, and `Response`, `Event` and `Control` each looped until their target arrived, so an endpoint that kept speaking but never answered renewed the deadline on every frame it sent.
- **hub: the readBody failure path answers completely, and the drain there is not pinned** ([#725](https://github.com/lsm/open-agent-protocol/pull/725)): The third and last `drain` call site, and the only one no existing proof reaches. **Two files, +119/-0, one production change: none.** `zig/src` is untouched.
- **python: an unstated lifecycle or source reads as unknown on both readers** ([#724](https://github.com/lsm/open-agent-protocol/pull/724)): Python was the last reader with this defect, in **both** of its readers and neither fixed.
- **zig: keep the process group's identity after the child is reaped** ([#723](https://github.com/lsm/open-agent-protocol/pull/723)): Follow-up to #699: the process group's identity has to survive the child being reaped.
- **tui: restore model context window after switching back** ([#722](https://github.com/lsm/open-agent-protocol/pull/722)): Related: #559
- **hub: the 413 path is refused whole, and the mutation says its drain is not pinned** ([#721](https://github.com/lsm/open-agent-protocol/pull/721)): Two corrections, both about claiming more than the evidence carries. **Neither changes an assertion.**
- **tui: remove redundant /provider command** ([#719](https://github.com/lsm/open-agent-protocol/pull/719)): Part of #349
- **go: an omitted lifecycle or source is omitted, not published as null** ([#718](https://github.com/lsm/open-agent-protocol/pull/718)): The Go reader now reads an omitted lifecycle as unknown and refuses a present null — and the writer was still emitting exactly that null.
- **hub: the cancellation proof runs its exit proof before any kill** ([#717](https://github.com/lsm/open-agent-protocol/pull/717)): The last of #656's replacement cuts, from fresh `main` now that the drain's helper has landed as #687. **Three files, +400/-0, one production change: none** — the drain's cap, budget, clock guard and large-body proof are all on `main`…
- **zig: give the endpoint reader one absolute budget per read** ([#713](https://github.com/lsm/open-agent-protocol/pull/713)): Reader half of a split, from fresh main, no consumer and no stack.
- **ts: a missing lifecycle reads as unknown instead of becoming stable** ([#712](https://github.com/lsm/open-agent-protocol/pull/712)): `ModelDescriptor.lifecycle` becomes optional in the TypeScript SDK, matching the `source` member #705 already made optional.
- **tui: build provider login picker from catalog (#353)** ([#711](https://github.com/lsm/open-agent-protocol/pull/711)): Closes #353
- **go: a missing lifecycle reads as unknown instead of being fabricated** ([#710](https://github.com/lsm/open-agent-protocol/pull/710)): `lifecycle` is the same shape as `source` and now follows it in Go.
- **rust: a missing lifecycle reads as unknown instead of being invented** ([#709](https://github.com/lsm/open-agent-protocol/pull/709)): `ModelDescriptor.lifecycle` becomes `Option<ModelLifecycle>` alongside the `source` member #688 already made optional.
- **test: assert one settlement per asked call, not one per run** ([#707](https://github.com/lsm/open-agent-protocol/pull/707)): The test `TestACallWhoseAnswerRacesACancelIsAnnouncedEitherWay` scripted **two** tool calls, then counted `resolved` and `cancelled` across both ids and asserted the total was one.
- **ts: record an unstated model source as unknown on both readers** ([#705](https://github.com/lsm/open-agent-protocol/pull/705)): TypeScript had two model readers and the null-versus-absence problem was in both, which is the same shape as the Go and Rust findings.
- **validation: make the literal-backslash case POSIX-only and pin the URI form** ([#703](https://github.com/lsm/open-agent-protocol/pull/703)): Follow-up to #663.
- **zig: give every streamed conformance fixture envelope an id of its own** ([#702](https://github.com/lsm/open-agent-protocol/pull/702)): Independent fixture cleanup from fresh main, not stacked on #693. **FINAL PASS.**
- **agent: carry a tool's reported directory onto the message** ([#701](https://github.com/lsm/open-agent-protocol/pull/701)): Slice 2 of the #586 working-directory series: the channel a tool reports a directory on, with its ownership rules.
- **ci: refuse an explicit --files that selects nothing** ([#698](https://github.com/lsm/open-agent-protocol/pull/698)): An explicit `--files` that selects no path still exited 0 with `files with comments: 0`, so a typo, a lost shell argument or a glob matching nothing tracked read as a clean pass having judged nothing — the same defect class as the…
- **ci: refuse a missing path in --stats and write mode too** ([#697](https://github.com/lsm/open-agent-protocol/pull/697)): Makes `--stats` and write mode exit non-zero on a selected path they could not read, so a typo or a mid-deletion file can no longer pass as coverage in the two modes an author runs before pushing.
- **zig: a catalog snapshot says which branch produced each entry** ([#696](https://github.com/lsm/open-agent-protocol/pull/696)): A catalog snapshot now says which branch produced each entry.
- **validation: refuse a contributed branch that does not pin type to a const** ([#695](https://github.com/lsm/open-agent-protocol/pull/695)): A pack's branch is added to the core envelope's `oneOf`, and `oneOf` requires exactly one match, so a branch broad enough to also match a core type would make every envelope of that type match twice and turn a core envelope invalid because…
- **ci: fail the comment check on a selected path it could not read** ([#694](https://github.com/lsm/open-agent-protocol/pull/694)): The comment checker skipped any selected path it could not read, counted it as clean, and exited 0 having judged nothing.
- **zig: oapx conformance bounds each probe stage by one absolute deadline** ([#693](https://github.com/lsm/open-agent-protocol/pull/693)): Independent cut after #683, not stacked on it. **FINAL PASS.**
- **zig: pin the session op matrix to what the endpoint advertises** ([#692](https://github.com/lsm/open-agent-protocol/pull/692)): #365 slice 7, the test-only half.
- **ci: drop the clients/ts exclusion from the zero-comments checker** ([#691](https://github.com/lsm/open-agent-protocol/pull/691)): All thirteen tracked `.ts` files under `clients/ts` are clean, so the `:!:clients/ts/**` pathspec in the checker's default enumeration has nothing left to protect; removing it selects 45 `.ts` files where it selected 32.
- **go: a missing source reads as unknown in the shared catalog result** ([#690](https://github.com/lsm/open-agent-protocol/pull/690)): The owner accepted optional `lifecycle` and `source` on the shared catalog result, so `ModelDescriptor.Source` becomes `*ModelSource` and the reader stops inventing.
- **fix: a replay is judged by the envelope ids the run already has** ([#689](https://github.com/lsm/open-agent-protocol/pull/689)): #684 compared a replay's types and sequences, which still let an endpoint re-issue every envelope under a fresh id and pass.
- **rust: a missing source reads as unknown in the shared catalog result** ([#688](https://github.com/lsm/open-agent-protocol/pull/688)): The owner accepted optional `lifecycle` and `source` on the shared catalog result, so `ModelDescriptor.source` becomes `Option<ModelSource>` and an omitted source reads as unknown rather than failing deserialisation.
- **drain: a byte cap, an elapsed-time budget, and a real-process proof** ([#687](https://github.com/lsm/open-agent-protocol/pull/687)): Second replacement cut for #656, from fresh `origin/main`. #656 is held at `e59f6ac872` and is not growing. **318 changed lines.** Cut 1 is #685 (the media gate), which is independent; this one does not depend on it.
- **zig: finish the driver's session lifecycle** ([#686](https://github.com/lsm/open-agent-protocol/pull/686)): Slice 9 of #365's loop-driver extraction: the two operations that end a run's life join the driver, so it now owns the whole lifecycle — admit, pump, publish, cancel, plus sweeping idle sessions out of the server and dropping the queued…
- **zig hub: a body must declare application/json, and only that** ([#685](https://github.com/lsm/open-agent-protocol/pull/685)): Replacement cut for part of #656, from fresh `origin/main`. #656 is held and is not growing; this is the first slice of its scope, split to the size limit. **101 changed lines.**
- **fix: a replay is judged by the envelope ids the run already has** ([#684](https://github.com/lsm/open-agent-protocol/pull/684)): The runner judged a replay by the first event's sequence and the arrival of any terminal, so an endpoint that dropped the run's middle events, reordered them, retyped one, or re-issued every envelope under a fresh id all passed while…
- **zig: oapx conformance judges a replay against the run it re-delivers** ([#683](https://github.com/lsm/open-agent-protocol/pull/683)): Post-merge correction for #667. **FINAL PASS.**
- **zig: pump the run table from a driver that owns it** ([#682](https://github.com/lsm/open-agent-protocol/pull/682)): Slice 8 of #365's loop-driver extraction: the run table gets an owner and the agent loop pump moves to it, so the host holds a run driver instead of a bare array it reaches into from four places.
- **validation: refuse a pack that declares outside its own namespace, or an overlapping id** ([#681](https://github.com/lsm/open-agent-protocol/pull/681)): A pack could mint into the spec's namespace and nothing noticed: `bad-unprefixed-name` declares the capability key `storage.objects`, `bad-foreign-prefix` declares `com.other.billing.charge`, and neither was refused.
- **test: an openai completions trace pins the part index it reports** ([#680](https://github.com/lsm/open-agent-protocol/pull/680)): `openai_completions` had no stream harness, so its index mapping had only ever been argued from the source.
- **ts: strip comments from the remaining oap-client sources** ([#679](https://github.com/lsm/open-agent-protocol/pull/679)): Strips the comments from the last five `clients/ts/src` files — `session`, `client`, `errors`, `sse`, `index` — 138 ranges and +1/−320 lines, and gates those eleven now-clean paths in CI, 364 add+del including the workflow step.
- **zig: publish run results and tool requests from the run module** ([#678](https://github.com/lsm/open-agent-protocol/pull/678)): Slice 7 of #365's loop-driver extraction: the four functions through which a run publishes — the settlement pair, the loop error, the terminal projection and the queued tool requests — move into `zig/src/tools/agent_run.zig` beside the run…
- **fix: a result message that cannot be built releases what it allocated** ([#677](https://github.com/lsm/open-agent-protocol/pull/677)): A provider allocated its result's content and then duped the `api`, `provider` and `model` strings inline with `catch { return ... }`, so any of those three failing returned without releasing what it had already taken —…
- **ts: strip comments from the oap-client protocol and events sources** ([#676](https://github.com/lsm/open-agent-protocol/pull/676)): Strips the comments from `clients/ts/src/protocol.ts` and `events.ts` — 186 ranges, −351 lines, no code line touched — and gates those eight now-clean `clients/ts` paths in CI, 391 add+del in total including the workflow step.
- **zig: move the loop event serializer into the run module** ([#675](https://github.com/lsm/open-agent-protocol/pull/675)): Slice 6 of #365's loop-driver extraction: the agent loop event serializer moves into `zig/src/tools/agent_run.zig` with the three writers only it calls and the event deinit helper, because the pump cannot move without them and this is what…
- **zig: admit a run through the run module** ([#674](https://github.com/lsm/open-agent-protocol/pull/674)): Slice 5 of #365's loop-driver extraction: everything from creating the run's context through appending it becomes `AgentRun.admit`, and the host keeps only the session generation lookup, the busy guard, `prepareAgentRun`, the model update…
- **zig: an OAP model entry that states no source omits the member** ([#673](https://github.com/lsm/open-agent-protocol/pull/673)): An OAP model entry that states no `source` omits the member, and a decoded omission stays omitted rather than becoming `discovered` — a peer is no longer handed a provenance conclusion it never made.
- **ts: document the oap-client API surface in its shipped README** ([#672](https://github.com/lsm/open-agent-protocol/pull/672)): Relocates the documentation of **67 of the package's 76 documented exports** into its shipped `README.md`, verbatim, under a new `## API reference` section; the other nine are the `errors` exports this README already covers under [the…
- **ts: strip comments from the oap-client test files** ([#671](https://github.com/lsm/open-agent-protocol/pull/671)): Strips the comments from the six `clients/ts/test/*.ts` files with the repository's own checker: 115 comment ranges, 162 changed lines, comments and the four trailing `// …` only.
- **compat: a direction-aware shutdown beside the one that always closed both** ([#670](https://github.com/lsm/open-agent-protocol/pull/670)): The narrow wrapper: `ShutdownHow` re-exported from `std.Io.net`, and `Stream.shutdownHow(how)` added beside the existing `shutdown()`, which is unchanged and still closes both.
- **zig: give the agent run its own module** ([#669](https://github.com/lsm/open-agent-protocol/pull/669)): Slice 4 of #365's loop-driver extraction, the slices recorded on #375.
- **validation: scope the fixture gate's registry to the fixture that declares packs** ([#668](https://github.com/lsm/open-agent-protocol/pull/668)): The gate built one registry before its loop over the manifest and loaded each fixture's packs into it, so a pack's documents outlived the fixture that declared them and every later fixture was compiled against a bundle containing packs it…
- **zig: oapx conformance judges a cursor replay and the run it re-delivers** ([#667](https://github.com/lsm/open-agent-protocol/pull/667)): Fourth slice of #368: ports goap's `replayRun`.
- **fix: a lost clone fails the turn, and a settled stream keeps its own terminal** ([#666](https://github.com/lsm/open-agent-protocol/pull/666)): A provider that deep-copies each queued event must not publish a terminal that silently omits one, so a clone that cannot be allocated now fails the turn instead of settling a clean result, and settlement is first-writer-wins: a caller…
- **zig: an OAP model entry that states no lifecycle omits the member** ([#665](https://github.com/lsm/open-agent-protocol/pull/665)): An OAP model entry that states no `lifecycle` omits the member, and a decoded omission stays omitted instead of becoming `stable`.
- **zig: move the tool executor and its result parsing into the bridge module** ([#664](https://github.com/lsm/open-agent-protocol/pull/664)): Slice 3 of #365's loop-driver extraction, the eight slices recorded on #375.
- **validation: a pack's schema paths may not leave the pack directory** ([#663](https://github.com/lsm/open-agent-protocol/pull/663)): Every `schemas` entry is checked before any of them is read: it must be non-empty and relative, is cleaned and refused if it climbs out lexically, is joined onto the pack's canonical root, has its symlinks resolved, and must land beneath…
- **zig hub: the refusal code to status mapping, read off the draft** ([#662](https://github.com/lsm/open-agent-protocol/pull/662)): The refusal-semantics helpers #388's dispatch needs, from fresh `origin/main`.
- **zig: map a catalog snapshot to owned serving entries, per provider row** ([#661](https://github.com/lsm/open-agent-protocol/pull/661)): Maps a catalog snapshot to owned serving entries, one provider row at a time, keeping only the models whose api maps to that row's wire.
- **zig: move the stdio tool bridge into its own module** ([#660](https://github.com/lsm/open-agent-protocol/pull/660)): Slice 2 of #365's loop-driver extraction, the eight slices recorded on #375.
- **refactor: a provider stream clones every event, so none outlives its thread** ([#659](https://github.com/lsm/open-agent-protocol/pull/659)): Removes `StreamOptions.requires_owned_stream_events` rather than leaving it optional, because a caller-chosen ownership flag is what let two lifetime models coexist, and makes every provider, tui mock and provider-protocol mock clone on…
- **zig: move the tool bridge's data types into their own module** ([#658](https://github.com/lsm/open-agent-protocol/pull/658)): Part of #365's loop-driver extraction, slice 1 of the eight recorded on #375.
- **ci: the defer-scope check sees else, capture and nested blocks** ([#657](https://github.com/lsm/open-agent-protocol/pull/657)): Closes #603.
- **zig hub: a body must declare application/json, and only that** ([#656](https://github.com/lsm/open-agent-protocol/pull/656)): Step 3 of #388's handoff: the `unsupported_media_type` 415 and the charset rule, from fresh `origin/main`.
- **docs: name the daemon, name the library, and both hubs for the TS client** ([#655](https://github.com/lsm/open-agent-protocol/pull/655)): The remaining doc steps of #390, from fresh `origin/main`.
- **zig hub: the six read-only ops are visible, and nothing else changes** ([#654](https://github.com/lsm/open-agent-protocol/pull/654)): The six read-only ops in `zig/src/hub/stdio.zig` — `adapters`, `sessions`, `capabilities`, `state`, `models`, `tools` — become `pub`, and so does the `Outcome` union three of them return.
- **zig hub: the adapter is handed the operator's source, not the wire's** ([#653](https://github.com/lsm/open-agent-protocol/pull/653)): Closes **#633** (D19).
- **ci: run the compatibility checker from the base, not the pull request** ([#652](https://github.com/lsm/open-agent-protocol/pull/652)): One file, `.github/workflows/compatibility.yml`. ci-infra is retired, so this is mine.
- **fix: a signed thinking block's thinking_end carries the block it ends** ([#651](https://github.com/lsm/open-agent-protocol/pull/651)): Final pass.
- **hub: an open says why it was refused, not only which condition** ([#650](https://github.com/lsm/open-agent-protocol/pull/650)): Part 1 of the refusal change, from `main`, not stacked.
- **zig: oapx conformance judges a stale revision and an unservable request** ([#649](https://github.com/lsm/open-agent-protocol/pull/649)): Third slice of #368: ports goap's `refuseStaleRevision` and `refuseAddressableEnvelope`.
- **sdk/typescript: carry the model entry's published facts** ([#648](https://github.com/lsm/open-agent-protocol/pull/648)): Second of three surfaces for #632.
- **oapx hub: four fixes to what #581 landed** ([#647](https://github.com/lsm/open-agent-protocol/pull/647)): Follow-up to #581, which merged at 14:57 before the review reached me.
- **release: return to the alpha line, so 0.1.0-alpha.5 can be cut** ([#646](https://github.com/lsm/open-agent-protocol/pull/646)): The tags run `v0.1.0-alpha.1` through `v0.1.0-alpha.4` (2026-09-27).
- **validation: oapx validate loads the extension packs it is given** ([#644](https://github.com/lsm/open-agent-protocol/pull/644)): `--pack` was a refusal naming a future issue.
- **go/sdk: carry the model entry's published facts** ([#643](https://github.com/lsm/open-agent-protocol/pull/643)): First of three surfaces for #632.
- **zig: oapx conformance judges whether an endpoint can cancel a run** ([#642](https://github.com/lsm/open-agent-protocol/pull/642)): Second slice of #368: ports goap's `answerCancel`.
- **fix: a thinking_end carries the part it ends, so the signature is not lost** ([#641](https://github.com/lsm/open-agent-protocol/pull/641)): Client fix 4 of 4 from #582's review, branched from `origin/main`.
- **zig: a TUI-side agent-control client over the in-process pipe** ([#640](https://github.com/lsm/open-agent-protocol/pull/640)): Part of #365, step 2.
- **go/internal/provider: the openai wire declares its text part** ([#639](https://github.com/lsm/open-agent-protocol/pull/639)): Client fix 3 of 4 from #582's review, branched from `origin/main`.
- **zig,go: services.ai.azure.com goes back to being unmatched** ([#638](https://github.com/lsm/open-agent-protocol/pull/638)): Follow-up to #637, and it undoes the one part of that PR that was not earned.
- **zig,go: azure label-anchors its three labels, and gains services.ai** ([#637](https://github.com/lsm/open-agent-protocol/pull/637)): Finishes #621's azure. #625 label-anchored `openai.azure.com` and deliberately left `cognitiveservices.azure.com` as an inline substring; that one is now a label, and **`services.ai.azure.com` joins them** — a host the tree has never…
- **zig,go: google matches its two api hosts and one regional label, exactly** ([#635](https://github.com/lsm/open-agent-protocol/pull/635)): Follow-up to #623, which was **wider than the rule you set**.
- **zig: a built-in tool declares its permission kind instead of having it guessed** ([#634](https://github.com/lsm/open-agent-protocol/pull/634)): Part of #364.
- **zig,go: an ollama host is loopback on 11434, and never a path** ([#631](https://github.com/lsm/open-agent-protocol/pull/631)): Last of #621, and the one that is not a host rule at all.
- **validation: the pack loader takes the io it reads with** ([#629](https://github.com/lsm/open-agent-protocol/pull/629)): `readAll` in `packs.zig` reached for `std.testing.io`, so `packs.load` compiled only inside a test build — nothing outside one could call it.
- **zig,go: isBedrock matches the first label under amazonaws, not the string** ([#628](https://github.com/lsm/open-agent-protocol/pull/628)): Third of #621.
- **tui: a context window the user chose survives the session** ([#627](https://github.com/lsm/open-agent-protocol/pull/627)): tui: a context window the user chose survives the session
- **go/internal/provider: text and reasoning hold different parts** ([#626](https://github.com/lsm/open-agent-protocol/pull/626)): Client fix 2 of 4 from #582's review, branched from `origin/main`.
- **zig,go: isAzureOpenAI label-anchors the openai host and never azure.com** ([#625](https://github.com/lsm/open-agent-protocol/pull/625)): Second of #621, scoped to the one label cleared for immediate work.
- **go/internal/provider: a tool call ends at the index it started at** ([#624](https://github.com/lsm/open-agent-protocol/pull/624)): Client fix 1 of 4 from #582's review, branched from `origin/main`.
- **zig,go: isGoogle is named, and keeps the regional Vertex hosts** ([#623](https://github.com/lsm/open-agent-protocol/pull/623)): First of #621.
- **zig: pin what the permission boundary does and does not reach** ([#622](https://github.com/lsm/open-agent-protocol/pull/622)): Step 1 of the [#587](https://github.com/lsm/open-agent-protocol/issues/587) design, and it changes nothing: pin what the permission boundary does and does not reach, so a later change that moves it is a decision rather than a drift.
- **zig,go: isZai reads a host, keeping the behaviour it has today** ([#620](https://github.com/lsm/open-agent-protocol/pull/620)): Tenth and last of the #533 vendor predicates, and the one the coordinator decided separately.
- **zig: map the TUI-to-agent seam onto agent-control-core** ([#619](https://github.com/lsm/open-agent-protocol/pull/619)): Closes #373.
- **zig,go: isQwen reads a two-label host, not the bare words** ([#611](https://github.com/lsm/open-agent-protocol/pull/611)): Ninth of the #533 vendor predicates, and the one the issue flagged as a different question from a domain match.
- **zig,go: isAnthropic reads the host, not the whole URL** ([#610](https://github.com/lsm/open-agent-protocol/pull/610)): Eighth of the #533 vendor predicates, and the largest consequence of the ten.
- **looptrace: the loop's events, as the protocol's envelopes** ([#609](https://github.com/lsm/open-agent-protocol/pull/609)): First of three for #370 step 3: a run's `agent.Event` values become the wire's `protocol.Envelope` values, numbered from one and stamped with the run's own session, run and revision.
- **ci: record an incompatible Go change in the pull request, not the changelog** ([#608](https://github.com/lsm/open-agent-protocol/pull/608)): `apidiffcheck` read `CHANGELOG.md`'s `## Unreleased` section as the record of an incompatible change to a public Go package.
- **release: record the version-line conflict as an open item** ([#607](https://github.com/lsm/open-agent-protocol/pull/607)): `docs/releasing.md` (added in #576) said the script refuses a version older than the newest section in the file, but did not say that this repository is currently in exactly that state — which is the part a maintainer needs to read before…
- **zig,go: isOpenRouter reads the host, not the whole URL** ([#606](https://github.com/lsm/open-agent-protocol/pull/606)): Seventh of the #533 vendor predicates, same shape as the six before it.
- **validation: a session's published sources must account for what it declared** ([#605](https://github.com/lsm/open-agent-protocol/pull/605)): `checkPublishedUnion` was called from both places Go calls it and did the one thing both of them already do elsewhere: it reported two tool sources with one id.
- **oapx hub: the route matcher, as a table the draft can be checked against** ([#604](https://github.com/lsm/open-agent-protocol/pull/604)): Part of #388, and deliberately on its own: this is `route(method, path)` and nothing else — no listener, no I/O, no dependency on the HTTP module that #581 adds, which is why it is a new file rather than a function in `http.zig`.
- **zig,go: isDeepSeek reads the host, not the whole URL** ([#602](https://github.com/lsm/open-agent-protocol/pull/602)): Sixth of the #533 vendor predicates, and the first of these with a **catalogued** row to check the narrowing against.
- **agent: a tool call is asked, waited for, and answered** ([#601](https://github.com/lsm/open-agent-protocol/pull/601)): Fourth and last of four for #370: the caller executes a tool call, so the loop's part is the round trip — emit `tool_call_requested`, wait for that one call, and turn the answer into a message the next turn carries.
- **zig,go: isGitHubCopilot reads the host, not the whole URL** ([#600](https://github.com/lsm/open-agent-protocol/pull/600)): Fifth of the #533 vendor predicates, and the domain is **not** the substring it was reading.
- **ci: fail on a defer scoped inside a block that closes before the call** ([#599](https://github.com/lsm/open-agent-protocol/pull/599)): A `defer` runs when its **block** closes.
- **tui: the status line shows the token rate for the turn and since the last model switch** ([#598](https://github.com/lsm/open-agent-protocol/pull/598)): #558.
- **zig,go: isChutes reads the host, not the whole URL** ([#597](https://github.com/lsm/open-agent-protocol/pull/597)): Fourth of the #533 vendor predicates, same shape as #583, #590 and #592.
- **ci: run the Go workflow's push trigger on main only** ([#596](https://github.com/lsm/open-agent-protocol/pull/596)): `ci.yml` runs `on: push:` with no branch filter, so every push to an in-repo branch starts a push-event run of the whole workflow *on the same sha* as the `pull_request` run that same push already started — the same `test`, `go-sdk-smoke`…
- **zig: say which status and what the provider said, on google and ollama** ([#595](https://github.com/lsm/open-agent-protocol/pull/595)): From [#562](https://github.com/lsm/open-agent-protocol/issues/562): `google_generative_api.zig` and `ollama_api.zig` ended a non-2xx response with a bare `"google request failed"` / `"ollama request failed"`, so a rejected credential read…
- **validation: let the semantic gate name a code it is still known to miss** ([#594](https://github.com/lsm/open-agent-protocol/pull/594)): The gate filtered every fixture's declared codes through `semantic.isImplemented` and compared only what survived, which works only because a listed code is fully ported.
- **providers: read every row's base_url_env, and hold a file to a stricter credential rule** ([#593](https://github.com/lsm/open-agent-protocol/pull/593)): Two halves of #360 that stand on their own. **Precedence:** six catalogued rows declare a `base_url_env` and only three were ever read — `ollama`, `azure` and `google` each declared a variable nothing consulted, so setting…
- **zig,go: isCerebras reads the host, not the whole URL** ([#592](https://github.com/lsm/open-agent-protocol/pull/592)): Third of the #533 vendor predicates, same shape as #583 and #590.
- **zig,go: isGroq reads the host, not the whole URL** ([#590](https://github.com/lsm/open-agent-protocol/pull/590)): Second of the #533 vendor predicates, the same shape as #583.
- **zig: the TUI path row shows the directory the agent is working in** ([#589](https://github.com/lsm/open-agent-protocol/pull/589)): #563, final pass.
- **oapx validate: take --mode strict|tolerant** ([#588](https://github.com/lsm/open-agent-protocol/pull/588)): The tolerant compile variant `tolerate.zig` has always existed for, and nothing reached it: the validator was built strict, so a trace using an envelope type no pack claims was refused by the schema, and the three fixtures the manifest…
- **zig: oapx conformance --command, driving the handshake and one run** ([#585](https://github.com/lsm/open-agent-protocol/pull/585)): Decision 0038 puts the conformance runner in oapx so a new language's implementers need no other toolchain.
- **agent: the run, with one terminal and a reason for every exit** ([#584](https://github.com/lsm/open-agent-protocol/pull/584)): Third of four for #370: `go/internal/agent` gains `Run` — a prompt, turns, and exactly one terminal per run — over the `Streamer` seam from #547.
- **zig,go: isMistral reads the host, not the whole URL** ([#583](https://github.com/lsm/open-agent-protocol/pull/583)): First of the #533 vendor PRs, both trees so the port and the runtime cannot drift.
- **oapx hub: the HTTP daemon binds, and the trust model holds** ([#581](https://github.com/lsm/open-agent-protocol/pull/581)): Part of #388.
- **go/internal/inferenceserve: the id allocator belongs to the connection** ([#579](https://github.com/lsm/open-agent-protocol/pull/579)): Final pass — it decides what a host deduplicates by, so it waits for the coordinator's go-ahead after the bot approves.
- **zig: the loaded list leads with the plans, in catalog order (#351 step 5)** ([#578](https://github.com/lsm/open-agent-protocol/pull/578)): The last step of #351: the model list leads with the coding plans, in catalog order.
- **release: build a release's changelog section from the merged pull requests** ([#576](https://github.com/lsm/open-agent-protocol/pull/576)): The owner moved changelog writing to the release process on 2026-09-29, and nothing was standing in that gap: `release-binaries.yml` builds, signs, notarizes, packages and publishes, but never touched `CHANGELOG.md`, and there is no…
- **zig: a key refused on a plan is offered nothing from it (#352 step 2)** ([#575](https://github.com/lsm/open-agent-protocol/pull/575)): #352 step 2, in two commits: a refused key is offered nothing from that plan, and the verdict is remembered.
- **zig: decode the interaction events the agent-control-core schema lists** ([#574](https://github.com/lsm/open-agent-protocol/pull/574)): The OAP envelope decoder knew the interaction *answers* and none of the events that open an interaction, so a client reading a run hit `UnknownEnvelopeType` on the first gate the endpoint opened — reproduced against `oapx serve agent…
- **docs: say the release process writes the changelog, not a pull request** ([#573](https://github.com/lsm/open-agent-protocol/pull/573)): `CLAUDE.md`'s conventions bullet told every agent to add an entry under `## Unreleased` in `CHANGELOG.md`; it now says a pull request does not edit that file, that the release process writes it in the Keep a Changelog shape from the PRs…
- **zig: one host parse the vendor predicates can share** ([#571](https://github.com/lsm/open-agent-protocol/pull/571)): The enabling change for #533, with no behaviour change of its own. #534 gave the OpenAI host predicate a real parse — parse the URL, take the host, match `openai.com` or a `.`-delimited suffix of it — while the file's other ten base-URL…
- **zig,go: the environment key wins, except where a login signs the request** ([#570](https://github.com/lsm/open-agent-protocol/pull/570)): The environment key now wins, on the paths that resolve a credential generically.
- **tui: /autocompact compacts the session at a share of the context window** ([#569](https://github.com/lsm/open-agent-protocol/pull/569)): #560.
- **tui: set the context window for a session with /context and --context-window** ([#568](https://github.com/lsm/open-agent-protocol/pull/568)): #559, second of two; it needs #564, which records the ceiling it refuses against.
- **ci: cancel a pull request's superseded run on its next push** ([#567](https://github.com/lsm/open-agent-protocol/pull/567)): `ci.yml`, `ci-zig.yml` and `benchmark-report.yml` each take one concurrency group per pull request, so a newer push to that PR cancels the run the previous push started, queued or in progress, and nothing outside a pull request is…
- **zig: the TUI path row moves left and the git branch takes the right end** ([#566](https://github.com/lsm/open-agent-protocol/pull/566)): #556.
- **zig: the TUI sends one continue of its own after an unretried provider error** ([#565](https://github.com/lsm/open-agent-protocol/pull/565)): Closes #562.
- **catalog: record the largest context window a row's models can be given** ([#564](https://github.com/lsm/open-agent-protocol/pull/564)): #559, first of two.
- **go: LookupCredential honours a row's credential_precedence** ([#561](https://github.com/lsm/open-agent-protocol/pull/561)): `go/providercatalog`'s `LookupCredential` left `credential_precedence` unread. #507 added the field to the catalog and taught `zig/src/provider_credential.zig` to honour it, but the Go lookup searched the environment first unconditionally…
- **go/internal/inferenceserve: the envelope vocabulary and the sequence gate** ([#557](https://github.com/lsm/open-agent-protocol/pull/557)): Final pass — this decides which envelope carries what, so it waits for the coordinator's go-ahead after the bot approves.
- **zig: fail when a module root's test blocks no addTest compiles** ([#555](https://github.com/lsm/open-agent-protocol/pull/555)): Six `build.zig` modules were in this state and the class is invisible from a test report, because the report counts what ran and not what did not.
- **zig: tell a refused key from an outage in catalog discovery (#352 step 2a)** ([#554](https://github.com/lsm/open-agent-protocol/pull/554)): Step 2 of #352, split from the step that uses it. 13 lines, one file.
- **zig: spawn the OAP endpoint client's child on an io it owns** ([#553](https://github.com/lsm/open-agent-protocol/pull/553)): `endpoint_client.Client.spawn` passed `std.Io.Threaded.global_single_threaded.io()` to `std.process.spawn`, which has no thread to run a child pipes on, so every spawn outside a test binary failed with `OutOfMemory` before a process…
- **providers: let a user override a catalogued row's endpoint** ([#552](https://github.com/lsm/open-agent-protocol/pull/552)): Adds an `overrides` array to `~/.oapx/providers.json`, which is #360 step 1: define what an override may change on a catalog row and refuse everything else at load with a named error.
- **zig,go: an orphan tool result is dropped wherever it falls in a run** ([#551](https://github.com/lsm/open-agent-protocol/pull/551)): The Anthropic wire carried the same orphan gap in **both** trees, so this closes both in one PR to keep the port and the runtime in step.
- **zig: derive credential groups from the catalog (#352 step 1)** ([#550](https://github.com/lsm/open-agent-protocol/pull/550)): Step 1 of #352, plus the audit that goes with it.
- **oapx hub: serve open, the first op that reads a nested envelope** ([#549](https://github.com/lsm/open-agent-protocol/pull/549)): **`open` — the first op that reads a nested envelope.** Part of [#387](https://github.com/lsm/open-agent-protocol/issues/387), and it unblocks `submit`, `resolve` and `cancel`, which all lean on the same machinery. 270 changed lines across…
- **go: an orphan tool result is dropped wherever it falls in a run** ([#548](https://github.com/lsm/open-agent-protocol/pull/548)): The Go `openai-completions` half of the orphan guard, matching Zig #530.
- **agent: a turn is the provider runtime's own events on a channel** ([#547](https://github.com/lsm/open-agent-protocol/pull/547)): Second of four for #370: a `Streamer` is the loop's one dependency on a model, returning the provider runtime's own `provider.Event` values on a channel, with `provider.StopReason` a typed set of six names rather than a bare string.
- **zig: the print path and the request default ask the catalog, not Kimi** ([#546](https://github.com/lsm/open-agent-protocol/pull/546)): The two leftovers #507's approval listed, plus the same two facts a third and fourth time in the same two functions.
- **zig: run the two TUI fixture modules' own tests** ([#545](https://github.com/lsm/open-agent-protocol/pull/545)): `tui_fixture_mod` and `tui_tests_fixtures_mod` were declared in `build.zig` with no `addTest`, so the three tests in `src/tui/fixture_provider.zig` and the one in `src/tui/tests/fixtures/mod.zig` had never been compiled or run.
- **zig: one ownership setting on EventStream, and nothing that can dangle** ([#544](https://github.com/lsm/open-agent-protocol/pull/544)): Step 3 of [#415](https://github.com/lsm/open-agent-protocol/issues/415): `EventStream` takes one `ownership` setting whose values cannot express the use-after-free, and the two functions that copy between streams free conditionally.
- **validation: give the validator a mode of its own** ([#543](https://github.com/lsm/open-agent-protocol/pull/543)): Go's validator is built with `validation.NewWith(Options{Mode, Packs})`, which turns `Mode` into a single bool the rest of the validator branches on; the audit behind #367 found the Zig tree had `tolerate.zig` and `packs.zig` but **no…
- **zig: build the streaming core's tests from the declared modules** ([#542](https://github.com/lsm/open-agent-protocol/pull/542)): build.zig gave `src/api_registry.zig` and `src/stream.zig` an `addTest` each, but both built a second module inline rather than taking the `api_registry_mod` and `stream_mod` the product imports — a hand-copied `root_source_file` with the…
- **go: the provider client matches oapx on the answered set and the host** ([#541](https://github.com/lsm/open-agent-protocol/pull/541)): The Go side of #514 and #511 — two transcriptions that were holding a defect in place on purpose until the Zig side decided it.
- **zig: run the anthropic oauth module's nine tests** ([#540](https://github.com/lsm/open-agent-protocol/pull/540)): build.zig declared `oauth_anthropic_mod` with no `addTest`, so the nine tests in `src/utils/oauth/anthropic.zig` had never been compiled or run — the pasted-credential input parser, the token-response parser and the auth-URL builder were…
- **validation: judge a published source that carries an attachment member** ([#539](https://github.com/lsm/open-agent-protocol/pull/539)): A source published in a catalog is a description of a tool source; `command`, `args` and `environment` belong to the attachment that serves it, so a catalog naming one is publishing the attachment as part of the source.
- **zig: the path-url test now uses a name in the path** ([#538](https://github.com/lsm/open-agent-protocol/pull/538)): Follow-up to #534.
- **zig: list TUI sessions by title, in local time** ([#537](https://github.com/lsm/open-agent-protocol/pull/537)): `/resume` labelled a session with its model, provider and a UTC time, so switching models mid-session made it look like a new session.
- **agent: decide a turn's fate from the reply alone** ([#536](https://github.com/lsm/open-agent-protocol/pull/536)): First of four PRs for the first slice of #370: `TurnOutcome` reads a `provider.AssistantContent` and says whether the turn failed, the run is answered, or the reply's tool calls run, carrying `oapx`'s cut-off rule with it.
- **zig: stop escaping the five characters Go's encoder escapes** ([#535](https://github.com/lsm/open-agent-protocol/pull/535)): Last step of [#417](https://github.com/lsm/open-agent-protocol/issues/417) under the amended parity rule in #473: `zig/src/json/writer.zig` and the ACP frame writer stop escaping `<`, `>`, `&`, U+2028 and U+2029, which #319 and #330 added…
- **zig: one predicate decides whether a base URL is an openai host** ([#534](https://github.com/lsm/open-agent-protocol/pull/534)): Fixes #511, final pass.
- **protocol: let a model entry publish the facts a listing learned** ([#532](https://github.com/lsm/open-agent-protocol/pull/532)): Implements Decision 0035 in the schema, the Zig types, the producer and both validators, then the docs.
- **oapx hub: the registry config, signals and a bounded sweep that retries** ([#531](https://github.com/lsm/open-agent-protocol/pull/531)): Closes #389, and takes #407's D6 with it because the shutdown sweep is the same function.
- **zig: drop an orphaned tool result wherever it falls in a run** ([#530](https://github.com/lsm/open-agent-protocol/pull/530)): Fixes #513's orphan half, and decides its malformed-chunk half.
- **zig: write the reasoning detail through the json writer** ([#529](https://github.com/lsm/open-agent-protocol/pull/529)): Fixes #515.
- **go: a base-URL override and a loopback provider, so a parity test can be pointed somewhere** ([#528](https://github.com/lsm/open-agent-protocol/pull/528)): Final pass — this decides where a credential is sent, so it waits for the coordinator's go-ahead after the bot approves.
- **zig: hand back a result waitResultFor's caller owns** ([#527](https://github.com/lsm/open-agent-protocol/pull/527)): Step 2 of [#415](https://github.com/lsm/open-agent-protocol/issues/415): `waitResultFor` and `waitResult` now return an `ai_types.OwnedMessage`, a deep copy, so the result is still valid after `removeStreamState` or `reset` frees the…
- **docs: the Go tree's agent loop, and the first slice** ([#526](https://github.com/lsm/open-agent-protocol/pull/526)): Maps `zig/src/agent/`'s loop onto what the Go tree needs, in order, and names the first slice: text turns and client-executed tool calls over `go/internal/provider`.
- **zig: key the answered tool calls by the rewritten id** ([#525](https://github.com/lsm/open-agent-protocol/pull/525)): Fixes #514.
- **hub: gate a subscribing open on the revision the host asked for** ([#524](https://github.com/lsm/open-agent-protocol/pull/524)): **The last thing standing between the Zig hub and #387's `open` op, and it is a dependency rather than part of it** — so it gets its own PR here, the same way D3 and D5 did.
- **zig: resume a TUI session from its last compaction, without its chunks** ([#523](https://github.com/lsm/open-agent-protocol/pull/523)): Resume parsed every streamed chunk (84% of a 50 MB session file) and refused files over 64 MB.
- **oapx validate: refuse a flag it does not carry, by name** ([#522](https://github.com/lsm/open-agent-protocol/pull/522)): **Half of the third box in [#367](https://github.com/lsm/open-agent-protocol/issues/367), and it is a defect rather than a missing feature.** The audit comment on the issue found it; this PR is the part that is wrong today rather than…
- **build: make build and make tui build ReleaseSafe** ([#521](https://github.com/lsm/open-agent-protocol/pull/521)): `make build`/`make tui` built Debug, where Zig's debug allocator records a stack trace for every allocation.
- **zig: add TUI session worktrees and settings** ([#520](https://github.com/lsm/open-agent-protocol/pull/520)): The PTY `commands` scenario is not runnable on macOS because the driver rejects macOS keychain access; it is intended for Linux/CI.
- **zig: bring back /think, show thinking off, and add a max level** ([#519](https://github.com/lsm/open-agent-protocol/pull/519))
- **zig: return shell output under 10 KB to the model whole** ([#518](https://github.com/lsm/open-agent-protocol/pull/518)): With compact output on (the TUI default), `shell_execute` stored every output as an artifact and returned a summary: ~430 bytes of retrieval instructions plus the first and last 512 bytes.
- **feat: the anthropic-messages client, on the block-indexed wire** ([#517](https://github.com/lsm/open-agent-protocol/pull/517)): Part of #358 step 3.
- **zig: an open's metadata reaches the adapter** ([#516](https://github.com/lsm/open-agent-protocol/pull/516)): **D5 from [#407](https://github.com/lsm/open-agent-protocol/issues/407)**, on its own.
- **feat: the openai-completions client, on the runtime's own event stream** ([#512](https://github.com/lsm/open-agent-protocol/pull/512)): Part of #358 step 2.
- **zig: the hub's models and tools** ([#510](https://github.com/lsm/open-agent-protocol/pull/510)): The two catalog operations of #387's six: `models` and `tools`.
- **go/providercatalog: a row's credentials, base and wire, as Zig resolves them** ([#509](https://github.com/lsm/open-agent-protocol/pull/509)): Part of #358, step 1.
- **test: the contested settlement, a second pi fixture over two calls open at once** ([#508](https://github.com/lsm/open-agent-protocol/pull/508)): Part of #433.
- **zig: Kimi on the generic loader, and the catalog names its default region** ([#507](https://github.com/lsm/open-agent-protocol/pull/507)): Part of #351, step 4.
- **docs: the system map is kept, and made true against the tree** ([#506](https://github.com/lsm/open-agent-protocol/pull/506)): The system map, kept and made true.
- **zig,go,rust,py,ts: auth providers carry how they accept a credential** ([#505](https://github.com/lsm/open-agent-protocol/pull/505)): Part of #354, step 2.
- **docs: what the parity job is for, and what the corpora cover instead** ([#504](https://github.com/lsm/open-agent-protocol/pull/504)): #433 step 5, branched from main.
- **sdk: the OAP path speaks protocol.Envelope, the legacy wire keeps the frame** ([#501](https://github.com/lsm/open-agent-protocol/pull/501)): #369 step 2, branched from main.
- **zig: a catalog carries the revision its lister served it under** ([#500](https://github.com/lsm/open-agent-protocol/pull/500)): **D3 from [#407](https://github.com/lsm/open-agent-protocol/issues/407)**, on its own — it is a `contract` change, and #407 is not this issue, so bundling it into a #387 PR would be two issues in one.
- **refactor: one duplicate-key JSON walk, in go/internal/jsonwalk** ([#499](https://github.com/lsm/open-agent-protocol/pull/499)): #489, and the audit it asked for is the shape of the diff.
- **tests: a parity fixture that watches a run settle, over the endpoint's stdio binding** ([#498](https://github.com/lsm/open-agent-protocol/pull/498)): #433's steps 2 and 3: a fixture whose run reaches a terminal, in the compared output.
- **zig: serve the CI fixture auth provider only when a test asks for it** ([#497](https://github.com/lsm/open-agent-protocol/pull/497)): Part of #354, which the owner has decided: **option 2** of the three in my note on the issue.
- **ci: run the two stdio hubs against each other on every change** ([#496](https://github.com/lsm/open-agent-protocol/pull/496)): The CI half of [#390](https://github.com/lsm/open-agent-protocol/issues/390), pulled forward ahead of its place in the order.
- **zig: let a run go until the model stops calling tools** ([#495](https://github.com/lsm/open-agent-protocol/pull/495)): The TUI stopped long tasks after exactly 100 model turns, right after a tool call, with no reply and no message: the loop's default cap when a caller sets none.
- **zig: serve the five coding plans and OpenAI, with the wire chosen per model** ([#494](https://github.com/lsm/open-agent-protocol/pull/494)): Part of #351.
- **zig: serve the five gateway rows through the catalog loader** ([#493](https://github.com/lsm/open-agent-protocol/pull/493)): Part of #351.
- **go: fold the sdk/go module into the main module as go/sdk, and retire the comment allowlist** ([#492](https://github.com/lsm/open-agent-protocol/pull/492)): #369.
- **docs: goap is this repository's tool, and the README says so** ([#491](https://github.com/lsm/open-agent-protocol/pull/491)): #371, all three steps, branched from main.
- **fuzz: every Go decoder that reads bytes from outside, one target each, and a weekly job** ([#490](https://github.com/lsm/open-agent-protocol/pull/490)): Closes #416's Go step.
- **zig endpoint: drain before answering a cancel, as goap does** ([#488](https://github.com/lsm/open-agent-protocol/pull/488)): Part of #433, and the prerequisite for its fixture.
- **zig: add an opt-in live smoke gate for a catalogued row** ([#487](https://github.com/lsm/open-agent-protocol/pull/487)): Part of #356.
- **auth: answer the provider list from the catalog** ([#486](https://github.com/lsm/open-agent-protocol/pull/486)): Part of #354.
- **zig,go: read the recorded version fact and drop the guess** ([#485](https://github.com/lsm/open-agent-protocol/pull/485)): Part of #410.
- **zig: grow the TUI composer into a scrolling multi-row panel** ([#484](https://github.com/lsm/open-agent-protocol/pull/484)): Implements §6/§9 of the TUI UX plan: the composer no longer windows a long draft with `…`; it grows into a scrolling multi-row panel.
- **zig: a run ends with exactly one event that ends it** ([#483](https://github.com/lsm/open-agent-protocol/pull/483)): Step 1 of [#415](https://github.com/lsm/open-agent-protocol/issues/415), and step 3 for this trap: the `CLAUDE.md` warning is gone because the shape it warned about can no longer be expressed.
- **zig: rework the TUI status line with a cwd row, bare values and priority dropping** ([#482](https://github.com/lsm/open-agent-protocol/pull/482)): PR A of the TUI UX plan (sections 1-5):
- **providers: record per endpoint whether its base carries the version** ([#481](https://github.com/lsm/open-agent-protocol/pull/481)): Part of #410.
- **zig: the hub's serve loop, the oapx hub verb, and the differential test** ([#480](https://github.com/lsm/open-agent-protocol/pull/480)): PR 2 of [#387](https://github.com/lsm/open-agent-protocol/issues/387): the serve loop, the `oapx hub --stdio` verb, and the differential test.
- **binding: a host-supplied binding record, and a file store that refuses a torn one** ([#479](https://github.com/lsm/open-agent-protocol/pull/479)): Part of #447: the Go half.
- **decisions: 0040, a session reopens through its own binding** ([#478](https://github.com/lsm/open-agent-protocol/pull/478)): Part of #446, step 1: the decision record for T7 (`session-reattach`), on the owner's direction of 2026-09-27 for the `reopen` member, the reply and the three refusals — and answering the two questions the staged plan left open.…
- **docs: a guide to the Go library, linked from the README** ([#477](https://github.com/lsm/open-agent-protocol/pull/477)): Part of #414, as decided on the issue: no doc comments, one written guide, and `goap` as the worked example that CI builds and runs.
- **hub: admit any charset on the HTTP body gate, and read the body as UTF-8** ([#476](https://github.com/lsm/open-agent-protocol/pull/476)): Part of #406: the charset decision, the G1 text, and the tables — then #406 closes.
- **tests: compare a run's envelopes in order, not as a multiset** ([#475](https://github.com/lsm/open-agent-protocol/pull/475)): Part of #433, step 1: make the parity comparison order-aware within a run.
- **tests: compare what a backend writes to its child as parsed data** ([#474](https://github.com/lsm/open-agent-protocol/pull/474)): Part of #417, step 2 of 3, following the 0038 amendment in #473.
- **decisions: 0038 compares the trees by parsed JSON, not bytes** ([#473](https://github.com/lsm/open-agent-protocol/pull/473)): Part of #417, step 1 of 3: amend Decision 0038's parity section and record the amendment in its status line.
- **research: read deepseek-harness' session store at dsh-v0.1.7-rc.2** ([#472](https://github.com/lsm/open-agent-protocol/pull/472)): Part of #445, step 7: the DeepSeek row of 0039's evidence table, read at the pin's own commit `477b4f420553e8a52c2fbccc464d7561b239c443`.
- **research: read opencode's session store at v1.18.32** ([#471](https://github.com/lsm/open-agent-protocol/pull/471)): Part of #445, step 6: the OpenCode row of 0039's evidence table, read at the pin's own commit `545f51d26cc39a907d2867492d498d9607ea5fa4` by the method the five earlier reload ledgers used.
- **research: read hermes' session store at v2026.9.24** ([#470](https://github.com/lsm/open-agent-protocol/pull/470)): Part of #445, step 5: the Hermes row of 0039's evidence table, read at the pin's own commit `f97608f178d1ffeca59860195ab7da295f7c8e5f` by the method the four earlier reload ledgers used (read the source, cite file and symbol, say plainly…
- **zig: time a provider stream out on silence, not on length** ([#469](https://github.com/lsm/open-agent-protocol/pull/469)): The TUI cut off any reply still streaming at two minutes with `Provider protocol stream timed out`.
- **decisions: accept 0039, and name the reopen member** ([#468](https://github.com/lsm/open-agent-protocol/pull/468)): Decision 0039 is executable on \`main\` in both trees now (#452 in \`go/serve\`, #455 in the Zig hub), so it is accepted under Decision 0003.
- **zig: have /compact summarize the TUI conversation with the model** ([#467](https://github.com/lsm/open-agent-protocol/pull/467)): `/compact [focus]` now has the current model write a sectioned summary, then replaces the TUI agent's history with it.
- **zig: the hub's stdio framing, and the five ops it serves** ([#466](https://github.com/lsm/open-agent-protocol/pull/466)): PR 1 of [#387](https://github.com/lsm/open-agent-protocol/issues/387): the stdio wire's framing and the five ops it carries.
- **zig: load a catalog row's models through one generic loader** ([#465](https://github.com/lsm/open-agent-protocol/pull/465)): Part of #351.
- **research: read pi's session store at v0.87.1** ([#464](https://github.com/lsm/open-agent-protocol/pull/464)): Part of #445, step 4 of 7 (pi v0.87.1).
- **pi, codex: a cancel answers its acceptance, not the reader's progress** ([#463](https://github.com/lsm/open-agent-protocol/pull/463)): Part of #462, the root cause of the `TestBackendsMatchOapx/pi` flake on #10.
- **research: record ACP's session reload at v1.9.1** ([#461](https://github.com/lsm/open-agent-protocol/pull/461)): Part of #445, step 3 of 7 (acp v1.9.1).
- **zig: pin the two properties the loop was chosen for** ([#459](https://github.com/lsm/open-agent-protocol/pull/459)): Step 3 of [#405](https://github.com/lsm/open-agent-protocol/issues/405), which closes the issue: #387 and #388 now build on one documented loop and one tested one.
- **research: record Codex's session reload at 0.157.0** ([#458](https://github.com/lsm/open-agent-protocol/pull/458)): Part of #445, step 2 of 7 (codex-app-server 0.157.0).
- **zig: show shell commands, queue follow-ups and filter pickers in the TUI** ([#457](https://github.com/lsm/open-agent-protocol/pull/457)): Six TUI fixes from daily use:
- **research: record Claude Code's session reload at 2.1.282** ([#456](https://github.com/lsm/open-agent-protocol/pull/456)): Part of #445, step 1 of 7 (claude-code 2.1.282).
- **zig: a closed session leaves the hub, and a stream failure does not** ([#455](https://github.com/lsm/open-agent-protocol/pull/455)): Part of #442, and the Zig half of the close [Decision 0039](https://github.com/lsm/open-agent-protocol/blob/main/decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md) makes executable.
- **zig: the loop waits on every child at once, not one after another** ([#454](https://github.com/lsm/open-agent-protocol/pull/454)): Step 2 of [#405](https://github.com/lsm/open-agent-protocol/issues/405), the one contract change [§8.6](https://github.com/lsm/open-agent-protocol/blob/main/DESIGN.md) requires.
- **go/serve: pin the three default windows a host waits on** ([#453](https://github.com/lsm/open-agent-protocol/pull/453)): Part of #406, step 6 of 7 (G5 and G8).
- **go/serve: release a session once it stops being open** ([#452](https://github.com/lsm/open-agent-protocol/pull/452)): Part of #442, and the Go half of what makes Decision 0039 executable.
- **design: one loop owns the hub, and waits on readiness** ([#451](https://github.com/lsm/open-agent-protocol/pull/451)): Step 1 of [#405](https://github.com/lsm/open-agent-protocol/issues/405), which blocks [#387](https://github.com/lsm/open-agent-protocol/issues/387) and [#388](https://github.com/lsm/open-agent-protocol/issues/388).
- **zig: an ended subscription stops counting against the ceiling** ([#450](https://github.com/lsm/open-agent-protocol/pull/450)): The new box on [#399](https://github.com/lsm/open-agent-protocol/issues/399).
- **drafts: write 0039's close into the hub draft** ([#449](https://github.com/lsm/open-agent-protocol/pull/449)): The hub draft now follows Decision 0039: close releases a session, later ops on it answer unknown_session, and sessions lists live ones only.
- **go/serve: drive stdio's three subscription endings and pin their members** ([#441](https://github.com/lsm/open-agent-protocol/pull/441)): Part of #406, step 5 of 7 (G9).
- **decisions: 0039 — a session is OAP's, and a harness is where it runs** ([#440](https://github.com/lsm/open-agent-protocol/pull/440)): Proposes Decision 0039.
- **go/serve: pin the stdio refusals, and correct what the draft said about G4** ([#439](https://github.com/lsm/open-agent-protocol/pull/439)): Part of #406, step 4 of 7 (G4, G6 and G7).
- **zig: a hold lapses into a value, and the hub frees it** ([#438](https://github.com/lsm/open-agent-protocol/pull/438)): Step 2 of [#399](https://github.com/lsm/open-agent-protocol/issues/399), option 1 as #411 decided it.
- **providers: one join rule for a base url, in both trees** ([#437](https://github.com/lsm/open-agent-protocol/pull/437)): Part of #349.
- **go: fail an unrecorded incompatible change to a public package** ([#436](https://github.com/lsm/open-agent-protocol/pull/436)): Part of #413, step 3 of 4 (owner, 2026-09-27: option (a), by package).
- **go/serve: pin that a cancelled subscription ends the stream** ([#435](https://github.com/lsm/open-agent-protocol/pull/435)): Part of #406, step 3's G2 decision (owner, 2026-09-27): option (a), a hub-level test, and no subscriber accounting.
- **go/serve: refuse a Content-Type that names a charset other than UTF-8** ([#434](https://github.com/lsm/open-agent-protocol/pull/434)): Part of #406, step 3's charset decision (owner, 2026-09-27).
- **drafts: decide #53 — stdio has no unsubscribe, and the ceiling is not a trap** ([#429](https://github.com/lsm/open-agent-protocol/pull/429)): Part of #384.
