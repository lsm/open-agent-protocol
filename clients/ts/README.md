# oap-client (TypeScript)

The TypeScript client for a local `goap serve` daemon: drives any OAP adapter
over the daemon's HTTP + SSE surface with verbatim schema/v0.1 envelopes. It
is the counterpart of the Go `client` package — same wire surface, same
semantics — so non-Go hosts (HyperNeo and friends) can consume the exact
envelope stream the Go client sees.

- **Zero-dependency runtime**: platform `fetch` and streams only; Node 18+
  baseline. `typescript` and `@types/node` are dev dependencies, nothing
  ships.
- The client never logs, carries no credentials, and adds no protocol
  semantics of its own: the daemon is a single-user local service.

## Install

The package is not published to npm — that is intentional (see below). Use it
from a checkout as a `file:` dependency:

```json
{
  "dependencies": {
    "oap-client": "file:../open-agent-protocol/clients/ts"
  }
}
```

(A Git remote cannot select a package subdirectory — npm resolves it at the
repository root, where this repo has no `package.json` — so advertise and use
the `file:` form.)

Prepare the checkout once before consumers install it: run `npm install` in
`clients/ts`, which builds `dist/`. npm does not install a local-path
package's own dependencies, so a `file:` install of an unprepared checkout
cannot build and fails; the `prepare` script therefore skips the build
whenever `dist/` already exists — a prepared checkout installs cleanly
without the dev toolchain, while a Git dependency (whose dev dependencies
npm does install) still builds from scratch. In a bare checkout, `npm install`
prepares the same way, or run `npm run build` directly.

## Canonical lifecycle

```ts
import { dial, EnvelopeType, finalText, payload, type PermissionRequestedPayload } from 'oap-client';

const client = dial('127.0.0.1:6270');          // or 'http://host:port'

// Discovery: the adapter listing and one capability snapshot.
const [adapter] = await client.adapters();
const caps = await client.capabilities(adapter.name);

// One session, subscribed before submitting so the run's first envelope
// cannot be missed.
const session = await client.open('memory', { sessionId: 'demo' });
const events = session.events();                // subscription starts now
await events.ready;                              // …and is live
await session.submit({
  messages: [{ role: 'user', content: 'run the golden script' }],
  delivery: 'auto',
});

// Consume the run, resolving interactive gates as they arrive.
for await (const envelope of events) {
  switch (envelope.type) {
    case EnvelopeType.ActionPermissionRequested: {
      const gate = payload<PermissionRequestedPayload>(envelope);
      await session.resolvePermission({
        interaction_id: gate.interaction_id,
        requested_by: gate.requested_by,
        run_id: gate.run_id,
        choice_id: 'approve',
        granted: true,
      });
      break;
    }
    case EnvelopeType.UserInputRequested:
      // …resolveInput({ interaction_id, requested_by, run_id, answers }) likewise
      break;
    case EnvelopeType.RunCompleted:
      console.log('final:', finalText(envelope));
      break;
  }
}
// The loop ends on its own at the run's terminal event.

await session.close();
```

`session.resolve(...)` dispatches on payload shape (`granted` → permission,
`answers` → user input) if you prefer one method.

## The event stream

`session.events()` returns an async iterable of typed envelopes. By default a
dropped connection is **resumed invisibly**: the client reconnects with the
last observed sequence as the `Last-Event-ID` / `?after=` cursor, the daemon
replays the suffix, and iteration continues without duplicates. A repeated or
skipped sequence within a run, a frame id disagreeing with its envelope, or
an envelope naming another session surface as typed errors — never silent
skips. Iteration ends cleanly (the `for await` finishes) once a run's
terminal event has been delivered.

`dial(addr, { strictResume: true })` turns resume off: a drop raises a
`DisconnectError` carrying the last `(runId, lastSequence)`.

The daemon's terminal transport signals surface as typed errors carrying the
reconnect cursor:

- `OverflowError` — this connection's bounded buffer fell behind; resume with
  `session.eventsAfter(err.runId, err.lastSequence)`.
- `ReplayGapError` — the requested cursor is no longer retained;
  `oldestAvailable`/`latestAvailable` bound what is, so a consumer that
  accepts the loss resumes at or after `oldestAvailable - 1`.
- `DisconnectError` — a drop that was not resumed (strict mode, or repeated
  empty reconnects).
- `ResumeMismatchError` — `eventsAfter` replayed a different run than the
  cursor belongs to.
- `DuplicateSequenceError` / `SequenceGapError` / `MalformedFrameError` —
  wire defects in the stream's positions or framing.

`session.eventsAfter(runId, after)` is the manual resume path: the run's
envelopes after `after` first, then live events.

Daemon refusals arrive as `ServerError` with the correlated
`error.response` code — `serverCode(err)` reads it, e.g. `unknown_session`,
`run_active`, `session_closed`. Requests and responses stay correlated and
session/run-scoped: a response citing another request or another scope is
rejected client-side as a protocol violation.

## Options

```ts
dial(addr, {
  fetch: myFetch,          // inject the transport (tests, proxies, undici)
  participant: 'user',     // responder identity for gate resolutions
  strictResume: false,     // true: report drops instead of resuming
});
client.open('memory', { sessionId, participant });  // per-session overrides
session.events({ signal: controller.signal });      // AbortSignal ends the stream
```

Dev-mode envelope validation (the Go client's `WithEnvelopeValidation`) is
deliberately absent here: it would need a JSON-Schema library at runtime, and
this client is zero-dependency by contract. The hand-written interfaces are
cross-checked against `schema/v0.1/*.json` by the test suite instead (shape
assertions on every envelope and payload type).

## Tests

```sh
npm ci
npm test
```

- Unit tests run against a scripted fake transport — no server, no sockets:
  the WHATWG parser rules, every typed error, and the resume machinery
  (mid-stream drops, speculative replay-from-start, empty-reconnect bounds,
  run changes under a cursor).
- The schema cross-check compares every payload interface against
  `schema/v0.1`.
- The integration test builds the `goap` binary (`go build ./go/cmd/goap`), boots
  it with the built-in memory adapter on a loopback port, and drives the full
  lifecycle — open → submit → gates → terminal → disconnect/resume — through
  the platform fetch. It skips (not fails) when the go toolchain is missing;
  point `OAP_GO` at a go binary that is not on `PATH`.

## Publishing

npm publishing is intentionally out of scope. The packaging surface is
prepared — `files`, `main`, and `types` entry points point at `dist/src` —
but nothing is published, and no release automation exists. Consumers should
pin a commit of this repository.
