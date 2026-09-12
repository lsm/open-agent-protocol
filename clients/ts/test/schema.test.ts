/**
 * Cross-checks the hand-written interfaces against the JSON schema files:
 * the samples below are type-checked against the payload interfaces at
 * compile time, and compared with the schema $defs at run time, so every
 * schema-required field exists on the interface and every interface field
 * exists in the schema. Full JSON-Schema validation at runtime is
 * deliberately out of scope for the zero-dependency client.
 */

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import * as protocol from '../src/protocol.js';
import type { ImageContent, InputAnswer, MessageContent } from '../src/protocol.js';
import { findRepoRoot } from './transport.js';

const schemaDir = join(findRepoRoot(dirname(fileURLToPath(import.meta.url))), 'schema', 'v0.1');

const schemas: Record<string, Record<string, unknown>> = {};
function schema(name: string): Record<string, unknown> {
  if (!schemas[name]) schemas[name] = JSON.parse(readFileSync(join(schemaDir, name), 'utf8')) as Record<string, unknown>;
  return schemas[name];
}

/** Resolves a def inside one schema file, following one level of $ref (local or cross-file). */
function def(file: string, defName: string): Record<string, unknown> {
  const defs = schema(file)['$defs'] as Record<string, Record<string, unknown>>;
  const entry = defs[defName];
  assert.ok(entry, `${file} has no $defs.${defName}`);
  return entry;
}

