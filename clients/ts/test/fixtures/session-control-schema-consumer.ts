import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { findRepoRoot } from '../transport.js';

const root = findRepoRoot(dirname(fileURLToPath(import.meta.url)));

function source(kind: 'switch' | 'attach', optional: boolean, missing?: 'request' | 'response'): string {
  const trace = JSON.parse(readFileSync(join(root, 'fixtures/valid', kind === 'switch' ? 'model-switch-live.json' : 'provider-attach-live.json'), 'utf8')) as unknown;
  const fields = kind === 'switch'
    ? `const request: SessionModelSwitchRequest = { session_id: 'switch-session'${missing === 'request' ? '' : ", model_id: 'model-two'"}${optional ? ", allow_degraded_features: ['session.model.switch']" : ''} };
const response: SessionModelSwitchResponse = { session_id: 'switch-session'${missing === 'response' ? '' : ", model_id: 'model-two'"}${optional ? ", previous_model_id: 'model-one'" : ''} };`
    : `const provider: ProviderAttachment = { id: 'provider-two'${missing === 'request' ? '' : ", provider_id: 'upstream-two'"}${optional ? ", service_id: 'example-service'" : ''} };
const request: SessionProviderAttachRequest = { session_id: 'attach-session', provider${optional ? ", allow_degraded_features: ['action.providers.attach']" : ''} };
const response: SessionProviderAttachResponse = { session_id: 'attach-session'${missing === 'response' ? '' : ", provider_id: 'provider-two'"} };`;
  return `import { EnvelopeType, parseEnvelope, serializeEnvelope } from 'oap-client';
import type { Envelope, SessionModelSwitchRequest, SessionModelSwitchResponse,
  ProviderAttachment, SessionProviderAttachRequest, SessionProviderAttachResponse } from 'oap-client';
${fields}
const trace: Envelope[] = ${JSON.stringify(trace)}.map(value => parseEnvelope(JSON.stringify(value)));
trace[6]!.type = EnvelopeType.${kind === 'switch' ? 'SessionModelSwitchRequest' : 'SessionProviderAttachRequest'};
trace[6]!.payload = { ...request };
trace[7]!.type = EnvelopeType.${kind === 'switch' ? 'SessionModelSwitchResponse' : 'SessionProviderAttachResponse'};
trace[7]!.payload = { ...response };
console.log('[' + trace.map(serializeEnvelope).join(',') + ']');
`;
}

export const sessionControlSchemaCases = [
  { name: 'switch-optional-fields', accepts: true, wire: true, source: source('switch', true) },
  { name: 'switch-omitted-fields', accepts: true, wire: true, source: source('switch', false) },
  { name: 'attach-optional-fields', accepts: true, wire: true, source: source('attach', true) },
  { name: 'attach-omitted-fields', accepts: true, wire: true, source: source('attach', false) },
  { name: 'switch-missing-request-model', accepts: false, wire: true, diagnostic: 'TS2741', source: source('switch', true, 'request') },
  { name: 'switch-missing-response-model', accepts: false, wire: true, diagnostic: 'TS2741', source: source('switch', true, 'response') },
  { name: 'attach-missing-upstream-provider', accepts: false, wire: true, diagnostic: 'TS2741', source: source('attach', true, 'request') },
  { name: 'attach-missing-response-provider', accepts: false, wire: true, diagnostic: 'TS2741', source: source('attach', true, 'response') },
] as const;
