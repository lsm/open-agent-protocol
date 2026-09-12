/**
 * Event-stream tests over the scripted transport: the cursor integrity
 * rules, the terminal signals, and the invisible-resume machinery, one
 * scenario each — the TypeScript port of the Go client's e2e stream tests.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { dial } from '../src/client.js';
import { OapSession } from '../src/session.js';
import {
  AbortedError,
  DisconnectError,
  DuplicateSequenceError,
  MalformedFrameError,
  OverflowError,
  ReplayGapError,
  ResumeMismatchError,
  SequenceGapError,
  ServerError,
} from '../src/errors.js';
import type { Envelope } from '../src/protocol.js';
import { EnvelopeType } from '../src/protocol.js';
import { FakeTransport, eventFrame, goldenFrames, signalFrame, testEnvelope, type ScriptedResponse } from './transport.js';

const BASE = 'http://127.0.0.1:6270';
const SESSION = 's-1';
const RUN = 'r-1';

/** The bare /events connection, with no cursor attached. */
const LIVE = /\/events$/;

function sessionWith(script: ScriptedResponse[], options: { strict?: boolean } = {}): { session: OapSession; transport: FakeTransport } {
  const transport = new FakeTransport(script);
  const client = dial(BASE, { fetch: transport.fetch, strictResume: options.strict ?? false });
  return { session: new OapSession(client, SESSION, 'memory', 'user'), transport };
}

async function collect(stream: AsyncIterable<Envelope>): Promise<Envelope[]> {
  const envelopes: Envelope[] = [];
  for await (const envelope of stream) envelopes.push(envelope);
  return envelopes;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
async function rejectsWith<T extends Error>(promise: Promise<unknown>, type: new (...args: any[]) => T, check?: (err: T) => void): Promise<T> {
  try {
    await promise;
  } catch (err) {
    assert.ok(err instanceof type, `expected ${type.name}, got ${String(err)}`);
    if (check) check(err);
    return err;
  }
  throw new Error(`expected rejection with ${type.name}`);
}

function sequences(envelopes: Envelope[]): number[] {
  return envelopes.map((envelope) => envelope.sequence ?? 0);
}

test('a live run delivers in order and ends cleanly at its terminal event', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 12) }]);
  const envelopes = await collect(session.events());
  assert.equal(envelopes.length, 12);
  assert.deepEqual(sequences(envelopes), Array.from({ length: 12 }, (_, i) => i + 1));
  assert.equal(envelopes[11].type, EnvelopeType.RunCompleted);
});

test('a mid-stream drop resumes from the cursor with no duplicates', { timeout: 10000 }, async () => {
  const { session, transport } = sessionWith([
    { match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 3) },
    { match: /after=3$/, chunks: goldenFrames(SESSION, RUN, 4, 12) },
  ]);
  const envelopes = await collect(session.events());
  assert.deepEqual(sequences(envelopes), Array.from({ length: 12 }, (_, i) => i + 1));

  const resumed = transport.callsFor('/events');
  assert.equal(resumed.length, 2);
  assert.equal(resumed[1].url.endsWith('?after=3'), true);
  assert.equal(resumed[1].init?.headers?.['Last-Event-ID'], '3');
});

test('a drop after a terminal event is the clean end, not a disconnect', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 12) }]);
  const envelopes = await collect(session.events());
  assert.equal(envelopes.length, 12);
});

test('a transport failure after a drop backs off and retries the cursor', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    { match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 3) },
    { match: /after=3$/, rejectWith: new Error('connection reset') },
    { match: /after=3$/, chunks: goldenFrames(SESSION, RUN, 4, 12) },
  ]);
  const envelopes = await collect(session.events());
  assert.deepEqual(sequences(envelopes), Array.from({ length: 12 }, (_, i) => i + 1));
});

test('strict mode reports a drop as a DisconnectError carrying the cursor', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 3) }], { strict: true });
  await rejectsWith(collect(session.events()), DisconnectError, (err) => {
    assert.equal(err.runId, RUN);
    assert.equal(err.lastSequence, 3);
  });
});

test('a drop before the first delivery speculatively replays from the run start', { timeout: 10000 }, async () => {
  const noRun = JSON.stringify(
    testEnvelope({
      type: EnvelopeType.ErrorResponse,
      payload: { error: { code: 'no_run_to_resume', message: 'the session has no run to replay' } },
    }),
  );
  const { session, transport } = sessionWith([
    { match: LIVE, chunks: [] },
    { match: /after=0$/, status: 409, body: noRun },
    { match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 12) },
  ]);
  const envelopes = await collect(session.events());
  assert.deepEqual(sequences(envelopes), Array.from({ length: 12 }, (_, i) => i + 1));
  const calls = transport.callsFor('/events');
  assert.equal(calls.length, 3);
  assert.equal(calls[1].url.endsWith('?after=0'), true);
  assert.equal(calls[2].url.endsWith('/events'), true);
});

