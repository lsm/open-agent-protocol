# Decision 0040: A Session Reopens Through Its Own Binding

Status: accepted 2026-10-05 (the wire, the `session.open.reopen` key and the
validator rules landed in both trees with the `session-reattach` fixtures
(#819); both memory references reopen a session they closed, through either
hub (#820), and both conformance runners refuse a reopen of a session an
endpoint never had (#826). Every pinned adapter answers a reopen: Codex resumes
its bound thread (#827) under the host's sandbox, approval policy and directory
(#895), Claude through `--resume` (#881), ACP through an advertised load
(#884), Pi from its session file (#887), Hermes through `session.resume`,
refusing a session with a restart pending (#890), and OpenCode by attaching to
the server session after its last stored event (#891); DeepSeek advertises
reopen unavailable because its pinned wire reaches no store (#892), and `oapx
serve agent` refuses it (#862). The binding is a host record with a file store
that refuses a torn line (#479), `goap hub` and `oapx hub` write and read the
same file (#893), and a record carries the reasoning level and compaction
policy the open asked for. The session list stays with T8)
Date: 2026-09-28
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `session-reattach` (plan sub-unit T7)
Amends: nothing. Executes the `session-reattach` unit
[Decision 0039](0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md)
staged as T7, and its T7 section, and adds the reopen half to
[Decision 0001](0001-agent-control-v0.1-executable-core.md)'s
"Resume, reconciliation, and replay are separate" without amending it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
"T7. Session reattach"
Owner direction: 2026-09-27 on #446, which fixed the `reopen` member, the reply
and the three refusals. This record answers T7's remaining question — what a
reopen answers when the harness's store is no longer where the binding says —
and settles the binding's shape. Its evidence is the seven reload ledgers, each
read at its own pin under [Decision 0032](0032-go-and-zig-are-peers.md)

## Context

Decision 0039 gives a session an identity that outlives every process and makes
`close` a detach: the harness's process ends and the session is released, while
the harness's own store keeps the conversation. The staged plan's T7 section
asks for the other half — loading that conversation again — and names two
questions it does not answer: how a host records a binding and where, and what
a reopen answers when the store is no longer where the binding says.

Both questions are now answerable from evidence rather than from design, because
every pinned harness' ledger has been re-read at its own pin's commit. What that
reading changed is worth stating first, because it decides the second question:

- **Three of the seven harnesses type their own absence.** Hermes answers `4007
  session not found` from `_resume_locate`; OpenCode's handler raises
  `SessionNotFoundError` off a one-row `select`; the DeepSeek harness's
  persistence layer throws `SessionPersistenceNotFoundError(id)`. Codex answers
  a missing rollout with an invalid-request code rather than a method-not-found
  one. So for those, `unknown_session` is *carried* from the wire.
- **Two cannot be read for a code, and they are not the same case.** pi *can*
  load a session natively — a `switch_session` is its first explicit session
  command, and the adapter sends none — but its discovery answers `null` for an
  id that is not there, so `unknown_session` has to be manufactured rather than
  read. ACP is different again: `session/load` is gated by `loadSession`, so a
  harness that does not advertise it is `unsupported_feature` outright, and one
  that does leaves the unknown-id case to a manufactured `unknown_session`,
  because the specification says nothing about it. Two manufactured codes, two
  different reasons, and neither is the rule.
- **One has a store the adapter cannot reach.** The DeepSeek harness ships a
  real JSONL session backend with a typed absence and a working reload
  (`packages/session/session-persistence-jsonl`), and the pinned SDK wire the Go
  adapter drives has no request that reaches it. That is `unsupported_feature`
  today, for a reason worth writing down: the wire is missing, not the harness.
- **And the store being gone is not a distinguishable event.** Hermes creates a
  missing `state.db` mode `0o600` and applies the schema before answering
  (`_secure_state_db_files(self.db_path, create_main=True)`, then
  `_init_schema`); OpenCode's migration runner does the same for a missing
  database. So a host whose store has been deleted, a host pointed at a
  different home, and a host naming a session that never existed all receive the
  *same* answer from the harness.

## Decisions

### A reopen is a member on the open request, not a new verb

`session.open.request` gains `"reopen": true`. Creating and reopening stay one
request with two meanings, because the host is doing one thing in both cases —
asking for a session — and the difference is whether it has one to load. The
unused `recovery` object already on the request is **not** reused for the
intent: `recovery` describes what happened to a session, and a reopen is a
request about the future, so overloading it would make one member mean both.

### The capability key is `session.open.reopen`, and a reopen is gated like `subscribe`

An endpoint that can reopen advertises `session.open.reopen`, beside
`session.open.subscribe`, and a reopen is one more election on the open
request, judged the way [Decision 0009](0009-compound-open.md) judges
`subscribe`: against a descriptor that does not advertise the key above
`unavailable` it is refused `unsupported_feature` naming the key with reason
`unadvertised`, and against a `degraded` disclosure it is refused
`capability_degraded` unless the request consents through
`allow_degraded_features`. An advertised reopen may still be refused
`unsupported_feature` naming the key, because a binding whose store is gone is
that case, below. `unknown_session` joins the open-level refusals that answer
every election an open carries at once, as `session_exists` already does.

The schema requires `session_id` on a request that sets `reopen: true`, since
there is nothing to reopen without one. The validator checks, in both trees,
that a successful answer to a reopen declares `recovery.recovered: true` and
lists no run under way unless the open also carried a message that admitted
one; either failure is `session_state_mismatch`.

### The reply is the state document, and it declares what the session runs under

A reopen answers `session.open.response`, which is the session's state document,
with `recovery.recovered: true` and the **model and settings the session
actually runs under** — the harness's own recorded configuration, not the
configuration of whatever process is loading it.

That is not a nicety, it is the finding. Codex's `ThreadResumeResponse`
*requires* `approvalPolicy`, `approvalsReviewer`, `cwd`, `model`,
`modelProvider` and `sandbox`, because they are the thread's and not the
resumed process's; the Go adapter decoded all six and dropped every one, so a
thread resumed under a different model or sandbox ran silently under the
process's own settings (#458). OpenCode's session record carries `model
{id, providerID, variant}`, `agent` and `location {directory, workspaceID}` for
the same reason, and Hermes deliberately reports the *session's* last route
rather than the profile default, because reporting the default flipped the
Desktop picker's model on every reload. A reopen that reported the loader's
configuration would be worse than no reopen: the host would believe it knows
what the session is doing.

The runs that ended before the close are not replayed. They are answered with a
replay gap, as T7 already says: this is the conversation half of Decision 0001's
resume, not event replay.

### The three refusals, and each fails closed

- a **create** naming an id the host already holds a binding for is
  `session_exists`;
- a **reopen** with no binding is `unknown_session` — the host is asking to
  reopen something it never opened, which is an absence the host can see;
- a **reopen** the harness cannot load is `unsupported_feature`, and the
  capabilities say so before the host tries. A harness with no native load is in
  this class permanently, which is why the DeepSeek adapter is in it until its
  wire grows a request, and why an ACP harness that does not advertise
  `loadSession` is in it too.

### A store that is gone answers `unsupported_feature`, and the code comes from the binding

A reopen whose binding names a store the harness can no longer honour is
`unsupported_feature`, as [Decision 0039](0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md)
already rules: the host *had* the binding and the harness cannot load what it
points at, which is the second case and not the first. A moved home, a moved
project and a store the operator deleted are all that case.

What the ledgers add is **why the code cannot come from the harness's answer**,
and it is the reason this paragraph exists. Two of them create a missing store
before looking in it — Hermes in `_secure_state_db_files(create_main=True)`
followed by `_init_schema`, OpenCode in its migration runner — and pi's
discovery answers `null` either way; ACP's specification does not reach the
case at all. So for those harnesses **a store-gone reopen is answered exactly as
a session that never existed is**, and the host is the only party that knows
which happened, because the host is the party that holds the binding. The code therefore comes from the binding's
existence, not from the harness's reply: a binding whose harness cannot load is
`unsupported_feature`, and no binding at all is `unknown_session`, whatever the
harness said. That also settles the "retry, it may be a transient mount"
worry, because the host is not told to retry a load it already knows it cannot
do.

**No new refusal code is proposed, and the question is answered rather than
deferred.** A distinct `session_store_gone` would be a fourth code for a
distinction only the host can make, and the host makes it from the binding
already. This record recommends against it, and the ledgers give no case that
needs it.

### The binding is the host's, and it says where the session was opened

A binding is a record the **host** supplies and the library reads and writes
through an interface, not a file format the protocol imposes. It carries: the
OAP session id, the harness id and the pin it was opened under, the harness'
own session id, the working directory and home directory the open ran in, and
the model and settings the open asked for. Two things it never carries: a
credential, and a resolved environment value — both would turn a record of what
was asked into a copy of what was secret.

It is written **atomically** and a torn write is **detected, never read**: a
record that cannot be parsed whole is an absence, not a partial truth, because
a half-written binding that loads would reopen the wrong session. It is
**appended to** — history, not replacement — so a host can see that a session
was reopened under a different home, which is precisely the event the refusal
above cannot distinguish.

The interface ships with a file implementation beside it, and `goap hub` records
a binding at open and reads it at reopen. Where it lives, who writes it, and
whether it is per-user or per-workspace is the host's choice, which is the point:
the protocol decides what a binding must *say*, and the host decides where it
keeps it.

## Consequences

- `session.open.request` gains a member, so the schema, `go/protocol`, the
  validator, the TypeScript client's protocol types and the fixtures move
  together, as CLAUDE.md requires for a schema change.
- The reference implementation is the memory backend: it reopens a session it
  closed, and a parity test compares that against the Zig backend's. A reference
  that cannot reopen cannot referee one that can.
- A reopen on a harness with no native load answers `unsupported_feature` from
  the first release, so the capability is honest from the start rather than
  discovered per deployment. `run.resume` and `run.replay` are unaffected: this
  is the conversation, not the stream.
- The hosts that need a session *list* still wait for T8. This record makes the
  binding exist, which is what a list would be built from, and does not decide
  whether the list is the host's records, the harness', or both.

## What this decision does not admit

- **A second store of our own.** The conversation stays the harness's. OAP does
  not persist a transcript here, and a binding is a pointer, not a copy.
- **A reopen that invents configuration.** If the harness cannot report the
  model and settings it will run under, the reopen reports what it knows and
  says in `recovery` that the configuration is the loader's, rather than
  reporting the loader's as the session's.
- **Cross-harness portability of a binding.** It names a harness and a pin. A
  different harness is a different session, refused `unknown_session` when the
  host has no binding for the id under that harness.
