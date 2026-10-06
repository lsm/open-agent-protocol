
export const PROTOCOL = 'open-agent-protocol';
export const VERSION = '0.1';
export const PROFILE = 'open-agent-protocol.agent-control-core';

export const EnvelopeType = {
  ProtocolInitializeRequest: 'protocol.initialize.request',
  ProtocolInitializeResponse: 'protocol.initialize.response',
  CapabilitiesRequest: 'capabilities.request',
  CapabilitiesResponse: 'capabilities.response',
  CapabilitiesUpdated: 'capabilities.updated',
  ModelsRequest: 'models.request',
  ModelsResponse: 'models.response',
  AuthProvidersRequest: 'auth.providers.request',
  AuthProvidersResponse: 'auth.providers.response',
  AuthLoginStartRequest: 'auth.login.start.request',
  AuthLoginStartResponse: 'auth.login.start.response',
  AuthLoginEvent: 'auth.login.event',
  AuthLoginCancelRequest: 'auth.login.cancel.request',
  AuthLoginCancelResponse: 'auth.login.cancel.response',
  AuthLoginCompleted: 'auth.login.completed',
  SessionOpenRequest: 'session.open.request',
  SessionOpenResponse: 'session.open.response',
  SessionStateRequest: 'session.state.request',
  SessionStateResponse: 'session.state.response',
  SessionStateUpdated: 'session.state.updated',
  SessionListRequest: 'session.list.request',
  SessionListResponse: 'session.list.response',
  SessionModelSwitchRequest: 'session.model.switch.request',
  SessionModelSwitchResponse: 'session.model.switch.response',
  SessionSettingsUpdateRequest: 'session.settings.update.request',
  SessionSettingsUpdateResponse: 'session.settings.update.response',
  SessionProviderAttachRequest: 'session.provider.attach.request',
  SessionProviderAttachResponse: 'session.provider.attach.response',
  SessionMessageSubmitRequest: 'session.message.submit.request',
  SessionMessageSubmitResponse: 'session.message.submit.response',
  SessionCompactRequest: 'session.compact.request',
  SessionCompactResponse: 'session.compact.response',
  RunCancelRequest: 'run.cancel.request',
  RunCancelResponse: 'run.cancel.response',
  RunSteerApplied: 'run.steer.applied',
  RunSteerDropped: 'run.steer.dropped',
  RunCompactionStarted: 'run.compaction.started',
  RunCompactionEnded: 'run.compaction.ended',
  RunStarted: 'run.started',
  RunStatusUpdated: 'run.status.updated',
  ContentDelta: 'content.delta',
  RunCompleted: 'run.completed',
  RunFailed: 'run.failed',
  RunCancelled: 'run.cancelled',
  ActionToolsListRequest: 'action.tools.list.request',
  ActionToolsListResponse: 'action.tools.list.response',
  ActionCallRequested: 'action.call.requested',
  ActionCallStarted: 'action.call.started',
  ActionCallProgress: 'action.call.progress',
  ActionCallCompleted: 'action.call.completed',
  ActionCallFailed: 'action.call.failed',
  ActionCallCancelled: 'action.call.cancelled',
  ActionCallResolveRequest: 'action.call.resolve.request',
  ActionCallResolveResponse: 'action.call.resolve.response',
  ActionPermissionRequested: 'action.permission.requested',
  ActionPermissionResolveRequest: 'action.permission.resolve.request',
  ActionPermissionResolveResponse: 'action.permission.resolve.response',
  ActionPermissionResolved: 'action.permission.resolved',
  UserInputRequested: 'user.input.requested',
  UserInputResolveRequest: 'user.input.resolve.request',
  UserInputResolveResponse: 'user.input.resolve.response',
  UserInputResolved: 'user.input.resolved',
  UserInputCancelRequest: 'user.input.cancel.request',
  UserInputCancelResponse: 'user.input.cancel.response',
  ErrorResponse: 'error.response',
} as const;

export type EnvelopeType = (typeof EnvelopeType)[keyof typeof EnvelopeType] | (string & {});

