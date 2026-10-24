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
const application = { isLosslessNumber: true, value: '123', n0: 'n0', s0: 's0', b123: 'b123', input_tokens: 1.5, sequence: 1e3 };
const parsed = parseEnvelope(frame('run.completed', JSON.stringify({ application })));
assert.deepEqual(parsed.payload.application, application);
assert.equal(typeof parsed.payload.application.input_tokens, 'number');
assert.deepEqual(parseEnvelope(serializeEnvelope(parsed)), parsed);
const prototype = parseEnvelope('{"__proto__":{"polluted":true},"payload":{},"type":"future.event","id":"e"}');
assert.equal(Object.hasOwn(prototype, '__proto__'), true);
assert.equal({}.polluted, undefined);
assert.throws(() => parseEnvelope('{1:2}'));
console.log('PASS: uniform bigint, exact round-trips, invalid counts refused, ordinary JSON unchanged');
`;
