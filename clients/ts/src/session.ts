
import type { OapClient } from './client.js';
import { ServerError } from './errors.js';
import { EventStream, type EventsOptions } from './events.js';
import {
  EnvelopeType,
  payload,
  type Envelope,
  type MessageSubmitRequest,
  type MessageSubmitResponse,
  type ModelsResponse,
  type PermissionResolveRequest,
  type PermissionResolveResponse,
  type RunCancelRequest,
  type RunCancelResponse,
  type RunCompletedPayload,
  type RunSteerAppliedPayload,
  type RunSteerDroppedPayload,
  type SessionOpenRequest,
  type SessionState,
  type ToolsListResponse,
  type UserInputResolveRequest,
  type UserInputResolveResponse,
} from './protocol.js';

export type SubmitInput = Omit<MessageSubmitRequest, 'session_id'> & { session_id?: string };

export type PermissionResolveInput = Omit<PermissionResolveRequest, 'session_id' | 'responded_by'> & {
  session_id?: string;
  responded_by?: string;
};

export type UserInputResolveInput = Omit<UserInputResolveRequest, 'session_id' | 'responded_by'> & {
  session_id?: string;
  responded_by?: string;
};

export type ModelsOptions = { allowDegradedFeatures?: string[] };

export type Catalog = { revision: string; models: ModelsResponse };

export type ToolCatalog = { revision: string; tools: ToolsListResponse };

export type OpenOptions = { sessionId?: string; participant?: string };

export class OapSession {
  private outstanding = new Set<string>();
  private wakeFire: (() => void) | null = null;
  private wakePromise: Promise<void> | null = null;
  private held = new Map<string, Envelope[]>();
  private released: Envelope[] = [];

  constructor(
    private readonly client: OapClient,
    private readonly sessionId: string,
    private readonly adapterName: string,
    private readonly participant: string,
  ) {}

  beginSubmit(id: string): void {
    this.outstanding.add(id);
  }

  endSubmit(id: string): void {
    if (!this.outstanding.delete(id)) return;
    const carried = this.held.get(id);
    if (!carried) return;
    this.held.delete(id);
    this.released.push(...carried);
    this.fireWake();
  }

  releaseWake(): Promise<void> {
    if (!this.wakeFire) {
      this.wakePromise = new Promise<void>((resolve) => {
        this.wakeFire = resolve;
      });
    }
    return this.wakePromise as Promise<void>;
  }

  private fireWake(): void {
    const fire = this.wakeFire;
    this.wakeFire = null;
    this.wakePromise = null;
    if (fire) fire();
  }

  holdSteer(envelope: Envelope): boolean {
    const request = steerSettlementRequest(envelope);
    if (request === '' || !this.outstanding.has(request)) return false;
    const carried = this.held.get(request);
    if (carried) carried.push(envelope);
    else this.held.set(request, [envelope]);
    return true;
  }

  takeSteer(): Envelope | null {
    return this.released.shift() ?? null;
  }

  get id(): string {
    return this.sessionId;
  }

  get adapter(): string {
    return this.adapterName;
  }

  get responder(): string {
    return this.participant;
  }

  async submit(request: SubmitInput): Promise<MessageSubmitResponse> {
    const scoped = { ...request, session_id: this.scope(request.session_id) };
    const envelope = this.client.envelope(EnvelopeType.SessionMessageSubmitRequest, scoped);
    envelope.session_id = this.sessionId;
    this.beginSubmit(envelope.id);
    try {
      const response = await this.client.exchange(
        'POST',
        this.path('/submit'),
        envelope,
        EnvelopeType.SessionMessageSubmitResponse,
      );
      const admission = payload<MessageSubmitResponse>(response);
      crossCheckPayload('submit response', admission, response);
      return admission;
    } finally {
      this.endSubmit(envelope.id);
    }
  }