export interface Envelope {
  protocol: string;
  version: string;
  profile: string;
  type: EnvelopeType;
  id: string;
  payload: Record<string, unknown>;
  sequence?: number;
  timestamp_ms?: number;
  in_reply_to?: string;
  session_id?: string;
  run_id?: string;
  turn_id?: string;
  tool_call_id?: string;
  capability_revision?: string;
  extensions?: Record<string, unknown>;
}

export function parseEnvelope(data: string): Envelope {
  let value: unknown;
  try {
    value = JSON.parse(data);
  } catch (err) {
    throw new Error(`decode envelope: ${err instanceof Error ? err.message : String(err)}`);
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('decode envelope: not a JSON object');
  }
  return value as Envelope;
}

export function payload<T>(envelope: Envelope): T {
  if (typeof envelope.payload !== 'object' || envelope.payload === null || Array.isArray(envelope.payload)) {
    throw new Error(`decode ${envelope.type} payload: missing payload`);
  }
  return envelope.payload as T;
}

export type MessageRole = 'system' | 'developer' | 'user' | 'assistant' | 'tool';

export type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };

export type SupportLevel = 'native' | 'emulated' | 'degraded' | 'unavailable';

export interface UrlImage {
  url: string;
  data?: undefined;
  media_type?: undefined;
}

export interface InlineImage {
  data: string;
  media_type: string;
  url?: undefined;
}

export type ImageContent = UrlImage | InlineImage;

export interface TextPart {
  type: 'text';
  text: string;
}

export interface ReasoningPart {
  type: 'reasoning';
  reasoning: string;
  carry?: string;
}

export interface ImagePart {
  type: 'image';
  image: ImageContent;
}

export interface ToolCallPart {
  type: 'tool_call';
  tool_call_id: string;
  name: string;
  arguments_json: JSONValue;
  carry?: string;
}

export interface ToolResultPart {
  type: 'tool_result';
  tool_call_id: string;
  result: JSONValue;
  is_error?: boolean;
}

export type ContentPart = TextPart | ReasoningPart | ImagePart | ToolCallPart | ToolResultPart;

export type MessageContent = string | [ContentPart, ...ContentPart[]];

export function textContent(content: MessageContent): string | null {
  return typeof content === 'string' ? content : null;
}

export function partsContent(content: MessageContent): [ContentPart, ...ContentPart[]] | null {
  return Array.isArray(content) ? content : null;
}

export interface Message {
  id?: string;
  role: MessageRole;
  content: MessageContent;
  metadata?: Record<string, unknown>;
}

export interface Usage {
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
}

export interface ProtocolError {
  code: string;
  message: string;
  retriable?: boolean;
  details?: Record<string, unknown>;
}

export interface ErrorResponse {
  error: ProtocolError;
}

export interface RecoveryMetadata {
  recovered?: boolean;
  previous_session_id?: string;
  previous_run_id?: string;
  resume_cursor?: string;
  reason?: string;
}

export interface EndpointDescriptor {
  id: string;
  name?: string;
  version?: string;
  adapter?: string;
}

export interface Participant {
  id: string;
  name?: string;
  version?: string;
}

export interface FeatureSupport {
  level: SupportLevel;
  reason?: string;
  scope?: string;
  modes?: string[];
  constraints?: { fixed_result?: Record<string, unknown> } & Record<string, unknown>;
  limits?: {
    max_sources?: number;
    transports?: ToolSourceKind[];
    max_tools?: number;
    name_pattern?: string;
    schema_dialect?: string;
  } & Record<string, unknown>;
}

export type ToolSourceKind = 'native' | 'local' | 'process' | 'remote' | 'hosted';

export interface ToolSourceDescriptor {
  id: string;
  kind: ToolSourceKind;
  display_name?: string;
  protocol?: string;
  endpoint?: string;
}

export interface ToolDefinition {
  name: string;
  description?: string;
  input_schema: Record<string, unknown>;
  execution_owner: string;
  source?: string;
  features?: Record<string, FeatureSupport>;
  annotations?: Record<string, unknown>;
}

export interface Binding {
  kind: string;
  serialization?: string;
}

export interface Degradation {
  feature: string;
  from?: SupportLevel;
  to: SupportLevel;
  mode?: string;
  reason: string;
}