test('repeated empty connections surface as a DisconnectError', { timeout: 10000 }, async () => {
  const { session, transport } = sessionWith([
    { match: /after=12$/, chunks: [] },
    { match: /after=12$/, chunks: [] },
    { match: /after=12$/, chunks: [] },
  ]);
  await rejectsWith(
    collect(session.eventsAfter(RUN, 12)),
    DisconnectError,
    (err) => assert.match(err.message, /without receiving an event/),
  );
  assert.equal(transport.callsFor('/events').length, 3);
});

test('the overflow signal surfaces the consumed run and its own cursor', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: LIVE,
      chunks: [
        ...goldenFrames(SESSION, RUN, 1, 5),
        signalFrame('oap-overflow', { run_id: 'newer-run', last_sequence: 99, message: 'fell behind' }),
      ],
    },
  ]);
  await rejectsWith(collect(session.events()), OverflowError, (err) => {
    assert.equal(err.runId, RUN); // the run this stream consumed, not the hub's current run
    assert.equal(err.lastSequence, 5);
    assert.equal(err.signalMessage, 'fell behind');
  });
});

test('an overflow before the first envelope uses the signal fields', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: LIVE,
      chunks: [signalFrame('oap-overflow', { run_id: 'r-9', last_sequence: 7, message: 'fell behind' })],
    },
  ]);
  await rejectsWith(collect(session.events()), OverflowError, (err) => {
    assert.equal(err.runId, 'r-9');
    assert.equal(err.lastSequence, 7);
  });
});

test('the replay-gap signal surfaces the retention bounds', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: /after=2$/,
      chunks: [
        signalFrame('oap-replay-gap', {
          requested_after: 2,
          oldest_available: 9,
          latest_available: 12,
          message: 'expired',
        }),
      ],
    },
  ]);
  await rejectsWith(collect(session.eventsAfter(RUN, 2)), ReplayGapError, (err) => {
    assert.equal(err.runId, RUN);
    assert.equal(err.requestedAfter, 2);
    assert.equal(err.oldestAvailable, 9);
    assert.equal(err.latestAvailable, 12);
  });
});

test('eventsAfter replays the suffix of the run it is bound to', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: /after=10$/, chunks: goldenFrames(SESSION, RUN, 11, 12) }]);
  const envelopes = await collect(session.eventsAfter(RUN, 10));
  assert.deepEqual(sequences(envelopes), [11, 12]);
  assert.equal(envelopes[1].type, EnvelopeType.RunCompleted);
});

test('a replayed suffix for another run raises a ResumeMismatchError', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: /after=10$/,
      chunks: goldenFrames(SESSION, 'r-other', 11, 11),
    },
  ]);
  await rejectsWith(collect(session.eventsAfter(RUN, 10)), ResumeMismatchError, (err) => {
    assert.equal(err.afterSequence, 10);
    assert.equal(err.expectedRunId, RUN);
    assert.equal(err.observedRunId, 'r-other');
    assert.equal(err.observedSequence, 11);
  });
});

test('a duplicate sequence on a resumed stream is a wire defect', { timeout: 10000 }, async () => {
  const frames = goldenFrames(SESSION, RUN, 1, 2);
  const duplicate = goldenFrames(SESSION, RUN, 2, 2);
  const { session } = sessionWith([{ match: LIVE, chunks: [...frames, ...duplicate] }]);
  await rejectsWith(collect(session.events()), DuplicateSequenceError, (err) => {
    assert.equal(err.sequence, 2);
    assert.equal(err.runId, RUN);
  });
});

test('a skipped sequence is surfaced, never silently accepted', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    { match: LIVE, chunks: [...goldenFrames(SESSION, RUN, 1, 2), ...goldenFrames(SESSION, RUN, 4, 4)] },
  ]);
  await rejectsWith(collect(session.events()), SequenceGapError, (err) => {
    assert.equal(err.expected, 3);
    assert.equal(err.observed, 4);
  });
});

test('a live run change must begin at sequence one', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: LIVE,
      chunks: [...goldenFrames(SESSION, RUN, 1, 3), ...goldenFrames(SESSION, 'r-next', 2, 2)],
    },
  ]);
  await rejectsWith(collect(session.events()), SequenceGapError, (err) => {
    assert.equal(err.expected, 1);
    assert.equal(err.observed, 2);
    assert.equal(err.runId, 'r-next');
  });
});

