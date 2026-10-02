
import type { FetchResponse, OapClient, StreamReader } from './client.js';
import {
  AbortedError,
  DisconnectError,
  MalformedFrameError,
  OverflowError,
  ReplayGapError,
  ResumeMismatchError,
  DuplicateSequenceError,
  SequenceGapError,
  ServerError,
} from './errors.js';
import { SSEParser, type SSEFrame } from './sse.js';
import { EnvelopeType, parseEnvelope, type Envelope } from './protocol.js';
import type { OapSession } from './session.js';

const SIGNAL_OVERFLOW = 'oap-overflow';
const SIGNAL_GAP = 'oap-replay-gap';

const RUN_EVENT_TYPES: ReadonlySet<string> = new Set([
  EnvelopeType.RunStarted,
  EnvelopeType.RunStatusUpdated,
  EnvelopeType.ContentDelta,
  EnvelopeType.RunCompleted,
  EnvelopeType.RunFailed,
  EnvelopeType.RunCancelled,
  EnvelopeType.ActionCallRequested,
  EnvelopeType.ActionCallStarted,
  EnvelopeType.ActionCallProgress,
  EnvelopeType.ActionCallCompleted,
  EnvelopeType.ActionCallFailed,
  EnvelopeType.ActionCallCancelled,
  EnvelopeType.ActionPermissionRequested,
  EnvelopeType.ActionPermissionResolved,
  EnvelopeType.UserInputRequested,
  EnvelopeType.UserInputResolved,
  EnvelopeType.RunCompactionStarted,
  EnvelopeType.RunCompactionEnded,
]);

const INITIAL_RECONNECT_BACKOFF_MS = 100;
const MAX_RECONNECT_BACKOFF_MS = 1_000;
const MAX_EMPTY_CYCLES = 3;

export interface EventsOptions {
  signal?: AbortSignal;
}

interface Connection {
  readonly response: FetchResponse;
  readonly reader: StreamReader;
  readonly parser: SSEParser;
}

export class EventStream implements AsyncIterable<Envelope> {
  readonly ready: Promise<void>;

  private readonly session: OapSession;
  private readonly client: OapClient;
  private readonly signal?: AbortSignal;
  private readonly strict: boolean;
  private readonly startAfter: number | null;

  private conn: Connection | null = null;
  private runId = '';
  private lastSeq = 0;
  private resumed = false;
  private everConnected = false;
  private speculated = false;
  private terminal = false;
  private connEvents = 0;
  private emptyCycles = 0;
  private backoffMs = 0;
  private buffered: SSEFrame[] = [];
  private iterator: AsyncIterator<Envelope> | null = null;

  constructor(
    session: OapSession,
    client: OapClient,
    options: EventsOptions,
    cursor: { startAfter: number | null; runId: string },
  ) {
    this.session = session;
    this.client = client;
    this.signal = options.signal;
    this.strict = client.strictResume;
    this.startAfter = cursor.startAfter;
    this.runId = cursor.runId;
    if (cursor.startAfter !== null) this.lastSeq = cursor.startAfter;
    const established = this.connect();
    this.ready = established.then(() => undefined);
    this.ready.catch(() => {});
  }

  [Symbol.asyncIterator](): AsyncIterator<Envelope> {
    if (!this.iterator) this.iterator = this.iterate();
    return this.iterator;
  }

  private async *iterate(): AsyncGenerator<Envelope, void, unknown> {
    try {
      await this.ready;
      for (;;) {
        if (!this.conn) await this.connect();
        let envelope: Envelope;
        try {
          envelope = await this.poll();
        } catch (err) {
          if (!(err instanceof ConnectionDrop)) {
            throw err;
          }
          this.closeConn();
          if (this.signal?.aborted) throw new AbortedError('event stream aborted');
          if (this.terminal) {
            return;
          }
          if (this.connEvents === 0) {
            this.emptyCycles += 1;
            if (this.emptyCycles >= MAX_EMPTY_CYCLES) {
              throw new DisconnectError(
                this.runId,
                this.lastSeq,
                new Error(`reconnected ${this.emptyCycles} times without receiving an event`),
              );
            }
          } else {
            this.emptyCycles = 0;
          }
          if (this.strict) {
            throw new DisconnectError(this.runId, this.lastSeq, err.cause);
          }
          continue;
        }
        yield envelope;
      }
    } finally {
      this.closeConn();
    }
  }

