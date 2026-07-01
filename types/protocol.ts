/**
 * Draft Open Agent Protocol TypeScript contract.
 *
 * This file is intentionally dependency-free and public-domain compatible under
 * the repository's CC0-1.0 dedication.
 */

export const PROTOCOL_ID = "open-agent-protocol" as const;
export const PROTOCOL_VERSION = "0.1" as const;

export type OpaqueId = string;
export type ProtocolVersion = typeof PROTOCOL_VERSION | (string & {});
export type ExtensionMap = Record<string, unknown>;
export type JsonSchema = boolean | Record<string, unknown>;

export type SupportLevel = "native" | "emulated" | "degraded" | "unavailable";

export interface FeatureSupport {
  level: SupportLevel;
  reason?: string;
}

export type FeatureMap = Record<string, FeatureSupport>;

export type ProtocolScopeKind =
  | "connection"
  | "session"
  | "run"
  | "turn"
  | "model_stream"
  | "tool_execution"
  | "auth_flow"
  | (string & {});

export interface ProtocolScope {
  kind: ProtocolScopeKind;
  id: OpaqueId;
}

export interface ProtocolTrace {
  session_id?: OpaqueId;
  run_id?: OpaqueId;
  turn_id?: OpaqueId;
  model_stream_id?: OpaqueId;
  tool_call_id?: OpaqueId;
  parent_id?: OpaqueId;
}

export interface ProtocolEnvelope<
  TPayload = unknown,
  TType extends ProtocolEventType = ProtocolEventType,
> {
  protocol: typeof PROTOCOL_ID;
  version: ProtocolVersion;
  type: TType;
  id: OpaqueId;
  scope: ProtocolScope;
  sequence?: number;
  timestamp_ms?: number;
  in_reply_to?: OpaqueId;
  trace?: ProtocolTrace;
  payload: TPayload;
  extensions?: ExtensionMap;
}

export type ControlEventType =
  | "runtime.capabilities.request"
  | "runtime.capabilities.response"
  | "runtime.models.request"
  | "runtime.models.response"
  | "runtime.auth.providers.request"
  | "runtime.auth.providers.response"
  | "runtime.auth.login.request"
  | "runtime.auth.event"
  | "runtime.auth.login.result";

export type ModelEventType =
  | "model.response.request"
  | "model.stream.started"
  | "model.content.delta"
  | "model.tool_call.started"
  | "model.tool_call.delta"
  | "model.tool_call.completed"
  | "model.stream.completed"
  | "model.stream.failed"
  | "model.stream.cancelled";

export type ActionEventType =
  | "action.tools.list.request"
  | "action.tools.list.response"
  | "action.call.requested"
  | "action.call.started"
  | "action.call.delta"
  | "action.call.completed"
  | "action.call.failed"
  | "action.call.cancel.request"
  | "action.call.cancelled"
  | "action.permission.requested"
  | "action.permission.resolved"
  | "action.artifact.created"
  | "action.artifact.retrieve.request"
  | "action.artifact.retrieve.response";

export type AgentEventType =
  | "agent.sessions.list.request"
  | "agent.sessions.list.response"
  | "agent.session.open.request"
  | "agent.session.opened"
  | "agent.session.closed"
  | "agent.transcript.load.request"
  | "agent.transcript.load.response"
  | "agent.run.request"
  | "agent.run.started"
  | "agent.turn.started"
  | "agent.turn.completed"
  | "agent.run.interrupt.request"
  | "agent.run.interrupted"
  | "agent.run.resume.request"
  | "agent.run.cancel.request"
  | "agent.run.completed"
  | "agent.run.failed"
  | "agent.run.cancelled"
  | "agent.checkpoint.created"
  | "agent.checkpoint.restore.request"
  | "agent.branch.created"
  | "agent.context.compacted";

export type KnownProtocolEventType =
  | ControlEventType
  | ModelEventType
  | ActionEventType
  | AgentEventType;

export type ProtocolEventType = KnownProtocolEventType | (string & {});

export type MessageRole = "system" | "developer" | "user" | "assistant" | "tool";
export type ReasoningVisibility = "full" | "summary" | "redacted" | "none";

export interface ProtocolMessage {
  id?: OpaqueId;
  role: MessageRole;
  content: ContentPart[];
  name?: string;
  created_at_ms?: number;
  metadata?: ExtensionMap;
}

export type ContentPart =
  | TextContentPart
  | ReasoningContentPart
  | ImageContentPart
  | AudioContentPart
  | FileContentPart
  | ToolCallContentPart
  | ToolResultContentPart
  | ArtifactRefContentPart;