test('a live run change that begins at one continues cleanly', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: LIVE,
      chunks: [...goldenFrames(SESSION, RUN, 1, 3), ...goldenFrames(SESSION, 'r-next', 1, 12)],
    },
  ]);
  const envelopes = await collect(session.events());
  assert.deepEqual(sequences(envelopes), [1, 2, 3, ...Array.from({ length: 12 }, (_, i) => i + 1)]);
});

test('a fresh live subscription may join a run in progress', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 7, 12) }]);
  const envelopes = await collect(session.events());
  assert.deepEqual(sequences(envelopes), Array.from({ length: 6 }, (_, i) => i + 7));
});

test('an envelope for another session never belongs on this stream', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames('s-other', RUN, 1, 1) }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /envelope for session "s-other"/),
  );
});

test('an envelope without a sequence is malformed', { timeout: 10000 }, async () => {
  const frame = eventFrame(testEnvelope({ type: EnvelopeType.RunStarted, sessionId: SESSION, runId: RUN }));
  const { session } = sessionWith([{ match: LIVE, chunks: [frame] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /no sequence/),
  );
});

test('an envelope without a run id is malformed', { timeout: 10000 }, async () => {
  const missing = testEnvelope({ type: EnvelopeType.RunStarted, sequence: 1, sessionId: SESSION });
  const { session } = sessionWith([{ match: LIVE, chunks: [eventFrame(missing)] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /no run id/),
  );
  const wrongTyped = JSON.stringify({
    ...testEnvelope({ type: EnvelopeType.RunStarted, sequence: 1, sessionId: SESSION }),
    run_id: 7,
  });
  const other = sessionWith([{ match: LIVE, chunks: [`data: ${wrongTyped}\n\n`] }]);
  await rejectsWith(collect(other.session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /no run id/),
  );
});

test('a payload naming another session, run, or tool call is malformed', { timeout: 10000 }, async () => {
  const foreignSession = testEnvelope({
    type: EnvelopeType.RunStarted,
    sequence: 1,
    sessionId: SESSION,
    runId: RUN,
    payload: { session_id: 's-other', run_id: RUN },
  });
  const first = sessionWith([{ match: LIVE, chunks: [eventFrame(foreignSession)] }]);
  await rejectsWith(collect(first.session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /payload names session "s-other"/),
  );
  const foreignRun = testEnvelope({
    type: EnvelopeType.ContentDelta,
    sequence: 2,
    sessionId: SESSION,
    runId: RUN,
    payload: { session_id: SESSION, run_id: 'r-other' },
  });
  const second = sessionWith([{ match: LIVE, chunks: [eventFrame(foreignRun)] }]);
  await rejectsWith(collect(second.session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /payload names run "r-other"/),
  );
  const foreignToolCall = testEnvelope({
    type: EnvelopeType.ActionCallStarted,
    sequence: 3,
    sessionId: SESSION,
    runId: RUN,
    toolCallId: 'tc-1',
    payload: { session_id: SESSION, run_id: RUN, tool_call_id: 'tc-other', execution_owner: 'user', name: 'echo' },
  });
  const third = sessionWith([{ match: LIVE, chunks: [eventFrame(foreignToolCall)] }]);
  await rejectsWith(collect(third.session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /payload names tool call "tc-other"/),
  );
});

test('a frame id disagreeing with its envelope sequence is malformed', { timeout: 10000 }, async () => {
  const envelope = testEnvelope({
    type: EnvelopeType.RunStarted,
    sequence: 2,
    sessionId: SESSION,
    runId: RUN,
    payload: {},
  });
  const mismatched = `id: 3\ndata: ${JSON.stringify(envelope)}\n\n`;
  const { session } = sessionWith([{ match: LIVE, chunks: [mismatched] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /disagrees with envelope sequence 2/),
  );
});

test('a non-numeric frame id is malformed', { timeout: 10000 }, async () => {
  const envelope = testEnvelope({
    type: EnvelopeType.RunStarted,
    sequence: 1,
    sessionId: SESSION,
    runId: RUN,
    payload: {},
  });
  const frame = `id: 1st\ndata: ${JSON.stringify(envelope)}\n\n`;
  const { session } = sessionWith([{ match: LIVE, chunks: [frame] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /is not a sequence/),
  );
});

test('a message frame that is not an envelope is malformed', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: ['data: not json{\n\n'] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /not an envelope/),
  );
});

test('an undecodable signal payload is malformed', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: ['event: oap-overflow\ndata: {\n\n'] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /signal frame payload did not decode/),
  );
});

