/**
 * The session event stream: SSE consumption over platform fetch streaming,
 * with the Go client's cursor integrity rules. The daemon's terminal
 * transport signals (oap-overflow, oap-replay-gap) are framing, not OAP
 * envelopes; they surface as typed errors carrying the reconnect cursor.
 */

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

/** SSE event names for the daemon's terminal transport signals. */
const SIGNAL_OVERFLOW = 'oap-overflow';
const SIGNAL_GAP = 'oap-replay-gap';

/** The first wait between reconnect attempts after a transport failure; it doubles up to the cap. */
const INITIAL_RECONNECT_BACKOFF_MS = 100;
const MAX_RECONNECT_BACKOFF_MS = 1_000;
/**
 * Bounds consecutive reconnects whose connection ends without delivering
 * anything: a cursor at a settled run's tail produces empty replays forever,
 * and an endless silent reconnect loop is worse than a reported disconnect.
 */
const MAX_EMPTY_CYCLES = 3;

/** events()/eventsAfter() options. */
export interface EventsOptions {
  /** Aborts the stream: iteration raises AbortedError and the connection is dropped. */
  signal?: AbortSignal;
}

/** One open SSE connection. */
interface Connection {
  readonly response: FetchResponse;
  readonly reader: StreamReader;
  readonly parser: SSEParser;
}

/**
 * EventStream is one ordered envelope stream over a session, consumed
 * exclusively through `for await`. Iteration ends cleanly (the loop simply
 * finishes) once a run's terminal event has been delivered; every other
 * error is terminal for the stream.
 */
export class EventStream implements AsyncIterable<Envelope> {
  /**
   * Resolves once the initial subscription is live — the daemon has
   * registered it — or rejects with the failure that prevented it. Await
   * this before submitting when the consuming loop does not start at once.
   */
  readonly ready: Promise<void>;

  private readonly session: OapSession;
  private readonly client: OapClient;
  private readonly signal?: AbortSignal;
  private readonly strict: boolean;
  private readonly startAfter: number | null;