export type RequestedDeliveryMode = 'auto' | 'queue' | 'steer' | 'btw';
export type EffectiveDeliveryMode = 'start' | 'queue' | 'steer' | 'btw';

export interface CapabilityLayer {
  features?: Record<string, FeatureSupport>;
  requested_delivery_modes?: [RequestedDeliveryMode, ...RequestedDeliveryMode[]];
  effective_delivery_modes?: [EffectiveDeliveryMode, ...EffectiveDeliveryMode[]];
  tools?: ToolDefinition[];
  sources?: ToolSourceDescriptor[];
}

export interface InitializeRequest {
  protocol_versions: [string, ...string[]];
  profiles: [string, ...string[]];
  participant?: Participant;
}

export interface InitializeResponse {
  protocol_version: string;
  profile: string;
  endpoint: EndpointDescriptor;
}

export interface EmptyRequestPayload {
  readonly [key: string]: never;
}

export type CapabilitiesRequest = EmptyRequestPayload;

export interface CapabilityDescriptor {
  endpoint: EndpointDescriptor;
  protocol_versions?: [string, ...string[]];
  profiles?: [string, ...string[]];
  bindings?: Binding[];
  features?: Record<string, FeatureSupport>;
  layers?: Record<string, CapabilityLayer>;
  tools?: ToolDefinition[];
  sources?: ToolSourceDescriptor[];
  degradation?: Degradation[];
  limits?: CapabilityLimits;
}

export interface CapabilityLimits {
  max_active_runs_per_session?: number;
  max_queued_runs_per_session?: number;
}

export interface CapabilitiesUpdated {
  previous_revision: string;
  reason?: string;
}

export interface ModelsRequest {
  session_id: string;
  allow_degraded_features?: string[];
}

export interface ModelDescriptor {
  id: string;
  display_name?: string;
  provider_id?: string;
  context_window?: number;
  features?: Record<string, FeatureSupport>;
  default?: boolean;
}

export interface ProviderDescriptor {
  id: string;
  display_name?: string;
  wire?: 'openai-responses' | 'anthropic-messages' | 'openai-chat-completions';
  kind?: 'direct' | 'gateway';
  endpoint?: string;
  service_id?: string;
  upstream_provider_id?: string;
}

export type ModelEventPosition =
  | { run_id: string; sequence: number; switch_request_id?: never }
  | { switch_request_id: string; run_id?: never; sequence?: never };

export interface ModelsResponse {
  session_id: string;
  current_model_id?: string;
  models: ModelDescriptor[];
  providers?: ProviderDescriptor[];
  as_of_model_event?: ModelEventPosition;
}

export type SessionStatus = 'idle' | 'queued' | 'running' | 'waiting_for_input' | 'closed' | 'error';

export interface ToolSourceAttachment {
  id: string;
  kind: ToolSourceKind;
  display_name?: string;
  protocol?: string;
  endpoint?: string;
  command?: string;
  args?: string[];
  environment?: string[];
}

export type OpenMessage = Omit<MessageSubmitRequest, 'session_id'>;

export interface SessionOpenRequest {
  session_id?: string;
  subscribe?: boolean;
  reopen?: boolean;
  message?: OpenMessage;
  metadata?: Record<string, unknown>;
  tool_sources?: ToolSourceAttachment[];
  tools?: ToolDefinition[];
  allow_degraded_features?: string[];
  recovery?: RecoveryMetadata;
  reasoning_level?: ReasoningLevel;
  compaction_policy?: CompactionPolicy;
}

export type ReasoningLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max';

export type CompactionPolicy =
  | { kind: 'auto' }
  | { kind: 'off' }
  | { kind: 'share'; share_percent: number }
  | { kind: 'tokens'; tokens: number };

export type SessionOpenResponse = SessionState;
export type SessionStateResponse = SessionState;
export type SessionStateUpdated = SessionState;

export interface SessionListRequest {
  cursor?: string;
  limit?: number;
  allow_degraded_features?: string[];
}

export interface SessionListEntry {
  session_id: string;
  adapter: string;
  harness_version?: string;
  state: 'live' | 'closed';
  updated_at_ms: number;
  model?: string;
  directory?: string;
}

