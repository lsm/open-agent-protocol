/**
 * A scripted fake transport for the unit tests: responses queue up and match
 * by URL, SSE bodies stream as chunk sequences, and a response can reject or
 * end mid-stream to exercise the resume logic. No server, no sockets.
 */

import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import type { FetchInit, FetchLike, FetchResponse, StreamReader } from '../src/client.js';
import { PROTOCOL, PROFILE, VERSION, type Envelope } from '../src/protocol.js';

const encoder = new TextEncoder();

export interface ScriptedResponse {
  /** Matches by substring, anchored RegExp, or predicate; a missing match matches any URL. */
  match?: string | RegExp | ((url: string) => boolean);
  status?: number;
  headers?: Record<string, string>;
  /** A complete (non-streaming) body; the function form sees the request, so it can echo the correlation id the client minted. */
  body?: string | ((call: { url: string; init?: FetchInit }) => string);
  /** Streamed chunks; implies content type text/event-stream. */
  chunks?: (string | Uint8Array)[];
  /** Wait between streamed chunks. */
  chunkDelayMs?: number;
  /** The body stream rejects with this error once the chunks are exhausted. */
  streamError?: Error;
  /** The fetch itself rejects with this error. */
  rejectWith?: Error;
  /** Wait before the response resolves. */
  delayMs?: number;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

class FakeReader implements StreamReader {
  private index = 0;
  private closed = false;

  constructor(
    private chunks: (string | Uint8Array)[],
    private readonly delayMs: number,
    private readonly streamError: Error | undefined,
    private readonly signal: AbortSignal | undefined,
  ) {}

  async read(): Promise<{ done: boolean; value?: Uint8Array }> {
    if (this.closed) return { done: true };
    if (this.index >= this.chunks.length) {
      if (this.streamError) throw this.streamError;
      return { done: true };
    }
    const chunk = this.chunks[this.index];
    this.index += 1;
    const delivery = sleep(this.delayMs).then(() => ({
      done: false,
      value: typeof chunk === 'string' ? encoder.encode(chunk) : chunk,
    }));
    const signal = this.signal;
    if (!signal) return delivery;
    // A real fetch stream rejects once its request is aborted.
    return Promise.race([
      delivery,
      new Promise<never>((_, reject) => {
        if (signal.aborted) {
          reject(new Error('the operation was aborted'));
          return;
        }
        signal.addEventListener('abort', () => reject(new Error('the operation was aborted')), {
          once: true,
        });
      }),
    ]);
  }

  async cancel(): Promise<void> {
    this.closed = true;
    this.chunks = [];
  }
}

export class FakeTransport {
  /** Every request the transport saw, in order. */
  readonly calls: Array<{ url: string; init?: FetchInit }> = [];
  private script: ScriptedResponse[];

  constructor(script: ScriptedResponse[]) {
    this.script = [...script];
  }

  readonly fetch: FetchLike = async (url, init) => {
    this.calls.push({ url, init });
    const index = this.script.findIndex((entry) => matches(entry.match, url));
    if (index === -1) {
      throw new Error(`test transport: unexpected request ${url}`);
    }
    const entry = this.script.splice(index, 1)[0];
    if (entry.rejectWith) throw entry.rejectWith;
    if (entry.delayMs) await sleep(entry.delayMs);
    const status = entry.status ?? 200;
    const streamed = entry.chunks !== undefined;
    const headers = new Map<string, string>(
      Object.entries({
        'content-type': streamed ? 'text/event-stream' : 'application/json',
        ...entry.headers,
      }),
    );
    const bodyText = typeof entry.body === 'function' ? entry.body({ url, init }) : (entry.body ?? '');
    const response: FetchResponse = {
      status,
      ok: status >= 200 && status < 300,
      headers: { get: (name: string) => headers.get(name.toLowerCase()) ?? null },
      body: {
        getReader: () =>
          new FakeReader(streamed ? (entry.chunks as (string | Uint8Array)[]) : [bodyText], entry.chunkDelayMs ?? 0, entry.streamError, init?.signal),
      },
      text: streamed ? () => Promise.reject(new Error('test transport: streamed response')) : () => Promise.resolve(bodyText),
    };
    return response;
  };

