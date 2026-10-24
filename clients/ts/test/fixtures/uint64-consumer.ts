export const uint64TypeCases = [
  { name: 'uniform-bigint', accepts: true, source: `
import { parseEnvelope, serializeEnvelope, OverflowError } from 'oap-client';
import type { Envelope, Usage, ActiveRun, ModelEventPosition, RunPosition, SettledRun,
  MessageSubmitResponse, RunCompactionStartedPayload, RunCompactionEndedPayload, OapSession } from 'oap-client';
const usage: Usage = { input_tokens: 0n, output_tokens: 1n, total_tokens: 1n,
  cache_read_input_tokens: 0n, cache_write_input_tokens: 0n, reasoning_tokens: 1n };
const sum: bigint = usage.input_tokens! + usage.output_tokens!;
const envelope: Envelope = { protocol: 'open-agent-protocol', version: '0.1', profile: 'open-agent-protocol.agent-control-core',
  type: 'run.completed', id: 'e', sequence: 1n, timestamp_ms: 1, payload: { usage } };
const position: ModelEventPosition = { run_id: 'r', sequence: 1n };
const genesis: RunPosition = { run_id: null, sequence: 0n };
const settled: SettledRun = { run_id: 'r', sequence: 1n };
const active: ActiveRun = { run_id: 'r', status: 'running', relationship: 'primary', as_of_sequence: 1n };
const admission: MessageSubmitResponse = { session_id: 's', accepted: true, submission_id: 'q', requested_delivery: 'steer',
  effective_delivery: 'steer', admission: 'steered', target_sequence: 1n };
const started: RunCompactionStartedPayload = { session_id: 's', run_id: 'r', compaction_id: 'c', reason: 'requested', history_tokens: 0n };
const ended: RunCompactionEndedPayload = { session_id: 's', run_id: 'r', compaction_id: 'c', outcome: 'completed', history_tokens: 1n };
const cursor: bigint = new OverflowError('r', 0n, '').lastSequence;
function replay(session: OapSession) { return session.eventsAfter('r', 0n); }
void [sum, position, genesis, settled, active, admission, started, ended, cursor, replay];
console.log(serializeEnvelope(parseEnvelope(serializeEnvelope(envelope))));
` },
  { name: 'number-usage-refused', accepts: false, source: `import type { Usage } from 'oap-client';
const value: Usage = { input_tokens: 0 };
void value;` },
  { name: 'number-cursor-refused', accepts: false, source: `import type { OapSession } from 'oap-client';
function replay(session: OapSession) { return session.eventsAfter('r', 0); }
void replay;` },
] as const;

