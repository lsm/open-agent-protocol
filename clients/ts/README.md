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

## API reference

The exported surface, with the documentation that lives on it in `src/`. `protocol` is the
wire model the client mirrors from `schema/v0.1`; the rest is the client surface built on it.
Nine of the eleven documented `errors` exports are covered under
[the event stream](#the-event-stream), where the resume rules that give them meaning are set out;
the two that are not appear under `errors` below.

### `protocol`

- **`PROTOCOL`** — The Open Agent Protocol agent-control-core wire model,
  hand-written to mirror schema/v0.1 exactly. Every envelope
  travels verbatim: the client adds no fields of its own and
  ignores none.
- **`EnvelopeType`** — Envelope type constants and their string union. The union
  stays open (`string & {}`): the protocol adds envelope
  types without a client release, and the event stream must
  deliver unknown types unhindered.
- **`Envelope`** — One protocol envelope; `payload` is decoded JSON, still
  untyped.
- **`parseEnvelope`** — Decodes one envelope from a JSON document (an SSE data
  field or a response body).
- **`payload`** — Types one envelope's payload. The cast is deliberate: payloads
  are trusted schema/v0.1 documents on this single-user wire,
  and runtime JSON-Schema validation is out of scope for the
  zero-dependency client.
- **`JSONValue`** — A JSON value: what the wire's JSON-typed fields carry.
  `undefined` is excluded deliberately — JSON.stringify
  silently drops it, which would turn a required field into a
  schema violation on the wire — and so are the other
  non-serializable values.
- **`UrlImage`** — An image carried by URL alone.
- **`InlineImage`** — An image carried as inline data with its media type.
- **`ImageContent`** — An image part: exactly one of a URL, or inline data with
  its media type.
- **`MessageContent`** — Message content is either a plain string or a non-empty
  list of parts.
- **`ToolSourceKind`** — Where a tool source's tools are executed from. An MCP
  source is a `process` or `remote` kind whose `protocol`
  is `mcp`.
- **`ToolSourceDescriptor`** — The published shape of one tool source: what
  `action.tools.list.response`, `session.state`,
  and the capability descriptor report back to
  clients.
It carries no `command`, `args`, or `environment` — those belong to
  `ToolSourceAttachment`, the open-time shape — because an attachment's
  environment can hold a literal credential and one shape serving both would
  make a leak into a published catalog valid.
- **`EmptyRequestPayload`** — An empty request payload: the schema's
  empty-object defs allow no fields
  (additionalProperties: false).
- **`CapabilityDescriptor`** — One adapter's capability snapshot; every envelope
  the adapter emits repeats the revision.
- **`CapabilityLimits`** — The admission bounds a descriptor discloses.
  `max_active_runs_per_session` bounds the nonterminal
  set — the started run plus every queued reservation,
  which is what `session.state.active_runs` lists — and
  `max_queued_runs_per_session` bounds the queued
  subset. Both are at least 1 on the wire; an endpoint
  that cannot queue advertises
  `session.message.delivery.queue` as `unavailable`
  rather than disclosing a bound of zero.
- **`ModelsRequest`** — Asks one session for its effective model catalog.
`allow_degraded_features` is the same per-request opt-in the submit request
  carries: an endpoint exposing `models.list` as `degraded` would otherwise
  have to refuse every query or serve degraded behaviour without consent.
- **`ModelDescriptor`** — One model a session can run. `id` is the value
  `model_id` accepts and is unique within a response.
- **`ProviderDescriptor`** — What a `ModelDescriptor.provider_id` resolves to.
  `id` is opaque and endpoint-scoped.
- **`ModelEventPosition`** — A model-affecting run event or a session model
  switch.
- **`ModelsResponse`** — The effective catalog for one session.
- **`ToolSourceAttachment`** — The open-time shape of one tool source: the
  descriptor's published members plus, for a
  `process` source, the attachment-only `command`,
  `args`, and `environment`. `environment` takes
  the registry's allowlist form — a bare `NAME`
  forwards the endpoint's own value, `NAME=value`
  passes literally.
The daemon's client-facing route accepts neither `command`, `args`, nor a
  literal `NAME=value`: a `process` attachment names an operator-configured
  source by `id` only and the daemon fills the rest from its own registry.
- **`OpenMessage`** — A first submission carried by an open: the submit request
  without `session_id`, which an open names or mints itself
  and which a host proposing no id could not fill in. Stated
  as an omission rather than a copied member list, so a
  control added to a submit is available at open by
  construction.
- **`ActiveRunRelationship`** — The only relationship an `active_runs` entry
  carries in this phase.
- **`ActiveRun`** — One nonterminal run of a session. A queued reservation
  carries its 1-based `queue_position`; the started run
  carries none.
`as_of_sequence` is the last sequence of this run the entry reflects, and an
  entry carrying `pending_interactions` must carry it: a state read is not
  serialized with lifecycle publication, so the position is what makes an
  accurate-but-stale pending set judgeable rather than guessed at.
- **`RunPosition`** — A stated position in a run's sequence domain; `run_id:
  null` with `sequence: 0` is the genesis position, before
  the session's first model-affecting event.
- **`SettledRun`** — One run a snapshot has already removed, with the sequence
  its terminal carries.
- **`SessionCapture`** — The session-level capture position of a state snapshot
  (queue unit).
- **`ToolChoicePolicy`** — The typed tool-selection policy `tool_choice`
  carries. The wire keeps `tool_choice` permissive, so
  this shape is enforced by the validator's
  run-controls rules and by adapters rather than by the
  schema.
The policy is a filter over the advertised catalog and nothing else. Exactly
  one of `allowed` or `disallowed` is present, and an empty `allowed` admits
  no tool at all.
- **`SettledBy`** — How the endpoint learned a run reached its terminal.
  `observed` is a run-scoped native terminal the endpoint saw;
  `inferred` is one it concluded from other evidence, such as
  a session-scoped stop or transport loss. Absent asserts
  observation. It is provenance about the endpoint's
  knowledge, not a second status: an inferred terminal is as
  absorbing as an observed one.
- **`ToolsListRequest`** — A catalog request. `session_id` asks for that
  session's effective catalog; an absent one asks for
  the endpoint-level catalog a static adapter serves.
- **`ActionCallAcknowledgeRequest`** — The control participant has begun
  executing a call it owns. An
  acknowledgement is not a resolution: at
  most one, and only before one.
- **`ActionCallResultRequest`** — The control participant's outcome for a call
  it owns.
- **`ActionCallErrorRequest`** — The control participant's failure for a call it
  owns.
- **`ResolveRefusalReason`** — Why a resolution was refused, ranked most
  informative first. One request can satisfy
  several at once and one response carries one
  reason, so the endpoint reports the highest the
  request satisfies: whether the interaction
  exists, then whether this sender may speak for it
  at all, then how far the call has already
  progressed.
- **`ActionCallResolveDetails`** — The closed detail object an
  `already_resolved` refusal carries.
- **`TextQuestion`** — A free-text question: the schema forbids options on it.
- **`ChoiceQuestion`** — A choice question: the schema requires a non-empty
  option list.
- **`InputQuestion`** — One asked question, discriminated by kind: text
  questions carry no options, choice questions carry at
  least one.
- **`TextAnswer`** — A text answer to one question.
- **`SelectedOptionsAnswer`** — A choice answer to one question: one or more
  selected option ids.
- **`InputAnswer`** — One answered question: a text answer or selected option
  ids, exactly one of the two.
- **`SubmittedInputResolved`** — A resolved gate whose answers were submitted:
  at least one, never empty.
- **`CancelledInputResolved`** — A cancelled gate resolution: the schema forbids
  answers on it.
- **`UserInputResolvedPayload`** — The confirmed outcome of one user-input gate,
  exclusive by status.

### `client`

- **`FetchResponse`** — The minimal structural fetch surface the client uses.
  The platform's global fetch satisfies it, so a custom
  transport only has to behave like fetch, not be it.
- **`ByteBody`** — Readable byte-stream pieces the client relies on.
- **`DEFAULT_PARTICIPANT`** — The responder identity the daemon acts as when it
  opens an adapter session, so it is also the
  identity a client resolves interactive gates with.
- **`DialOptions`** — dial() options; see dial.
- **`AdapterInfo`** — One adapter-listing entry. A probing adapter reports its
  capabilities; a failing one reports error.
- **`Capabilities`** — One adapter's capability snapshot; `revision` names the
  descriptor every emitted envelope repeats.
- **`dial`** — Returns a client for the daemon at addr, which may carry a scheme
  ("http://127.0.0.1:6270") or not ("127.0.0.1:6270").
- **`OapClient`** — One daemon connection. Construct with dial.

### `session`

- **`SubmitInput`** — submit() input: the payload session_id is optional and
  filled from the session.
- **`PermissionResolveInput`** — resolvePermission() input: the payload
  session_id and responded_by are optional and
  filled from the session.
- **`UserInputResolveInput`** — resolveInput() input: the payload session_id and
  responded_by are optional and filled from the
  session.
- **`ModelsOptions`** — models() input: the per-query degraded opt-in, absent by
  default.
- **`Catalog`** — One session's model listing together with the capability
  revision that governs it, mirroring the shape capabilities()
  returns.
The revision is not decoration. The catalog is part of the capability
  snapshot, so it is valid for exactly that revision: a caller caches it
  against the revision and discards it when the descriptor moves. The payload
  alone would leave a caller unable to tell which `models.list` promise it
  read, and a later probe may already report a different revision than the one
  the listing came under.
- **`ToolCatalog`** — One session's tool listing together with the capability
  revision that governs it, mirroring Catalog on the models
  route.
A tool catalog needs that pairing more than a model one does, not less. The
  listing is a function of the descriptor and of the sources this session
  attached under it, and an endpoint that republishes its tools mid-session
  changes what it lists without anyone asking; `capabilities.updated` is the
  only signal that a cached listing has stopped describing the session.
  Without the revision a caller cannot tell which snapshot it read, so it
  cannot tell which update invalidates it.
- **`OpenOptions`** — open() input; see OapClient.open.
- **`SessionOpenRequest`** — One session.open request payload, re-exported for
  callers minting custom opens.
- **`finalText`** — Returns the final-response text of a run.completed envelope,
  or null for any other envelope or a non-text final response.

### `events`

- **`EventsOptions`** — events()/eventsAfter() options.
- **`EventStream`** — EventStream is one ordered envelope stream over a session,
  consumed exclusively through `for await`. Iteration ends
  cleanly (the loop simply finishes) once a run's terminal
  event has been delivered; every other error is terminal
  for the stream.

### `errors`

Nine of the eleven documented `errors` exports are covered under
[the event stream](#the-event-stream), where the resume rules that give them meaning are set out.
The other two are not resume errors, so that section cannot describe them:

- **`OapError`** — Base class of every client error, carrying an optional cause.
- **`AbortedError`** — The operation was cancelled through its AbortSignal (the
  counterpart of Go's context.Canceled).

### `sse`

- **`SSEFrame`** — One dispatched frame: a blank line's worth of accumulated
  field lines.
- **`SSEParser`** — SSEParser consumes the event stream incrementally: push byte
  chunks as they arrive and take the frames each one
  completed. Push must receive the stream's bytes in order;
  multi-byte UTF-8 sequences split across chunks are decoded
  correctly. Call finish() at end of stream — an unterminated
  trailing frame is discarded, never dispatched.

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