/** Resolves a property declaration to a bare schema object, following one $ref hop. */
function resolveProperty(file: string, property: unknown): Record<string, unknown> {
  const entry = property as Record<string, unknown>;
  if (typeof entry.$ref === 'string') {
    const [fileRef, defRef] = entry.$ref.split('#');
    const targetFile = fileRef === '' ? file : fileRef;
    const targetDef = defRef.replace(/^\/\$defs\//, '');
    return def(targetFile, targetDef);
  }
  if (Array.isArray(entry.allOf)) {
    // The envelope defs stack response/session/run/runEvent requirements;
    // property shapes come from the plain branches.
    for (const branch of entry.allOf as Record<string, unknown>[]) {
      if (branch.$ref === undefined) return branch as Record<string, unknown>;
    }
    return {} as Record<string, unknown>;
  }
  return entry;
}

interface PayloadSample {
  name: string;
  sample: Record<string, unknown>;
  file: string;
  def: string;
  /** When set, the def under comparison is this property of the named def (the error.response payload is inline). */
  property?: string;
}

/** Registers a sample whose object literal is compile-time checked against the interface T. */
function sample<T extends object>(
  name: string,
  file: string,
  defName: string,
  value: T,
  property?: string,
): PayloadSample {
  return { name, sample: value as Record<string, unknown>, file, def: defName, property };
}

const samples: PayloadSample[] = [
  // capabilities.schema.json
  sample<protocol.InitializeRequest>('InitializeRequest', 'capabilities.schema.json', 'initializeRequest', {
    protocol_versions: ['0.1'],
    profiles: ['open-agent-protocol.agent-control-core'],
    participant: { id: 'user', name: 'User', version: '1' },
  }),
  sample<protocol.InitializeResponse>('InitializeResponse', 'capabilities.schema.json', 'initializeResponse', {
    protocol_version: '0.1',
    profile: 'open-agent-protocol.agent-control-core',
    endpoint: { id: 'reference.memory', name: 'Memory', version: '0.1', adapter: 'memory' },
  }),
  sample<protocol.CapabilitiesRequest>('CapabilitiesRequest', 'capabilities.schema.json', 'capabilitiesRequest', {}),
  sample<protocol.CapabilityDescriptor>('CapabilityDescriptor', 'capabilities.schema.json', 'capabilitiesResponse', {
    endpoint: { id: 'reference.memory', name: 'Memory', version: '0.1', adapter: 'memory' },
    protocol_versions: ['0.1'],
    profiles: ['open-agent-protocol.agent-control-core'],
    bindings: [{ kind: 'stdio', serialization: 'jsonrpc' }],
    features: { 'session.open': { level: 'native', reason: 'because', mode: 'direct' } },
    layers: {
      core: {
        features: { 'session.open': { level: 'native', reason: 'because', mode: 'direct' } },
        requested_delivery_modes: ['auto'],
        effective_delivery_modes: ['start'],
        tools: [
          {
            name: 'echo',
            description: 'echoes',
            input_schema: { type: 'object' },
            execution_owner: 'user',
            annotations: { area: 'test' },
          },
        ],
      },
    },
    tools: [
      {
        name: 'echo',
        description: 'echoes',
        input_schema: { type: 'object' },
        execution_owner: 'user',
        annotations: { area: 'test' },
      },
    ],
    degradation: [{ feature: 'run.replay', from: 'native', to: 'degraded', mode: 'journal', reason: 'bounded' }],
  }),
  sample<protocol.CapabilitiesUpdated>('CapabilitiesUpdated', 'capabilities.schema.json', 'capabilitiesUpdated', {
    previous_revision: 'rev-1',
    reason: 'reloaded',
  }),

  // session.schema.json
  sample<protocol.SessionOpenRequest>('SessionOpenRequest', 'session.schema.json', 'openRequest', {
    session_id: 's-1',
    metadata: { origin: 'test' },
    recovery: {
      recovered: true,
      previous_session_id: 's-0',
      previous_run_id: 'r-0',
      resume_cursor: '3',
      reason: 'reattached',
    },
  }),
  sample<protocol.SessionState>('SessionState', 'session.schema.json', 'state', {
    session_id: 's-1',
    status: 'running',
    active_run_id: 'r-1',
    current_model_id: 'model-a',
    transcript_cursor: '12',
    updated_at_ms: 1700000000000,
    metadata: { origin: 'test' },
    recovery: { recovered: false },
  }),
  sample<protocol.SessionStateRequest>('SessionStateRequest', 'session.schema.json', 'stateRequest', {
    session_id: 's-1',
  }),
  sample<protocol.MessageSubmitRequest>('MessageSubmitRequest', 'session.schema.json', 'messageSubmitRequest', {
    session_id: 's-1',
    messages: [
      { id: 'm-1', role: 'user', content: 'hello', metadata: { turn: 1 } },
      { role: 'assistant', content: [{ type: 'text', text: 'hi' }] },
    ],
    delivery: 'auto',
    model_id: 'model-a',
    instructions: 'be brief',
    tool_choice: { mode: 'auto' },
    output_schema: { type: 'object' },
    allow_degraded_features: ['run.replay'],
    metadata: { origin: 'test' },
  }),
  sample<protocol.MessageSubmitResponse>('MessageSubmitResponse', 'session.schema.json', 'messageSubmitResponse', {
    session_id: 's-1',
    accepted: true,
    submission_id: 'sub-1',
    requested_delivery: 'auto',
    effective_delivery: 'start',
    delivery_resolution: 'no active run',
    admission: 'started',
    run_id: 'r-1',
    status: 'running',
    model_id: 'model-a',
    message_ids: ['m-1'],
  }),

  // run.schema.json
  sample<protocol.RunCancelRequest>('RunCancelRequest', 'run.schema.json', 'cancelRequest', {
    session_id: 's-1',
    run_id: 'r-1',
    reason: 'user asked',
  }),
  sample<protocol.RunCancelResponse>('RunCancelResponse', 'run.schema.json', 'cancelResponse', {
    session_id: 's-1',
    run_id: 'r-1',
    accepted: true,
    status: 'cancelling',
  }),
  sample<protocol.RunStartedPayload>('RunStartedPayload', 'run.schema.json', 'started', {
    session_id: 's-1',
    run_id: 'r-1',
    status: 'running',
    model_id: 'model-a',
    started_at_ms: 1700000000000,
  }),
  sample<protocol.RunStatusUpdatedPayload>('RunStatusUpdatedPayload', 'run.schema.json', 'statusUpdated', {
    session_id: 's-1',
    run_id: 'r-1',
    status: 'waiting_for_input',
    pending_user_input_id: 'i-1',
    updated_at_ms: 1700000000000,
  }),
  sample<protocol.ContentDeltaPayload>('ContentDeltaPayload', 'run.schema.json', 'contentDelta', {
    session_id: 's-1',
    run_id: 'r-1',
    message_id: 'm-1',
    part: { type: 'text', text: 'chunk' },
  }),
  sample<protocol.RunCompletedPayload>('RunCompletedPayload', 'run.schema.json', 'completed', {
    session_id: 's-1',
    run_id: 'r-1',
    final_response: { id: 'm-2', role: 'assistant', content: 'done', metadata: { turn: 2 } },
    stop_reason: 'end_turn',
    result: { answer: 42 },
    usage: { input_tokens: 1, output_tokens: 2, total_tokens: 3 },
    duration_ms: 1200,
  }),
  sample<protocol.RunFailedPayload>('RunFailedPayload', 'run.schema.json', 'failed', {
    session_id: 's-1',
    run_id: 'r-1',
    error: { code: 'provider_down', message: 'gone', retriable: true, details: { provider: 'x' } },
    usage: { input_tokens: 1, output_tokens: 2, total_tokens: 3 },
    duration_ms: 1200,
    recovery: { recovered: false, reason: 'nothing to resume' },
  }),
  sample<protocol.RunCancelledPayload>('RunCancelledPayload', 'run.schema.json', 'cancelled', {
    session_id: 's-1',
    run_id: 'r-1',
    reason: 'user asked',
    usage: { input_tokens: 1, output_tokens: 2, total_tokens: 3 },
    duration_ms: 1200,
  }),

  // action.schema.json
  sample<protocol.ToolsListRequest>('ToolsListRequest', 'action.schema.json', 'toolsListRequest', {}),
  sample<protocol.ToolsListResponse>('ToolsListResponse', 'action.schema.json', 'toolsListResponse', {
    tools: [
      {
        name: 'echo',
        description: 'echoes',
        input_schema: { type: 'object' },
        execution_owner: 'user',
        annotations: { area: 'test' },
      },
    ],
  }),
  sample<protocol.ActionCallRequestedPayload>('ActionCallRequestedPayload', 'action.schema.json', 'callRequested', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    requested_by: 'agent',
    responded_by: 'user',
    execution_owner: 'user',
    name: 'echo',
    arguments_json: { text: 'hi' },
  }),
  sample<protocol.ActionCallStartedPayload>('ActionCallStartedPayload', 'action.schema.json', 'callStarted', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    requested_by: 'agent',
    responded_by: 'user',
    execution_owner: 'user',
    name: 'echo',
    arguments_json: { text: 'hi' },
  }),
  sample<protocol.ActionCallProgressPayload>('ActionCallProgressPayload', 'action.schema.json', 'callProgress', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    requested_by: 'agent',
    responded_by: 'user',
    execution_owner: 'user',
    name: 'echo',
    progress: { percent: 50 },
  }),
  sample<protocol.ActionCallCompletedPayload>('ActionCallCompletedPayload', 'action.schema.json', 'callCompleted', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    requested_by: 'agent',
    responded_by: 'user',
    execution_owner: 'user',
    name: 'echo',
    result: { output: 'hi' },
  }),
  sample<protocol.ActionCallFailedPayload>('ActionCallFailedPayload', 'action.schema.json', 'callFailed', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    requested_by: 'agent',
    responded_by: 'user',
    execution_owner: 'user',
    name: 'echo',
    error: { code: 'tool_error', message: 'boom' },
  }),
  sample<protocol.ActionCallCancelledPayload>('ActionCallCancelledPayload', 'action.schema.json', 'callCancelled', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    requested_by: 'agent',
    responded_by: 'user',
    execution_owner: 'user',
    name: 'echo',
  }),
  sample<protocol.PermissionRequestedPayload>('PermissionRequestedPayload', 'action.schema.json', 'permissionRequested', {
    interaction_id: 'i-1',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    title: 'Allow tool',
    description: 'the tool wants to run',
    choices: [{ id: 'approve', label: 'Approve', description: 'allow once' }],
    arguments_json: { cmd: 'ls' },
  }),
  sample<protocol.PermissionResolveRequest>('PermissionResolveRequest', 'action.schema.json', 'permissionResolveRequest', {
    interaction_id: 'i-1',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    choice_id: 'approve',
    granted: true,
    reason: 'fine this time',
    updated_arguments_json: { cmd: 'ls', sandbox: true },
  }),
  sample<protocol.PermissionResolveResponse>('PermissionResolveResponse', 'action.schema.json', 'permissionResolveResponse', {
    interaction_id: 'i-1',
    session_id: 's-1',
    run_id: 'r-1',
    accepted: true,
  }),
  sample<protocol.PermissionResolvedPayload>('PermissionResolvedPayload', 'action.schema.json', 'permissionResolved', {
    interaction_id: 'i-1',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    outcome: 'resolved',
    choice_id: 'approve',
    granted: true,
    reason: { code: 'not_needed', message: 'no error' },
  }),

  // interaction.schema.json
  sample<protocol.UserInputRequestedPayload>('UserInputRequestedPayload', 'interaction.schema.json', 'requested', {
    interaction_id: 'i-2',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    tool_call_id: 't-1',
    title: 'Golden input',
    description: 'choose',
    questions: [
      {
        id: 'q-1',
        prompt: 'Proceed?',
        kind: 'single_choice',
        required: true,
        options: [{ id: 'yes', label: 'Yes', description: 'continue' }],
      },
    ],
    allow_cancel: true,
    draft_answers: [{ question_id: 'q-1', selected_option_ids: ['yes'] }],
  }),
  sample<protocol.UserInputResolveRequest>('UserInputResolveRequest', 'interaction.schema.json', 'resolveRequest', {
    interaction_id: 'i-2',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    answers: [{ question_id: 'q-1', text: 'yes please' }],
  }),
  sample<protocol.UserInputResolveResponse>('UserInputResolveResponse', 'interaction.schema.json', 'resolveResponse', {
    interaction_id: 'i-2',
    session_id: 's-1',
    run_id: 'r-1',
    accepted: true,
  }),
  sample<protocol.UserInputResolvedPayload>('UserInputResolvedPayload', 'interaction.schema.json', 'resolved', {
    interaction_id: 'i-2',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    status: 'submitted',
    answers: [{ question_id: 'q-1', text: 'yes please' }],
  }),
  sample<protocol.UserInputCancelRequest>('UserInputCancelRequest', 'interaction.schema.json', 'cancelRequest', {
    interaction_id: 'i-2',
    requested_by: 'agent',
    responded_by: 'user',
    session_id: 's-1',
    run_id: 'r-1',
    reason: 'never mind',
  }),
  sample<protocol.UserInputCancelResponse>('UserInputCancelResponse', 'interaction.schema.json', 'cancelResponse', {
    interaction_id: 'i-2',
    session_id: 's-1',
    run_id: 'r-1',
    accepted: true,
  }),

  // common.schema.json
  sample<protocol.ErrorResponse>('ErrorResponse', 'envelope.schema.json', 'errorResponse', {
    error: { code: 'unknown_session', message: 'no such session', retriable: false, details: { session_id: 's-x' } },
  }, 'payload'),
  sample<protocol.ProtocolError>('ProtocolError', 'common.schema.json', 'protocolError', {
    code: 'unknown_session',
    message: 'no such session',
    retriable: false,
    details: { session_id: 's-x' },
  }),
  sample<protocol.Usage>('Usage', 'common.schema.json', 'usage', {
    input_tokens: 1,
    output_tokens: 2,
    total_tokens: 3,
  }),
  sample<protocol.RecoveryMetadata>('RecoveryMetadata', 'common.schema.json', 'recovery', {
    recovered: true,
    previous_session_id: 's-0',
    previous_run_id: 'r-0',
    resume_cursor: '3',
    reason: 'reattached',
  }),
  sample<protocol.Message>('Message', 'common.schema.json', 'message', {
    id: 'm-1',
    role: 'user',
    content: 'hello',
    metadata: { turn: 1 },
  }),
];

