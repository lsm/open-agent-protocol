/**
 * The Open Agent Protocol agent-control-core wire model, hand-written to
 * mirror schema/v0.1 exactly. Every envelope travels verbatim: the client
 * adds no fields of its own and ignores none.
 */

export const PROTOCOL = 'open-agent-protocol';
export const VERSION = '0.1';
export const PROFILE = 'open-agent-protocol.agent-control-core';

/**
 * Envelope type constants and their string union. The union stays open
 * (`string & {}`): the protocol adds envelope types without a client
 * release, and the event stream must deliver unknown types unhindered.
 */
export const EnvelopeType = {
  ProtocolInitializeRequest: 'protocol.initialize.request',
  ProtocolInitializeResponse: 'protocol.initialize.response',
  CapabilitiesRequest: 'capabilities.request',
  CapabilitiesResponse: 'capabilities.response',
  CapabilitiesUpdated: 'capabilities.updated',
  ModelsRequest: 'models.request',
  ModelsResponse: 'models.response',
  SessionOpenRequest: 'session.open.request',
  SessionOpenResponse: 'session.open.response',
  SessionStateRequest: 'session.state.request',
  SessionStateResponse: 'session.state.response',
  SessionStateUpdated: 'session.state.updated',
  SessionMessageSubmitRequest: 'session.message.submit.request',
  SessionMessageSubmitResponse: 'session.message.submit.response',
  RunCancelRequest: 'run.cancel.request',
  RunCancelResponse: 'run.cancel.response',
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

/** One protocol envelope; `payload` is decoded JSON, still untyped. */
export interface Envelope {
  protocol: string;
  version: string;
  profile: string;
  type: EnvelopeType;
  id: string;
  payload: Record<string, unknown>;
  /** Sequences are 1-based and contiguous within a run. */
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

/** Decodes one envelope from a JSON document (an SSE data field or a response body). */
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

/**
 * Types one envelope's payload. The cast is deliberate: payloads are trusted
 * schema/v0.1 documents on this single-user wire, and runtime JSON-Schema
 * validation is out of scope for the zero-dependency client.
 */
export function payload<T>(envelope: Envelope): T {
  if (typeof envelope.payload !== 'object' || envelope.payload === null || Array.isArray(envelope.payload)) {
    throw new Error(`decode ${envelope.type} payload: missing payload`);
  }
  return envelope.payload as T;
}

// --- common.schema.json ---

export type MessageRole = 'system' | 'developer' | 'user' | 'assistant' | 'tool';

/**
 * A JSON value: what the wire's JSON-typed fields carry. `undefined` is
 * excluded deliberately — JSON.stringify silently drops it, which would turn
 * a required field into a schema violation on the wire — and so are the
 * other non-serializable values.
 */
export type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };

export type SupportLevel = 'native' | 'emulated' | 'degraded' | 'unavailable';

/** An image carried by URL alone. */
export interface UrlImage {
  url: string;
  data?: undefined;
  media_type?: undefined;
}

/** An image carried as inline data with its media type. */
export interface InlineImage {
  data: string;
  media_type: string;
  url?: undefined;
}

/** An image part: exactly one of a URL, or inline data with its media type. */
export type ImageContent = UrlImage | InlineImage;

export interface TextPart {
  type: 'text';
  text: string;
}

export interface ReasoningPart {
  type: 'reasoning';
  reasoning: string;
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
}

export interface ToolResultPart {
  type: 'tool_result';
  tool_call_id: string;
  result: JSONValue;
  is_error?: boolean;
}

export type ContentPart = TextPart | ReasoningPart | ImagePart | ToolCallPart | ToolResultPart;

/** Message content is either a plain string or a non-empty list of parts. */
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

// --- capabilities.schema.json ---

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
  /** The one application mode a key has one of: `run.model_selection` discloses `per_run` or `session_mutation`. */
  mode?: string;
  /** The modes a key can enforce more than one of: `run.tool_selection` lists the `tool_choice` modes the endpoint honours, so a refusal is conforming only for a mode outside it, and `action.tool_sources.attach` lists where it attaches — `session_open` wherever attachment is usable at all, plus `remote` when a source the operator never configured is accepted. */
  modes?: string[];
  /** Endpoint-specific limits a caller can check: `run.structured_output`'s `fixed_result` is the exact object every `run.completed` under an accepted `output_schema` carries — an object, because only an object is a structured result. */
  constraints?: { fixed_result?: Record<string, unknown> } & Record<string, unknown>;
  /** The bounds that make a refusal checkable: `action.tool_sources.attach` discloses `max_sources` and the `transports` it accepts, and refusing an array that violates neither is a conformance failure. Both are held to values a request can satisfy — a positive ceiling, and transports drawn from the source-kind vocabulary — because a limit no attachment can meet would make refusing every one of them conforming, so the schema refuses it. */
  limits?: { max_sources?: number; transports?: ToolSourceKind[] } & Record<string, unknown>;
}