export interface SessionListResponse {
  sessions: SessionListEntry[];
  next_cursor?: string;
}

export interface SessionStateRequest {
  session_id: string;
}

export type SessionSettingsUpdateRequest = {
  session_id: string;
  allow_degraded_features?: string[];
} & SessionSettings;

export type SessionSettingsUpdateResponse = {
  session_id: string;
  previous_reasoning_level?: ReasoningLevel;
  previous_compaction_policy?: CompactionPolicy;
} & SessionSettings;

export type SessionSettings =
  | { reasoning_level: ReasoningLevel; compaction_policy?: CompactionPolicy }
  | { reasoning_level?: ReasoningLevel; compaction_policy: CompactionPolicy };

export interface SessionState {
  session_id: string;
  status: SessionStatus;
  active_run_id?: string;
  active_runs?: ActiveRun[];
  current_model_id?: string;
  transcript_cursor?: string;
  updated_at_ms?: number;
  metadata?: Record<string, unknown>;
  sources?: ToolSourceDescriptor[];
  recovery?: RecoveryMetadata;
  as_of?: SessionCapture;
  reasoning_level?: ReasoningLevel;
  compaction_policy?: CompactionPolicy;
}

export type ActiveRunRelationship = 'primary';

export interface PendingSteer {
  submission_id: string;
  request_id: string;
}

export interface ActiveRun {
  pending_steers?: PendingSteer[];
  run_id: string;
  status: RunStatus;
  relationship: ActiveRunRelationship;
  queue_position?: number;
  as_of_sequence?: number;
  admitted_submit_requests?: string[];
  pending_interactions?: string[];
  acknowledged_interactions?: string[];
}

export interface RunPosition {
  run_id: string | null;
  sequence: number;
}

export interface SettledRun {
  run_id: string;
  sequence: number;
}

export interface SessionCapture {
  admitted_submit_requests?: string[];
  settled?: SettledRun[];
  model_run_sequence?: RunPosition;
}

export type ToolChoicePolicy =
  | { allowed: string[]; disallowed?: undefined }
  | { allowed?: undefined; disallowed: string[] };

export interface MessageSubmitRequest {
  target_run_id?: string;
  session_id: string;
  messages: [Message, ...Message[]];
  delivery: RequestedDeliveryMode;
  model_id?: string;
  instructions?: string;
  tool_choice?: ToolChoicePolicy | unknown;
  output_schema?: Record<string, unknown>;
  allow_degraded_features?: string[];
  metadata?: Record<string, unknown>;
}

export type Admission = 'started' | 'queued' | 'steered' | 'side_started' | 'rejected';

export interface MessageSubmitResponse {
  target_sequence?: number;
  session_id: string;
  accepted: boolean;
  submission_id: string;
  requested_delivery: RequestedDeliveryMode;
  effective_delivery: EffectiveDeliveryMode;
  delivery_resolution?: string;
  admission: Admission;
  run_id?: string;
  status?: RunStatus;
  model_id?: string;
  message_ids?: string[];
}

export type RunStatus = 'queued' | 'running' | 'waiting_for_input' | 'cancelling' | 'completed' | 'failed' | 'cancelled';

export interface RunCancelRequest {
  session_id: string;
  run_id: string;
  reason?: string;
}

export interface RunCancelResponse {
  session_id: string;
  run_id: string;
  accepted: boolean;
  status: RunStatus;
}

export interface RunStartedPayload {
  session_id: string;
  run_id: string;
  status: 'running';
  model_id?: string;
  started_at_ms?: number;
}

export interface RunStatusUpdatedPayload {
  session_id: string;
  run_id: string;
  status: RunStatus;
  pending_user_input_id?: string;
  updated_at_ms?: number;
}

export interface ContentDeltaPayload {
  session_id: string;
  run_id: string;
  message_id?: string;
  part: ContentPart;
}

export type SettledBy = 'observed' | 'inferred';

export interface RunCompletedPayload {
  session_id: string;
  run_id: string;
  final_response: Message;
  stop_reason: string;
  model_id?: string;
  result?: Record<string, unknown>;
  usage?: Usage;
  duration_ms?: number;
  settled_by?: SettledBy;
}