export interface TextContentPart {
  type: "text";
  text: string;
  text_signature?: string;
}

export interface ReasoningContentPart {
  type: "reasoning";
  text: string;
  signature?: string;
  visibility?: ReasoningVisibility;
}

export interface ImageContentPart {
  type: "image";
  data?: string;
  uri?: string;
  mime_type: string;
}

export interface AudioContentPart {
  type: "audio";
  data?: string;
  uri?: string;
  mime_type: string;
}

export interface FileContentPart {
  type: "file";
  data?: string;
  uri?: string;
  mime_type?: string;
  name?: string;
}

export interface ToolCallContentPart {
  type: "tool_call";
  tool_call_id: OpaqueId;
  name: string;
  arguments_json: string;
}

export interface ToolResultContentPart {
  type: "tool_result";
  tool_call_id: OpaqueId;
  name: string;
  content: ContentPart[];
  is_error?: boolean;
}

export interface ArtifactRefContentPart {
  type: "artifact_ref";
  artifact_id: OpaqueId;
  uri?: string;
  mime_type?: string;
  byte_size?: number;
  sha256?: string;
}

export type ProtocolErrorLayer = "binding" | "model" | "action" | "agent" | "control";

export type ProtocolErrorCode =
  | "invalid_request"
  | "unsupported_feature"
  | "capability_degraded"
  | "auth_required"
  | "auth_expired"
  | "auth_refresh_failed"
  | "permission_denied"
  | "cancelled"
  | "interrupted"
  | "timeout"
  | "rate_limited"
  | "provider_error"
  | "tool_error"
  | "transport_error"
  | "internal_error";

export interface ProtocolError {
  code: ProtocolErrorCode | (string & {});
  message: string;
  layer?: ProtocolErrorLayer;
  retriable?: boolean;
  provider_id?: string;
  model_id?: string;
  tool_name?: string;
  details?: ExtensionMap;
}

export interface ProtocolFailurePayload {
  error: ProtocolError;
}

export interface CancelledPayload {
  reason?: string;
}

export interface RuntimeCapabilities {
  runtime: {
    id: string;
    name: string;
    version?: string;
    adapter?: string;
  };
  protocol_versions: ProtocolVersion[];
  bindings: RuntimeBinding[];
  layers: {
    model?: ModelLayerCapabilities;
    action?: ActionLayerCapabilities;
    agent?: AgentLayerCapabilities;
    control?: ControlPlaneCapabilities;
  };
  degradation?: CapabilityDegradation[];
}

export interface RuntimeBinding {
  kind: "in_process" | "json_stdio" | "websocket" | "http_sse" | (string & {});
  serialization?: "json" | "jsonl" | "binary" | (string & {});
  endpoint?: string;
}

export interface ModelLayerCapabilities {
  features: FeatureMap;
}

export interface ActionLayerCapabilities {
  features: FeatureMap;
  tool_sources?: Array<"native" | "local" | "process" | "remote" | "hosted" | (string & {})>;
}

export interface AgentLayerCapabilities {
  features: FeatureMap;
}

export interface ControlPlaneCapabilities {
  features: FeatureMap;
}

export interface CapabilityDegradation {
  feature: string;
  from?: SupportLevel;
  to: SupportLevel;
  mode?:
    | "stripped"
    | "summarized"
    | "redacted"
    | "buffered"
    | "polling"
    | "emulated"
    | "opaque"
    | (string & {});
  reason?: string;
}

export interface RuntimeCapabilitiesRequestPayload {
  protocol_versions?: ProtocolVersion[];
  requested_layers?: Array<"model" | "action" | "agent" | "control" | (string & {})>;
}

export interface ModelCatalogRequestPayload {
  provider_id?: string;
  include_unavailable?: boolean;
  capability_filter?: string[];
}

export interface ModelCatalogResponsePayload {
  models: ModelDescriptor[];
  cache?: {
    refreshed_at_ms?: number;
    expires_at_ms?: number;
  };
}

export interface ModelDescriptor {
  id: string;
  provider_id?: string;
  display_name?: string;
  family?: string;
  context_window_tokens?: number;
  max_output_tokens?: number;
  input_modalities?: Array<"text" | "image" | "audio" | "file" | (string & {})>;
  output_modalities?: Array<"text" | "image" | "audio" | "file" | (string & {})>;
  features?: FeatureMap;
  unavailable_reason?: string;
}

export interface AuthProvidersRequestPayload {
  provider_id?: string;
}

export interface AuthProvidersResponsePayload {
  providers: AuthProviderState[];
}

