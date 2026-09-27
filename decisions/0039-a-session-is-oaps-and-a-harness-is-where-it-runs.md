# Decision 0039: A Session Is OAP's, and a Harness Is Where It Runs

Status: proposed
Date: 2026-09-27
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md), whose
"no cross-process persistence" gains an exception for reattaching a session;
[Decision 0009](0009-compound-open.md), whose open naming an existing session
is refused rather than reopened; [Decision 0012](0012-persistence-is-not-in-v0.1-core.md),
whose staging list gains two units and whose `transcript-load` gains a
constraint; and [the hub draft](../drafts/hub.md), whose closed session stays
listed
Follows: [Decision 0015](0015-evidence-from-implementations-we-do-not-control.md)
for the evidence each unit needs

## Context

Closing a session in either hub ends its harness process and then keeps an
entry for it. `go/serve` leaves the closed session in its table, listed with
its final state; the Zig core keeps the entry, its journal and a cursor for
every run it had. The entry answers nothing a client can use: it cannot run,
it cannot be subscribed to, and it holds its id, so opening a session under
that id is refused `session_exists`. Nothing ever removes it. #408 proposed
bounding how many a hub keeps. The better question is why a hub keeps any.

Close destroys nothing the harness holds. Every pinned harness with a store
keeps the conversation after its process ends, and most can load it again:

| Harness | What it keeps, and how it loads it again | Recorded in |
| --- | --- | --- |
| Claude Code | `persistSession` writes `~/.claude/projects/<dir>/<sessionId>.jsonl`; `--resume=<uuid>` makes a new process load the conversation | [2.1.263 ledger](../research/claude-code-agent-sdk-2.1.263-mapping.md), "Session state and recovery" |
| Codex app-server | `thread/resume` restores the native conversation; the Go adapter already selects it through `Config.ResumeThreadID` | [codex ledger](../research/codex-app-server-8d7cc24-mapping.md) |
| ACP agents | `session/load`, gated by `loadSession`, restores a session and replays its conversation through `session/update`; `session/resume` restores without replay; list, close and delete are separately optional | [v1.7.0 ledger](../research/acp-v1.7.0-mapping.md) |
| Pi | a durable session file backs `sessionId`; `switch_session` loads another; `get_entries` with `since` reads the persisted tree | [v0.87.1 ledger](../research/pi-v0.87.1-mapping.md) |
| Hermes | a native `session.resume` exists, and the adapter refuses a native session id rather than writing it | [v2026.8.31 ledger](../research/hermes-v2026.8.31-mapping.md), "Corpus" |
| OpenCode | a session is a server record (`POST /api/session`), outliving the adapter's connection; its reload and list routes are not yet recorded | [v1.18.32 ledger](../research/opencode-v1.18.32-mapping.md) |
| DeepSeek harness | nothing recorded | — |

Decision 0001 defines resume as restoring "an attachment to adapter-owned
execution or conversation state". Only the execution half exists on the wire:
a host reattaches to a session still running through `events` and its cursor,
or reads a recovered open response for a session already under way. A session
whose process has ended cannot be reached again, which is why close has to
look destructive and why a restart "ends every session".

Decision 0012 retired `+persistence` because its vocabulary existed only on
paper, and staged `transcript-load`. It asked whether OAP stores transcripts.
The question a client asks first is how to get back to a session, and 0012
itself names reattachment as core — which nothing built.

The owner intends a native OAP persistence layer, in the protocol and in both
implementations, so a harness can come and go and continuing a session on
another harness, provider or model is an ordinary operation. This record is
the first step toward it and must not become a dead end: it gives a session an
identity of its own now, and leaves the store of record free to move from the
harness to OAP later.

## Decisions

### A session's identity is OAP's, and it outlives every process

An OAP session id names a conversation. It is not a harness process, an
endpoint process or a native session.

The harness and native session a session runs on are its **binding**: the
adapter, the native session id, the working directory and the configuration
entry it was opened with. A binding is a fact a host records about a session,
never an OAP identity; a native id stays in a namespaced extension, as every
foreign identifier already does. A session has one binding at a time, and the
host records bindings as a history, so a later rebinding appends rather than
overwrites.

### Close detaches

Close ends the binding's process, ends the session's subscriptions with a
`session_closed` ending, and releases everything the endpoint held in memory
for the session: its journal, cursors, holds and state. It keeps the binding
record and nothing else.

A session that is not open is not listed among live sessions, and an
operation addressed to it is refused `unknown_session` until it is reopened.
Close is still refused while a run is active, so a run is never cut off by
close; the host cancels first.

A restart is a close of every session. Harness processes end, and every
session reopens on its next open.

### An open that asks to reopen a session reattaches it