/** Where a tool source's tools are executed from. An MCP source is a `process` or `remote` kind whose `protocol` is `mcp`. */
export type ToolSourceKind = 'native' | 'local' | 'process' | 'remote' | 'hosted';

/**
 * The published shape of one tool source: what `action.tools.list.response`,
 * `session.state`, and the capability descriptor report back to clients.
 *
 * It carries no `command`, `args`, or `environment` — those belong to
 * `ToolSourceAttachment`, the open-time shape — because an attachment's
 * environment can hold a literal credential and one shape serving both would
 * make a leak into a published catalog valid.
 */
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
  /** The id of the `ToolSourceDescriptor` this tool comes from — never an inline copy — so a consumer can attribute a tool to an MCP server without parsing its name. */
  source?: string;
  /** This one tool's effective support map. */
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
  /** At least one mode when present: the schema refuses an empty list. */
  requested_delivery_modes?: [RequestedDeliveryMode, ...RequestedDeliveryMode[]];
  /** At least one mode when present: the schema refuses an empty list. */
  effective_delivery_modes?: [EffectiveDeliveryMode, ...EffectiveDeliveryMode[]];
  tools?: ToolDefinition[];
  sources?: ToolSourceDescriptor[];
}

export interface InitializeRequest {
  /** At least one version: the schema refuses an empty list. */
  protocol_versions: [string, ...string[]];
  /** At least one profile: the schema refuses an empty list. */
  profiles: [string, ...string[]];
  participant?: Participant;
}

export interface InitializeResponse {
  protocol_version: string;
  profile: string;
  endpoint: EndpointDescriptor;
}

/** An empty request payload: the schema's empty-object defs allow no fields (additionalProperties: false). */
export interface EmptyRequestPayload {
  readonly [key: string]: never;
}

export type CapabilitiesRequest = EmptyRequestPayload;

/** One adapter's capability snapshot; every envelope the adapter emits repeats the revision. */
export interface CapabilityDescriptor {
  endpoint: EndpointDescriptor;
  /** At least one version when present: the schema refuses an empty list. */
  protocol_versions?: [string, ...string[]];
  /** At least one profile when present: the schema refuses an empty list. */
  profiles?: [string, ...string[]];
  bindings?: Binding[];
  features?: Record<string, FeatureSupport>;
  layers?: Record<string, CapabilityLayer>;
  tools?: ToolDefinition[];
  sources?: ToolSourceDescriptor[];
  degradation?: Degradation[];
  /** The admission bounds the endpoint discloses (queue unit). */
  limits?: CapabilityLimits;
}

/**
 * The admission bounds a descriptor discloses. `max_active_runs_per_session`
 * bounds the nonterminal set — the started run plus every queued reservation,
 * which is what `session.state.active_runs` lists — and
 * `max_queued_runs_per_session` bounds the queued subset. Both are at least 1
 * on the wire; an endpoint that cannot queue advertises
 * `session.message.delivery.queue` as `unavailable` rather than disclosing a
 * bound of zero.
 */
export interface CapabilityLimits {
  max_active_runs_per_session?: number;
  max_queued_runs_per_session?: number;
}

export interface CapabilitiesUpdated {
  previous_revision: string;
  reason?: string;
}

/**
 * Asks one session for its effective model catalog.
 *
 * `allow_degraded_features` is the same per-request opt-in the submit request
 * carries: an endpoint exposing `models.list` as `degraded` would otherwise
 * have to refuse every query or serve degraded behaviour without consent.
 */
export interface ModelsRequest {
  session_id: string;
  allow_degraded_features?: string[];
}

/** One model a session can run. `id` is the value `model_id` accepts and is unique within a response. */
export interface ModelDescriptor {
  id: string;
  display_name?: string;
  provider_id?: string;
  context_window?: number;
  features?: Record<string, FeatureSupport>;
  /** At most one descriptor per response carries it. */
  default?: boolean;
}

/** One run-scoped event, named by its run and sequence because sequences restart per run. */
export interface ModelEventPosition {
  run_id: string;
  sequence: number;
}

