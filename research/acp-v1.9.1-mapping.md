# ACP v1.9.1 mapping ledger

Status: pin move over `research/acp-v1.7.0-mapping.md`. That ledger remains
the mapping; this one records only what moved between the two pins and the
evidence for it. Everything not named here is unchanged.

## Provenance and version boundary

- Repository: `https://github.com/agentclientprotocol/agent-client-protocol`
- Stable release: `v1.9.1`
- Schema release: `schema-v1.23.0` (tag commit
  `6d08f412a7a1370d3cc9a124e3be3d6acf92641e`, 2026-09-18; its `schema/v1`
  tree is identical to the release's)
- Commit: `7e87dc205a7325bd07d0249fd20bb7486ee6ba95`
- Commit date: 2026-09-18
- Stable specification tree SHA-256:
  `676ed241ba56ffbc6d17d0ca05cc28b1d33290dd1052048599e9b7dd10183de7`
- Stable generated-schema tree SHA-256:
  `d9a3a86abf71a3b3f995fb91f37778fa264df5a711a056c3dc8a6dde3ebc2ce9`
- Hash recipes: unchanged from the v1.7.0 ledger. Run with `shasum -a 256` on
  macOS; the same recipe reproduces the v1.7.0 digests
  `a23fba5f…1995` and `135854da…ff3` byte for byte, so the two pairs are
  comparable.

ACP wire version stays `1`; `initialize` negotiation is unchanged.

## Wire difference, v1.7.0 to v1.9.1

`git diff v1.7.0 v1.9.1 -- schema/v1/schema.json` is ten added lines and
nothing else. `schema/v1/meta.json` is unchanged, so the method and
notification registry is identical. The schema changelog records
`schema-v1.22.0` (stabilize tool call name, #2166; unstable session notices,
#2004) and `schema-v1.23.0` (unstable session notice capability, #2171). The
notices live only in `schema.unstable.json` and are outside the stable
boundary this adapter targets.

The one stable change:

| Surface | Change | Classification |
|---|---|---|
| `tool_call` (`ToolCall`) | optional `name`: `string \| null`, `x-deserialize-default-on-error` | additive; mapped |
| `tool_call_update` (`ToolCallUpdate`) | optional `name`: same; omission and `null` leave the name unchanged, a string sets it, v1 cannot clear it | additive; mapped |

No `sessionUpdate` discriminator, stop reason, tool status, permission option
kind, request or response member was added, removed or retyped. The prose
changes under `docs/protocol/v1/` outside `name` are clarifications of
existing schema (`rawInput`/`rawOutput` accept any JSON value, `line` is a
nullable u32, `switch_mode` tool kind listed, `required` markers on
`session/prompt` params) or examples corrected to the schema (grouped config
options, `currentModeId`, `content`-wrapped tool content, `{}` results,
`_meta` not on every type). Since `schema.json` is otherwise unchanged, each
already held at v1.21.0, and none changes what the adapter reads.

### Mapping of `name`

v1.7.0 had no programmatic tool name, so `action.call.*` `name` carried the
ACP `title`. From v1.9.1 both adapters (Go `go/adapter/acp`, Zig
`zig/src/adapter/acp/session.zig`) emit the ACP `name` when it is a non-empty
string and fall back to `title` otherwise.

- `tool_call` replaces the name: absent, `null`, or non-string means none.
- `tool_call_update` sets it only from a non-empty string.
- A non-string `name` is ignored rather than refused, honoring
  `x-deserialize-default-on-error`; a wrongly typed `title` is still refused,
  as before.
- `action.permission.requested` `title` still carries the ACP `title`.

Pinned by `TestToolNamePrefersProgrammaticNameAndIgnoresWrongType`,
`TestToolNameFallsBackToTitle` and the two matching Zig reducer tests. The
Zig fallback was mutated to title-only and the first Zig test failed.

The capability descriptor is unchanged, so only the revision's version prefix
moves: `acp-v1.9.1-schema-v1.23.0-oap-v3`. Both trees carry it — since the
Zig served backend gained the journal and the `session/new` tool-source
attach, the catalog holds one revision for the pin and
`zig/src/adapter/acp/adapter.zig` reads it.

## Corpus

`fixtures/adapters/acp-v1.9.1` is `fixtures/adapters/acp-v1` carried forward
(`corpus_from: v1.7.0`). **No case was re-recorded.** Every `native.jsonl` is
byte-identical to the v1.7.0 corpus, because no frame in the eleven cases
touches a member that changed: none carries `name`, and every other member is
unchanged per the diff above. Only the provenance and revision strings in
`manifest.json`, `case.json` and `expected-oap.json` moved.

No case exercises `name`. The pinned agent cannot emit it (below), and a
corpus line carrying it would be invented, so its coverage is the unit tests
only.

## Process gate

- `docker/cagent` tag `v1.143.0`, commit
  `d27c65ce59e6474fb4a57d0fa879fc53f5bf3f1a`, tree
  `7f30826b2e13ca967595c9e63a2d223d8aaab40f` (2026-09-24), the latest release.
- It still builds on `coder/acp-go-sdk v0.13.5`, whose generated
  `SessionUpdateToolCall` and `ToolCallUpdate` have no `name` member, so the
  agent speaks the pre-1.22 tool shape. Evidence against it proves the
  unchanged surface, not the new field.
- Built locally with Go 1.27.0 on darwin/arm64:
  `f3231c71eb46dda57964a535a1491b055bb3182dd85fac94bb51bc710f2b9acf`.
- `OAP_ACP_SMOKE=1` and `OAP_ACP_INTEGRATION=1` with that binary and
  `OAP_ACP_SHA256` bound both passed (`TestACPProcessSmoke`,
  `TestACPProcessAgainstChatCompletionsMock`).
- The Zig adapter was not driven against the live agent; its evidence is the
  corpus replay and reducer tests.

## Model-provider settings at this pin

ACP has none, and the absence is a recorded fact rather than a gap in the
reading. The protocol configures nothing about a model provider: a client spawns
an agent process and speaks to it, and the agent's own CLI or config decides
which vendor, base URL and key it uses. Nothing in `schema/v1/schema.json` at
this pin carries a model provider, a base URL or a credential.

The schema's one `model` is a session mode category — the enum a client offers as
a selector (`mode`, `model`, `model_config`, `thought_level`, or a free-form
custom name) — which presents a choice the agent already supports. It configures
no provider, and it is explicitly advisory: `schema/v1/schema.json` says such a
category is for UX and must not be required for correctness.

So an ACP entry cannot be translated into a provider setting by the adapter, and
a row's key reaches such an agent only through the settings that agent's own
project documents. A backend built on ACP inherits the answer from the agent
behind it rather than from this protocol.

## Session reload at v1.9.1

Decision 0039's evidence table cites the v1.7.0 ledger for this row. Every
line below is read from the specification and the generated schema at this
pin's commit `7e87dc205a7325bd07d0249fd20bb7486ee6ba95`, and the last
paragraph says what the Go adapter does with it — which is the part 0039
cannot do without.

**Each operation has its own gate, and a client must check it before
calling.** `AgentCapabilities` carries a top-level `loadSession: boolean` for
`session/load`, and a `sessionCapabilities` object whose `list`, `resume`,
`close`, `delete` and `additionalDirectories` members are each present-or-null
(`SessionListCapabilities`, `SessionResumeCapabilities`,
`SessionCloseCapabilities`, `SessionDeleteCapabilities` and
`SessionAdditionalDirectoriesCapabilities` in
`schema.unstable.json`, the last two of which the stable schema does not
define). The spec states the obligation per operation: "If `loadSession` is
`false` or not present, the Agent does not support loading sessions and
Clients **MUST NOT** attempt to call `session/load`", and the same shape for
`sessionCapabilities.resume` and `sessionCapabilities.list`. So a reattach
cannot assume any of the three: the gates are advertised per agent, and an
agent that advertises none of them cannot be reattached at all.

**A load is transcript reconstruction, and the spec says so in the
imperative.** `docs/protocol/v1/session-setup.mdx`: "The Agent **MUST**
replay the entire conversation to the Client in the form of `session/update`
notifications (like `session/prompt`)", each replayed message may carry an
opaque `messageId` "for the replayed message", and "When **all** of the
conversation entries have been streamed to the Client, the Agent **MUST**
respond to the original `session/load` request." The response therefore
arrives *after* the whole transcript, which is what makes it reconstruction
and not replay: there is no run id, no sequence, no gap semantics, and no
proof of what was omitted. `LoadSessionRequest` requires `sessionId`, `cwd`
and `mcpServers`; the response carries `modes` and `configOptions` and
nothing else — no ids to resume from later.

**A resume is attachment, and it is the operation that re-establishes the
MCP servers.** The same document: "Unlike `session/load`, the Agent **MUST
NOT** replay the conversation history via `session/update` notifications
before responding. Instead, it restores the session context, reconnects to
the requested MCP servers, and returns once the session is ready to
continue." `ResumeSessionRequest` requires only `sessionId` and `cwd` —
`mcpServers` is optional here and required on load — and the response is the
same `modes`/`configOptions` pair. The difference is the whole of it:
transcript, or attachment with the client's own servers back.

**A list exists, gated, and paginated on an opaque cursor.**
`sessionCapabilities.list` gates `session/list`;
`docs/protocol/v1/session-list.mdx`: "All parameters are optional. A request
with an empty `params` object returns the first page of sessions", `cwd` "Must
be an absolute path", and `cursor` is "Opaque cursor token from a previous
response's `nextCursor` field". `ListSessionsResponse` requires `sessions` and
carries `nextCursor`. Each entry reports `hasErrors`, and the spec is explicit
that a listed session's omitted and empty values "MUST NOT" be merged with
prior values — so a list entry is what the agent claims it knows, not a
portable transcript, and a host must not read across two pages of it.

**What it answers when the store is gone: nothing is specified.** For
`session/load` and `session/resume` of an id the agent does not have, the
stable specification at this pin says neither MUST nor MAY. The one
session-lifecycle sentence about a missing session is in the **close**
section — "Agents MAY return an error if the session does not exist or is not
currently active" — which is a MAY with no code attached, and it is about
close. So unlike Codex, which answers `-32602` with "no rollout found for
thread id …", an ACP agent may answer a load or a resume of an unknown id
with any error, with a success-shaped response, or with silence. Decision
0039's answer for that case is `unsupported_feature` — the host had the
binding and the harness cannot load it — which means the *adapter* has to
produce it whatever the agent does, and cannot read it off the wire. That is
the one thing in this row a port cannot inherit.

**What the Go adapter does with all of it: nothing, yet.** `go/adapter/acp`
calls `session/new` and only `session/new` — there is no `session/load`,
`session/resume` or `session/list` anywhere in it, and the descriptor's own
`attach` reason says why the *new* path carries the MCP server array "at this
pin". So ACP has no reattach today, and not because a gate is closed: the
adapter has no way to name a session it did not create, which is the
`session/list` this pin does advertise. Whoever takes #446 should read that
as the shape of the work: an ACP reattach is a list, a resume, and a state
document, with the load path available only for an agent that advertises
`loadSession` and only as reconstruction.

**What is not established here.** Every claim above is read from the spec
and schema at this pin's commit. The v1.9.1 corpus drives `session/new`
(above), so no frame in it exercises load, resume or list, and this pin's
process gate is the one that ran those frames. The gates are read from the
generated schema and the MUSTs from the prose; whether a given agent honours
them is a property of that agent and not of this pin.

## Reasoning level and compaction at v1.9.1

Recorded for [Decision 0045](../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md).
Read from the protocol at `7e87dc205a7325bd07d0249fd20bb7486ee6ba95` and the
pinned agent, cagent, at `d27c65ce59e6474fb4a57d0fa879fc53f5bf3f1a`.

**The protocol.** A session config option may carry the category
`thought_level`, a "thought/reasoning level selector"
(`schema/v1/schema.json`), and `session/set_config_option` changes one on a
live session. The category is a UI hint, and the values are the agent's own.
ACP has no compaction setting.

**cagent.** `SetSessionConfigOption` answers method-not-found
(`pkg/acp/agent.go`), so a level cannot be changed over ACP with this agent.
Its agent config (`pkg/config/latest/types.go`) carries:

- `thinking_budget`: a token count or an effort name;
- `session_compaction`: on/off, default on;
- `compaction_threshold`: the fraction of the context window that triggers
  compaction, in (0, 1], default 0.9.

All three are read from the agent YAML at launch, so with cagent both
settings are fixed when the session opens.
