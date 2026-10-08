export {
  checkAbort,
  isAbortError,
  raceWithAbort,
} from "./abort_signal";

export { resolveMakaiBinary, type BinaryResolverOptions, type ModuleResolver } from "./binary_resolver";

export {
  MakaiAuthError,
  flattenAuthEvent,
  type AuthFlowHandlers,
  type AuthStatus,
  type MakaiAuthApi,
  type MakaiAuthErrorKind,
  type MakaiAuthEvent,
  type ProviderAuthInfo,
  type AuthKind,
  type ProviderId,
} from "./auth_protocol";

export {
  createMakaiClient,
  type CreateMakaiClientOptions,
  type MakaiAgentModelsApi,
  type MakaiClient,
} from "./execution_client";

export {
  createOapClient,
  OapStdioTransport,
  OapUnsupportedFeatureError,
  OAP_PROTOCOL,
  OAP_VERSION,
  OAP_AGENT_PROFILE,
  OAP_PROVIDER_PROFILE,
  type OapEnvelope,
} from "./oap_client";

export {
  MakaiAuthRequiredError,
  MakaiStreamError,
  type AgentRunRequest,
  type AgentRunResponse,
  type AgentStreamEvent,
  type AuthRetryPolicy,
  type ChatMessage,
  type CompletionResponse,
  type ContentPart,
  type ImageContentPart,
  type MakaiAgentApi,
  type MakaiClientOptions,
  type MakaiProviderApi,
  type MakaiStreamErrorKind,
  type ProviderCompleteRequest,
  type ProviderCompleteResponse,
  type ProviderStreamEvent,
  type RunOptions,
  type StopReason,
  type TextContentPart,
  type ThinkingContentPart,
  type ToolCallContentPart,
  type ToolDefinition,
  type ToolResultContentPart,
  type UsageSummary,
} from "./execution_types";

export {
  type TimeoutDiagnosticContext,
  type TimeoutDiagnostics,
} from "./timeout_diagnostics";

export {
  type MakaiLogger,
  getNoopLogger,
  isNoopLogger,
} from "./logger";

export {
  MakaiProtocolError,
  type ApiId,
  type ListModelsRequest,
  type ListModelsResponse,
  type MakaiModelsApi,
  type Modality,
  type ModelCapability,
  type ModelCatalog,
  type ModelCost,
  type ModelDescriptor,
  type ModelLifecycle,
  type ModelSource,
  type ReasoningLevel,
  type ResolveModelRequest,
  type ResolveModelResponse,
} from "./models_types";