/** The effective catalog for one session. */
export interface ModelsResponse {
  session_id: string;
  current_model_id?: string;
  models: ModelDescriptor[];
  /** The last model-affecting event this catalog reflects; absent when it reflects none. */
  as_of_model_event?: ModelEventPosition;
}

// --- session.schema.json ---

export type SessionStatus = 'idle' | 'queued' | 'running' | 'waiting_for_input' | 'closed' | 'error';

/**
 * The open-time shape of one tool source: the descriptor's published members
 * plus, for a `process` source, the attachment-only `command`, `args`, and
 * `environment`. `environment` takes the registry's allowlist form — a bare
 * `NAME` forwards the endpoint's own value, `NAME=value` passes literally.
 *
 * The daemon's client-facing route accepts neither `command`, `args`, nor a
 * literal `NAME=value`: a `process` attachment names an operator-configured
 * source by `id` only and the daemon fills the rest from its own registry.
 */
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

export interface SessionOpenRequest {
  session_id?: string;
  metadata?: Record<string, unknown>;
  /** The sources the session resolves for its lifetime. */
  tool_sources?: ToolSourceAttachment[];
  /** Consent to the degraded application of the capabilities the open elects. */
  allow_degraded_features?: string[];
  recovery?: RecoveryMetadata;
}

export type SessionOpenResponse = SessionState;
export type SessionStateResponse = SessionState;
export type SessionStateUpdated = SessionState;

export interface SessionStateRequest {
  session_id: string;
}

export interface SessionState {
  session_id: string;
  status: SessionStatus;
  active_run_id?: string;
  /** Every nonterminal run of the session, in admission order (queue unit). */
  active_runs?: ActiveRun[];
  current_model_id?: string;
  transcript_cursor?: string;
  updated_at_ms?: number;
  metadata?: Record<string, unknown>;
  /** The sanitized projection of the session's attached and declared sources. */
  sources?: ToolSourceDescriptor[];
  recovery?: RecoveryMetadata;
  /** What the snapshot knew when it was taken, so membership is judged against the endpoint's knowledge rather than the reader's. */
  as_of?: SessionCapture;
}

/** The only relationship an `active_runs` entry carries in this phase. */
export type ActiveRunRelationship = 'primary';

/**
 * One nonterminal run of a session. A queued reservation carries its 1-based
 * `queue_position`; the started run carries none.
 *
 * `as_of_sequence` is the last sequence of this run the entry reflects, and an
 * entry carrying `pending_interactions` must carry it: a state read is not
 * serialized with lifecycle publication, so the position is what makes an
 * accurate-but-stale pending set judgeable rather than guessed at.
 */
export interface ActiveRun {
  run_id: string;
  status: RunStatus;
  relationship: ActiveRunRelationship;
  queue_position?: number;
  as_of_sequence?: number;
  /** The submit request envelope ids on this run the entry reflects as admitted. */
  admitted_submit_requests?: string[];
  /** The run's unresolved permission and user-input interactions at `as_of_sequence`. */
  pending_interactions?: string[];
}

/** A stated position in a run's sequence domain; `run_id: null` with `sequence: 0` is the genesis position, before the session's first model-affecting event. */
export interface RunPosition {
  run_id: string | null;
  sequence: number;
}

/** One run a snapshot has already removed, with the sequence its terminal carries. */
export interface SettledRun {
  run_id: string;
  sequence: number;
}

/** The session-level capture position of a state snapshot (queue unit). */
export interface SessionCapture {
  /** The submit requests on the session the snapshot reflects as admitted. */
  admitted_submit_requests?: string[];
  /** The runs the snapshot has removed, each with the sequence of its terminal. */
  settled?: SettledRun[];
  /** The last model-affecting event the snapshot reflects. */
  model_run_sequence?: RunPosition;
}

/**
 * The typed tool-selection policy `tool_choice` carries. The wire keeps
 * `tool_choice` permissive, so this shape is enforced by the validator's
 * run-controls rules and by adapters rather than by the schema.
 *
 * Precedence is fixed: `allowed` or `disallowed` filters the advertised
 * catalog first, then `mode` applies to the filtered set. `name` is present
 * when and only when `mode` is `named`, and `allowed`/`disallowed` are
 * mutually exclusive.
 */
export type ToolChoicePolicy =
  | { mode: 'auto' | 'none' | 'required'; name?: undefined; allowed?: string[]; disallowed?: undefined }
  | { mode: 'auto' | 'none' | 'required'; name?: undefined; allowed?: undefined; disallowed?: string[] }
  | { mode: 'named'; name: string; allowed?: string[]; disallowed?: undefined }
  | { mode: 'named'; name: string; allowed?: undefined; disallowed?: string[] };

