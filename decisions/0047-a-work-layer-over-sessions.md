# Decision 0047: A Work Profile Over Sessions

Status: accepted 2026-10-07 (the owner answered its open questions on
2026-10-06, see "Owner answers"; what shipped, and where it departs from the
decisions below, is "What shipped")
Date: 2026-10-06
Protocol: `open-agent-protocol` version `0.1`
Profile: a new `open-agent-protocol.work` profile over
`open-agent-protocol.agent-control-core`
Leans on: [Decision 0039](0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md),
[Decision 0040](0040-a-session-reopens-through-its-own-binding.md) and
[Decision 0046](0046-a-session-list-is-the-hosts-bindings.md), whose session
list this record widens
Amends: [the hub draft](../drafts/hub.md), whose name it retires

## Context

HyperNeo now drives Codex Desktop and Claude Code Desktop through its own
drivers (`packages/daemon/src/lib/drivers` in lsm/HyperNeo, #5589 to #5681).
Its interface has five verbs: `find`, `start`, `send`, `status` and `stop`.
Each piece of work reports one of six statuses. Its design doc names OAP as the
adapter it wants later, and says why OAP is not that adapter yet: OAP cannot
see or drive a session it did not open, and nothing it opens shows in the
desktop apps.

The core already does most of what the five verbs need:

| Verb | What the core has |
| --- | --- |
| `start` | open, then submit. When this record was proposed, the Zig hub refused a message carried on the open (hub draft, D11); #914 closed that. |
| `send` | submit, steer ([0013](0013-steer.md)) and queue ([0007](0007-queue-delivery.md)) |
| `stop` | cancel, on every adapter but DeepSeek's, whose wire has no cancel request |
| `status` | `session.state` and the event stream, with no summary a caller can read in one call |
| `list` (HyperNeo's `find`) | only the sessions a host opened (0046); none a harness holds |

Session bindings already outlive a restart in both hubs (`--session-history`, first `--bindings`, #893).

### What a Claude Code session is, probed on 2026-10-06

Claude Code CLI 2.1.289 and the Claude desktop app with its bundled CLI
2.1.288, on macOS:

- **The app runs the CLI.** Each desktop session is a child process of the
  app: the CLI copy under `~/Library/Application Support/Claude/claude-code/<version>/`,
  run with `--input-format stream-json --output-format stream-json
  --permission-prompt-tool stdio --resume=<id>`. This is the control wire the
  OAP Claude adapters drive (`zig/src/adapter/claude/backend.zig`,
  `go/adapter/claude/adapter.go`), over the process's own pipes.
- **One conversation, two records.** The transcript is the CLI's
  `~/.claude/projects/<dir>/<id>.jsonl`. The app adds a record of its own,
  `claude-code-sessions/<…>/local_<uuid>.json`, holding `cliSessionId`, the
  folder, title, archive flag, permission mode and the app's state. A session
  with no app record does not show in the app; `claude --desktop --resume <id>`
  creates one (HyperNeo verified this on 2026-10-04; not repeated here).
- **Live sessions are registered, not locked.** Every running CLI writes
  `~/.claude/sessions/<pid>.json` (`sessionId`, `entrypoint`, and the app's
  `hostSessionId` when the app runs it). `claude agents --json` reads that
  registry and reports `busy`, `waiting` or `idle`. No lock was observed that
  keeps a second process from resuming the same id.
- **The CLI's own answer to a running session is a copy.** `claude --bg
  --resume <id>` "starts a copy and says so when the session is already
  running" (`claude --help`). A second writer on one transcript forks the
  conversation.
- **No outside process can join a running session's wire.** It is the app's
  pipes. Cross-session `SendMessage` can deliver text into it, which is how
  HyperNeo sends, but that carries no events and cannot answer a permission
  prompt.
- **OAP's adapters open clean sessions.** The Zig adapter passes
  `--setting-sources=` and an empty `--system-prompt`. Both adapters pass
  `--resume` only when reopening a session their own binding names
  (Decision 0040); never for a session the app made. A session the app made
  runs with `--setting-sources=user,project,local` and the app's own prompt.

Two more probes, the same day:

- **A pending Claude prompt can be answered by a `PermissionRequest` hook.**
  With a hook in `--settings`, the hook ran when the session asked to `Write`,
  returned `allow`, the host received `control_cancel_request` for its pending
  `can_use_tool`, and the tool ran. Without a hook, nothing outside the
  session's stdin answers it: `claude agents --json` says
  `waitingFor: "permission prompt"` and no more, and a cross-session message
  is only enqueued (filed as anthropics/claude-code#99964).
- **Codex has a shared app-server, but it is not the desktop app's.** The
  ChatGPT app runs its own `codex app-server` child over stdio. Separately,
  Codex keeps a managed daemon (`codex app-server --listen unix://
  --managed-daemon`, 0.159.2 here) behind
  `~/.codex/app-server-control/app-server-control.sock`, which speaks
  JSON-RPC over a WebSocket; this is the socket HyperNeo's driver uses. A
  client there gets the full wire: `thread/start` and `turn/start` answered,
  `item/agentMessage/delta` and the other notifications streamed, and
  `item/commandExecution/requestApproval` sent to it as a server request. The
  thread lands in `~/.codex/state_5.sqlite` with source `vscode`, the source
  the app's own threads carry. Whether the app shows it live, and what
  happens when the app opens a thread the daemon holds, was not observed.

## Decisions

### 1. A work profile, built on the core

`open-agent-protocol.work` defines the verbs over core operations (five here,
and `work.read` added with [the draft](../drafts/work.md)), so an
endpoint gains it without new run machinery:

- `work.list {directory?, adapters?, include_closed?, limit?, cursor?}`:
  groups by directory, newest first, the host's sessions (0046) and the
  sessions each adapter can list natively (decision 2).
- `work.start {adapter, directory, title, message}`: open with the message.
  This is D11: the Zig hub admits a message at open, as Go does.
- `work.send {ref, message}`: submit, or queue while a run is active.
- `work.status {ref}`: one of six statuses and the last reply.
- `work.stop {ref}`: cancel. Where the adapter does not advertise `run.cancel`
  (DeepSeek), it is refused `unsupported_feature` naming `run.cancel`, as
  cancel already is.

The six statuses are a summary of core state, not new state:

| Status | From |
| --- | --- |
| `queued` | admitted, no run started |
| `running` | a run is active |
| `needs_you` | a permission or input request is pending |
| `done` | the last run completed |
| `failed` | the last run failed |
| `stopped` | the last run was cancelled |

### 2. A harness's own sessions can be listed

This widens 0046, which leaves native lists out until it has an identity rule.
The rule: a native session no binding names is listed with its native id and
**no** OAP session id. It gets an OAP id only when a host adopts it (decision
3), and the adoption writes the binding. Both of 0046's rules stand. The list
never invents an identity. And `session.list` still never carries a native id
or a home directory: native entries appear only in `work.list`, which must
carry the native id because adopting needs it, so an endpoint serves
`work.list` only to a caller it trusts with the harness's own pointers, as it
trusts its binding file.

Sources, by harness: Codex `thread/list`, ACP `session/list` when advertised,
OpenCode `GET /api/session`, Hermes `session.list`. Claude and Pi have no list
on their wire; their adapters may read the harness's own store read-only
(Claude: the app's session records and `claude agents --json`). That reverses
0012's refusal to read a private store, for listing only, and only for these
two. The DeepSeek harness ships a store (`session-persistence-jsonl`, which
can enumerate), but a deployment mounts it only by composition, under a `root`
the adapter is not told, and its SDK wire cannot reach it
(`research/deepseek-harness-dsh-v0.1.7-rc.2-mapping.md`). Its adapter lists
nothing native until a deployment says where that store is.

### 3. A native session can be adopted, in one of four ways

An open naming a native id adopts it. The adapter answers which way it can:

| Way | When | What the host gets |
| --- | --- | --- |
| `attach` | the harness serves a socket other clients can join | the full core: events, prompts, cancel, in a process other clients share |
| `resume` | no process runs the session | the full core, in a process the adapter owns, via the harness's own resume (for Claude, 0040's `--resume`, given a native id instead of a binding) |
| `observe` | another process runs it, there is no socket to join, and the harness has a prompt hook | events read from the harness's transcript, prompts answered through the hook; no submit, no cancel |
| refused `session_running_elsewhere` | another process runs it and none of the above applies | nothing; the caller relays outside OAP |

Codex is `attach` through the managed daemon's socket. Claude Code is
`resume` when `claude agents --json` does not list the session, and `observe`
when it does, because a second writer forks the conversation: progress comes
from tailing `~/.claude/projects/<dir>/<id>.jsonl`, and a prompt reaches the
host through a `PermissionRequest` hook the user installed, which asks the
endpoint and falls back to the app's own prompt when no answer comes. A hook
must be in the session's settings before it starts, so `observe` without one
answers no prompts.

The hook is `oapx claude-permission-hook --endpoint <url>` (#911). It posts
the hook input (`session_id`, `transcript_path`, `cwd`, `tool_name`,
`tool_input`, `permission_mode` and the rest Claude sends) to `<url>`; a
`200` carrying `{"behavior":"allow"}` or `{"behavior":"deny","message":…}`
answers the prompt, and anything else, or no answer, leaves it with the app.
Probed: the app's prompt is raised at the same moment the hook runs, and
whichever answers first wins, so a slow or absent endpoint never blocks the
user. Codex's `attach` is `oapx codex-bridge` in place of `codex app-server`
(#910). An adopting Claude adapter resumes with the settings
the app recorded for the session, not with `--setting-sources=`.

OAP does not carry a relay. A relayed message has no run, no events and no
prompts, so it is not a session.

### 4. `hub` becomes `serve`

`oapx hub` and `goap hub` become `oapx serve` and `goap serve`; the
single-session `oapx serve agent` keeps its name. `drafts/hub.md` becomes
`drafts/serve.md`. The work profile ships as `oapx work`. The old command
stays as an alias for one release.

## Consequences

- Each decision above graduates as its own unit under
  [Decision 0003](0003-staged-unit-graduation.md); the rename is mechanical
  and can land first.
- HyperNeo can put an `oap` adapter behind its `WorkAdapter` for Codex and for
  the harnesses with no desktop app, and keep its relay for a Claude session
  the app is running.

## Owner answers, 2026-10-06

1. **The work layer is in this repository, as its own profile.**
   `open-agent-protocol.work` sits over `agent-control-core` the way
   `presentation-control` does. It is not an affordance: in this repository an
   affordance is presentation state, what a surface may do
   ([0036](0036-a-presentation-layer-is-not-evidence-for-its-own-profile.md),
   [0037](0037-presentation-state-is-versioned-and-every-intent-is-idempotent.md)),
   and the work verbs are operations a caller runs. Each verb is a capability
   key an endpoint advertises (`work.list`, `work.start`, `work.send`,
   `work.status`, `work.stop`, `work.read`), so a missing one, such as `work.stop` over
   DeepSeek, is known before it is called.
2. **Reading Claude's and Pi's own stores for listing is accepted, read-only.**
   A store whose format changed lists nothing; it is never written.
3. **An adopting Claude adapter does not write the app's session record.** A
   caller that wants the session in the app uses `claude --desktop --resume`.
4. **The name is `serve`.** It landed in #913, with `hub` as an alias for one
   release.
   The work verbs are served by `serve` itself, next to its own operations
   ([work](../drafts/work.md)), not by a separate `oapx work` command as
   decision 4 first said.
5. **The listing verb is `work.list`, not `work.find`** (owner, 2026-10-06).
   It lists and filters; it does not search. Search, by text or meaning, is
   the caller's, built on its own index over what OAP hands it.

## What shipped

Recorded 2026-10-07. Where this list and a decision above disagree, this list
is what the code does, and [the work draft](../drafts/work.md) is its
specification.

- **The verbs.** `serve` answers `work.list`, `work.status`, `work.start`,
  `work.send`, `work.stop` and `work.read` over both transports (#919), and
  `work.capabilities`, which names each adapter's verbs so a missing one is
  known before it is called (#942): a verb is withdrawn when the feature it
  rests on is undeclared or `unavailable`, and an adapter that cannot be
  probed is listed as unavailable with its reason. Owner answer 1 asked for
  each verb to be a capability key; it is answered per adapter by
  `work.capabilities` rather than in the core descriptor. `work.start` takes
  any absolute directory through an `any_directory` registry entry (#928).
- **Listing (decision 2).** Native entries appear only in `work.list` with
  `include_native`, carrying `native: true`, the native id and no OAP id.
  Shipped: Codex `thread/list` (#919), Claude Code's project transcripts and
  live-session registry read-only (#919), and Pi's session store read-only
  (#944). ACP `session/list` (#950) and Hermes `session.list` (#951) are in
  review. OpenCode waits on its v2 port, whose `GET /api/session` replaces the
  v1 list (the v2 ledger is #949, in review). DeepSeek lists
  nothing native, as decided. A native list runs off the serve loop, so a slow
  harness does not stall other callers (#947, in review).
- **Search is the harness's (decision 5).** `work.list` takes a `search`
  term and hands it to the native lists that take one, Codex `thread/list`'s
  `searchTerm` and OpenCode `GET /api/session`'s `search`, both a title
  substring; the answer is only what they matched, each answered as the held,
  recorded or native work it is. `serve` still matches nothing itself, so held
  and recorded work the harness did not match, and every adapter whose list
  takes no term, are left out. `work.capabilities` names the adapters that
  take one as `native.search`.
- **Reading.** `work.read` reads the harness's own transcript where one is
  readable (Claude Code's project jsonl, Codex `thread/turns/list`, Pi's
  session file) for held and unheld sessions, and otherwise the turns `serve`
  recorded (#929). This verb was added by the draft, not by decision 1.
- **Adoption (decision 3) is one of the four ways, plus refusal.**
  `work.start` with a `native_id` adopts by **`resume`** only: the adapter
  resumes the harness session in a process it owns and writes the binding,
  marked `adopted`. An adopted Claude session runs with the user's own
  settings and Claude Code's own prompt (#919), as decided. Codex adoption is
  also a resume, in the adapter's own app-server process; `oapx codex-bridge`
  (#910) exists, but adoption does not `attach` through it.
- **No `observe`.** Nothing tails a transcript a foreign process is writing.
  The prompt hook (`oapx claude-permission-hook`, #911) shipped and answers
  prompts for any session it is installed in, but no verb builds a read-only
  session from it.
- **The refusal is `run_active`, not `session_running_elsewhere`.** A native
  session its harness lists as running is refused `run_active`, the core's
  existing code for a session that is busy; no new code was added.
- **Go serves the profile too** (#943, in review), through one implementation
  both of its transports call, compared byte for byte against `oapx` on the
  stdio differential. Its adapters list and read no native sessions yet.
- **The rename (decision 4)** landed for the commands (#913), not for the
  draft: `drafts/hub.md` keeps its name rather than becoming
  `drafts/serve.md`. The work verbs are served by `serve` itself, per owner
  answer 4, not by a separate command.