  async resolvePermission(request: PermissionResolveInput): Promise<void> {
    const scoped = this.resolveScope(request);
    const envelope = this.client.envelope(EnvelopeType.ActionPermissionResolveRequest, scoped);
    envelope.session_id = this.sessionId;
    envelope.run_id = scoped.run_id;
    const response = await this.client.exchange(
      'POST',
      this.path('/resolve'),
      envelope,
      EnvelopeType.ActionPermissionResolveResponse,
    );
    const resolved = payload<PermissionResolveResponse>(response);
    crossCheckPayload('permission resolve response', resolved, response);
    if (resolved.interaction_id !== scoped.interaction_id) {
      throw new Error(
        `client: permission resolve response names interaction "${resolved.interaction_id}", want "${scoped.interaction_id}"`,
      );
    }
  }

  async resolveInput(request: UserInputResolveInput): Promise<void> {
    const scoped = this.resolveScope(request);
    const envelope = this.client.envelope(EnvelopeType.UserInputResolveRequest, scoped);
    envelope.session_id = this.sessionId;
    envelope.run_id = scoped.run_id;
    const response = await this.client.exchange('POST', this.path('/resolve'), envelope, EnvelopeType.UserInputResolveResponse);
    const resolved = payload<UserInputResolveResponse>(response);
    crossCheckPayload('input resolve response', resolved, response);
    if (resolved.interaction_id !== scoped.interaction_id) {
      throw new Error(
        `client: input resolve response names interaction "${resolved.interaction_id}", want "${scoped.interaction_id}"`,
      );
    }
  }

  async resolve(request: PermissionResolveInput | UserInputResolveInput): Promise<void> {
    if ('granted' in request) return this.resolvePermission(request);
    return this.resolveInput(request);
  }

  async cancel(runId: string, reason?: string): Promise<RunCancelResponse> {
    const requestPayload: RunCancelRequest = { session_id: this.sessionId, run_id: runId };
    if (reason !== undefined) requestPayload.reason = reason;
    const envelope = this.client.envelope(EnvelopeType.RunCancelRequest, requestPayload);
    envelope.session_id = this.sessionId;
    envelope.run_id = runId;
    const response = await this.client.exchange('POST', this.path('/cancel'), envelope, EnvelopeType.RunCancelResponse);
    const ack = payload<RunCancelResponse>(response);
    crossCheckPayload('cancel response', ack, response);
    return ack;
  }

  async state(): Promise<SessionState> {
    const response = await this.client.exchange('GET', this.path('/state'), null, EnvelopeType.SessionStateResponse);
    if (response.session_id !== this.sessionId) {
      throw new Error(
        `client: ${this.path('/state')} response is scoped to session "${response.session_id ?? ''}", want "${this.sessionId}"`,
      );
    }
    const state = payload<SessionState>(response);
    if (state.session_id !== response.session_id) {
      throw new Error(
        `client: ${this.path('/state')} payload names session "${state.session_id}", envelope "${response.session_id}"`,
      );
    }
    return state;
  }

  async tools(options: { allowDegradedFeatures?: string[] } = {}): Promise<ToolCatalog> {
    let path = this.path('/tools');
    if (options.allowDegradedFeatures?.length) {
      const query = new URLSearchParams();
      for (const key of options.allowDegradedFeatures) query.append('allow_degraded', key);
      path += `?${query.toString()}`;
    }
    const response = await this.client.exchange('GET', path, null, EnvelopeType.ActionToolsListResponse);
    if (response.session_id !== this.sessionId) {
      throw new Error(
        `client: ${this.path('/tools')} response is scoped to session "${response.session_id ?? ''}", want "${this.sessionId}"`,
      );
    }
    if (!response.capability_revision) {
      throw new Error(`client: ${this.path('/tools')} response carries no capability revision`);
    }
    const catalog = payload<ToolsListResponse>(response);
    if (catalog.session_id !== response.session_id) {
      throw new Error(
        `client: ${this.path('/tools')} payload names session "${catalog.session_id ?? ''}", envelope "${response.session_id}"`,
      );
    }
    return { revision: response.capability_revision, tools: catalog };
  }

