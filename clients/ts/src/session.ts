/**
 * One open adapter session on the daemon. The methods mirror the daemon's
 * HTTP operations one-to-one; submit resolves with the admission, and run
 * events arrive through events().
 */

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
  type SessionOpenRequest,
  type SessionState,
  type ToolsListResponse,
  type UserInputResolveRequest,
  type UserInputResolveResponse,
} from './protocol.js';

/** submit() input: the payload session_id is optional and filled from the session. */
export type SubmitInput = Omit<MessageSubmitRequest, 'session_id'> & { session_id?: string };

/** resolvePermission() input: the payload session_id and responded_by are optional and filled from the session. */
export type PermissionResolveInput = Omit<PermissionResolveRequest, 'session_id' | 'responded_by'> & {
  session_id?: string;
  responded_by?: string;
};

/** resolveInput() input: the payload session_id and responded_by are optional and filled from the session. */
export type UserInputResolveInput = Omit<UserInputResolveRequest, 'session_id' | 'responded_by'> & {
  session_id?: string;
  responded_by?: string;
};

/** models() input: the per-query degraded opt-in, absent by default. */
export type ModelsOptions = { allowDegradedFeatures?: string[] };

/**
 * One session's model listing together with the capability revision that
 * governs it, mirroring the shape capabilities() returns.
 *
 * The revision is not decoration. The catalog is part of the capability
 * snapshot, so it is valid for exactly that revision: a caller caches it
 * against the revision and discards it when the descriptor moves. The payload
 * alone would leave a caller unable to tell which `models.list` promise it
 * read, and a later probe may already report a different revision than the one
 * the listing came under.
 */
export type Catalog = { revision: string; models: ModelsResponse };

/** open() input; see OapClient.open. */
export type OpenOptions = { sessionId?: string; participant?: string };

export class OapSession {
  constructor(
    private readonly client: OapClient,
    private readonly sessionId: string,
    private readonly adapterName: string,
    private readonly participant: string,
  ) {}

  /** The daemon-confirmed session identifier. */
  get id(): string {
    return this.sessionId;
  }

  /** The adapter name the session was opened on. */
  get adapter(): string {
    return this.adapterName;
  }

  /** The responder identity gate resolutions use when none is given. */
  get responder(): string {
    return this.participant;
  }

  /**
   * Admits one message submission and returns the adapter's admission. A
   * missing session_id is filled from the session; a mismatching one is
   * refused before the wire.
   */
  async submit(request: SubmitInput): Promise<MessageSubmitResponse> {
    const scoped = { ...request, session_id: this.scope(request.session_id) };
    const envelope = this.client.envelope(EnvelopeType.SessionMessageSubmitRequest, scoped);
    envelope.session_id = this.sessionId;
    const response = await this.client.exchange(
      'POST',
      this.path('/submit'),
      envelope,
      EnvelopeType.SessionMessageSubmitResponse,
    );
    const admission = payload<MessageSubmitResponse>(response);
    crossCheckPayload('submit response', admission, response);
    return admission;
  }

  /**
   * Resolves one pending permission gate. interaction_id, run_id, and
   * requested_by must echo the action.permission.requested payload; a
   * missing session_id or responded_by is filled from the session.
   */
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

  /**
   * Resolves one pending user-input gate. interaction_id, run_id, and
   * requested_by must echo the user.input.requested payload; a missing
   * session_id or responded_by is filled from the session.
   */
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

  /**
   * Resolves one pending gate of either kind: a payload carrying `granted`
   * resolves a permission gate, one carrying `answers` resolves a
   * user-input gate.
   */
  async resolve(request: PermissionResolveInput | UserInputResolveInput): Promise<void> {
    if ('granted' in request) return this.resolvePermission(request);
    return this.resolveInput(request);
  }

  /**
   * Requests cancellation of one run and returns the acknowledgement. The
   * confirmed run.cancelled event on the event stream is authoritative.
   */
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

  /** Reads the session's authoritative state. */
  async state(): Promise<SessionState> {
    const response = await this.client.exchange('GET', this.path('/state'), null, EnvelopeType.SessionStateResponse);
    // The GET carries no request envelope, so the exchange's request-based
    // scope check never runs: verify the response names this session before
    // treating it as this session's authoritative state.
    if (response.session_id !== this.sessionId) {
      throw new Error(
        `client: ${this.path('/state')} response is scoped to session "${response.session_id ?? ''}", want "${this.sessionId}"`,
      );
    }
    const state = payload<SessionState>(response);
    // The payload and the envelope naming it are individually schema-valid;
    // the protocol binds them to one scope, so a payload naming another
    // session is not this session's state.
    if (state.session_id !== response.session_id) {
      throw new Error(
        `client: ${this.path('/state')} payload names session "${state.session_id}", envelope "${response.session_id}"`,
      );
    }
    return state;
  }