  /** The requests whose URL contains fragment, in order. */
  callsFor(fragment: string): Array<{ url: string; init?: FetchInit }> {
    return this.calls.filter((call) => call.url.includes(fragment));
  }
}

function matches(match: ScriptedResponse['match'], url: string): boolean {
  if (match === undefined) return true;
  if (typeof match === 'string') return url.includes(match);
  if (typeof match === 'function') return match(url);
  return match.test(url);
}

// --- envelope and frame builders ---

export interface TestEnvelopeInput {
  type: string;
  id?: string;
  sequence?: number;
  sessionId?: string;
  runId?: string;
  inReplyTo?: string;
  capabilityRevision?: string;
  payload?: Record<string, unknown>;
}

let nextTestID = 0;

export function testEnvelope(input: TestEnvelopeInput): Envelope {
  nextTestID += 1;
  const envelope: Envelope = {
    protocol: PROTOCOL,
    version: VERSION,
    profile: PROFILE,
    type: input.type,
    id: input.id ?? `env-${nextTestID}`,
    payload: input.payload ?? {},
  };
  if (input.sequence !== undefined) envelope.sequence = input.sequence;
  if (input.sessionId !== undefined) envelope.session_id = input.sessionId;
  if (input.runId !== undefined) envelope.run_id = input.runId;
  if (input.inReplyTo !== undefined) envelope.in_reply_to = input.inReplyTo;
  if (input.capabilityRevision !== undefined) envelope.capability_revision = input.capabilityRevision;
  return envelope;
}

/** One SSE message frame: the sequence as the id field (as the daemon writes it) plus the envelope as data. */
export function eventFrame(envelope: Envelope): string {
  const id = envelope.sequence !== undefined ? `id: ${envelope.sequence}\n` : '';
  return `${id}data: ${JSON.stringify(envelope)}\n\n`;
}

export function signalFrame(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

/** Extracts the request envelope id a recorded call carried. */
export function sentEnvelopeId(call: { init?: FetchInit }): string {
  return (JSON.parse(call.init?.body ?? '{}') as { id?: string }).id ?? '';
}

/** The event types of the reference adapter's golden run, in delivery order. */
export const GOLDEN_RUN = [
  'run.started',
  'content.delta',
  'action.call.requested',
  'action.permission.requested',
  'action.permission.resolved',
  'action.call.started',
  'action.call.completed',
  'user.input.requested',
  'run.status.updated',
  'user.input.resolved',
  'content.delta',
  'run.completed',
] as const;

/** Frames for the golden run's envelopes between sequences from and to inclusive. */
export function goldenFrames(sessionId: string, runId: string, from: number, to: number): string[] {
  const frames: string[] = [];
  for (let sequence = from; sequence <= to; sequence += 1) {
    const type = GOLDEN_RUN[sequence - 1];
    if (type === undefined) throw new Error(`golden run has no sequence ${sequence}`);
    frames.push(
      eventFrame(
        testEnvelope({
          type,
          sequence,
          sessionId,
          runId,
          payload: { session_id: sessionId, run_id: runId },
        }),
      ),
    );
  }
  return frames;
}

/** Walks up from this file (compiled: dist/test) to the repository root, marked by go.mod. */
export function findRepoRoot(fromDir: string): string {
  let dir = fromDir;
  for (;;) {
    if (existsSync(join(dir, 'go.mod')) && existsSync(join(dir, 'clients'))) return dir;
    const parent = dirname(dir);
    if (parent === dir) throw new Error('test: repository root not found above the package');
    dir = parent;
  }
}
