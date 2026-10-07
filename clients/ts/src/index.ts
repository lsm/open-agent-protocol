
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
  type Work,
  type WorkAdapterCapabilities,
  type WorkGroup,
  type WorkRef,
  type WorkStartInput,
  type WorkStatus,
  type WorkTurn,
  type WorkVerb,
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