export interface MessageSubmitRequest {
  session_id: string;
  /** At least one message: the schema refuses an empty submission. */
  messages: [Message, ...Message[]];
  delivery: RequestedDeliveryMode;
  /**
   * The per-submit run controls. Each is gated on its own capability key and
   * is applied or refused before admission, never dropped: an endpoint that
   * has not advertised the key answers `unsupported_feature` naming it.
   * Presence is what the gate judges, so an empty `model_id` is a control the
   * endpoint must refuse, not an absent one.
   */
  model_id?: string;
  instructions?: string;
  tool_choice?: ToolChoicePolicy | unknown;
  output_schema?: Record<string, unknown>;
  allow_degraded_features?: string[];
  metadata?: Record<string, unknown>;
}

export type Admission = 'started' | 'queued' | 'steered' | 'side_started' | 'rejected';

export interface MessageSubmitResponse {
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

// --- run.schema.json ---

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

/**
 * How the endpoint learned a run reached its terminal. `observed` is a
 * run-scoped native terminal the endpoint saw; `inferred` is one it concluded
 * from other evidence, such as a session-scoped stop or transport loss. Absent
 * asserts observation. It is provenance about the endpoint's knowledge, not a
 * second status: an inferred terminal is as absorbing as an observed one.
 */
export type SettledBy = 'observed' | 'inferred';

export interface RunCompletedPayload {
  session_id: string;
  run_id: string;
  final_response: Message;
  stop_reason: string;
  /** The model that produced the final response; under an admitted `model_id` it names that model. */
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

// --- action.schema.json ---

/** A catalog request. `session_id` asks for that session's effective catalog; an absent one asks for the endpoint-level catalog a static adapter serves. */
export interface ToolsListRequest {
  session_id?: string;
  /** Consent to a catalog the endpoint advertises `degraded`; without it such a request is refused `capability_degraded`. */
  allow_degraded_features?: string[];
}

export interface ToolsListResponse {
  /** Repeated from the request when the catalog is a session's effective catalog. */
  session_id?: string;
  sources?: ToolSourceDescriptor[];
  tools: ToolDefinition[];
}

/** The fields every action.call.* payload shares; the variants below each declare their own exclusive fields. */
interface ActionCallBase {
  interaction_id?: string;
  session_id: string;
  run_id: string;
  tool_call_id: string;
  requested_by?: string;
  responded_by?: string;
  execution_owner: string;
  /** The id of the tool source this call is attributed to, so a consumer need not parse the name. */
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
  arguments_json?: undefined;
  progress?: undefined;
  error?: undefined;
}

export interface ActionCallFailedPayload extends ActionCallBase {
  error: ProtocolError;
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
  /** At least one choice: the schema refuses an empty permission request. */
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

// --- interaction.schema.json ---

export type InputQuestionKind = 'text' | 'single_choice' | 'multi_choice';

export interface InputOption {
  id: string;
  label: string;
  description?: string;
}

/** A free-text question: the schema forbids options on it. */
export interface TextQuestion {
  id: string;
  prompt: string;
  kind: 'text';
  required?: boolean;
  options?: undefined;
}

/** A choice question: the schema requires a non-empty option list. */
export interface ChoiceQuestion {
  id: string;
  prompt: string;
  kind: 'single_choice' | 'multi_choice';
  required?: boolean;
  options: [InputOption, ...InputOption[]];
}

/** One asked question, discriminated by kind: text questions carry no options, choice questions carry at least one. */
export type InputQuestion = TextQuestion | ChoiceQuestion;

/** A text answer to one question. */
export interface TextAnswer {
  question_id: string;
  text: string;
  selected_option_ids?: undefined;
}

/** A choice answer to one question: one or more selected option ids. */
export interface SelectedOptionsAnswer {
  question_id: string;
  selected_option_ids: [string, ...string[]];
  text?: undefined;
}

/** One answered question: a text answer or selected option ids, exactly one of the two. */
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
  /** At least one question: the schema refuses an empty input request. */
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
  /** At least one answer: the schema refuses an empty resolution. */
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

/** A resolved gate whose answers were submitted: at least one, never empty. */
export interface SubmittedInputResolved extends UserInputResolvedFields {
  status: 'submitted';
  answers: [InputAnswer, ...InputAnswer[]];
}

/** A cancelled gate resolution: the schema forbids answers on it. */
export interface CancelledInputResolved extends UserInputResolvedFields {
  status: 'cancelled';
  answers?: undefined;
}

/** The confirmed outcome of one user-input gate, exclusive by status. */
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