Creating a session and reopening one are different requests, and the request
says which. Each fails closed:

- A create naming an id the host has bound is refused `session_exists`, as
  Decision 0009 has it.
- A reopen naming a session the host has no binding for is refused
  `unknown_session`, and a reopen whose harness cannot load the session is
  refused `unsupported_feature`. Neither is ever answered with a fresh, empty
  session.

The member carrying the intent is chosen when the unit graduates. The first
candidate is the `recovery` object `session.open.request` already carries,
which no rule gives a meaning today.

A reopen is for a session with no live process. A session still open on the
endpoint is reattached through `events` and its cursor, as Decision 0009
says; a reopen naming it is refused `session_exists`.

The endpoint loads the conversation through the binding's native mechanism
and answers with the session's state document, declaring
`recovery.recovered: true`. No run is under way — close was refused while one
was — so the document lists none. The conversation comes back, as the harness
holds it. The OAP events of earlier runs do not: reattach is resume, not
replay, and a cursor from before the close is answered with a replay gap.

A harness that cannot load a session declines with `unsupported_feature`, and
its capabilities say so, as they report every other effective fidelity.

### Persistence is three units, and the harness is the store until OAP has one

Each unit is staged under [Decision 0003](0003-staged-unit-graduation.md) and
graduates through its ordinary gate.

1. **`session-reattach`** is the reopen above. Its evidence is the table in
   the context: Claude Code, Codex, ACP and Pi load a session natively today.
2. **`transcript-load`**, staged by Decision 0012, keeps that record's four
   questions and gains one constraint: its entries and its cursor are OAP's
   vocabulary, not a harness's rows. That answers 0012's first question in
   part — the cursor is OAP's opaque string, not a harness entry id — and it
   is what lets the same read serve a session whose store is later OAP's own.
   Its evidence is Pi's `get_entries` and ACP's replay through
   `session/update`.
3. **`session-list`** lists the sessions a client could reopen. Decision 0012
   declined to stage it because no pinned harness exposed a list over its
   control wire. A host's binding records now say what it can reopen, and the
   ACP ledger records an optional native list; that is enough to stage it,
   not to decide it.

`transcript.delta` stays out, on 0012's evidence.

### OAP becomes the store of record later, and this record leaves room for it

Today the harness's store is the store of record, and OAP holds only the
binding. The persistence decision that follows gives OAP a native store: a
session's canonical transcript in OAP's vocabulary, held by the endpoint and
independent of any harness. When it lands:

- a harness's native store becomes a projection of the session, which OAP can
  rebuild;
- continuing a session on another harness becomes seeding that harness from
  OAP's transcript and appending a binding;
- switching the provider or the model already has its core operations under
  [Decision 0028](0028-live-model-and-provider-control.md), and needs nothing
  more from persistence.

Until then, rebinding a session to another harness is refused
`unsupported_feature`. It is named here so the later decision turns a refusal
into an operation rather than inventing one. Everything this record adds is
shaped for that decision: the identity is OAP's, bindings are a history, and a
transcript entry is OAP's vocabulary.

## Consequences

- **The hub draft changes.** Close releases the session; the rule that a
  closed session stays listed with its final state goes; `sessions` lists live
  sessions. D2 stops being a divergence, because the Zig contract's
  destructive close is now the specified shape, and #408 is superseded. The
  trust model's "a restart ends every session" becomes "a restart ends every
  harness process; sessions reopen".
- **A host keeps binding records, and only those.** `oapx` keeps them in its
  state directory. The Go library takes them from an interface the embedding
  program supplies, since Decision 0038 makes it a library a program links.
  A record holds ids, adapter names, native ids, working directories and
  configuration entry names — never a credential and never a resolved
  environment value.
- **A reopen depends on the harness finding its store again.** The harness
  needs the same home, configuration directory and working directory it had.
  A host that isolates a harness's home per run opts out of reopening that
  session, and that is the host's call.
- **Each adapter accepts a binding at open.** Codex's `ResumeThreadID` is the
  pattern: an adapter told a native session loads it instead of creating one.
  An adapter that cannot declines as above.
- **Decision 0001's sentence gains one exception.** A session may be
  reattached across processes; replay stays bounded to one process's journal,
  and no durable admission is claimed.
- **Order.** `session-reattach` first: it removes the zombie entries and has
  the most evidence. Then `transcript-load`, since a reopened client needs the
  history to show. Then `session-list`.

## What this decision does not admit

- That OAP stores transcripts today. It stores bindings.
- That a reopen replays events. Replay stays within one process's journal.
- That a session moves to another harness now. That waits for the native
  store's decision.
- That close may cut off a run.
- That a binding, or any native id, is an OAP identity.
- `transcript.delta`.
