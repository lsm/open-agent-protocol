
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
  type ToolSourceAttachment,
} from './protocol.js';

export type WorkStatus = 'queued' | 'running' | 'needs_you' | 'done' | 'failed' | 'stopped';

export interface WorkRef {
  adapter: string;
  session_id?: string;
  native_id?: string;
}

export interface Work {
  ref: WorkRef;
  status?: WorkStatus;
  held?: false;
  native?: true;
  state?: 'live' | 'closed' | 'running' | 'idle';
  directory?: string;
  title?: string;
  run_id?: string;
  last_reply?: string;
  link?: string;
  updated_at_ms: number;
  pending?: { interaction_id: string };
}

export interface WorkGroup {
  directory: string;
  last_activity_ms: number;
  work: Work[];
}

export interface WorkTurn {
  index: number;
  role: 'user' | 'assistant';
  text: string;
  run_id?: string;
  outcome?: 'completed' | 'failed' | 'cancelled';
  at_ms: number;
}

export type WorkVerb = 'work.list' | 'work.start' | 'work.send' | 'work.status' | 'work.stop' | 'work.read';

export interface WorkAdapterCapabilities {
  adapter: string;
  directory?: string;
  any_directory: boolean;
  verbs: WorkVerb[];
  native: { list: boolean; read: boolean; search: boolean };
}

export interface WorkStartInput {
  message: string;
  title?: string;
  directory?: string;
  native_id?: string;
}

export interface FetchResponse {
  readonly status: number;
  readonly ok: boolean;
  readonly headers: { get(name: string): string | null };
  readonly body: ByteBody | null;
  text(): Promise<string>;
}

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

export const DEFAULT_PARTICIPANT = 'user';

export interface DialOptions {
  fetch?: FetchLike;
  participant?: string;
  strictResume?: boolean;
}

export interface AdapterInfo {
  name: string;
  capability_revision?: string;
  capabilities?: CapabilityDescriptor;
  error?: string;
}

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

export function dial(addr: string, options: DialOptions = {}): OapClient {
  return new OapClient(addr, options);
}

export class OapClient {
  private readonly base: string;
  private readonly fetchLike: FetchLike;
  private readonly participant: string;
  readonly strictResume: boolean;
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

  async workList(
    options: {
      directory?: string;
      adapters?: string[];
      includeClosed?: boolean;
      includeNative?: boolean;
      limit?: number;
      cursor?: string;
      search?: string;
    } = {},
  ): Promise<{ groups: WorkGroup[]; nextCursor?: string; unavailable: { adapter: string; message: string }[] }> {
    const query = new URLSearchParams();
    if (options.directory) query.set('directory', options.directory);
    if (options.adapters && options.adapters.length > 0) query.set('adapters', options.adapters.join(','));
    if (options.includeClosed) query.set('include_closed', 'true');
    if (options.includeNative) query.set('include_native', 'true');
    if (options.limit !== undefined) query.set('limit', String(options.limit));
    if (options.cursor) query.set('cursor', options.cursor);
    if (options.search) query.set('search', options.search);
    const suffix = query.toString() ? `?${query.toString()}` : '';
    const listed = await this.plain<{
      groups?: WorkGroup[];
      next_cursor?: string;
      unavailable?: { adapter: string; message: string }[];
    }>('GET', `/work${suffix}`, null, 'list work');
    const answer: { groups: WorkGroup[]; nextCursor?: string; unavailable: { adapter: string; message: string }[] } = {
      groups: listed.groups ?? [],
      unavailable: listed.unavailable ?? [],
    };
    if (listed.next_cursor) answer.nextCursor = listed.next_cursor;
    return answer;
  }

  async workCapabilities(): Promise<{
    adapters: WorkAdapterCapabilities[];
    unavailable: { adapter: string; message: string }[];
  }> {
    const answered = await this.plain<{
      adapters?: WorkAdapterCapabilities[];
      unavailable?: { adapter: string; message: string }[];
    }>('GET', '/work/capabilities', null, 'read work capabilities');
    return { adapters: answered.adapters ?? [], unavailable: answered.unavailable ?? [] };
  }

