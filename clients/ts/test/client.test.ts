/**
 * Client and session request-plumbing tests over the scripted transport:
 * discovery, open, the session operations, and every refusal path.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { dial } from '../src/client.js';
import { OapSession } from '../src/session.js';
import { ServerError, serverCode } from '../src/errors.js';
import { EnvelopeType, PROTOCOL, PROFILE, VERSION } from '../src/protocol.js';
import { FakeTransport, sentEnvelopeId, testEnvelope } from './transport.js';

const BASE = 'http://127.0.0.1:6270';

function errorBody(code: string, message: string, inReplyTo = 'req-1'): string {
  return JSON.stringify(
    testEnvelope({
      type: EnvelopeType.ErrorResponse,
      id: 'err-1',
      inReplyTo,
      payload: { error: { code, message } },
    }),
  );
}

test('dial accepts a bare address and lists adapters', async () => {
  const transport = new FakeTransport([
    {
      match: '/adapters',
      body: (call) => JSON.stringify({
        adapters: [
          {
            name: 'memory',
            capability_revision: 'reference-memory-v1',
            capabilities: { endpoint: { id: 'reference.memory' } },
          },
          { name: 'broken', error: 'probe failed' },
        ],
      }),
    },
  ]);
  const client = dial('127.0.0.1:6270', { fetch: transport.fetch });
  const adapters = await client.adapters();
  assert.equal(adapters.length, 2);
  assert.equal(adapters[0].name, 'memory');
  assert.equal(adapters[0].capability_revision, 'reference-memory-v1');
  assert.equal(adapters[1].error, 'probe failed');
  assert.equal(transport.calls[0].url, `${BASE}/adapters`);
});

test('capabilities returns the revision and descriptor', async () => {
  const transport = new FakeTransport([
    {
      match: '/capabilities',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.CapabilitiesResponse,
          id: 'resp-1',
          inReplyTo: 'oap-request-1',
          capabilityRevision: 'reference-memory-v1',
          payload: { endpoint: { id: 'reference.memory', name: 'Memory' }, protocol_versions: ['0.1'] },
        }),
      ),
    },
  ]);
  const client = dial(BASE, { fetch: transport.fetch });
  const caps = await client.capabilities('memory');
  assert.equal(caps.revision, 'reference-memory-v1');
  assert.equal(caps.descriptor.endpoint.id, 'reference.memory');
  assert.deepEqual(caps.descriptor.protocol_versions, ['0.1']);
});

test('open sends a session.open.request and adopts the confirmed id', async () => {
  const transport = new FakeTransport([
    {
      match: '/sessions',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionOpenResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 's-9',
          payload: { session_id: 's-9', status: 'idle' },
        }),
      ),
    },
  ]);
  const client = dial(BASE, { fetch: transport.fetch });
  const session = await client.open('memory', { sessionId: 's-9' });
  assert.equal(session.id, 's-9');
  assert.equal(session.adapter, 'memory');

  const call = transport.calls[0];
  assert.equal(call.url, `${BASE}/adapters/memory/sessions`);
  assert.equal(call.init?.method, 'POST');
  assert.equal(call.init?.headers?.['Content-Type'], 'application/json');
  const sent = JSON.parse(call.init?.body ?? '{}');
  assert.equal(sent.protocol, PROTOCOL);
  assert.equal(sent.version, VERSION);
  assert.equal(sent.profile, PROFILE);
  assert.equal(sent.type, EnvelopeType.SessionOpenRequest);
  assert.equal(sent.session_id, 's-9');
  assert.equal(sent.payload.session_id, 's-9');
});

test('open without a session id lets the adapter mint one', async () => {
  const transport = new FakeTransport([
    {
      match: '/sessions',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionOpenResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 'minted-1',
          payload: { session_id: 'minted-1', status: 'idle' },
        }),
      ),
    },
  ]);
  const client = dial(BASE, { fetch: transport.fetch });
  const session = await client.open('memory');
  assert.equal(session.id, 'minted-1');
  const sent = JSON.parse(transport.calls[0].init?.body ?? '{}');
  assert.equal(sent.session_id, undefined);
});

function openedSession(transport: FakeTransport, sessionId = 's-1'): OapSession {
  const client = dial(BASE, { fetch: transport.fetch });
  return new OapSession(client, sessionId, 'memory', 'user');
}

test('submit fills the payload scope and returns the admission', async () => {
  const transport = new FakeTransport([
    {
      match: '/submit',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionMessageSubmitResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 's-1',
          runId: 'r-1',
          payload: {
            session_id: 's-1',
            accepted: true,
            submission_id: 'sub-1',
            requested_delivery: 'auto',
            effective_delivery: 'start',
            admission: 'started',
            run_id: 'r-1',
          },
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  const admission = await session.submit({
    messages: [{ role: 'user', content: 'run the golden script' }],
    delivery: 'auto',
  });
  assert.equal(admission.admission, 'started');
  assert.equal(admission.run_id, 'r-1');
  const sent = JSON.parse(transport.calls[0].init?.body ?? '{}');
  assert.equal(sent.type, EnvelopeType.SessionMessageSubmitRequest);
  assert.equal(sent.session_id, 's-1');
  assert.equal(sent.payload.session_id, 's-1');
  assert.equal(sent.payload.delivery, 'auto');
});

test('submit refuses a mismatching payload session id before the wire', async () => {
  const transport = new FakeTransport([]);
  const session = openedSession(transport);
  await assert.rejects(
    session.submit({
      session_id: 'other',
      messages: [{ role: 'user', content: 'x' }],
      delivery: 'auto',
    }),
    /does not match session/,
  );
  assert.equal(transport.calls.length, 0);
});

test('resolvePermission echoes the gate and defaults the responder', async () => {
  const transport = new FakeTransport([
    {
      match: '/resolve',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.ActionPermissionResolveResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 's-1',
          runId: 'r-1',
          payload: { interaction_id: 'i-1', session_id: 's-1', run_id: 'r-1', accepted: true },
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  await session.resolvePermission({
    interaction_id: 'i-1',
    requested_by: 'user',
    run_id: 'r-1',
    choice_id: 'approve',
    granted: true,
  });
  const sent = JSON.parse(transport.calls[0].init?.body ?? '{}');
  assert.equal(sent.type, EnvelopeType.ActionPermissionResolveRequest);
  assert.equal(sent.session_id, 's-1');
  assert.equal(sent.run_id, 'r-1');
  assert.equal(sent.payload.session_id, 's-1');
  assert.equal(sent.payload.responded_by, 'user');
  assert.equal(sent.payload.granted, true);
});

test('resolveInput echoes the gate, and resolve dispatches on payload shape', async () => {
  const transport = new FakeTransport([
    {
      match: '/resolve',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.UserInputResolveResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 's-1',
          runId: 'r-1',
          payload: { interaction_id: 'i-2', session_id: 's-1', run_id: 'r-1', accepted: true },
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  await session.resolve({
    interaction_id: 'i-2',
    requested_by: 'user',
    run_id: 'r-1',
    answers: [{ question_id: 'q-1', selected_option_ids: ['yes'] }],
  });
  const sent = JSON.parse(transport.calls[0].init?.body ?? '{}');
  assert.equal(sent.type, EnvelopeType.UserInputResolveRequest);
  assert.equal(sent.payload.responded_by, 'user');
  assert.equal(sent.payload.answers[0].question_id, 'q-1');
});

test('cancel returns the acknowledgement and state returns the snapshot', async () => {
  const transport = new FakeTransport([
    {
      match: '/cancel',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.RunCancelResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 's-1',
          runId: 'r-1',
          payload: { session_id: 's-1', run_id: 'r-1', accepted: true, status: 'cancelling' },
        }),
      ),
    },
    {
      match: '/state',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionStateResponse,
          id: 'resp-2',
          inReplyTo: 'oap-request-2',
          sessionId: 's-1',
          payload: { session_id: 's-1', status: 'idle' },
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  const ack = await session.cancel('r-1');
  assert.equal(ack.accepted, true);
  assert.equal(ack.status, 'cancelling');
  const state = await session.state();
  assert.equal(state.status, 'idle');
  assert.equal(state.session_id, 's-1');
});

test('close accepts exactly 204 No Content', async () => {
  const transport = new FakeTransport([{ match: '/close', status: 204, body: '' }]);
  const session = openedSession(transport);
  await session.close();
  assert.equal(transport.calls[0].init?.method, 'POST');
});

test('close surfaces a refusal as a coded ServerError', async () => {
  const transport = new FakeTransport([
    { match: '/close', status: 409, body: errorBody('run_active', 'run still active') },
  ]);
  const session = openedSession(transport);
  await assert.rejects(session.close(), (err: unknown) => {
    assert.ok(err instanceof ServerError);
    assert.equal(err.status, 409);
    assert.equal(serverCode(err), 'run_active');
    return true;
  });
});

test('close rejects any other success status', async () => {
  const transport = new FakeTransport([{ match: '/close', status: 200, body: 'OK' }]);
  const session = openedSession(transport);
  await assert.rejects(session.close(), /want 204 No Content/);
});

test('a 204 where an envelope was expected is an error', async () => {
  const transport = new FakeTransport([{ match: '/state', status: 204, body: '' }]);
  const session = openedSession(transport);
  await assert.rejects(session.state(), /no content where session.state.response was expected/);
});

test('a state response scoped to another session is refused', async () => {
  const transport = new FakeTransport([
    {
      match: '/state',
      body: JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionStateResponse,
          id: 'resp-2',
          inReplyTo: 'oap-request-2',
          sessionId: 's-other',
          payload: { session_id: 's-other', status: 'idle' },
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  await assert.rejects(session.state(), /scoped to session "s-other", want "s-1"/);
});

test('a wrong response type is refused', async () => {
  const transport = new FakeTransport([
    {
      match: '/capabilities',
      body: (call) => JSON.stringify(
        testEnvelope({ type: EnvelopeType.SessionOpenResponse, id: 'r', payload: { session_id: 'x', status: 'idle' } }),
      ),
    },
  ]);
  const client = dial(BASE, { fetch: transport.fetch });
  await assert.rejects(client.capabilities('memory'), /returned session.open.response, want capabilities.response/);
});

test('a response citing another correlation is a protocol violation', async () => {
  const transport = new FakeTransport([
    {
      match: '/submit',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionMessageSubmitResponse,
          id: 'resp-1',
          inReplyTo: 'someone-else',
          sessionId: 's-1',
          payload: {},
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  await assert.rejects(
    session.submit({ messages: [{ role: 'user', content: 'x' }], delivery: 'auto' }),
    /cites correlation "someone-else", want the request id/,
  );
});

test('an error response citing another correlation is a protocol violation', async () => {
  const transport = new FakeTransport([
    { match: '/submit', status: 409, body: errorBody('run_active', 'busy', 'not-this-request') },
  ]);
  const session = openedSession(transport);
  await assert.rejects(
    session.submit({ messages: [{ role: 'user', content: 'x' }], delivery: 'auto' }),
    /error response cites correlation "not-this-request", want the request id/,
  );
});

test('a successful response scoped to another session is refused', async () => {
  const transport = new FakeTransport([
    {
      match: '/submit',
      body: (call) => JSON.stringify(
        testEnvelope({
          type: EnvelopeType.SessionMessageSubmitResponse,
          id: 'resp-1',
          inReplyTo: sentEnvelopeId(call),
          sessionId: 's-other',
          payload: {},
        }),
      ),
    },
  ]);
  const session = openedSession(transport);
  await assert.rejects(
    session.submit({ messages: [{ role: 'user', content: 'x' }], delivery: 'auto' }),
    /scoped to session "s-other", want "s-1"/,
  );
});

test('a non-envelope error body still reports the status', async () => {
  const transport = new FakeTransport([{ match: '/adapters', status: 502, body: '<html>bad gateway</html>' }]);
  const client = dial(BASE, { fetch: transport.fetch });
  await assert.rejects(client.adapters(), (err: unknown) => {
    assert.ok(err instanceof ServerError);
    assert.equal(err.status, 502);
    assert.equal(err.code, '');
    assert.equal(serverCode(err), null);
    return true;
  });
});

test('an unknown adapter is a coded ServerError', async () => {
  const transport = new FakeTransport([
    {
      match: '/nope/sessions',
      status: 404,
      body: (call) => errorBody('unknown_adapter', 'no adapter "nope"', sentEnvelopeId(call)),
    },
  ]);
  const client = dial(BASE, { fetch: transport.fetch });
  await assert.rejects(client.open('nope'), (err: unknown) => serverCode(err) === 'unknown_adapter');
});