  private async connect(): Promise<void> {
    for (;;) {
      this.throwIfAborted();
      const [after, speculative] = this.cursor();
      const path = this.session.path('/events') + (after === '' ? '' : `?after=${after}`);
      const headers: Record<string, string> = { Accept: 'text/event-stream' };
      if (after !== '') headers['Last-Event-ID'] = after;
      let response: FetchResponse;
      try {
        response = await this.client.request(path, { method: 'GET', headers, signal: this.signal });
      } catch (err) {
        if (this.signal?.aborted) throw new AbortedError('event stream aborted');
        if (!this.everConnected) throw err;
        await this.wait();
        continue;
      }
      if (response.status !== 200) {
        let body = '';
        try {
          body = await response.text();
        } catch {
        }
        const failure =
          this.client.failureError(response.status, body) ??
          new ServerError(response.status, '', `event stream returned status ${response.status}, want 200 OK`);
        if (speculative && failure.code === 'no_run_to_resume') {
          this.speculated = true;
          continue;
        }
        throw failure;
      }
      const contentType = response.headers.get('content-type') ?? '';
      const mediaType = contentType.split(';', 1)[0]?.trim().toLowerCase() ?? '';
      if (mediaType !== 'text/event-stream') {
        throw new ServerError(
          response.status,
          '',
          `event stream content type "${contentType}", want text/event-stream`,
        );
      }
      if (!response.body) {
        throw new ServerError(response.status, '', 'event stream response carries no body');
      }
      this.everConnected = true;
      this.conn = { response, reader: response.body.getReader(), parser: new SSEParser() };
      this.resumed = after !== '';
      this.connEvents = 0;
      this.backoffMs = 0;
      return;
    }
  }

  private cursor(): [string, boolean] {
    if (this.everConnected && this.runId !== '') return [String(this.lastSeq), false];
    if (this.startAfter !== null) return [String(this.startAfter), false];
    if (this.everConnected && !this.speculated) return ['0', true];
    return ['', false];
  }