  async workStatus(sessionId: string): Promise<Work> {
    return this.plain<Work>('GET', `/work/sessions/${encodeURIComponent(sessionId)}`, null, 'read work status');
  }

  async workStart(adapter: string, input: WorkStartInput): Promise<Work> {
    return this.plain<Work>('POST', `/adapters/${encodeURIComponent(adapter)}/work`, input, 'start work');
  }

  async workSend(sessionId: string, message: string): Promise<Work> {
    return this.plain<Work>('POST', `/work/sessions/${encodeURIComponent(sessionId)}/send`, { message }, 'send work');
  }

  async workStop(sessionId: string): Promise<Work> {
    return this.plain<Work>('POST', `/work/sessions/${encodeURIComponent(sessionId)}/stop`, null, 'stop work');
  }

  async workRead(sessionId: string, options: { after?: number; limit?: number } = {}): Promise<WorkTurn[]> {
    const query = new URLSearchParams();
    if (options.after !== undefined) query.set('after', String(options.after));
    if (options.limit !== undefined) query.set('limit', String(options.limit));
    const suffix = query.toString() ? `?${query.toString()}` : '';
    const read = await this.plain<{ turns?: WorkTurn[] }>(
      'GET',
      `/work/sessions/${encodeURIComponent(sessionId)}/read${suffix}`,
      null,
      'read work',
    );
    return read.turns ?? [];
  }

  private async plain<T>(method: string, path: string, body: unknown, what: string): Promise<T> {
    const init: FetchInit = { method, headers: { Accept: 'application/json' } };
    if (body !== null) {
      init.headers = { ...init.headers, 'Content-Type': 'application/json' };
      init.body = JSON.stringify(body);
    }
    const response = await this.request(path, init);
    const text = await this.readBody(response, what);
    const failure = this.failureError(response.status, text);
    if (failure) throw failure;
    try {
      return JSON.parse(text) as T;
    } catch (err) {
      throw wrap(`client: decode ${what}: ${message(err)}`, err);
    }
  }

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

  async open(
    adapter: string,
    options: {
      sessionId?: string;
      participant?: string;
      toolSources?: ToolSourceAttachment[];
      allowDegradedFeatures?: string[];
    } = {},
  ): Promise<OapSession> {
    const requestPayload: SessionOpenRequest = options.sessionId ? { session_id: options.sessionId } : {};
    if (options.toolSources?.length) requestPayload.tool_sources = options.toolSources;
    if (options.allowDegradedFeatures?.length) {
      requestPayload.allow_degraded_features = options.allowDegradedFeatures;
    }
    const request = this.envelope(EnvelopeType.SessionOpenRequest, requestPayload);
    if (options.sessionId) request.session_id = options.sessionId;
    if (requestPayload.tool_sources?.length) {
      request.capability_revision = (await this.capabilities(adapter)).revision;
    }
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
    if (opened.session_id !== envelope.session_id) {
      throw new Error(
        `client: open response payload names session "${opened.session_id}", envelope "${envelope.session_id ?? ''}"`,
      );
    }
    return new OapSession(this, opened.session_id, adapter, options.participant ?? this.participant);
  }

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

  async request(path: string, init: FetchInit): Promise<FetchResponse> {
    try {
      return await this.fetchLike(this.url(path), init);
    } catch (err) {
      throw wrap(`client: ${init.method ?? 'GET'} ${path}: ${message(err)}`, err);
    }
  }

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
          details = payload.error.details;
        }
      }
    } catch {
    }
    const runes = Array.from(messageText);
    if (runes.length > 300) messageText = `${runes.slice(0, 300).join('')}…`;
    return new ServerError(status, code, messageText || 'no body', envelope, details);
  }

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
