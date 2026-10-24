const imports = `
import { EnvelopeType, PROTOCOL, PROFILE, VERSION, parseEnvelope, serializeEnvelope } from 'oap-client';
import type { Envelope, AuthProvider, AuthStatus, AuthProvidersRequest, AuthProvidersResponse,
  AuthLoginStartRequest, AuthLoginStartResponse, AuthLoginEvent, AuthLoginCancelRequest,
  AuthLoginCancelResponse, AuthLoginCompleted } from 'oap-client';
`;

function source(kinds: string, urlFields: string, progressFields: string, optional = true): string {
  return imports + `
const provider: AuthProvider = { id: 'example', name: 'Example', auth_kinds: ${kinds}, auth_status: 'login_required'
  ${optional ? ", last_error: 'fixture prior failure', override_host: 'proxy.example'" : ''} };
const statuses: AuthStatus[] = ['authenticated', 'login_required', 'expired', 'refreshing', 'login_in_progress', 'failed', 'unknown'];
const providersRequest: AuthProvidersRequest = {};
const providersResponse: AuthProvidersResponse = { providers: [provider] };
const emptyProviders: AuthProvidersResponse = { providers: [] };
const startRequest: AuthLoginStartRequest = { provider_id: 'example' };
const startResponse: AuthLoginStartResponse = { flow_id: 'flow-1' };
const url: AuthLoginEvent = { flow_id: 'flow-1', provider_id: 'example', kind: 'url', ${urlFields} };
const progress: AuthLoginEvent = { flow_id: 'flow-1', provider_id: 'example', kind: 'progress', ${progressFields} };
const cancelRequest: AuthLoginCancelRequest = { flow_id: 'flow-1' };
const cancelResponse: AuthLoginCancelResponse = { flow_id: 'flow-1', accepted: true };
const completed: AuthLoginCompleted = { flow_id: 'flow-1', provider_id: 'example', status: 'cancelled'
  ${optional ? ", error: { code: 'cancelled_by_user', message: 'fixture cancellation' }" : ''} };
const success: AuthLoginCompleted = { flow_id: 'flow-2', provider_id: 'example', status: 'success' };
const failed: AuthLoginCompleted = { flow_id: 'flow-3', provider_id: 'example', status: 'failed' };
function frame(type: string, id: string, payload: object, extra: Partial<Envelope> = {}): Envelope {
  return parseEnvelope(serializeEnvelope({ protocol: PROTOCOL, version: VERSION, profile: PROFILE,
    type, id, payload: { ...payload }, ...extra }));
}
const trace = [
  frame(EnvelopeType.AuthProvidersRequest, 'providers-req', providersRequest),
  frame(EnvelopeType.AuthProvidersResponse, 'providers-resp', providersResponse, { in_reply_to: 'providers-req' }),
  frame(EnvelopeType.AuthLoginStartRequest, 'start-req', startRequest),
  frame(EnvelopeType.AuthLoginStartResponse, 'start-resp', startResponse, { in_reply_to: 'start-req' }),
  frame(EnvelopeType.AuthLoginEvent, 'url', url, { sequence: 1n }),
  frame(EnvelopeType.AuthLoginEvent, 'progress', progress, { sequence: 2n }),
  frame(EnvelopeType.AuthLoginCancelRequest, 'cancel-req', cancelRequest),
  frame(EnvelopeType.AuthLoginCancelResponse, 'cancel-resp', cancelResponse, { in_reply_to: 'cancel-req' }),
  frame(EnvelopeType.AuthLoginCompleted, 'done', completed, { sequence: 3n }),
];
void statuses;
void emptyProviders;
void success;
void failed;
console.log('[' + trace.map(serializeEnvelope).join(',') + ']');
`;
}

const url = "url: 'https://example.invalid/login'";
const progress = "message: 'Complete the synthetic flow'";

export const authSchemaPrecisionCases = [
  { name: 'auth-optional-fields', accepts: true, wire: true,
    source: source("['api_key', 'oauth', 'none']", url + ", message: 'also allowed', instructions: 'fixture instructions'",
      progress + ", url: 'https://example.invalid/progress', instructions: 'also allowed'") },
  { name: 'auth-omitted-fields', accepts: true, wire: true, source: source("['none']", url, progress, false) },
  { name: 'auth-empty-kinds', accepts: false, wire: true, source: source('[]', url, progress) },
  { name: 'auth-url-without-url', accepts: false, wire: true, source: source("['api_key']", "message: 'missing url'", progress) },
  { name: 'auth-progress-without-message', accepts: false, wire: true, source: source("['api_key']", url, "url: 'https://example.invalid/progress'") },
] as const;
