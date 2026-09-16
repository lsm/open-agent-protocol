/**
 * Typed client errors. Each class names the exact wire condition that raised
 * it; `instanceof` discriminates them. The client never logs and carries no
 * credentials: the daemon is a single-user local service.
 */

import type { Envelope } from './protocol.js';

/** Base class of every client error, carrying an optional cause. */
export class OapError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options);
    this.name = new.target.name;
  }
}

/** A daemon error.response for one request: a correlated, schema-valid envelope describing why it was refused. */
export class ServerError extends OapError {
  /** HTTP status code. */
  readonly status: number;
  /** The error.response code, e.g. "unknown_session"; empty when the daemon answered with a non-envelope body. */
  readonly code: string;
  /** The daemon's error message on its own; `message` carries the full rendered text. */
  readonly serverMessage: string;
  /**
   * The error's typed details, when it carries any. A refused run control
   * names what to change there: `feature` and `reason` on
   * `unsupported_feature`, `feature` on `capability_degraded`, `model_id` on
   * `model_not_found`.
   */
  readonly details?: Record<string, unknown>;
  /** The full error envelope; absent when the body carried none. */
  readonly envelope?: Envelope;

  constructor(
    status: number,
    code: string,
    serverMessage: string,
    envelope?: Envelope,
    details?: Record<string, unknown>,
  ) {
    super(
      code
        ? `client: server error ${code} (status ${status}): ${serverMessage}`
        : `client: server error (status ${status}): ${serverMessage}`,
    );
    this.status = status;
    this.code = code;
    this.serverMessage = serverMessage;
    this.envelope = envelope;
    this.details = details;
  }
}

/** Reports the daemon error code carried by err, or null when err carries no coded ServerError. */
export function serverCode(err: unknown): string | null {
  return err instanceof ServerError && err.code !== '' ? err.code : null;
}

/**
 * The daemon's oap-overflow signal: this connection's bounded buffer fell
 * behind. `lastSequence` is the last sequence delivered on the stream; resume
 * with `session.eventsAfter(runId, lastSequence)`.
 */
export class OverflowError extends OapError {
  readonly runId: string;
  readonly lastSequence: number;
  readonly signalMessage: string;

  constructor(runId: string, lastSequence: number, signalMessage: string) {
    super(
      `client: event stream overflowed behind sequence ${lastSequence} (run ${runId}); resume with a cursor after it`,
    );
    this.runId = runId;
    this.lastSequence = lastSequence;
    this.signalMessage = signalMessage;
  }
}

/**
 * The daemon's oap-replay-gap signal: the requested cursor is no longer
 * retained. `oldestAvailable`/`latestAvailable` bound what is; a consumer
 * that accepts the loss resumes with a cursor at or after
 * `oldestAvailable - 1`, bound to `runId` when the stream knew it.
 */
export class ReplayGapError extends OapError {
  readonly runId: string;
  readonly requestedAfter: number;
  readonly oldestAvailable: number;
  readonly latestAvailable: number;
  readonly signalMessage: string;

  constructor(
    runId: string,
    requestedAfter: number,
    oldestAvailable: number,
    latestAvailable: number,
    signalMessage: string,
  ) {
    const floor = oldestAvailable > 1 ? oldestAvailable - 1 : 0;
    super(
      `client: replay cursor ${requestedAfter} expired (retained ${oldestAvailable} through ${latestAvailable}); resume at or after ${floor}`,
    );
    this.runId = runId;
    this.requestedAfter = requestedAfter;
    this.oldestAvailable = oldestAvailable;
    this.latestAvailable = latestAvailable;
    this.signalMessage = signalMessage;
  }
}

/**
 * A dropped event stream that was not resumed: strict mode reports every
 * drop, and auto-resume reports a stream that ends repeatedly without
 * events. `runId`/`lastSequence` are the stream's last observed position;
 * resume with `session.eventsAfter(runId, lastSequence)`.
 */
export class DisconnectError extends OapError {
  readonly runId: string;
  readonly lastSequence: number;

  constructor(runId: string, lastSequence: number, cause?: unknown) {
    super(`client: event stream disconnected after sequence ${lastSequence} (run ${runId}): ${describe(cause)}`, {
      cause,
    });
    this.runId = runId;
    this.lastSequence = lastSequence;
  }
}

/** A stream frame the client cannot interpret: a non-envelope message frame, an undecodable signal, or an id field disagreeing with the envelope it frames. */
export class MalformedFrameError extends OapError {
  readonly detail: string;

  constructor(detail: string, cause?: unknown) {
    super(
      cause === undefined
        ? `client: malformed event stream frame: ${detail}`
        : `client: malformed event stream frame: ${detail}: ${describe(cause)}`,
      { cause },
    );
    this.detail = detail;
  }
}

/** An envelope whose (run, sequence) position was already delivered: a resumed stream replayed what the consumer already saw. */
export class DuplicateSequenceError extends OapError {
  readonly runId: string;
  readonly sequence: number;

  constructor(runId: string, sequence: number) {
    super(`client: duplicate sequence ${sequence} in run ${runId}`);
    this.runId = runId;
    this.sequence = sequence;
  }
}

/** An envelope that skipped one or more sequences in its run: an envelope was lost in transit or never published. */
export class SequenceGapError extends OapError {
  readonly runId: string;
  readonly expected: number;
  readonly observed: number;

  constructor(runId: string, expected: number, observed: number) {
    super(`client: run ${runId} skipped from sequence ${expected} to ${observed}`);
    this.runId = runId;
    this.expected = expected;
    this.observed = observed;
  }
}

/** A replayed suffix that does not continue the stream it was asked to: the run changed under the cursor, or the first replayed sequence is not the cursor plus one. */
export class ResumeMismatchError extends OapError {
  readonly afterSequence: number;
  readonly expectedRunId: string;
  readonly observedRunId: string;
  readonly observedSequence: number;

  constructor(afterSequence: number, expectedRunId: string, observedRunId: string, observedSequence: number) {
    super(
      `client: replay after sequence ${afterSequence} continued run ${observedRunId} at sequence ${observedSequence}, want run ${expectedRunId} at ${afterSequence + 1}`,
    );
    this.afterSequence = afterSequence;
    this.expectedRunId = expectedRunId;
    this.observedRunId = observedRunId;
    this.observedSequence = observedSequence;
  }
}

/** The operation was cancelled through its AbortSignal (the counterpart of Go's context.Canceled). */
export class AbortedError extends OapError {
  constructor(detail = 'operation aborted') {
    super(`client: ${detail}`);
  }
}

function describe(cause: unknown): string {
  if (cause instanceof Error) return cause.message;
  if (cause === undefined || cause === null) return 'connection ended';
  return String(cause);
}
