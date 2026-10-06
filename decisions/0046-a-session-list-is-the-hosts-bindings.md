# Decision 0046: A Session List Is the Host's Bindings

Status: proposed
Date: 2026-10-06
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `session-list` (plan sub-unit T8)
Amends: nothing. Executes the `session-list` unit
[Decision 0039](0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md)
staged after [Decision 0012](0012-persistence-is-not-in-v0.1-core.md) declined
to stage it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Leans on: [Decision 0040](0040-a-session-reopens-through-its-own-binding.md),
whose binding is what this list is read from
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
"T8. Session list"

## Context

Decision 0040 made a session reopenable through a binding the host keeps, and
both hubs now write that binding to one file format (`--bindings`). What a host
still cannot do is ask which sessions it could reopen. The staged plan's T8
section leaves two questions to this record: whether the list is the host's
records, the harness's, or both; and how a long list is paged.

The evidence, from the ledgers:

- **Four harnesses have a native list, and none of the adapters calls it.**
  ACP gates `session/list` on `sessionCapabilities.list`, pages it on an opaque
  `nextCursor`, and says an entry is what the agent claims and must not be
  merged across pages (`research/acp-v1.9.1-mapping.md`). OpenCode serves
  `GET /api/session`, cursor-paged with a default limit of 50
  (`research/opencode-v1.18.32-mapping.md`). Codex's `thread/list` pages on
  `nextCursor` and `backwardsCursor` and filters on `cwd`, `searchTerm`,
  `archived`, source and provider (`research/codex-app-server-0.157.0-mapping.md`).
  Hermes serves `session.list` among its pooled handlers
  (`research/hermes-v2026.8.31-mapping.md`).
- **Three do not.** Claude, Pi and the DeepSeek harness expose no list over the
  wire their adapter drives. Pi keeps its sessions in files the adapter could
  scan, but scanning a harness's private store is not a control wire, and
  Decision 0012 already declined to make OAP read one.
- **A harness's list does not answer the host's question.** It lists the
  harness's sessions, including ones no OAP host opened, and it says nothing
  about which OAP session id each one is bound to. The binding is the only
  record of that, and it is the host's.

## Decisions

### The list is the host's bindings, and only those

`session.list` answers from the host's binding records: one entry per OAP
session id whose latest binding entry is not `refused`. A harness's own list is
**not** merged in. A harness session no binding names is not an OAP session —
Decision 0039 makes a session OAP's, and an OAP session exists only once a host
has opened it — so listing it would invent an identity rather than report one.

An entry reports what the binding says and nothing it would have to ask a
harness for: `session_id`, `adapter`, `harness_version` when recorded,
`state` (`live` when the latest entry is `opened` or `reopened`, `closed` when
it is `closed`), `updated_at_ms` (the latest entry's time), and `model` and
`directory` when recorded. It never carries a native session id, a home
directory, a credential or an environment value: the first two are the host's
pointers into a harness, and the list is read by clients the host may trust less
than it trusts its own file.

A `closed` entry is one a client may try to reopen. Whether the reopen succeeds
is still the harness's answer under Decision 0040; the list makes no promise
that a listed session can be loaded, because a store that has gone is not a
distinguishable event (0040's own finding).

### A list is paged on an opaque cursor, newest first

`session.list.request` takes an optional `cursor` and an optional `limit`
(1 to 100, default 50). `session.list.response` carries `sessions` and, when
more remain, `next_cursor`. Entries are ordered by `updated_at_ms`, newest first,
ties broken by `session_id`. The cursor is opaque and is only valid against the
endpoint that issued it; an unknown or expired one is refused `invalid_cursor`.
Every native list in the evidence pages this way, so a host that later fronts
one needs no second shape.

A page is a snapshot of the records when it was read, and a client does not
merge entries across pages, for the reason ACP gives: a session may change
between two reads, and the later page is not a correction of the earlier one.

### The capability key is `session.list`, advertised by the host

An endpoint that keeps bindings advertises `session.list`. One that keeps none
— a host with no binding store, like `goap hub` without `--bindings` — answers
`unsupported_feature` naming the key, with reason `unadvertised`. The key is the
host's, not an adapter's: no adapter descriptor advertises it, because no
adapter holds the records it reads.

## Consequences

- `session.list.request` and `session.list.response` join the schema, with
  `go/protocol`, the validator, a fixture per judgement and
  `clients/ts/src/protocol.ts` moving together, as a schema change requires.
- Both hubs answer it from the binding store they already keep. The memory
  reference answers from the in-process records it reopens from, so the parity
  test can compare the two trees.
- The hub's existing `GET /sessions` listing stays what it is — the sessions
  this process holds — because it answers a different question (what is open
  here now) and its clients depend on that.

## What this decision does not admit

- **A harness-sourced list.** Adding the native lists ACP, OpenCode, Codex and
  Hermes serve, for sessions no host opened, is a later decision if a host needs
  one; it would need an identity rule first.
- **A search or a filter beyond paging.** Codex's list filters on a directory,
  a search term and more, but the others filter on a directory at most, and a
  host can filter a page itself. A filter joins when a host needs one the page
  cannot answer.
- **A transcript or a preview in an entry.** The conversation is the harness's
  (Decision 0040), and an entry is a pointer.

## Open questions for review

1. Should `state` include the `refused` entries a host recorded, so a client can
   see a reopen that failed, or is omitting them right?
2. Is 100 the right ceiling for `limit`? It matches OpenCode's history page and
   is twice its list default.
3. Should the hub expose this as a new route (`GET /bindings`) or widen
   `GET /sessions` behind a query parameter?
