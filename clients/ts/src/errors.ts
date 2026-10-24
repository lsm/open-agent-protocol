
import type { Envelope } from './protocol.js';

export class OapError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options);
    this.name = new.target.name;
  }
}

export class ServerError extends OapError {
  readonly status: number;
  readonly code: string;
  readonly serverMessage: string;
  readonly details?: Record<string, unknown>;
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

export function serverCode(err: unknown): string | null {
  return err instanceof ServerError && err.code !== '' ? err.code : null;
}

export class OverflowError extends OapError {
  readonly runId: string;
  readonly lastSequence: bigint;
  readonly signalMessage: string;

  constructor(runId: string, lastSequence: bigint, signalMessage: string) {
    super(
      `client: event stream overflowed behind sequence ${lastSequence} (run ${runId}); resume with a cursor after it`,
    );
    this.runId = runId;
    this.lastSequence = lastSequence;
    this.signalMessage = signalMessage;
  }
}

export class ReplayGapError extends OapError {
  readonly runId: string;
  readonly requestedAfter: bigint;
  readonly oldestAvailable: bigint;
  readonly latestAvailable: bigint;
  readonly signalMessage: string;

  constructor(
    runId: string,
    requestedAfter: bigint,
    oldestAvailable: bigint,
    latestAvailable: bigint,
    signalMessage: string,
  ) {
    const floor = oldestAvailable > 1n ? oldestAvailable - 1n : 0n;
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

export class DisconnectError extends OapError {
  readonly runId: string;
  readonly lastSequence: bigint;

  constructor(runId: string, lastSequence: bigint, cause?: unknown) {
    super(`client: event stream disconnected after sequence ${lastSequence} (run ${runId}): ${describe(cause)}`, {
      cause,
    });
    this.runId = runId;
    this.lastSequence = lastSequence;
  }
}

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

export class DuplicateSequenceError extends OapError {
  readonly runId: string;
  readonly sequence: bigint;

  constructor(runId: string, sequence: bigint) {
    super(`client: duplicate sequence ${sequence} in run ${runId}`);
    this.runId = runId;
    this.sequence = sequence;
  }
}

export class SequenceGapError extends OapError {
  readonly runId: string;
  readonly expected: bigint;
  readonly observed: bigint;

  constructor(runId: string, expected: bigint, observed: bigint) {
    super(`client: run ${runId} skipped from sequence ${expected} to ${observed}`);
    this.runId = runId;
    this.expected = expected;
    this.observed = observed;
  }
}

export class ResumeMismatchError extends OapError {
  readonly afterSequence: bigint;
  readonly expectedRunId: string;
  readonly observedRunId: string;
  readonly observedSequence: bigint;

  constructor(afterSequence: bigint, expectedRunId: string, observedRunId: string, observedSequence: bigint) {
    super(
      `client: replay after sequence ${afterSequence} continued run ${observedRunId} at sequence ${observedSequence}, want run ${expectedRunId} at ${afterSequence + 1n}`,
    );
    this.afterSequence = afterSequence;
    this.expectedRunId = expectedRunId;
    this.observedRunId = observedRunId;
    this.observedSequence = observedSequence;
  }
}

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