export const uint64RuntimeConsumer = `
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import * as client from 'oap-client';
const { parseEnvelope, payload, serializeEnvelope } = client;
const fields = ['input_tokens', 'output_tokens', 'total_tokens', 'cache_read_input_tokens', 'cache_write_input_tokens', 'reasoning_tokens'];
const sites = ['content.completed', 'run.completed', 'run.failed', 'run.cancelled', 'inference.completed', 'inference.failed'];
const tokens = ['0', '1', '9007199254740991', '9007199254740992', '9007199254740993', '18446744073709551615'];
const frame = (type, body) => '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":' + JSON.stringify(type) + ',"id":"e","sequence":1,"payload":' + body + '}';
const rows = [];
for (const site of sites) for (const field of fields) for (const token of tokens) {
  const raw = frame(site, '{"usage":{' + JSON.stringify(field) + ':' + token + '}}');
  const parsed = parseEnvelope(raw);
  const count = payload(parsed).usage[field];
  rows.push({ site, field, token, type: typeof count, value: String(count) });
}
if (process.env.OAP_TS_UINT64_OUTPUT) writeFileSync(process.env.OAP_TS_UINT64_OUTPUT + '/runtime.json', JSON.stringify(rows, null, 2));
console.log(JSON.stringify(rows));
for (const row of rows) { assert.equal(row.type, 'bigint', row.site + '.' + row.field); assert.equal(row.value, row.token); }
for (const site of sites) for (const field of fields) for (const token of tokens) {
  const parsed = parseEnvelope(frame(site, '{"usage":{' + JSON.stringify(field) + ':' + token + '}}'));
  const wire = serializeEnvelope(parsed);
  assert.ok(wire.includes(JSON.stringify(field) + ':' + token));
  assert.deepEqual(parseEnvelope(wire), parsed);
}
for (const site of sites) for (const field of fields) for (const invalid of ['-0','-1','1.0','1e3','1.5','null','true','"1"','18446744073709551616']) {
  assert.throws(() => parseEnvelope(frame(site, '{"usage":{' + JSON.stringify(field) + ':' + invalid + '}}')));
}
const previousJSON = Object.getOwnPropertyDescriptor(BigInt.prototype, 'toJSON');
try {
  const parsed = parseEnvelope(frame('run.completed', '{"usage":{"input_tokens":18446744073709551615}}'));
  for (const toJSON of [function () { return Number(this); }, function () { return this.toString(); }]) {
    Object.defineProperty(BigInt.prototype, 'toJSON', { configurable: true, value: toJSON });
    const encoded = serializeEnvelope(parsed);
    assert.ok(encoded.includes('"input_tokens":18446744073709551615'));
    assert.deepEqual(parseEnvelope(encoded), parsed);
  }
} finally {
  if (previousJSON) Object.defineProperty(BigInt.prototype, 'toJSON', previousJSON);
  else delete BigInt.prototype.toJSON;
}
const usage = payload(parseEnvelope(frame('run.completed', '{"usage":{}}'))).usage;
assert.equal(usage.input_tokens, undefined);
assert.equal(payload(parseEnvelope(frame('run.completed', '{}'))).usage, undefined);
const safe = payload(parseEnvelope(frame('run.completed', '{"usage":{"input_tokens":0,"output_tokens":1}}'))).usage;
const large = payload(parseEnvelope(frame('run.completed', '{"usage":{"input_tokens":9007199254740993,"output_tokens":1}}'))).usage;
assert.equal(safe.input_tokens + safe.output_tokens, 1n);
assert.equal(large.input_tokens + large.output_tokens, 9007199254740994n);
assert.throws(() => serializeEnvelope({ ...parseEnvelope(frame('run.completed', '{}')), payload: { usage: { input_tokens: 1 } } }));
for (const sequence of ['1.0', '1e2', '9007199254740993', '18446744073709551615']) {
  const parsed = parseEnvelope(frame('future.event', '{}').replace('"sequence":1', '"sequence":' + sequence));
  assert.equal(typeof parsed.sequence, 'bigint');
  assert.deepEqual(parseEnvelope(serializeEnvelope(parsed)), parsed);
}
const positions = [
  ['session.open.response', '{"active_runs":[{"as_of_sequence":18446744073709551615}],"as_of":{"settled":[{"sequence":18446744073709551615}],"model_run_sequence":{"sequence":18446744073709551615}}}', p => [p.active_runs[0].as_of_sequence, p.as_of.settled[0].sequence, p.as_of.model_run_sequence.sequence]],
  ['session.state.response', '{"active_runs":[{"as_of_sequence":0}]}', p => [p.active_runs[0].as_of_sequence]],
  ['session.state.updated', '{"as_of":{"model_run_sequence":{"sequence":1}}}', p => [p.as_of.model_run_sequence.sequence]],
  ['models.response', '{"as_of_model_event":{"sequence":18446744073709551615}}', p => [p.as_of_model_event.sequence]],
  ['session.message.submit.response', '{"target_sequence":18446744073709551615}', p => [p.target_sequence]],
  ['run.compaction.started', '{"history_tokens":0}', p => [p.history_tokens]],
  ['run.compaction.ended', '{"history_tokens":18446744073709551615}', p => [p.history_tokens]],
];
for (const [type, body, select] of positions) {
  const parsed = parseEnvelope(frame(type, body));
  for (const count of select(parsed.payload)) assert.equal(typeof count, 'bigint', type);
  assert.deepEqual(parseEnvelope(serializeEnvelope(parsed)), parsed);
}
assert.throws(() => serializeEnvelope({ ...parseEnvelope(frame('future.event', '{}')), payload: { application: 1n } }));
const application = { isLosslessNumber: true, value: '123', n0: 'n0', s0: 's0', b123: 'b123', input_tokens: 1.5, sequence: 1e3 };
const parsed = parseEnvelope(frame('run.completed', JSON.stringify({ application })));
assert.deepEqual(parsed.payload.application, application);
assert.equal(typeof parsed.payload.application.input_tokens, 'number');
assert.deepEqual(parseEnvelope(serializeEnvelope(parsed)), parsed);
const boxed = parseEnvelope(frame('future.event', '{}'));
boxed.payload.text = new String('b123');
assert.deepEqual(parseEnvelope(serializeEnvelope(boxed)).payload, JSON.parse(JSON.stringify(boxed.payload)));
boxed.payload.text.toString = () => 'custom';
assert.deepEqual(parseEnvelope(serializeEnvelope(boxed)).payload, JSON.parse(JSON.stringify(boxed.payload)));
const prototype = parseEnvelope('{"__proto__":{"polluted":true},"payload":{},"type":"future.event","id":"e"}');
assert.equal(Object.hasOwn(prototype, '__proto__'), true);
assert.equal({}.polluted, undefined);
assert.throws(() => parseEnvelope('{1:2}'));
console.log('PASS: uniform bigint, exact round-trips, invalid counts refused, ordinary JSON unchanged');
`;