test('every payload interface mirrors its schema def', () => {
  for (const { name, sample: value, file, def: defName, property } of samples) {
    const definition = def(file, defName);
    // openResponse/stateResponse/stateUpdated/cancelResponse $ref directly to their target.
    let resolved =
      typeof definition.$ref === 'string' ? resolveProperty(file, definition) : definition;
    if (property !== undefined) {
      const properties = resolved.properties as Record<string, unknown>;
      assert.ok(properties, `${file} ${defName} has no properties`);
      resolved = resolveProperty(file, properties[property]);
    }
    const required = (resolved.required as string[] | undefined) ?? [];
    const properties = (resolved.properties as Record<string, unknown> | undefined) ?? {};
    const keys = Object.keys(value);
    for (const field of required) {
      assert.ok(keys.includes(field), `${name}: schema-required field ${field} missing from the interface sample`);
    }
    for (const key of keys) {
      assert.ok(key in properties, `${name}: interface field ${key} absent from ${file} ${defName}`);
      const property = resolveProperty(file, properties[key]);
      const enumValues = property.enum as unknown[] | undefined;
      const sampleValue = value[key];
      if (enumValues && typeof sampleValue === 'string') {
        assert.ok(
          enumValues.includes(sampleValue),
          `${name}.${key}: sample ${sampleValue} not in the schema enum ${JSON.stringify(enumValues)}`,
        );
      }
    }
  }
});

