/**
 * The OAP client for a local `oap serve` daemon: OAP operations travel as
 * verbatim schema/v0.1 envelopes over the daemon's HTTP surface, and the
 * event stream is consumed through a real text/event-stream parser with
 * invisible cursor resume. It is the TypeScript counterpart of the Go
 * `client` package — same wire surface, same semantics.
 *
 * The client is safe for concurrent use, never logs, and carries no
 * credentials: the daemon is a single-user local service. The runtime has
 * zero dependencies: platform fetch and streams, Node 18+ baseline.
 */

import { OapSession } from './session.js';
import { ServerError } from './errors.js';
import {
  PROTOCOL,
  PROFILE,
  VERSION,
  EnvelopeType,
  parseEnvelope,
  payload,
  type CapabilityDescriptor,
  type Envelope,
  type ErrorResponse,
  type SessionOpenRequest,
} from './protocol.js';

/**
 * The minimal structural fetch surface the client uses. The platform's
 * global fetch satisfies it, so a custom transport only has to behave like
 * fetch, not be it.
 */
export interface FetchResponse {
  readonly status: number;
  readonly ok: boolean;
  readonly headers: { get(name: string): string | null };
  readonly body: ByteBody | null;
  text(): Promise<string>;
}

/** Readable byte-stream pieces the client relies on. */
export interface ByteBody {
  getReader(): StreamReader;
}

export interface StreamReader {
  read(): Promise<{ done: boolean; value?: Uint8Array }>;
  cancel?(reason?: unknown): Promise<void>;
}

export interface FetchInit {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
}

export type FetchLike = (url: string, init?: FetchInit) => Promise<FetchResponse>;

/** The responder identity the daemon acts as when it opens an adapter session, so it is also the identity a client resolves interactive gates with. */
export const DEFAULT_PARTICIPANT = 'user';

/** dial() options; see dial. */
export interface DialOptions {
  /** Substitutes the transport used for daemon requests. */
  fetch?: FetchLike;
  /** Sets the responder identity written into interactive-gate resolutions. It must match the identity the gates declare; against an `oap serve` daemon that is always "user", the default. */
  participant?: string;
  /** Turns off invisible cursor resume: a dropped event stream is reported as a DisconnectError carrying the last observed sequence instead of being reconnected. */
  strictResume?: boolean;
}

/** One adapter-listing entry. A probing adapter reports its capabilities; a failing one reports error. */
export interface AdapterInfo {
  name: string;
  capability_revision?: string;
  capabilities?: CapabilityDescriptor;
  error?: string;
}

/** One adapter's capability snapshot; `revision` names the descriptor every emitted envelope repeats. */
export interface Capabilities {
  revision: string;
  descriptor: CapabilityDescriptor;
}

const platformFetch: FetchLike | undefined = (globalThis as { fetch?: FetchLike }).fetch;