export const uint64TransportConsumer = String.raw`
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { writeFileSync } from 'node:fs';
import { dial, payload, OverflowError, ReplayGapError, MalformedFrameError } from 'oap-client';
const MAX = 18446744073709551615n;
const AFTER = 9007199254740992n;
let mode = 'normal';
const calls = [];
const event = (type, sequence, body) => '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":' + JSON.stringify(type) + ',"id":' + JSON.stringify(type) + ',"session_id":"s","run_id":"r","sequence":' + sequence + ',"payload":' + body + '}';
const server = createServer(async (request, response) => {
  let body = '';
  for await (const chunk of request) body += chunk;
  calls.push({ path: request.url, after: request.headers['last-event-id'], body });
  if (request.url === '/echo') {
    const id = JSON.parse(body).id;
    response.setHeader('connection', 'close');
    response.setHeader('content-type', 'application/json');
    response.end(body.replace('future.request', 'future.response').replace('"payload":', '"in_reply_to":' + JSON.stringify(id) + ',"payload":'));
  } else if (request.url === '/adapters/memory/sessions') {
    response.setHeader('connection', 'close');
    response.setHeader('content-type', 'application/json');
    response.end(JSON.stringify({ protocol: 'open-agent-protocol', version: '0.1', profile: 'open-agent-protocol.agent-control-core',
      type: 'session.open.response', id: 'opened', in_reply_to: JSON.parse(body).id, session_id: 's', payload: { session_id: 's', status: 'idle' } }));
  } else {
    response.setHeader('connection', 'close');
    response.setHeader('content-type', 'text/event-stream');
    if (mode === 'normal') {
      response.write('id: 1\ndata: ' + event('run.started', '1', '{"session_id":"s","run_id":"r","status":"running"}') + '\n\n');
      response.end('id: 2\ndata: ' + event('run.completed', '2', '{"session_id":"s","run_id":"r","final_response":{"role":"assistant","content":"done"},"stop_reason":"completed","usage":{"input_tokens":18446744073709551615,"output_tokens":0}}') + '\n\n');
    } else if (mode === 'resume' || mode === 'mismatch') {
      const id = mode === 'resume' ? '9007199254740993' : '9007199254740992';
      response.end('id: ' + id + '\ndata: ' + event('run.completed', '9007199254740993', '{"session_id":"s","run_id":"r","final_response":{"role":"assistant","content":"done"},"stop_reason":"completed"}') + '\n\n');
    } else if (mode === 'overflow') {
      response.end('event: oap-overflow\ndata: {"run_id":"r","last_sequence":18446744073709551615}\n\n');
    } else {
      response.end('event: oap-replay-gap\ndata: {"requested_after":9007199254740992,"oldest_available":18446744073709551614,"latest_available":18446744073709551615}\n\n');
    }
  }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
try {
  const address = server.address();
  assert.ok(address && typeof address !== 'string');
  assert.notEqual(address.port, 8399);
  const client = dial('http://127.0.0.1:' + address.port);
  const request = client.envelope('future.request', {});
  request.sequence = MAX;
  const echoed = await client.exchange('POST', '/echo', request, 'future.response');
  assert.equal(echoed.sequence, MAX);
  assert.ok(calls[0].body.includes('"sequence":18446744073709551615'));
  const session = await client.open('memory', { sessionId: 's' });
  const received = [];
  for await (const envelope of session.events({ signal: AbortSignal.timeout(5000) })) {
    received.push(envelope);
    if (envelope.type === 'run.completed') break;
  }
  assert.deepEqual(received.map(e => e.sequence), [1n, 2n]);
  assert.equal(payload(received[1]).usage.input_tokens, MAX);
  assert.equal(payload(received[1]).usage.output_tokens, 0n);
  mode = 'resume';
  for await (const envelope of session.eventsAfter('r', AFTER, { signal: AbortSignal.timeout(5000) })) {
    assert.equal(envelope.sequence, AFTER + 1n);
    break;
  }
  const resumed = calls.find(call => call.path === '/sessions/s/events?after=9007199254740992');
  assert.equal(resumed.after, '9007199254740992');
  mode = 'mismatch';
  await assert.rejects(async () => {
    for await (const envelope of session.eventsAfter('r', AFTER, { signal: AbortSignal.timeout(5000) })) void envelope;
  }, MalformedFrameError);
  mode = 'overflow';
  await assert.rejects(async () => {
    for await (const envelope of session.events({ signal: AbortSignal.timeout(5000) })) void envelope;
  }, error => error instanceof OverflowError && error.lastSequence === MAX);
  mode = 'gap';
  await assert.rejects(async () => {
    for await (const envelope of session.eventsAfter('r', AFTER, { signal: AbortSignal.timeout(5000) })) void envelope;
  }, error => error instanceof ReplayGapError && error.requestedAfter === AFTER && error.oldestAvailable === MAX - 1n && error.latestAvailable === MAX);
  if (process.env.OAP_TS_UINT64_OUTPUT) writeFileSync(process.env.OAP_TS_UINT64_OUTPUT + '/transport.json', JSON.stringify({ calls, input: String(MAX), sequences: received.map(e => String(e.sequence)) }, null, 2));
  console.log('PASS: real loopback HTTP exchange, SSE counts, exact resume/header, mismatched frame id, overflow and gap cursors');
} finally {
  server.closeAllConnections?.();
  await new Promise(resolve => server.close(resolve));
}
`;

