/**
 * Draft Open Agent Protocol UI Runtime Core contract.
 *
 * This is the small 80/20 surface for agent UIs. Broader protocol profiles can
 * extend it without changing these core event meanings.
 */

export const PROTOCOL_ID = "open-agent-protocol" as const;
export const PROTOCOL_VERSION = "0.1" as const;
export const CORE_PROFILE_ID = "open-agent-protocol.ui-runtime-core" as const;

export type OpaqueId = string;
export type JsonSchema = boolean | Record<string, unknown>;
export type Metadata = Record<string, unknown>;

export interface CoreEnvelope<
  TPayload = unknown,
  TType extends CoreEventType = CoreEventType,
> {
  protocol: typeof PROTOCOL_ID;
  version: typeof PROTOCOL_VERSION | (string & {});
  profile: typeof CORE_PROFILE_ID | (string & {});
  type: TType;
  id: OpaqueId;
  sequence?: number;
  timestamp_ms?: number;
  in_reply_to?: OpaqueId;
  session_id?: OpaqueId;
  run_id?: OpaqueId;
  turn_id?: OpaqueId;
  tool_call_id?: OpaqueId;
  payload: TPayload;
  extensions?: Metadata;
}

export type CoreEventType =
  | "runtime.initialize.request"
  | "runtime.initialize.response"
  | "runtime.capabilities.request"
  | "runtime.capabilities.response"
  | "runtime.status.request"
  | "runtime.status.response"
  | "runtime.status.updated"
  | "runtime.models.list.request"
  | "runtime.models.list.response"
  | "session.open.request"
  | "session.opened"
  | "session.state.request"
  | "session.state.response"
  | "session.state.updated"
  | "session.list.request"
  | "session.list.response"
  | "transcript.load.request"
  | "transcript.load.response"
  | "transcript.delta"
  | "run.start.request"
  | "run.start.response"
  | "run.input.request"
  | "run.input.response"
  | "run.cancel.request"
  | "run.started"
  | "run.status.updated"
  | "turn.started"
  | "content.delta"
  | "tool.call.requested"
  | "tool.call.started"
  | "tool.call.progress"
  | "tool.call.completed"
  | "permission.requested"
  | "permission.resolve.request"
  | "permission.resolved"
  | "user.input.requested"
  | "user.input.resolve.request"
  | "user.input.cancel.request"
  | "user.input.resolved"
  | "turn.completed"
  | "run.completed"
  | "run.failed"
  | "run.cancelled"
  | (string & {});

export type SupportLevel = "native" | "emulated" | "degraded" | "unavailable";

export interface RuntimeInitializeRequest {
  client: {
    name: string;
    title?: string;
    version?: string;
  };
  supported_protocol_versions?: string[];
  supported_profiles?: string[];
}

export interface RuntimeInitializeResponse {
  runtime: {
    id: string;
    name: string;
    version?: string;
  };
  protocol_version: string;
  profile: string;
}

export interface RuntimeCapabilities {
  features: Record<string, SupportLevel>;
  degradation?: CapabilityDegradation[];
}

export type RuntimeStatusKind = "ok" | "degraded" | "unavailable";

export interface RuntimeStatusRequest {
  include_details?: boolean;
}

export interface RuntimeStatus {
  status: RuntimeStatusKind;
  message?: string;
  checked_at_ms?: number;
  active_sessions?: number;
  active_runs?: number;
  metadata?: Metadata;
}

export interface CapabilityDegradation {
  feature: string;
  to: SupportLevel;
  reason?: string;
}

export interface ModelDescriptor {
  id: string;
  display_name?: string;
  provider_id?: string;
  capabilities?: string[];
  context_window_tokens?: number;
  max_output_tokens?: number;
}

export interface ModelsListRequest {
  provider_id?: string;
  include_unavailable?: boolean;
}

export interface ModelsListResponse {
  models: ModelDescriptor[];
  fetched_at_ms?: number;
  cache_max_age_ms?: number;
}

export type MessageRole = "system" | "developer" | "user" | "assistant" | "tool";

export interface Message {
  id?: OpaqueId;
  role: MessageRole;
  content: string | ContentPart[];
  name?: string;
  tool_call_id?: OpaqueId;
  metadata?: Metadata;
}

export type ContentPart =
  | { type: "text"; text: string }
  | { type: "reasoning"; text: string; visibility?: "full" | "summary" | "redacted" | "none" }
  | { type: "image"; data?: string; uri?: string; mime_type: string }
  | { type: "tool_call"; tool_call_id: OpaqueId; name: string; arguments_json: string }
  | { type: "tool_result"; tool_call_id: OpaqueId; name: string; content: string | ContentPart[]; is_error?: boolean };

export interface ToolDefinition {
  name: string;
  description?: string;
  input_schema: JsonSchema;
  annotations?: {
    read_only?: boolean;
    destructive?: boolean;
    idempotent?: boolean;
    requires_approval?: boolean;
  };
}