  private conn: Connection | null = null;
  /** The last observed (run, sequence); run is empty until the first envelope fixes it. */
  private runId = '';
  private lastSeq = 0;
  /** Marks a connection opened with a cursor: its first envelope must continue runId at lastSeq+1 exactly. */
  private resumed = false;
  /** Distinguishes the initial connect (whose transport failures surface at once) from reconnects (which back off and retry). */
  private everConnected = false;
  /** Records that the replay-from-start reconnect fallback was already tried, so a session without a run parks live instead of retrying the speculative cursor forever. */
  private speculated = false;
  /** Records that a run's terminal envelope was delivered, so a subsequent end of stream is the documented clean end, not a drop. */
  private terminal = false;
  /** Counts envelopes delivered by the current connection. */
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
    // A manual cursor anchors the sequence expectation: the replay must
    // continue at startAfter + 1, not at 1.
    if (cursor.startAfter !== null) this.lastSeq = cursor.startAfter;
    const established = this.connect();
    this.ready = established.then(() => undefined);
    // Neither the consuming loop nor `ready` is awaited in every usage shape;
    // an unobserved initial-connect failure must not crash the process.
    this.ready.catch(() => {});
  }

  [Symbol.asyncIterator](): AsyncIterator<Envelope> {
    // One stream, one iterator: a second loop resumes the same position
    // instead of rewinding or double-reading the wire.
    if (!this.iterator) this.iterator = this.iterate();
    return this.iterator;
  }

  private async *iterate(): AsyncGenerator<Envelope, void, unknown> {
    try {
      // The initial connection was opened when events() was called; its
      // failure is surfaced here, by the first next().
      await this.ready;
      for (;;) {
        if (!this.conn) await this.connect();
        let envelope: Envelope;
        try {
          envelope = await this.poll();
        } catch (err) {
          if (!(err instanceof ConnectionDrop)) {
            // A daemon signal or a stream defect: surfaced, never retried.
            throw err;
          }
          this.closeConn();
          if (this.signal?.aborted) throw new AbortedError('event stream aborted');
          if (this.terminal) {
            // The stream's documented clean end at run terminality.
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
          // Invisible resume: reconnect with the cursor and continue.
          continue;
        }
        yield envelope;
      }
    } finally {
      this.closeConn();
    }
  }

  /**
   * Opens one SSE connection, retrying transport failures with backoff after
   * the first successful connection. A cursor is attached whenever the
   * stream holds one; a reconnect with no observed envelope yet replays the
   * current run from its start, falling back to a live subscription when
   * the session has no run to replay.
   */
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
          // The status itself already says the stream was refused.
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
        // A 200 with some other body would parse as an empty or garbage
        // stream — read errors that masquerade as drops, or a connection
        // that parks forever. The endpoint must declare the event stream.
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

  /**
   * Reports the reconnect cursor for the next connection. It is empty for a
   * fresh live subscription; a reconnect that has observed nothing
   * speculatively replays the current run from its start, so envelopes
   * emitted during the disconnect are not missed.
   */
  private cursor(): [string, boolean] {
    if (this.everConnected && this.runId !== '') return [String(this.lastSeq), false];
    if (this.startAfter !== null) return [String(this.startAfter), false];
    if (this.everConnected && !this.speculated) return ['0', true];
    return ['', false];
  }

  /** Sleeps one backoff step, doubling up to the cap. */
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
      // Release the connection; a released body read no longer delivers.
      void this.conn.reader.cancel?.(undefined).catch(() => {});
      this.conn = null;
      this.buffered = [];
    }
  }

  private throwIfAborted(): void {
    if (this.signal?.aborted) throw new AbortedError('event stream aborted');
  }

  /**
   * Reads frames until one envelope is delivered, a terminal signal or
   * stream defect is found, or the connection ends.
   */
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
        // The document ended: a frame whose blank line lands at the very
        // end still dispatches, then the connection has ended.
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

  /** Applies one frame; returns the envelope it delivered, or undefined for a skipped frame. */
  private handleFrame(frame: SSEFrame): Envelope | undefined {
    switch (frame.event) {
      case SIGNAL_OVERFLOW: {
        const signal = decodeSignal<OverflowSignal>(frame);
        let runId = signal.run_id ?? '';
        let lastSequence = signal.last_sequence ?? 0;
        if (this.runId !== '') {
          // The hub names the run current at signal time, which can be a
          // newer run than the one this connection was consuming while
          // last_sequence counts the consumed run's envelopes. The stream's
          // own cursor is the pair it actually delivered, so recovery
          // resumes the run that overflowed.
          runId = this.runId;
          lastSequence = this.lastSeq;
        }
        throw new OverflowError(runId, lastSequence, signal.message ?? '');
      }
      case SIGNAL_GAP: {
        const signal = decodeSignal<GapSignal>(frame);
        throw new ReplayGapError(
          this.runId,
          signal.requested_after ?? 0,
          signal.oldest_available ?? 0,
          signal.latest_available ?? 0,
          signal.message ?? '',
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
        // An unknown named event is framing the client does not define;
        // skipping it keeps the stream forward-compatible.
        return undefined;
    }
  }

  /** Applies the stream's cursor integrity rules to one envelope and records its position. */
  private deliver(envelope: Envelope, frame: SSEFrame): void {
    // An envelope naming another session never belongs on this stream: a
    // misrouted stream must not deliver another session's content and
    // interaction requests as this one's.
    if (envelope.session_id !== this.session.id) {
      throw new MalformedFrameError(
        `envelope for session "${envelope.session_id ?? ''}" on the "${this.session.id}" stream`,
      );
    }
    // Every envelope on this stream is a sequenced run event; one without a
    // sequence cannot be positioned, and delivering it would leave the
    // cursor behind it — a later resume would replay it without any way to
    // detect the duplicate. Sequences start at one.
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
    let resumed = this.resumed;
    if (resumed) {
      this.resumed = false;
      if (this.runId !== '' && envelope.run_id !== this.runId) {
        throw new ResumeMismatchError(this.lastSeq, this.runId, envelope.run_id ?? '', sequence);
      }
      // A cursor-bearing connection — including one replaying after zero —
      // requests the run from a known position, unlike a fresh live
      // subscription that may join mid-run: its first envelope must continue
      // the cursor exactly, or envelopes were skipped.
      if (sequence !== this.lastSeq + 1) {
        throw new SequenceGapError(envelope.run_id ?? '', this.lastSeq + 1, sequence);
      }
    }
    if (envelope.run_id !== this.runId) {
      if (resumed && this.runId === '') {
        // The first envelope of a cursor-carrying stream names the run the
        // cursor belongs to: the run is adopted, and the sequence
        // expectation stays bound to the cursor.
        this.runId = envelope.run_id ?? '';
        this.terminal = isTerminal(envelope.type);
      } else if (this.runId === '') {
        // The stream's first observed envelope may join a run in progress:
        // sequences before the join were never deliverable to this
        // subscription, so the join position becomes the baseline.
        this.runId = envelope.run_id ?? '';
        this.lastSeq = 0;
        this.terminal = isTerminal(envelope.type);
      } else {
        // A live transition to a new run starts a fresh sequence space, and
        // this stream was attached throughout, so it must witness the run
        // from its first envelope: anything later means the opening events
        // were lost.
        if (sequence !== 1) {
          throw new SequenceGapError(envelope.run_id ?? '', 1, sequence);
        }
        this.runId = envelope.run_id ?? '';
        this.lastSeq = 0;
        this.terminal = isTerminal(envelope.type);
      }
    } else if (isTerminal(envelope.type)) {
      this.terminal = true;
    }
    // Run sequences are contiguous: within one run every envelope carries
    // the previous sequence plus one. A regression is a replay defect, and a
    // skip means an envelope was lost — advancing past it would hide it from
    // the consumer and from a later cursor resume, so both surface.
    if (this.lastSeq > 0) {
      if (sequence <= this.lastSeq) {
        throw new DuplicateSequenceError(envelope.run_id ?? '', sequence);
      }
      if (sequence !== this.lastSeq + 1) {
        throw new SequenceGapError(envelope.run_id ?? '', this.lastSeq + 1, sequence);
      }
    }
    this.lastSeq = sequence;
    this.connEvents += 1;
  }
}

interface OverflowSignal {
  run_id?: string;
  last_sequence?: number;
  message?: string;
}

interface GapSignal {
  requested_after?: number;
  oldest_available?: number;
  latest_available?: number;
  message?: string;
}

function decodeSignal<Signal extends OverflowSignal | GapSignal>(frame: SSEFrame): Signal {
  let value: unknown;
  try {
    value = JSON.parse(frame.data);
  } catch (err) {
    throw new MalformedFrameError('signal frame payload did not decode', err);
  }
  if (typeof value !== 'object' || value === null) {
    throw new MalformedFrameError('signal frame payload did not decode');
  }
  return value as Signal;
}

/** Wraps the read error (clean end included) that ended one SSE connection: the stream may resume from its cursor. */
class ConnectionDrop {
  constructor(readonly cause: unknown) {}
}

function isTerminal(type: string): boolean {
  return type === EnvelopeType.RunCompleted || type === EnvelopeType.RunFailed || type === EnvelopeType.RunCancelled;
}
