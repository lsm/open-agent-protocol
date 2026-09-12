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
  type PermissionResolveRequest,
  type RunCancelRequest,
  type RunCancelResponse,
  type RunCompletedPayload,
  type SessionOpenRequest,
  type SessionState,
  type UserInputResolveRequest,
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
    return payload<MessageSubmitResponse>(response);
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
    await this.client.exchange(
      'POST',
      this.path('/resolve'),
      envelope,
      EnvelopeType.ActionPermissionResolveResponse,
    );
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
    await this.client.exchange('POST', this.path('/resolve'), envelope, EnvelopeType.UserInputResolveResponse);
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
    return payload<RunCancelResponse>(response);
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
    return payload<SessionState>(response);
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
 * Returns the final-response text of a run.completed envelope, or null for
 * any other envelope or a non-text final response.
 */
export function finalText(envelope: Envelope): string | null {
  if (envelope.type !== EnvelopeType.RunCompleted) return null;
  const completed = payload<RunCompletedPayload>(envelope);
  const content = completed.final_response?.content;
  return typeof content === 'string' ? content : null;
}