export interface RunFailedPayload {
  session_id: string;
  run_id: string;
  error: ProtocolError;
  usage?: Usage;
  duration_ms?: number;
  recovery?: RecoveryMetadata;
  settled_by?: SettledBy;
}

export interface RunCancelledPayload {
  session_id: string;
  run_id: string;
  reason?: string;
  usage?: Usage;
  duration_ms?: number;
  settled_by?: SettledBy;
}

export interface ToolsListRequest {
  session_id?: string;
  allow_degraded_features?: string[];
}

export interface ToolsListResponse {
  session_id?: string;
  sources?: ToolSourceDescriptor[];
  tools: ToolDefinition[];
}

interface ActionCallBase {
  interaction_id?: string;
  session_id: string;
  run_id: string;
  tool_call_id: string;
  requested_by?: string;
  responded_by?: string;
  execution_owner: string;
  source?: string;
  name?: string;
}

export interface ActionCallRequestedPayload extends ActionCallBase {
  requested_by: string;
  name: string;
  arguments_json: JSONValue;
  progress?: undefined;
  result?: undefined;
  error?: undefined;
}

export interface ActionCallStartedPayload extends ActionCallBase {
  name: string;
  request_id?: string;
  arguments_json?: unknown;
  progress?: undefined;
  result?: undefined;
  error?: undefined;
}

export interface ActionCallProgressPayload extends ActionCallBase {
  progress: JSONValue;
  arguments_json?: undefined;
  result?: undefined;
  error?: undefined;
}

export interface ActionCallCompletedPayload extends ActionCallBase {
  result: JSONValue;
  request_id?: string;
  arguments_json?: undefined;
  progress?: undefined;
  error?: undefined;
}

export interface ActionCallFailedPayload extends ActionCallBase {
  error: ProtocolError;
  request_id?: string;
  arguments_json?: undefined;
  progress?: undefined;
  result?: undefined;
}

export interface ActionCallCancelledPayload extends ActionCallBase {
  arguments_json?: undefined;
  progress?: undefined;
  result?: undefined;
  error?: undefined;
}

interface ActionCallResolveBase {
  interaction_id: string;
  session_id: string;
  run_id: string;
  tool_call_id: string;
  requested_by: string;
  responded_by: string;
}

export interface ActionCallAcknowledgeRequest extends ActionCallResolveBase {
  started: Record<string, never>;
  result?: undefined;
  error?: undefined;
}

export interface ActionCallResultRequest extends ActionCallResolveBase {
  result: JSONValue;
  started?: undefined;
  error?: undefined;
}

export interface ActionCallErrorRequest extends ActionCallResolveBase {
  error: ProtocolError;
  started?: undefined;
  result?: undefined;
}

export type ActionCallResolveRequest =
  | ActionCallAcknowledgeRequest
  | ActionCallResultRequest
  | ActionCallErrorRequest;

export type ResolveRefusalReason =
  | 'unknown_interaction'
  | 'wrong_responder'
  | 'already_resolved'
  | 'repeated_acknowledgement'
  | 'late_acknowledgement';

export interface ActionCallResolveDetails {
  settlement_id: string;
}

interface ActionCallResolveResponseBase {
  interaction_id: string;
  session_id: string;
  run_id: string;
  tool_call_id: string;
}

export interface ActionCallResolveAccepted extends ActionCallResolveResponseBase {
  accepted: true;
  reason?: undefined;
  details?: undefined;
}

export interface ActionCallResolveSettled extends ActionCallResolveResponseBase {
  accepted: false;
  reason: 'already_resolved';
  details: ActionCallResolveDetails;
}

export interface ActionCallResolveRefused extends ActionCallResolveResponseBase {
  accepted: false;
  reason: Exclude<ResolveRefusalReason, 'already_resolved'>;
  details?: undefined;
}

export type ActionCallResolveResponse =
  | ActionCallResolveAccepted
  | ActionCallResolveSettled
  | ActionCallResolveRefused;

export interface PermissionChoice {
  id: string;
  label: string;
  description?: string;
}

export interface PermissionRequestedPayload {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
  tool_call_id?: string;
  title: string;
  description?: string;
  choices: [PermissionChoice, ...PermissionChoice[]];
  arguments_json?: unknown;
}