test('every envelope type appears exactly once in the envelope schema', () => {
  const envelopeSchema = schema('envelope.schema.json');
  const defs = envelopeSchema['$defs'] as Record<string, { properties?: { type?: { const?: string } } }>;
  const schemaTypes = new Set<string>();
  for (const [name, definition] of Object.entries(defs)) {
    const typeConst = definition.properties?.type?.const;
    if (typeConst === undefined) continue; // response/session/run/runEvent helpers
    assert.ok(!schemaTypes.has(typeConst), `duplicate type const ${typeConst} on def ${name}`);
    schemaTypes.add(typeConst);
  }
  const clientTypes = new Set(Object.values(protocol.EnvelopeType) as string[]);
  for (const type of clientTypes) {
    assert.ok(schemaTypes.has(type), `client envelope type ${type} absent from the schema`);
  }
  for (const type of schemaTypes) {
    assert.ok(clientTypes.has(type), `schema envelope type ${type} missing from the client`);
  }
});

test('the Envelope interface covers exactly the envelope schema properties', () => {
  const envelopeSchema = schema('envelope.schema.json');
  const properties = Object.keys(envelopeSchema.properties as Record<string, unknown>);
  const full: protocol.Envelope = {
    protocol: protocol.PROTOCOL,
    version: protocol.VERSION,
    profile: protocol.PROFILE,
    type: 'run.completed',
    id: 'env-1',
    payload: {},
    sequence: 1,
    timestamp_ms: 1700000000000,
    in_reply_to: 'env-0',
    session_id: 's-1',
    run_id: 'r-1',
    turn_id: 't-1',
    tool_call_id: 'tc-1',
    capability_revision: 'rev-1',
    extensions: { trace: true },
  };
  assert.deepEqual(Object.keys(full).sort(), properties.sort());
});