test('a signal that is not a JSON object is malformed', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: ['event: oap-overflow\ndata: []\n\n'] }]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /signal frame payload did not decode/),
  );
});

test('a wrong-typed signal field is malformed, never a fabricated cursor', { timeout: 10000 }, async () => {
  const overflow = sessionWith([
    {
      match: LIVE,
      chunks: [
        ...goldenFrames(SESSION, RUN, 1, 2),
        'event: oap-overflow\ndata: {"run_id": 7, "last_sequence": 5, "message": "fell behind"}\n\n',
      ],
    },
  ]);
  await rejectsWith(collect(overflow.session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /signal field run_id is not a string/),
  );
  const gap = sessionWith([
    {
      match: /after=2$/,
      chunks: ['event: oap-replay-gap\ndata: {"requested_after": "2", "oldest_available": 9}\n\n'],
    },
  ]);
  await rejectsWith(collect(gap.session.eventsAfter(RUN, 2)), MalformedFrameError, (err) =>
    assert.match(err.detail, /signal field requested_after is not a sequence/),
  );
});

test('a fractional signal sequence is malformed', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: LIVE,
      chunks: ['event: oap-overflow\ndata: {"run_id": "r-9", "last_sequence": 1.5, "message": "fell behind"}\n\n'],
    },
  ]);
  await rejectsWith(collect(session.events()), MalformedFrameError, (err) =>
    assert.match(err.detail, /signal field last_sequence is not a sequence/),
  );
});

test('unknown named events are skipped, keeping the stream forward-compatible', { timeout: 10000 }, async () => {
  const { session } = sessionWith([
    {
      match: LIVE,
      chunks: [
        ': keepalive\n\n',
        'event: oap-ping\ndata: {}\n\n',
        ...goldenFrames(SESSION, RUN, 1, 2),
        'event: oap-ping\ndata: {}\n\n',
        ...goldenFrames(SESSION, RUN, 3, 12),
      ],
    },
  ]);
  const envelopes = await collect(session.events());
  assert.deepEqual(sequences(envelopes), Array.from({ length: 12 }, (_, i) => i + 1));
});

test('a non-200 stream response is a ServerError', { timeout: 10000 }, async () => {
  const closed = JSON.stringify(
    testEnvelope({
      type: EnvelopeType.ErrorResponse,
      payload: { error: { code: 'session_closed', message: 'the session is closed' } },
    }),
  );
  const { session } = sessionWith([{ match: /events/, status: 409, body: closed }]);
  await rejectsWith(collect(session.events()), ServerError, (err) => {
    assert.equal(err.status, 409);
    assert.equal(err.code, 'session_closed');
  });
});

test('a 200 without the event-stream content type is a ServerError', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: /events/, body: '<html></html>', headers: { 'content-type': 'text/html' } }]);
  await rejectsWith(collect(session.events()), ServerError, (err) =>
    assert.match(err.message, /want text\/event-stream/),
  );
});

test('an initial connection failure surfaces at the first iteration', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: /events/, rejectWith: new Error('connection refused') }]);
  await rejectsWith(collect(session.events()), Error, (err) =>
    assert.match(err.message, /connection refused/),
  );
});

test('aborting the signal ends the stream with an AbortedError', { timeout: 10000 }, async () => {
  const controller = new AbortController();
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 12), chunkDelayMs: 5 }]);
  const collected: number[] = [];
  await rejectsWith(
    (async () => {
      for await (const envelope of session.events({ signal: controller.signal })) {
        collected.push(envelope.sequence ?? 0);
        if (collected.length >= 2) controller.abort();
      }
    })(),
    AbortedError,
  );
  assert.ok(collected.length >= 2);
});

test('a second iteration resumes the same stream instead of rewinding', { timeout: 10000 }, async () => {
  const { session } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 12) }]);
  const stream = session.events();
  const iterator = stream[Symbol.asyncIterator]();
  const first = await iterator.next();
  assert.equal(first.value?.sequence, 1);
  assert.equal((await stream[Symbol.asyncIterator]().next()).value?.sequence, 2);
  await iterator.return?.();
});

test('ready resolves once the subscription is live', { timeout: 10000 }, async () => {
  const { session, transport } = sessionWith([{ match: LIVE, chunks: goldenFrames(SESSION, RUN, 1, 12), delayMs: 10 }]);
  const stream = session.events();
  await stream.ready;
  assert.equal(transport.callsFor('/events').length, 1);
  await collect(stream);
});