export interface PermissionResolveRequest {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
  choice_id?: string;
  granted: boolean;
  reason?: string;
  updated_arguments_json?: unknown;
}

export interface PermissionResolveResponse {
  interaction_id: string;
  session_id: string;
  run_id: string;
  accepted: boolean;
}

export interface PermissionResolvedPayload {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
  tool_call_id?: string;
  outcome: 'resolved' | 'rejected' | 'cancelled' | 'failed';
  choice_id?: string;
  granted?: boolean;
  reason?: ProtocolError;
}

export type InputQuestionKind = 'text' | 'single_choice' | 'multi_choice';

export interface InputOption {
  id: string;
  label: string;
  description?: string;
}

export interface TextQuestion {
  id: string;
  prompt: string;
  kind: 'text';
  required?: boolean;
  options?: undefined;
}

export interface ChoiceQuestion {
  id: string;
  prompt: string;
  kind: 'single_choice' | 'multi_choice';
  required?: boolean;
  options: [InputOption, ...InputOption[]];
}

export type InputQuestion = TextQuestion | ChoiceQuestion;

export interface TextAnswer {
  question_id: string;
  text: string;
  selected_option_ids?: undefined;
}

export interface SelectedOptionsAnswer {
  question_id: string;
  selected_option_ids: [string, ...string[]];
  text?: undefined;
}

export type InputAnswer = TextAnswer | SelectedOptionsAnswer;

export interface UserInputRequestedPayload {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
  tool_call_id?: string;
  title: string;
  description?: string;
  questions: [InputQuestion, ...InputQuestion[]];
  allow_cancel?: boolean;
  draft_answers?: InputAnswer[];
}

export interface UserInputResolveRequest {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
  answers: [InputAnswer, ...InputAnswer[]];
}

export interface UserInputResolveResponse {
  interaction_id: string;
  session_id: string;
  run_id: string;
  accepted: boolean;
}

interface UserInputResolvedFields {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
}

export interface SubmittedInputResolved extends UserInputResolvedFields {
  status: 'submitted';
  answers: [InputAnswer, ...InputAnswer[]];
}

export interface CancelledInputResolved extends UserInputResolvedFields {
  status: 'cancelled';
  answers?: undefined;
}

export type UserInputResolvedPayload = SubmittedInputResolved | CancelledInputResolved;

export interface UserInputCancelRequest {
  interaction_id: string;
  requested_by: string;
  responded_by: string;
  session_id: string;
  run_id: string;
  reason?: string;
}

export type UserInputCancelResponse = UserInputResolveResponse;

export type SteerBoundary = "immediate" | "turn" | "tool_result" | "unknown";

export interface RunSteerAppliedPayload {
  session_id: string;
  run_id: string;
  submission_id: string;
  request_id: string;
  message_ids: string[];
  boundary: SteerBoundary;
}

export type CompactionReason = 'requested' | 'threshold' | 'overflow';

export type CompactionOutcome = 'completed' | 'failed' | 'cancelled';

export interface RunCompactionStartedPayload {
  session_id: string;
  run_id: string;
  compaction_id: string;
  reason: CompactionReason;
  history_tokens?: number;
}

export interface RunCompactionEndedPayload {
  session_id: string;
  run_id: string;
  compaction_id: string;
  outcome: CompactionOutcome;
  summary?: Message;
  history_tokens?: number;
  error?: ProtocolError;
}

export interface SessionCompactRequest {
  session_id: string;
  delivery?: RequestedDeliveryMode;
  focus?: string;
  continue?: boolean;
  allow_degraded_features?: string[];
  metadata?: Record<string, unknown>;
}

export interface SessionCompactResponse {
  session_id: string;
  accepted: boolean;
  submission_id: string;
  requested_delivery: RequestedDeliveryMode;
  effective_delivery: EffectiveDeliveryMode;
  delivery_resolution?: string;
  admission: Admission;
  run_id?: string;
  status?: RunStatus;
}

export interface RunSteerDroppedPayload {
  session_id: string;
  run_id: string;
  submission_id: string;
  request_id: string;
  reason: ProtocolError;
}