test('every envelope type maps to the payload def its schema declares', () => {
  const envelopeSchema = schema('envelope.schema.json');
  const defs = envelopeSchema['$defs'] as Record<string, Record<string, unknown>>;
  const envelopeFileOf = new Map<string, string>();
  const payloadDefOf = new Map<string, string>();
  for (const [name, definition] of Object.entries(defs)) {
    const typeConst = (definition.properties as { type?: { const?: string } } | undefined)?.type?.const;
    if (typeConst === undefined) continue;
    envelopeFileOf.set(typeConst, name);
    const payloadRef = (definition.properties as { payload?: { $ref?: string } } | undefined)?.payload?.$ref;
    if (payloadRef === undefined) continue;
    const [file, ref] = payloadRef.split('#');
    payloadDefOf.set(typeConst, `${file}:${ref.replace(/^\/\$defs\//, '')}`);
  }
  // Spot-check the mapping the client's operations depend on.
  assert.equal(payloadDefOf.get('session.open.request'), 'session.schema.json:openRequest');
  assert.equal(payloadDefOf.get('session.message.submit.response'), 'session.schema.json:messageSubmitResponse');
  assert.equal(payloadDefOf.get('action.permission.resolve.request'), 'action.schema.json:permissionResolveRequest');
  assert.equal(payloadDefOf.get('user.input.resolve.request'), 'interaction.schema.json:resolveRequest');
  assert.equal(envelopeFileOf.get('error.response'), 'errorResponse');
});

