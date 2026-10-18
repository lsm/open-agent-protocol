# Storage Interface

Status: accepted 2026-10-18 (owner). No protocol change.

OAP's durable state is written by two hard-wired file stores today:

- **Bindings.** `zig/src/hub/binding.zig` `Store`: one JSONL file
  (`~/.oapx/sessions.jsonl` by default), a CRC-32 per line, an exclusive
  `<path>.lock`, and an atomic rewrite on every append. Go already has the
  interface, `go/binding.Store` (`Append`, `Latest`, `History`, `Sessions`),
  with a file and a memory implementation.
- **`oapx` sessions.** `zig/src/tui/session_store.zig` `Store`: per session an
  events file `<id>.jsonl`, a `.stream.jsonl` of chunks, an index written by
  atomic replace, and `compaction-<n>.jsonl` transcripts. The `oapx` adapter
  reaches it through three callbacks: `HistoryLoader` (`load`, `read`),
  `TranscriptStore` (`save`) and `Recorder` (`record`).

This makes both one interface per tree, so a host can supply SQLite or a
service. JSONL stays the default implementation, and its file format does not
change.

## Operations

One `Storage` value with four groups. Each tree spells it in its own idiom: a
Go interface, a Zig vtable struct like `HistoryLoader`.

### Bindings (Decision 0040)

| operation | does |
| --- | --- |
| `appendBinding(entry)` | appends one opened, reopened, closed or refused entry |
| `latestBinding(session_id)` | the newest entry for a session, or none |
| `bindingHistory(session_id)` | every entry for a session, oldest first |
| `listSessions(cursor?, limit)` | the latest entry per session, newest first, one page with `next_cursor` |

A backend's session listing may return raw rows: the interface reduces them to
the latest non-refused entry per session, in first-seen order, so a backend
need not. `lastFailure()` is the error of the most recent failed append,
cleared by the next successful one; a reopen that finds no binding cites it, so
a lost write does not read as `unknown_session`.

`listSessions` moves the paging that `binding.list` and `go/binding/page.go`
do over a whole file into the backend, so SQLite can page with an index.

### Runs (Decision 0050, #1204)

A queued run that a shutdown holds, or a crash leaves, keeps no `settled`
record until the next start re-delivers or withdraws it. Re-delivery is one
append, `admitted {run_id: new, request, replaces: old}`, and `openRuns` drops
the run an `admitted` replaces, so a crash at any point leaves exactly one of
the two open. A replacing run takes the admission position of the run it
replaces, so a crash part-way through re-delivering several prompts never
changes their order. Withdrawal names the root of a `replaces` chain, the id
the host was told, and appends `settled` for that chain's open run.

| operation | does |
| --- | --- |
| `appendRun(session_id, record)` | appends `admitted` (with the prompt when queued, and `replaces` naming the held run a re-delivery takes over; the store stamps that run's `root` on it), `started` or `settled` |
| `openRuns(session_id)` | runs with no `settled` record and no later `admitted` replacing them, in admission order; a re-delivered run takes the slot of the run it replaces; each run reports its `root`, the first run id that carried its prompt |

A run that is settled or replaced is over: a writer appends nothing more for
its id, and a record for it that a log still holds after its end is ignored
rather than reopening it. A store may forget an ended run once it compacts,
so the writer's rule is the guarantee.

### Transcript (Decision 0051, #1205)

| operation | does |
| --- | --- |
| `appendEntries(session_id, entries)` | appends transcript entries in order |
| `readEntries(session_id, cursor?, limit)` | one page, the next cursor, and a gap when the cursor is gone |
| `saveCompaction(session_id, index, messages)` | keeps a compacted history, as `TranscriptStore.save` does |
| `loadHistory(session_id)` | the messages a reopened `oapx` session resumes with, as `HistoryLoader.load` does |

`Recorder.record` becomes a caller of `appendEntries`, and
`HistoryLoader.read` becomes `readEntries`.

### Lifecycle

`open(config)` takes the lock and returns the value, and `close()` releases
it. A backend reports a held lock as `SessionHistoryInUse` (Zig) or its Go
twin, so both hubs refuse a second writer the way they do today.

## What a backend must guarantee

- **One writer.** One process writes a store at a time. JSONL holds
  `<path>.lock`, and SQLite would hold its own lock or an equivalent. A second
  `open` fails at once and never waits.
- **Atomic append.** An append is all there or not there. A torn entry is never
  read as an entry: it is an absence, and a reopen that meets one refuses
  rather than reading past it, as 0040 requires.
- **Order.** Entries of one session are read in the order they were appended.
  Across sessions, only `listSessions`'s order is promised.
- **Durability before the answer.** An append returns only once the entry
  survives a crash. Decision 0050 depends on this: an accepted submit is
  answered only after its `admitted` record is durable. The Go file store
  meets this (`go/binding/file.go` syncs). Neither Zig store does today:
  `compat.fs.atomicReplace` writes and renames without a sync, and
  `session_store` appends and closes. Landing step 2 adds the syncs.
- **Stable cursors.** A cursor stays valid for as long as its entry exists,
  and a backend that loses one reports the gap rather than restarting the read.
- **No secrets.** A backend never receives a credential or a resolved
  environment value. Nothing above passes one.

Callers make all writes from one thread per store. A backend need not be safe
for concurrent writers, and must be safe for one writer with concurrent
readers, because `work.read` reads off the hub's loop today, and the
planned `transcript.load` would too.

## Where it lands

1. Zig: a `Storage` vtable over the existing `binding.Store` and
   `session_store.Store`, the hub and the `oapx` adapter taking it instead of
   the concrete types, with no behaviour change.
2. Zig: make the JSONL backend durable: sync the staged file before the
   rename and the directory after it in `atomicReplace`, and sync after each
   `session_store` append. This must land before Decision 0050's run records
   rely on it.
3. Go: `go/binding.Store` grows the run and transcript groups, and the file
   implementation the paging.
4. The run and transcript groups fill in as Decisions 0050 and 0051 land.
5. A conformance test per tree. Every backend runs order and cursor gap. Lock
   refusal and torn entry apply only to a backend that locks and writes bytes,
   which today is JSONL; Go's memory store runs the first two only. Zig has no
   memory binding store, so its suite runs against JSONL alone.