export type ToolChoice =
  | "auto"
  | "none"
  | { type: "tool"; name: string }
  | { type: "allowed_tools"; names: string[] };

export interface SessionOpenRequest {
  session_id?: OpaqueId;
  metadata?: Metadata;
}

export interface SessionOpened {
  session_id: OpaqueId;
  restored?: boolean;
  title?: string;
  current_model_id?: string;
  state?: SessionState;
}

export type SessionStatus =
  | "idle"
  | "queued"
  | "running"
  | "waiting_for_input"
  | "closed"
  | "error"
  | (string & {});

export interface SessionState {
  session_id: OpaqueId;
  status: SessionStatus;
  title?: string;
  active_run_id?: OpaqueId;
  current_model_id?: string;
  transcript_cursor?: string;
  updated_at_ms?: number;
  metadata?: Metadata;
}

export interface SessionStateRequest {
  session_id: OpaqueId;
}

export interface SessionStateResponse {
  state: SessionState;
}

export interface SessionSummary {
  session_id: OpaqueId;
  title?: string;
  status?: SessionStatus;
  current_model_id?: string;
  updated_at_ms?: number;
  last_run_status?: RunStatus;
}

export interface SessionListRequest {
  limit?: number;
  cursor?: string;
}

export interface SessionListResponse {
  sessions: SessionSummary[];
  next_cursor?: string;
}

export interface TranscriptLoadRequest {
  session_id: OpaqueId;
  cursor?: string;
  direction?: "before" | "after";
  limit?: number;
}

export interface TranscriptLoadResponse {
  session_id: OpaqueId;
  messages: Message[];
  next_cursor?: string;
  sync_cursor?: string;
}

export interface TranscriptDelta {
  session_id: OpaqueId;
  added?: Message[];
  updated?: Message[];
  removed?: OpaqueId[];
  cursor?: string;
}

export interface RunStartRequest {
  session_id: OpaqueId;
  input: Message[];
  model_id?: string;
  instructions?: Message[];
  tools?: ToolDefinition[];
  tool_choice?: ToolChoice;
  output_schema?: JsonSchema;
  metadata?: Metadata;
}

export interface RunStartResponse {
  session_id: OpaqueId;
  run_id: OpaqueId;
  accepted: true;
  status: RunStatus;
  model_id?: string;
  input_message_ids?: OpaqueId[];
}

export interface RunInputRequest {
  session_id: OpaqueId;
  run_id: OpaqueId;
  input: Message[];
}

export interface RunInputResponse {
  session_id: OpaqueId;
  run_id: OpaqueId;
  accepted: true;
  status?: RunStatus;
  input_message_ids?: OpaqueId[];
}

export interface RunCancelRequest {
  session_id: OpaqueId;
  run_id: OpaqueId;
  reason?: string;
}

export type RunStatus =
  | "queued"
  | "running"
  | "waiting_for_input"
  | "cancelling"
  | "completed"
  | "failed"
  | "cancelled"
  | (string & {});

export interface RunStarted {
  session_id: OpaqueId;
  run_id: OpaqueId;
  status: Extract<RunStatus, "running">;
  model_id?: string;
}

export interface RunStatusUpdated {
  session_id: OpaqueId;
  run_id: OpaqueId;
  status: RunStatus;
  phase?: "initializing" | "thinking" | "streaming" | "using_tool" | "finalizing" | string;
  message?: string;
  pending_user_input_id?: OpaqueId;
  updated_at_ms?: number;
}

export interface TurnStarted {
  session_id: OpaqueId;
  run_id: OpaqueId;
  turn_id: OpaqueId;
  index?: number;
}

export interface ContentDelta {
  session_id: OpaqueId;
  run_id: OpaqueId;
  turn_id?: OpaqueId;
  part: Extract<ContentPart, { type: "text" | "reasoning" }>;
}

export interface ToolCallRequested {
  session_id: OpaqueId;
  run_id: OpaqueId;
  turn_id?: OpaqueId;
  tool_call_id: OpaqueId;
  name: string;
  arguments_json: string;
}

export interface ToolCallStarted {
  session_id: OpaqueId;
  run_id: OpaqueId;
  tool_call_id: OpaqueId;
  name: string;
}

export interface ToolCallProgress {
  session_id: OpaqueId;
  run_id: OpaqueId;
  tool_call_id: OpaqueId;
  message?: string;
  current?: number;
  total?: number;
  unit?: string;
}

export interface ToolCallCompleted {
  session_id: OpaqueId;
  run_id: OpaqueId;
  tool_call_id: OpaqueId;
  name: string;
  content?: string | ContentPart[];
  is_error?: boolean;
}

export interface PermissionRequest {
  permission_id: OpaqueId;
  session_id: OpaqueId;
  run_id: OpaqueId;
  tool_call_id?: OpaqueId;
  title?: string;
  description: string;
  choices: PermissionChoice[];
}

export interface PermissionChoice {
  id: string;
  label: string;
  destructive?: boolean;
}