// Compile-time assertions of the schema's exclusive choices: each suppressed
// line below fails the build if the interface ever accepts a value the
// daemon's schema gate would reject.
test('the schema exclusivity rules are compile-time errors', () => {
  // @ts-expect-error url and inline data are mutually exclusive image forms
  const badImage: ImageContent = { url: 'https://example.test/i.png', data: 'aGk=', media_type: 'text/plain' };
  // @ts-expect-error an image with data requires its media type
  const dataOnly: ImageContent = { data: 'aGk=' };
  // @ts-expect-error an empty parts list is not message content
  const emptyParts: MessageContent = [];
  // @ts-expect-error an answer carries text or selections, not both
  const bothAnswer: InputAnswer = { question_id: 'q-1', text: 'hi', selected_option_ids: ['yes'] };
  // @ts-expect-error an answer must carry exactly one value
  const bareAnswer: InputAnswer = { question_id: 'q-1' };
  // @ts-expect-error a choice answer needs at least one selected option
  const noSelection: InputAnswer = { question_id: 'q-1', selected_option_ids: [] };
  // @ts-expect-error a submission needs at least one message
  const noMessages: protocol.MessageSubmitRequest = { session_id: 's-1', messages: [], delivery: 'auto' };
  // @ts-expect-error an input resolution needs at least one answer
  const noAnswers: protocol.UserInputResolveRequest = { interaction_id: 'i-2', requested_by: 'agent', responded_by: 'user', session_id: 's-1', run_id: 'r-1', answers: [] };
  // @ts-expect-error a permission request needs at least one choice
  const noChoices: protocol.PermissionRequestedPayload = { interaction_id: 'i-1', requested_by: 'agent', responded_by: 'user', session_id: 's-1', run_id: 'r-1', title: 'Allow tool', choices: [] };
  // @ts-expect-error an input request needs at least one question
  const noQuestions: protocol.UserInputRequestedPayload = { interaction_id: 'i-2', requested_by: 'agent', responded_by: 'user', session_id: 's-1', run_id: 'r-1', title: 'Golden input', questions: [] };
  // @ts-expect-error a text question carries no options
  const textWithOptions: protocol.InputQuestion = { id: 'q-1', prompt: 'name', kind: 'text', options: [{ id: 'a', label: 'A' }] };
  // @ts-expect-error a choice question requires its options
  const choiceWithoutOptions: protocol.InputQuestion = { id: 'q-1', prompt: 'pick', kind: 'single_choice' };
  // @ts-expect-error a submitted resolution carries at least one answer
  const submittedWithoutAnswers: protocol.UserInputResolvedPayload = { interaction_id: 'i-2', requested_by: 'agent', responded_by: 'user', session_id: 's-1', run_id: 'r-1', status: 'submitted' };
  // @ts-expect-error a cancelled resolution carries no answers
  const cancelledWithAnswers: protocol.UserInputResolvedPayload = { interaction_id: 'i-2', requested_by: 'agent', responded_by: 'user', session_id: 's-1', run_id: 'r-1', status: 'cancelled', answers: [{ question_id: 'q-1', text: 'hi' }] };
  // @ts-expect-error a completed call carries no progress field
  const completedWithProgress: protocol.ActionCallCompletedPayload = { session_id: 's-1', run_id: 'r-1', tool_call_id: 't-1', execution_owner: 'user', result: { ok: true }, progress: { percent: 1 } };
  // @ts-expect-error a progress event carries no result field
  const progressWithResult: protocol.ActionCallProgressPayload = { session_id: 's-1', run_id: 'r-1', tool_call_id: 't-1', execution_owner: 'user', progress: { percent: 1 }, result: { ok: true } };
  // @ts-expect-error a failed call carries no arguments field
  const failedWithArguments: protocol.ActionCallFailedPayload = { session_id: 's-1', run_id: 'r-1', tool_call_id: 't-1', execution_owner: 'user', error: { code: 'x', message: 'boom' }, arguments_json: {} };
  // @ts-expect-error a cancelled call carries none of the exclusive fields
  const cancelledWithError: protocol.ActionCallCancelledPayload = { session_id: 's-1', run_id: 'r-1', tool_call_id: 't-1', execution_owner: 'user', error: { code: 'x', message: 'boom' } };
  // @ts-expect-error an initialize request needs at least one version
  const noVersions: protocol.InitializeRequest = { protocol_versions: [], profiles: ['open-agent-protocol.agent-control-core'] };
  // @ts-expect-error a capability snapshot needs at least one profile when present
  const noProfiles: protocol.CapabilityDescriptor = { endpoint: { id: 'e-1' }, profiles: [] };
  // @ts-expect-error a layer needs at least one delivery mode when present
  const noModes: protocol.CapabilityLayer = { requested_delivery_modes: [] };
  // @ts-expect-error a required JSON field cannot be undefined (stringify would drop it)
  const undefinedArguments: protocol.ToolCallPart = { type: 'tool_call', tool_call_id: 't-1', name: 'echo', arguments_json: undefined };
  // @ts-expect-error a required JSON result cannot be undefined either
  const undefinedResult: protocol.ToolResultPart = { type: 'tool_result', tool_call_id: 't-1', result: undefined };
  // @ts-expect-error an empty request payload allows no fields
  const loadedEmptyRequest: protocol.CapabilitiesRequest = { unexpected: 'value' };
  void badImage;
  void dataOnly;
  void emptyParts;
  void bothAnswer;
  void bareAnswer;
  void noSelection;
  void noMessages;
  void noAnswers;
  void noChoices;
  void noQuestions;
  void textWithOptions;
  void choiceWithoutOptions;
  void submittedWithoutAnswers;
  void cancelledWithAnswers;
  void completedWithProgress;
  void progressWithResult;
  void failedWithArguments;
  void cancelledWithError;
  void noVersions;
  void noProfiles;
  void noModes;
  void undefinedArguments;
  void undefinedResult;
  void loadedEmptyRequest;
  // Legal JSON values still type-check on the required fields.
  const jsonArguments: protocol.ToolCallPart = { type: 'tool_call', tool_call_id: 't-2', name: 'echo', arguments_json: { cmd: ['ls', '-l'] } };
  const nullResult: protocol.ToolResultPart = { type: 'tool_result', tool_call_id: 't-2', result: null };
  const emptyRequest: protocol.CapabilitiesRequest = {};
  void jsonArguments;
  void nullResult;
  void emptyRequest;
  // The two legal shapes still type-check.
  const urlImage: ImageContent = { url: 'https://example.test/i.png' };
  const inlineImage: ImageContent = { data: 'aGk=', media_type: 'text/plain' };
  const parts: MessageContent = [{ type: 'text', text: 'hi' }];
  const textAnswer: InputAnswer = { question_id: 'q-1', text: 'hi' };
  const choiceAnswer: InputAnswer = { question_id: 'q-1', selected_option_ids: ['yes'] };
  const textQuestion: protocol.InputQuestion = { id: 'q-1', prompt: 'name', kind: 'text' };
  const choiceQuestion: protocol.InputQuestion = { id: 'q-2', prompt: 'pick', kind: 'single_choice', options: [{ id: 'a', label: 'A' }] };
  const cancelledResolution: protocol.UserInputResolvedPayload = { interaction_id: 'i-2', requested_by: 'agent', responded_by: 'user', session_id: 's-1', run_id: 'r-1', status: 'cancelled' };
  void urlImage;
  void inlineImage;
  void parts;
  void textAnswer;
  void choiceAnswer;
  void textQuestion;
  void choiceQuestion;
  void cancelledResolution;
});