export const uint64CorpusConsumer = String.raw`
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { parseEnvelope, serializeEnvelope } from 'oap-client';
const cases = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const output = process.argv[3];
mkdirSync(output, { recursive: true });
const results = [];
for (const entry of cases) {
  const raw = readFileSync(entry.input, 'utf8');
  const lines = raw.trim().split('\n').map(line => line.trim());
  assert.equal(lines.shift(), '[');
  assert.equal(lines.pop(), ']');
  const frames = lines.map(line => line.replace(/,$/, ''));
  let accepted = false;
  let stage = 'decode';
  let error;
  const path = join(output, entry.id + '.json');
  try {
    const decoded = frames.map(parseEnvelope);
    accepted = true;
    stage = 'encode';
    const encoded = decoded.map(serializeEnvelope);
    writeFileSync(path, '[\n' + encoded.join(',\n') + '\n]\n');
    stage = 'roundtrip';
    assert.deepEqual(encoded.map(parseEnvelope), decoded);
  } catch (caught) {
    error = String(caught);
  }
  if (!entry.valid) writeFileSync(path, raw);
  results.push({ ...entry, path, accepted, stage, error });
}
writeFileSync(join(output, 'results.json'), JSON.stringify(results, null, 2));
const mismatches = results.filter(row => row.accepted !== row.valid || (row.valid && row.error));
assert.deepEqual(mismatches, []);
console.log('PASS: ' + results.length + ' shared raw usage cases accepted/refused and valid cases round-tripped by installed client');
`;