export interface PermissionResolveRequest {
  permission_id: OpaqueId;
  choice_id: string;
  granted: boolean;
  reason?: string;
  updated_arguments_json?: string;
}

export interface PermissionResolved extends PermissionResolveRequest {
  session_id: OpaqueId;
  run_id: OpaqueId;
}

export interface UserInputRequested {
  input_request_id: OpaqueId;
  session_id: OpaqueId;
  run_id: OpaqueId;
  tool_call_id?: OpaqueId;
  title?: string;
  description?: string;
  questions: UserInputQuestion[];
  allow_cancel?: boolean;
  draft_answers?: UserInputAnswer[];
}

export interface UserInputQuestion {
  id?: string;
  prompt: string;
  header?: string;
  kind?: "text" | "single_choice" | "multi_choice";
  options?: UserInputOption[];
  required?: boolean;
  default_value?: string | string[];
}

export interface UserInputOption {
  id: string;
  label: string;
  description?: string;
}

export interface UserInputAnswer {
  question_id?: string;
  question_index?: number;
  selected_option_ids?: string[];
  text?: string;
  value?: unknown;
}

export interface UserInputResolveRequest {
  input_request_id: OpaqueId;
  session_id: OpaqueId;
  run_id: OpaqueId;
  answers: UserInputAnswer[];
}

export interface UserInputCancelRequest {
  input_request_id: OpaqueId;
  session_id: OpaqueId;
  run_id: OpaqueId;
  reason?: string;
}

export interface UserInputResolved {
  input_request_id: OpaqueId;
  session_id: OpaqueId;
  run_id: OpaqueId;
  status: "submitted" | "cancelled";
  answers?: UserInputAnswer[];
  reason?: string;
}

export interface Usage {
  input_tokens?: number;
  output_tokens?: number;
  reasoning_tokens?: number;
  cached_input_tokens?: number;
  total_tokens?: number;
}

export interface TurnCompleted {
  session_id: OpaqueId;
  run_id: OpaqueId;
  turn_id: OpaqueId;
  usage?: Usage;
}

export interface RunCompleted {
  session_id: OpaqueId;
  run_id: OpaqueId;
  final_response?: Message;
  stop_reason?: string;
  usage?: Usage;
  duration_ms?: number;
}

export interface RunFailed {
  session_id: OpaqueId;
  run_id: OpaqueId;
  error: ProtocolError;
}

export interface RunCancelled {
  session_id: OpaqueId;
  run_id: OpaqueId;
  reason?: string;
}

export interface ProtocolError {
  code: string;
  message: string;
  retriable?: boolean;
  details?: Metadata;
}

export interface CorePayloads {
  "runtime.initialize.request": RuntimeInitializeRequest;
  "runtime.initialize.response": RuntimeInitializeResponse;
  "runtime.capabilities.request": Record<string, never>;
  "runtime.capabilities.response": RuntimeCapabilities;
  "runtime.status.request": RuntimeStatusRequest;
  "runtime.status.response": RuntimeStatus;
  "runtime.status.updated": RuntimeStatus;
  "runtime.models.list.request": ModelsListRequest;
  "runtime.models.list.response": ModelsListResponse;
  "session.open.request": SessionOpenRequest;
  "session.opened": SessionOpened;
  "session.state.request": SessionStateRequest;
  "session.state.response": SessionStateResponse;
  "session.state.updated": SessionState;
  "session.list.request": SessionListRequest;
  "session.list.response": SessionListResponse;
  "transcript.load.request": TranscriptLoadRequest;
  "transcript.load.response": TranscriptLoadResponse;
  "transcript.delta": TranscriptDelta;
  "run.start.request": RunStartRequest;
  "run.start.response": RunStartResponse;
  "run.input.request": RunInputRequest;
  "run.input.response": RunInputResponse;
  "run.cancel.request": RunCancelRequest;
  "run.started": RunStarted;
  "run.status.updated": RunStatusUpdated;
  "turn.started": TurnStarted;
  "content.delta": ContentDelta;
  "tool.call.requested": ToolCallRequested;
  "tool.call.started": ToolCallStarted;
  "tool.call.progress": ToolCallProgress;
  "tool.call.completed": ToolCallCompleted;
  "permission.requested": PermissionRequest;
  "permission.resolve.request": PermissionResolveRequest;
  "permission.resolved": PermissionResolved;
  "user.input.requested": UserInputRequested;
  "user.input.resolve.request": UserInputResolveRequest;
  "user.input.cancel.request": UserInputCancelRequest;
  "user.input.resolved": UserInputResolved;
  "turn.completed": TurnCompleted;
  "run.completed": RunCompleted;
  "run.failed": RunFailed;
  "run.cancelled": RunCancelled;
}

export type KnownCoreEnvelope<TType extends keyof CorePayloads = keyof CorePayloads> = {
  [K in TType]: CoreEnvelope<CorePayloads[K], K>;
}[TType];
