/**
 * The TypeScript OAP client: drives a local `goap serve` daemon over its
 * HTTP + SSE surface with verbatim schema/v0.1 envelopes. Zero-dependency
 * runtime (platform fetch + streams, Node 18+ baseline); the Go `client`
 * package is the behavioral reference — same wire surface, same semantics.
 *
 * ```ts
 * const client = dial('127.0.0.1:6270');
 * const session = await client.open('memory', { sessionId: 'demo' });
 * const events = session.events();          // subscribe before submitting
 * await events.ready;                        // the subscription is live
 * await session.submit({ messages: [{ role: 'user', content: 'hi' }], delivery: 'auto' });
 * for await (const envelope of events) {
 *   // resolve gates, collect deltas, end at run.completed
 * }
 * ```
 */

export {
  dial,
  OapClient,
  DEFAULT_PARTICIPANT,
  type AdapterInfo,
  type Capabilities,
  type DialOptions,
  type FetchLike,
  type FetchInit,
  type FetchResponse,
  type ByteBody,
  type StreamReader,
} from './client.js';

export {
  OapSession,
  finalText,
  type Catalog,
  type ToolCatalog,
  type ModelsOptions,
  type SubmitInput,
  type PermissionResolveInput,
  type UserInputResolveInput,
} from './session.js';

export { EventStream, type EventsOptions } from './events.js';

export {
  OapError,
  ServerError,
  serverCode,
  OverflowError,
  ReplayGapError,
  DisconnectError,
  MalformedFrameError,
  DuplicateSequenceError,
  SequenceGapError,
  ResumeMismatchError,
  AbortedError,
} from './errors.js';

export { SSEParser, type SSEFrame } from './sse.js';

export * from './protocol.js';