function randomPrefix(): string {
  const crypto = (globalThis as { crypto?: { getRandomValues?: (buffer: Uint8Array) => Uint8Array } }).crypto;
  if (crypto?.getRandomValues) {
    const buffer = new Uint8Array(4);
    crypto.getRandomValues(buffer);
    return Array.from(buffer, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }
  return Math.random().toString(16).slice(2, 10);
}

/**
 * Returns a client for the daemon at addr, which may carry a scheme
 * ("http://127.0.0.1:6270") or not ("127.0.0.1:6270").
 */
export function dial(addr: string, options: DialOptions = {}): OapClient {
  return new OapClient(addr, options);
}

/** One daemon connection. Construct with dial. */
export class OapClient {
  private readonly base: string;
  private readonly fetchLike: FetchLike;
  private readonly participant: string;
  readonly strictResume: boolean;
  /** Makes this client's envelope ids unique in any trace combining traffic from several clients. */
  private readonly idPrefix: string;
  private ids = 0;

  constructor(addr: string, options: DialOptions) {
    if (options.fetch) {
      this.fetchLike = options.fetch;
    } else if (platformFetch) {
      this.fetchLike = platformFetch;
    } else {
      throw new Error('client: no platform fetch available; pass a custom transport to dial');
    }
    this.participant = options.participant ?? DEFAULT_PARTICIPANT;
    this.strictResume = options.strictResume ?? false;
    this.base = addr.includes('://') ? addr : `http://${addr}`;
    this.idPrefix = randomPrefix();
  }

  /** Lists the daemon's registered adapters. */
  async adapters(): Promise<AdapterInfo[]> {
    const response = await this.request('/adapters', { method: 'GET', headers: { Accept: 'application/json' } });
    const body = await this.readBody(response, 'list adapters');
    const failure = this.failureError(response.status, body);
    if (failure) throw failure;
    let listing: { adapters?: AdapterInfo[] };
    try {
      listing = JSON.parse(body) as { adapters?: AdapterInfo[] };
    } catch (err) {
      throw wrap(`client: decode adapter listing: ${message(err)}`, err);
    }
    return listing.adapters ?? [];
  }

  /**
   * Probes one adapter's descriptor. The daemon's response cites a
   * correlation id of its own; the paired request envelope, if a caller
   * needs one for a trace, is a capabilities.request citing it.
   */
  async capabilities(adapter: string): Promise<Capabilities> {
    const envelope = await this.exchange(
      'GET',
      `/adapters/${encodeURIComponent(adapter)}/capabilities`,
      null,
      EnvelopeType.CapabilitiesResponse,
    );
    const descriptor = payload<CapabilityDescriptor>(envelope);
    return { revision: envelope.capability_revision ?? '', descriptor };
  }

  /**
   * Opens one adapter session and returns an OapSession bound to it. An
   * empty sessionId lets the adapter mint one; the returned session reports
   * whatever id the daemon confirmed.
   */
  async open(adapter: string, options: { sessionId?: string; participant?: string } = {}): Promise<OapSession> {
    const requestPayload: SessionOpenRequest = options.sessionId ? { session_id: options.sessionId } : {};
    const request = this.envelope(EnvelopeType.SessionOpenRequest, requestPayload);
    if (options.sessionId) request.session_id = options.sessionId;
    const envelope = await this.exchange(
      'POST',
      `/adapters/${encodeURIComponent(adapter)}/sessions`,
      request,
      EnvelopeType.SessionOpenResponse,
    );
    const opened = envelope.payload as { session_id?: string };
    if (!opened.session_id) {
      throw new Error('client: open response carries no session id');
    }
    // The envelope and its payload are individually schema-valid objects;
    // the protocol binds them to one scope. A payload naming another
    // session must not become this client's session identity.
    if (opened.session_id !== envelope.session_id) {
      throw new Error(
        `client: open response payload names session "${opened.session_id}", envelope "${envelope.session_id ?? ''}"`,
      );
    }
    return new OapSession(this, opened.session_id, adapter, options.participant ?? this.participant);
  }

  // --- request plumbing ---

  /** Mints one request envelope with a fresh correlation id unique to this client. */
  envelope(type: string, requestPayload: unknown): Envelope {
    this.ids += 1;
    return {
      protocol: PROTOCOL,
      version: VERSION,
      profile: PROFILE,
      type,
      id: `client-${this.idPrefix}-${this.ids}`,
      payload: (requestPayload ?? {}) as Record<string, unknown>,
    };
  }

  /** Performs one fetch against the daemon base address. */
  async request(path: string, init: FetchInit): Promise<FetchResponse> {
    try {
      return await this.fetchLike(this.url(path), init);
    } catch (err) {
      throw wrap(`client: ${init.method ?? 'GET'} ${path}: ${message(err)}`, err);
    }
  }

  /**
   * Performs one OAP operation: it sends the request envelope, if any,
   * requires the expected response type, and returns it undecoded.
   */
  async exchange(method: string, path: string, request: Envelope | null, want: string): Promise<Envelope> {
    const init: FetchInit = { method, headers: { Accept: 'application/json' } };
    if (request) {
      init.headers = { ...init.headers, 'Content-Type': 'application/json' };
      init.body = JSON.stringify(request);
    }
    const response = await this.request(path, init);
    const body = await this.readBody(response, `read ${path} response`);
    if (response.status === 204) {
      throw new Error(`client: ${path} returned no content where ${want} was expected`);
    }
    const failure = this.failureError(response.status, body);
    if (failure) {
      // A parsed error.response must also cite the request it answers and
      // stay in its scope: an envelope correlated elsewhere, or scoped to
      // another session or run, is a protocol violation, not this
      // operation's answer — surfacing it would attribute another
      // operation's refusal to this one.
      if (
        failure instanceof ServerError &&
        failure.envelope &&
        failure.envelope.type === EnvelopeType.ErrorResponse &&
        request
      ) {
        const envelope = failure.envelope;
        if (envelope.in_reply_to !== request.id) {
          throw new Error(
            `client: ${path} error response cites correlation "${envelope.in_reply_to ?? ''}", want the request id ${request.id}`,
          );
        }
        if (request.session_id && envelope.session_id !== request.session_id) {
          throw new Error(
            `client: ${path} error response is scoped to session "${envelope.session_id ?? ''}", want "${request.session_id}"`,
          );
        }
        if (request.run_id && envelope.run_id !== request.run_id) {
          throw new Error(
            `client: ${path} error response is scoped to run "${envelope.run_id ?? ''}", want "${request.run_id}"`,
          );
        }
      }
      throw failure;
    }
    let envelope: Envelope;
    try {
      envelope = parseEnvelope(body);
    } catch (err) {
      throw wrap(`client: decode ${path} response envelope: ${message(err)}`, err);
    }
    if (envelope.type !== want) {
      throw new Error(`client: ${path} returned ${envelope.type}, want ${want}`);
    }
    if (request) {
      // OAP correlation: a response must cite the request envelope it
      // answers, and stay in its scope. Anything else is a stale or
      // misrouted envelope, and decoding it would attribute another
      // operation's answer to this one.
      if (envelope.in_reply_to !== request.id) {
        throw new Error(
          `client: ${path} response cites correlation "${envelope.in_reply_to ?? ''}", want the request id ${request.id}`,
        );
      }
      if (request.session_id && envelope.session_id !== request.session_id) {
        throw new Error(
          `client: ${path} response is scoped to session "${envelope.session_id ?? ''}", want "${request.session_id}"`,
        );
      }
      if (request.run_id && envelope.run_id !== request.run_id) {
        throw new Error(
          `client: ${path} response is scoped to run "${envelope.run_id ?? ''}", want "${request.run_id}"`,
        );
      }
    }
    return envelope;
  }

  private async readBody(response: FetchResponse, what: string): Promise<string> {
    try {
      return await response.text();
    } catch (err) {
      throw wrap(`client: ${what}: ${message(err)}`, err);
    }
  }

  /**
   * Converts a non-2xx status into a ServerError, keeping the correlated
   * envelope when the body carries one; returns null on success. The message
   * is bounded so a runaway body cannot flood the error.
   */
  failureError(status: number, body: string): ServerError | null {
    if (status >= 200 && status < 300) return null;
    let code = '';
    let messageText = body.trim();
    let envelope: Envelope | undefined;
    let details: Record<string, unknown> | undefined;
    try {
      const parsed = parseEnvelope(body);
      if (parsed.type === EnvelopeType.ErrorResponse) {
        envelope = parsed;
        const payload = parsed.payload as unknown as ErrorResponse;
        if (payload.error && typeof payload.error.code === 'string') {
          code = payload.error.code;
          messageText = payload.error.message ?? '';
          // A typed refusal names what to change in its details; the client
          // exposes them rather than leaving a caller to re-parse the body.
          details = payload.error.details;
        }
      }
    } catch {
      // A non-envelope body keeps its trimmed text as the message.
    }
    const runes = Array.from(messageText);
    if (runes.length > 300) messageText = `${runes.slice(0, 300).join('')}…`;
    return new ServerError(status, code, messageText || 'no body', envelope, details);
  }

  /** Joins the daemon base address with an absolute path. */
  url(path: string): string {
    return `${this.base.replace(/\/+$/, '')}${path}`;
  }
}

export function wrap(text: string, cause: unknown): Error {
  const err = new Error(text);
  (err as { cause?: unknown }).cause = cause;
  return err;
}

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