  private wait(): Promise<void> {
    if (this.backoffMs === 0) this.backoffMs = INITIAL_RECONNECT_BACKOFF_MS;
    else if (this.backoffMs < MAX_RECONNECT_BACKOFF_MS) this.backoffMs *= 2;
    const delay = this.backoffMs;
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.signal?.removeEventListener('abort', onAbort);
        resolve();
      }, delay);
      const onAbort = () => {
        clearTimeout(timer);
        reject(new AbortedError('event stream aborted during reconnect'));
      };
      if (this.signal) {
        if (this.signal.aborted) {
          clearTimeout(timer);
          reject(new AbortedError('event stream aborted'));
          return;
        }
        this.signal.addEventListener('abort', onAbort, { once: true });
      }
    });
  }

  private closeConn(): void {
    if (this.conn) {
      void this.conn.reader.cancel?.(undefined).catch(() => {});
      this.conn = null;
      this.buffered = [];
    }
  }

  private throwIfAborted(): void {
    if (this.signal?.aborted) throw new AbortedError('event stream aborted');
  }

  private async poll(): Promise<Envelope> {
    for (;;) {
      while (this.buffered.length > 0) {
        const frame = this.buffered.shift();
        if (frame === undefined) break;
        const envelope = this.handleFrame(frame);
        if (envelope) return envelope;
      }
      const conn = this.conn;
      if (!conn) throw new Error('client: event stream poll without a connection');
      let read: { done: boolean; value?: Uint8Array };
      try {
        read = await conn.reader.read();
      } catch (err) {
        throw new ConnectionDrop(err);
      }
      if (read.done) {
        const tail = conn.parser.finish();
        if (tail.length > 0) {
          this.buffered.push(...tail);
          continue;
        }
        throw new ConnectionDrop(undefined);
      }
      if (read.value) this.buffered.push(...conn.parser.push(read.value));
    }
  }

  private handleFrame(frame: SSEFrame): Envelope | undefined {
    switch (frame.event) {
      case SIGNAL_OVERFLOW: {
        const signal = decodeSignal(frame);
        let runId = signalString(signal, 'run_id');
        let lastSequence = signalSequence(signal, 'last_sequence');
        if (this.runId !== '') {
          runId = this.runId;
          lastSequence = this.lastSeq;
        }
        throw new OverflowError(runId, lastSequence, signalString(signal, 'message'));
      }
      case SIGNAL_GAP: {
        const signal = decodeSignal(frame);
        throw new ReplayGapError(
          this.runId,
          signalSequence(signal, 'requested_after'),
          signalSequence(signal, 'oldest_available'),
          signalSequence(signal, 'latest_available'),
          signalString(signal, 'message'),
        );
      }
      case 'message': {
        let parsed: Envelope;
        try {
          parsed = parseEnvelope(frame.data);
        } catch (err) {
          throw new MalformedFrameError('message frame is not an envelope', err);
        }
        this.deliver(parsed, frame);
        return parsed;
      }
      default:
        return undefined;
    }
  }

  private deliver(envelope: Envelope, frame: SSEFrame): void {
    if (envelope.session_id !== this.session.id) {
      throw new MalformedFrameError(
        `envelope for session "${envelope.session_id ?? ''}" on the "${this.session.id}" stream`,
      );
    }
    const payloadScope = envelope.payload as { session_id?: unknown; run_id?: unknown; tool_call_id?: unknown };
    if (payloadScope.session_id !== undefined && payloadScope.session_id !== envelope.session_id) {
      throw new MalformedFrameError(
        `payload names session "${String(payloadScope.session_id)}", envelope "${envelope.session_id}"`,
      );
    }
    if (payloadScope.run_id !== undefined && payloadScope.run_id !== envelope.run_id) {
      throw new MalformedFrameError(`payload names run "${String(payloadScope.run_id)}", envelope "${envelope.run_id ?? ''}"`);
    }
    if (
      payloadScope.tool_call_id !== undefined &&
      envelope.tool_call_id !== undefined &&
      payloadScope.tool_call_id !== envelope.tool_call_id
    ) {
      throw new MalformedFrameError(
        `payload names tool call "${String(payloadScope.tool_call_id)}", envelope "${envelope.tool_call_id}"`,
      );
    }
    const sequence = envelope.sequence;
    if (typeof sequence !== 'number' || !Number.isInteger(sequence) || sequence <= 0) {
      throw new MalformedFrameError('event envelope carries no sequence');
    }
    if (frame.hasId) {
      if (!/^\d+$/.test(frame.lastId)) {
        throw new MalformedFrameError(`frame id "${frame.lastId}" is not a sequence`);
      }
      const id = Number(frame.lastId);
      if (sequence !== id) {
        throw new MalformedFrameError(`frame id ${id} disagrees with envelope sequence ${sequence}`);
      }
    }
    if (!RUN_EVENT_TYPES.has(envelope.type)) {
      this.connEvents += 1;
      return;
    }
    const runId = envelope.run_id;
    if (typeof runId !== 'string' || runId === '') {
      throw new MalformedFrameError('event envelope carries no run id');
    }
    let resumed = this.resumed;
    if (resumed) {
      this.resumed = false;
      if (this.runId !== '' && runId !== this.runId) {
        throw new ResumeMismatchError(this.lastSeq, this.runId, runId, sequence);
      }
      if (sequence !== this.lastSeq + 1) {
        throw new SequenceGapError(runId, this.lastSeq + 1, sequence);
      }
    }
    if (runId !== this.runId) {
      if (resumed && this.runId === '') {
        this.runId = runId;
        this.terminal = isTerminal(envelope.type);
      } else if (this.runId === '') {
        this.runId = runId;
        this.lastSeq = 0;
        this.terminal = isTerminal(envelope.type);
      } else {
        if (sequence !== 1) {
          throw new SequenceGapError(runId, 1, sequence);
        }
        this.runId = runId;
        this.lastSeq = 0;
        this.terminal = isTerminal(envelope.type);
      }
    } else if (isTerminal(envelope.type)) {
      this.terminal = true;
    }
    if (this.lastSeq > 0) {
      if (sequence <= this.lastSeq) {
        throw new DuplicateSequenceError(runId, sequence);
      }
      if (sequence !== this.lastSeq + 1) {
        throw new SequenceGapError(runId, this.lastSeq + 1, sequence);
      }
    }
    this.lastSeq = sequence;
    this.connEvents += 1;
  }
}

function decodeSignal(frame: SSEFrame): Record<string, unknown> {
  let value: unknown;
  try {
    value = JSON.parse(frame.data);
  } catch (err) {
    throw new MalformedFrameError('signal frame payload did not decode', err);
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new MalformedFrameError('signal frame payload did not decode');
  }
  return value as Record<string, unknown>;
}

function signalString(signal: Record<string, unknown>, field: string): string {
  const value = signal[field];
  if (typeof value === 'string') return value;
  if (value === undefined) return '';
  throw new MalformedFrameError(`signal field ${field} is not a string`);
}

function signalSequence(signal: Record<string, unknown>, field: string): number {
  const value = signal[field];
  if (value === undefined) return 0;
  if (typeof value === 'number' && Number.isInteger(value) && value >= 0) return value;
  throw new MalformedFrameError(`signal field ${field} is not a sequence`);
}

class ConnectionDrop {
  constructor(readonly cause: unknown) {}
}

function isTerminal(type: string): boolean {
  return type === EnvelopeType.RunCompleted || type === EnvelopeType.RunFailed || type === EnvelopeType.RunCancelled;
}