  async models(options: ModelsOptions = {}): Promise<Catalog> {
    const query = (options.allowDegradedFeatures ?? [])
      .map((key) => `allow_degraded=${encodeURIComponent(key)}`)
      .join('&');
    const path = this.path('/models') + (query ? `?${query}` : '');
    const response = await this.client.exchange('GET', path, null, EnvelopeType.ModelsResponse);
    if (response.session_id !== this.sessionId) {
      throw new Error(
        `client: ${this.path('/models')} response is scoped to session "${response.session_id ?? ''}", want "${this.sessionId}"`,
      );
    }
    if (!response.capability_revision) {
      throw new Error(`client: ${this.path('/models')} response carries no capability revision`);
    }
    const catalog = payload<ModelsResponse>(response);
    if (catalog.session_id !== response.session_id) {
      throw new Error(
        `client: ${this.path('/models')} payload names session "${catalog.session_id}", envelope "${response.session_id}"`,
      );
    }
    return { revision: response.capability_revision, models: catalog };
  }

  async close(): Promise<void> {
    const response = await this.client.request(this.path('/close'), {
      method: 'POST',
      headers: { Accept: 'application/json' },
    });
    const body = await response.text();
    if (response.status !== 204) {
      const failure = this.client.failureError(response.status, body);
      if (failure) throw failure;
      throw new ServerError(response.status, '', `close returned status ${response.status}, want 204 No Content`);
    }
  }

  events(options: EventsOptions = {}): EventStream {
    return new EventStream(this, this.client, options, { startAfter: null, runId: '' });
  }

  eventsAfter(runId: string, after: number, options: EventsOptions = {}): EventStream {
    return new EventStream(this, this.client, options, { startAfter: after, runId });
  }

  path(suffix: string): string {
    return `/sessions/${encodeURIComponent(this.sessionId)}${suffix}`;
  }

  private scope(sessionId: string | undefined): string {
    if (sessionId === undefined || sessionId === '') return this.sessionId;
    if (sessionId !== this.sessionId) {
      throw new Error(`client: request session_id ${sessionId} does not match session ${this.sessionId}`);
    }
    return sessionId;
  }

  private resolveScope<T extends { session_id?: string; responded_by?: string }>(request: T): T {
    const scoped = { ...request };
    scoped.session_id = this.scope(scoped.session_id);
    if (scoped.responded_by === undefined || scoped.responded_by === '') scoped.responded_by = this.participant;
    return scoped;
  }
}

export type { SessionOpenRequest };

function crossCheckPayload(
  what: string,
  responsePayload: { session_id?: unknown; run_id?: unknown },
  envelope: Envelope,
): void {
  if (responsePayload.session_id !== undefined && responsePayload.session_id !== envelope.session_id) {
    throw new Error(
      `client: ${what} payload names session "${responsePayload.session_id}", envelope "${envelope.session_id ?? ''}"`,
    );
  }
  if (responsePayload.run_id !== undefined && responsePayload.run_id !== envelope.run_id) {
    throw new Error(`client: ${what} payload names run "${responsePayload.run_id}", envelope "${envelope.run_id ?? ''}"`);
  }
}

export function steerSettlementRequest(envelope: Envelope): string {
  switch (envelope.type) {
    case EnvelopeType.RunSteerApplied:
    case EnvelopeType.RunSteerDropped: {
      const body: unknown = envelope.payload;
      if (body === null || typeof body !== 'object' || Array.isArray(body)) return '';
      const request = (body as Record<string, unknown>).request_id;
      return typeof request === 'string' ? request : '';
    }
    default:
      return '';
  }
}

export function finalText(envelope: Envelope): string | null {
  if (envelope.type !== EnvelopeType.RunCompleted) return null;
  const completed = payload<RunCompletedPayload>(envelope);
  const content = completed.final_response?.content;
  return typeof content === 'string' ? content : null;
}