export interface AuthProviderState {
  provider_id: string;
  display_name?: string;
  status: "authenticated" | "unauthenticated" | "expired" | "refreshing" | "unavailable" | (string & {});
  required?: boolean;
  scopes?: string[];
  expires_at_ms?: number;
  reason?: string;
}

export interface AuthLoginRequestPayload {
  provider_id: string;
  scopes?: string[];
  redirect_uri?: string;
}

export interface AuthEventPayload {
  provider_id: string;
  auth_flow_id: OpaqueId;
  status: "login_url" | "prompt" | "progress" | "success" | "error" | (string & {});
  url?: string;
  message?: string;
  error?: ProtocolError;
}

export interface AuthLoginResultPayload {
  provider_id: string;
  auth_flow_id: OpaqueId;
  status: "success" | "failed" | "cancelled" | (string & {});
  error?: ProtocolError;
}

export interface ResponseFormat {
  type: "text" | "json_object" | "json_schema" | (string & {});
  schema?: JsonSchema;
}

export interface ModelResponseRequestPayload {
  model_id: string;
  messages: ProtocolMessage[];
  stream?: boolean;
  tools?: ToolDescriptor[];
  response_format?: ResponseFormat;
  temperature?: number;
  top_p?: number;
  max_output_tokens?: number;
  allow_degraded_features?: string[];
  provider_options?: ExtensionMap;
}

export interface ModelStreamStartedPayload {
  model_stream_id: OpaqueId;
  model_id: string;
  provider_id?: string;
}

export interface ModelContentDeltaPayload {
  model_stream_id: OpaqueId;
  index?: number;
  part: PartialContentPart;
  omitted?: OmittedField[];
  redactions?: RedactionRecord[];
}

export type PartialContentPart =
  | { type: "text"; text: string; text_signature?: string }
  | { type: "reasoning"; text: string; signature?: string; visibility?: ReasoningVisibility }
  | { type: "image"; data?: string; uri?: string; mime_type?: string }
  | { type: "audio"; data?: string; uri?: string; mime_type?: string }
  | { type: "file"; data?: string; uri?: string; mime_type?: string; name?: string };

export interface OmittedField {
  path: string;
  reason: string;
}

export interface RedactionRecord {
  path: string;
  mode: "redacted" | "summarized" | "stripped" | (string & {});
  reason?: string;
}

export interface ModelToolCallStartedPayload {
  model_stream_id: OpaqueId;
  tool_call_id: OpaqueId;
  name?: string;
}

export interface ModelToolCallDeltaPayload {
  model_stream_id: OpaqueId;
  tool_call_id: OpaqueId;
  name_delta?: string;
  arguments_json_delta?: string;
}

export interface ModelToolCallCompletedPayload {
  model_stream_id: OpaqueId;
  tool_call_id: OpaqueId;
  name: string;
  arguments_json: string;
}

export interface TokenUsage {
  input_tokens?: number;
  output_tokens?: number;
  reasoning_tokens?: number;
  cached_input_tokens?: number;
  total_tokens?: number;
}

export interface ModelStreamCompletedPayload {
  model_stream_id: OpaqueId;
  stop_reason?: "end_turn" | "max_tokens" | "tool_call" | "content_filter" | (string & {});
  usage?: TokenUsage;
}

export interface ToolSourceDescriptor {
  kind: "native" | "local" | "process" | "remote" | "hosted" | (string & {});
  id?: string;
  display_name?: string;
  protocol?: string;
  endpoint?: string;
  metadata?: ExtensionMap;
}

export interface ToolDescriptor {
  name: string;
  description?: string;
  input_schema: JsonSchema;
  output_schema?: JsonSchema;
  source?: ToolSourceDescriptor;
  annotations?: {
    read_only?: boolean;
    destructive?: boolean;
    idempotent?: boolean;
    requires_network?: boolean;
    requires_approval?: boolean;
  };
  features?: FeatureMap;
}

export interface ToolsListRequestPayload {
  source?: ToolSourceDescriptor;
  include_disabled?: boolean;
}

export interface ToolsListResponsePayload {
  tools: ToolDescriptor[];
  sources?: ToolSourceDescriptor[];
}

export interface ToolCallRequestedPayload {
  tool_call_id: OpaqueId;
  name: string;
  arguments_json: string;
  origin?: "model" | "user" | "runtime" | (string & {});
  source?: ToolSourceDescriptor;
  allow_degraded_features?: string[];
}

export interface ToolCallStartedPayload {
  tool_call_id: OpaqueId;
  name: string;
  source?: ToolSourceDescriptor;
}

export interface ProgressRecord {
  current?: number;
  total?: number;
  unit?: string;
  message?: string;
}