  /**
   * Reads this session's effective tool catalog: its tools, each attributed
   * to a source id, and every source the session resolves. The degraded
   * opt-in is sent only when given, so an unmodified call is byte-identical
   * to one made before the option existed.
   *
   * An endpoint that serves no portable catalog answers the typed
   * `unsupported_feature` refusal naming `action.tools.list`, which surfaces
   * as a ServerError whose details say which capability to stop requesting.
   */
  async tools(options: { allowDegradedFeatures?: string[] } = {}): Promise<ToolsListResponse> {
    let path = this.path('/tools');
    if (options.allowDegradedFeatures?.length) {
      const query = new URLSearchParams();
      for (const key of options.allowDegradedFeatures) query.append('allow_degraded', key);
      path += `?${query.toString()}`;
    }
    const response = await this.client.exchange('GET', path, null, EnvelopeType.ActionToolsListResponse);
    // The GET carries no request envelope, so the exchange's request-based
    // scope check never runs: a catalog that names another session is not
    // this session's catalog.
    if (response.session_id !== this.sessionId) {
      throw new Error(
        `client: ${this.path('/tools')} response is scoped to session "${response.session_id ?? ''}", want "${this.sessionId}"`,
      );
    }
    const catalog = payload<ToolsListResponse>(response);
    // The payload must name this session too. `session_id` is optional on a
    // catalog payload — an endpoint-level catalog belongs to no session — but
    // it is the answer to an unscoped request, and this call never sends one:
    // it asks for this session's effective catalog. An unscoped answer would
    // be missing exactly the sources this session attached at open, handed
    // back as its effective catalog.
    if (catalog.session_id !== response.session_id) {
      throw new Error(
        `client: ${this.path('/tools')} payload names session "${catalog.session_id ?? ''}", envelope "${response.session_id}"`,
      );
    }
    return catalog;
  }

  /**
   * Reads the session's effective model catalog: the models a submission may
   * select, and the one the session would use without a selection.
   *
   * `allowDegradedFeatures` opts into the degraded application of the named
   * capability keys for this query alone. An endpoint advertising
   * `models.list` as degraded refuses a query without it, so the option is
   * what makes a degraded catalog readable at all; a call without it is
   * identical to one made before the option existed.
   */
  async models(options: ModelsOptions = {}): Promise<Catalog> {
    const query = (options.allowDegradedFeatures ?? [])
      .map((key) => `allow_degraded=${encodeURIComponent(key)}`)
      .join('&');
    const path = this.path('/models') + (query ? `?${query}` : '');
    const response = await this.client.exchange('GET', path, null, EnvelopeType.ModelsResponse);
    // The GET carries no request envelope, so the exchange's request-based
    // scope check never runs: verify the response names this session before
    // reading it as this session's catalog.
    if (response.session_id !== this.sessionId) {
      throw new Error(
        `client: ${this.path('/models')} response is scoped to session "${response.session_id ?? ''}", want "${this.sessionId}"`,
      );
    }
    // The revision is checked with the scope, and for the same reason: a
    // listing nothing can bind to a descriptor cannot be cached or
    // invalidated, so it is refused rather than handed back with an empty
    // revision the caller has to notice on its own.
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

  /** Closes the session. An active run refuses the close; cancel it first. */
  async close(): Promise<void> {
    const response = await this.client.request(this.path('/close'), {
      method: 'POST',
      headers: { Accept: 'application/json' },
    });
    const body = await response.text();
    if (response.status !== 204) {
      const failure = this.client.failureError(response.status, body);
      if (failure) throw failure;
      // The close contract is exactly 204 No Content: any other success — a
      // proxy page, an incompatible daemon — is not a confirmation.
      throw new ServerError(response.status, '', `close returned status ${response.status}, want 204 No Content`);
    }
  }

  /**
   * Returns the session's event stream as a live async iterable. The
   * subscription is opened before events() returns — the daemon registers it
   * once the response begins — so the canonical order (events before
   * submit) cannot miss the run's first envelope; a subscription opened
   * mid-run receives only events from that point on.
   *
   * Envelopes arrive in order. By default a dropped connection is resumed
   * invisibly: the client reconnects with the last observed sequence as the
   * Last-Event-ID / ?after= cursor, the daemon replays the suffix, and the
   * stream continues without duplicates. Iteration ends cleanly once a
   * run's terminal event has been delivered; every other error is terminal
   * for the stream.
   */
  events(options: EventsOptions = {}): EventStream {
    return new EventStream(this, this.client, options, { startAfter: null, runId: '' });
  }

  /**
   * Returns the event stream replayed from a cursor: the given run's
   * envelopes after the sequence first, then live events. It is the manual
   * resume path for consumers holding a cursor from an OverflowError,
   * ReplayGapError, or DisconnectError — pass the error's runId so the
   * replay is bound to the run the cursor belongs to; if the daemon's
   * current run no longer matches, the stream raises a ResumeMismatchError
   * instead of silently mixing runs. An empty runId binds to whichever run
   * is current.
   */
  eventsAfter(runId: string, after: number, options: EventsOptions = {}): EventStream {
    return new EventStream(this, this.client, options, { startAfter: after, runId });
  }

  /** Builds one session-scoped path with the session id escaped. */
  path(suffix: string): string {
    return `/sessions/${encodeURIComponent(this.sessionId)}${suffix}`;
  }

  /** Fills or verifies the payload session id the daemon's scope check compares against the addressed session. */
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

/** One session.open request payload, re-exported for callers minting custom opens. */
export type { SessionOpenRequest };

/**
 * Verifies that a response payload's scope fields agree with the envelope the
 * exchange already correlated and scope-checked: a payload naming another
 * session or run is another operation's answer, not this one's.
 */
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

/**
 * Returns the final-response text of a run.completed envelope, or null for
 * any other envelope or a non-text final response.
 */
export function finalText(envelope: Envelope): string | null {
  if (envelope.type !== EnvelopeType.RunCompleted) return null;
  const completed = payload<RunCompletedPayload>(envelope);
  const content = completed.final_response?.content;
  return typeof content === 'string' ? content : null;
}