export interface ToolCallDeltaPayload {
  tool_call_id: OpaqueId;
  kind: "stdout" | "stderr" | "progress" | "partial_result" | "status" | (string & {});
  text?: string;
  progress?: ProgressRecord;
  content?: ContentPart[];
  artifact?: ArtifactRef;
  status?: string;
}

export interface ToolCallCompletedPayload {
  tool_call_id: OpaqueId;
  name: string;
  content?: ContentPart[];
  artifacts?: ArtifactRef[];
  usage?: ExtensionMap;
}

export interface ToolCallCancelRequestPayload {
  tool_call_id: OpaqueId;
  reason?: string;
}

export interface PermissionRequestPayload {
  permission_id: OpaqueId;
  tool_call_id?: OpaqueId;
  title?: string;
  description: string;
  choices: PermissionChoice[];
  default_choice?: string;
  expires_at_ms?: number;
}

export interface PermissionChoice {
  id: string;
  label: string;
  destructive?: boolean;
}

export interface PermissionResolvedPayload {
  permission_id: OpaqueId;
  choice_id: string;
  granted: boolean;
  reason?: string;
}

export interface ArtifactRef {
  artifact_id: OpaqueId;
  uri?: string;
  mime_type?: string;
  byte_size?: number;
  sha256?: string;
  metadata?: ExtensionMap;
}

export interface ArtifactCreatedPayload {
  artifact: ArtifactRef;
  producer?: {
    tool_call_id?: OpaqueId;
    run_id?: OpaqueId;
  };
}

export interface ArtifactRetrieveRequestPayload {
  artifact_id: OpaqueId;
  include_content?: boolean;
}

export interface ArtifactRetrieveResponsePayload {
  artifact: ArtifactRef;
  content?: ContentPart[];
}

export interface SessionOpenRequestPayload {
  session_id?: OpaqueId;
  metadata?: ExtensionMap;
}

export interface SessionOpenedPayload {
  session_id: OpaqueId;
  restored?: boolean;
  metadata?: ExtensionMap;
}

export interface SessionClosedPayload {
  session_id: OpaqueId;
  reason?: string;
}

export interface SessionsListRequestPayload {
  limit?: number;
  cursor?: string;
  metadata_filter?: ExtensionMap;
}

export interface SessionSummary {
  session_id: OpaqueId;
  title?: string;
  created_at_ms?: number;
  updated_at_ms?: number;
  last_run_status?: AgentRunStatus;
  metadata?: ExtensionMap;
}

export interface SessionsListResponsePayload {
  sessions: SessionSummary[];
  next_cursor?: string;
}

export interface TranscriptLoadRequestPayload {
  session_id: OpaqueId;
  cursor?: string;
  limit?: number;
  include_events?: boolean;
  include_artifacts?: boolean;
}

export interface TranscriptLoadResponsePayload {
  session_id: OpaqueId;
  messages?: ProtocolMessage[];
  events?: Array<ProtocolEnvelope<unknown>>;
  next_cursor?: string;
}

export interface AgentRunRequestPayload {
  session_id: OpaqueId;
  run_id?: OpaqueId;
  input?: ProtocolMessage[];
  instructions?: ProtocolMessage[];
  model_id?: string;
  tools?: ToolDescriptor[];
  tool_choice?: ToolChoice;
  allow_degraded_features?: string[];
  metadata?: ExtensionMap;
}

export type ToolChoice =
  | "auto"
  | "none"
  | { type: "tool"; name: string }
  | { type: "allowed_tools"; names: string[] }
  | { type: "required_tools"; names: string[] };

export type AgentRunStatus =
  | "queued"
  | "running"
  | "interrupted"
  | "completed"
  | "failed"
  | "cancelled"
  | (string & {});

export interface AgentRunStartedPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  status: Extract<AgentRunStatus, "running">;
  model_id?: string;
  parent_run_id?: OpaqueId;
}

export interface AgentTurnStartedPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  turn_id: OpaqueId;
  index?: number;
}

export interface AgentTurnCompletedPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  turn_id: OpaqueId;
  usage?: TokenUsage;
}

export interface AgentRunInterruptRequestPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  reason?: string;
}

export interface AgentRunInterruptedPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  reason?: string;
  resumable?: boolean;
}

export interface AgentRunResumeRequestPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  input?: ProtocolMessage[];
}

export interface AgentRunCancelRequestPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  reason?: string;
}

export interface AgentRunCompletedPayload {
  session_id: OpaqueId;
  run_id: OpaqueId;
  usage?: TokenUsage;
}

export interface CheckpointRef {
  checkpoint_id: OpaqueId;
  session_id: OpaqueId;
  run_id?: OpaqueId;
  turn_id?: OpaqueId;
  message_index?: number;
  kind?: "message_index" | "state_snapshot" | "transcript_snapshot" | (string & {});
  created_at_ms?: number;
  metadata?: ExtensionMap;
}

export interface CheckpointCreatedPayload {
  checkpoint: CheckpointRef;
}

export interface CheckpointRestoreRequestPayload {
  checkpoint_id: OpaqueId;
  branch?: boolean;
}

export interface BranchCreatedPayload {
  branch_id: OpaqueId;
  session_id: OpaqueId;
  from_checkpoint_id?: OpaqueId;
  from_message_id?: OpaqueId;
}

export interface ContextCompactedPayload {
  session_id: OpaqueId;
  run_id?: OpaqueId;
  before_tokens?: number;
  after_tokens?: number;
  strategy?: string;
  omitted?: OmittedField[];
}

export interface ProtocolPayloads {
  "runtime.capabilities.request": RuntimeCapabilitiesRequestPayload;
  "runtime.capabilities.response": RuntimeCapabilities;
  "runtime.models.request": ModelCatalogRequestPayload;
  "runtime.models.response": ModelCatalogResponsePayload;
  "runtime.auth.providers.request": AuthProvidersRequestPayload;
  "runtime.auth.providers.response": AuthProvidersResponsePayload;
  "runtime.auth.login.request": AuthLoginRequestPayload;
  "runtime.auth.event": AuthEventPayload;
  "runtime.auth.login.result": AuthLoginResultPayload;
  "model.response.request": ModelResponseRequestPayload;
  "model.stream.started": ModelStreamStartedPayload;
  "model.content.delta": ModelContentDeltaPayload;
  "model.tool_call.started": ModelToolCallStartedPayload;
  "model.tool_call.delta": ModelToolCallDeltaPayload;
  "model.tool_call.completed": ModelToolCallCompletedPayload;
  "model.stream.completed": ModelStreamCompletedPayload;
  "model.stream.failed": ProtocolFailurePayload;
  "model.stream.cancelled": CancelledPayload;
  "action.tools.list.request": ToolsListRequestPayload;
  "action.tools.list.response": ToolsListResponsePayload;
  "action.call.requested": ToolCallRequestedPayload;
  "action.call.started": ToolCallStartedPayload;
  "action.call.delta": ToolCallDeltaPayload;
  "action.call.completed": ToolCallCompletedPayload;
  "action.call.failed": ProtocolFailurePayload;
  "action.call.cancel.request": ToolCallCancelRequestPayload;
  "action.call.cancelled": CancelledPayload;
  "action.permission.requested": PermissionRequestPayload;
  "action.permission.resolved": PermissionResolvedPayload;
  "action.artifact.created": ArtifactCreatedPayload;
  "action.artifact.retrieve.request": ArtifactRetrieveRequestPayload;
  "action.artifact.retrieve.response": ArtifactRetrieveResponsePayload;
  "agent.sessions.list.request": SessionsListRequestPayload;
  "agent.sessions.list.response": SessionsListResponsePayload;
  "agent.session.open.request": SessionOpenRequestPayload;
  "agent.session.opened": SessionOpenedPayload;
  "agent.session.closed": SessionClosedPayload;
  "agent.transcript.load.request": TranscriptLoadRequestPayload;
  "agent.transcript.load.response": TranscriptLoadResponsePayload;
  "agent.run.request": AgentRunRequestPayload;
  "agent.run.started": AgentRunStartedPayload;
  "agent.turn.started": AgentTurnStartedPayload;
  "agent.turn.completed": AgentTurnCompletedPayload;
  "agent.run.interrupt.request": AgentRunInterruptRequestPayload;
  "agent.run.interrupted": AgentRunInterruptedPayload;
  "agent.run.resume.request": AgentRunResumeRequestPayload;
  "agent.run.cancel.request": AgentRunCancelRequestPayload;
  "agent.run.completed": AgentRunCompletedPayload;
  "agent.run.failed": ProtocolFailurePayload;
  "agent.run.cancelled": CancelledPayload;
  "agent.checkpoint.created": CheckpointCreatedPayload;
  "agent.checkpoint.restore.request": CheckpointRestoreRequestPayload;
  "agent.branch.created": BranchCreatedPayload;
  "agent.context.compacted": ContextCompactedPayload;
}

export type KnownProtocolEnvelope<
  TType extends keyof ProtocolPayloads = keyof ProtocolPayloads,
> = {
  [K in TType]: ProtocolEnvelope<ProtocolPayloads[K], K>;
}[TType];
