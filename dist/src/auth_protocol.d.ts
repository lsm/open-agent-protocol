import { BinaryResolverOptions } from "./binary_resolver";
import { type MakaiLogger } from "./logger";
import { MakaiStdioClient, type CreateMakaiStdioClientOptions } from "./stdio_client";
import { type TimeoutDiagnostics } from "./timeout_diagnostics";
export type ProviderId = string;
export declare const AUTH_STATUSES: readonly ["authenticated", "login_required", "expired", "refreshing", "login_in_progress", "failed", "unknown"];
export type AuthStatus = (typeof AUTH_STATUSES)[number];
export interface ProviderAuthInfo {
    id: ProviderId;
    name: string;
    auth_status: AuthStatus;
    last_error?: string;
}
export type MakaiAuthEvent = {
    type: "auth_url";
    flow_id: string;
    provider_id: ProviderId;
    url: string;
    instructions?: string;
} | {
    type: "prompt";
    flow_id: string;
    prompt_id: string;
    provider_id: ProviderId;
    message: string;
    allow_empty: boolean;
} | {
    type: "progress";
    flow_id: string;
    provider_id: ProviderId;
    message: string;
} | {
    type: "success";
    flow_id: string;
    provider_id: ProviderId;
} | {
    type: "error";
    flow_id: string;
    provider_id: ProviderId;
    code?: string;
    message: string;
};
export interface AuthFlowHandlers {
    onEvent?: (event: MakaiAuthEvent) => void;
    onPrompt?: (prompt: Extract<MakaiAuthEvent, {
        type: "prompt";
    }>) => Promise<string> | string;
}
export type MakaiAuthErrorKind = "provider_error" | "cancelled" | "transport_error" | "unknown";
export declare class MakaiAuthError extends Error {
    readonly kind: MakaiAuthErrorKind;
    readonly code?: string;
    readonly diagnostics?: TimeoutDiagnostics;
    constructor(message: string, options?: {
        kind?: MakaiAuthErrorKind;
        code?: string;
        diagnostics?: TimeoutDiagnostics;
    });
}
export interface MakaiAuthApi {
    listProviders(): Promise<ProviderAuthInfo[]>;
    login(providerId: ProviderId, handlers?: AuthFlowHandlers, options?: {
        signal?: AbortSignal;
    }): Promise<{
        status: "success";
    }>;
}
export declare function flattenAuthEvent(payload: Record<string, unknown>): MakaiAuthEvent;
export interface MakaiAuthClientOptions {
    handlers?: AuthFlowHandlers;
    frameTimeoutMs?: number;
    logger?: MakaiLogger;
}
export declare class MakaiAuthClient implements MakaiAuthApi {
    private readonly transport;
    private readonly defaultHandlers?;
    private readonly frameTimeoutMs;
    private readonly logger;
    constructor(transport: MakaiStdioClient, options?: MakaiAuthClientOptions);
    listProviders(): Promise<ProviderAuthInfo[]>;
    login(providerId: ProviderId, handlers?: AuthFlowHandlers, options?: {
        signal?: AbortSignal;
    }): Promise<{
        status: "success";
    }>;
    private drainLoginResult;
    private nextFrameForStream;
    private sendOrThrow;
    private bestEffortCancel;
}
export type CreateMakaiAuthClientOptions = CreateMakaiStdioClientOptions & {
    handlers?: AuthFlowHandlers;
    frameTimeoutMs?: number;
    resolver?: BinaryResolverOptions;
    logger?: MakaiLogger;
};
export interface MakaiAuthClientHandle {
    auth: MakaiAuthApi;
    close(): Promise<void>;
}
export declare function createMakaiAuthClient(options?: CreateMakaiAuthClientOptions): Promise<MakaiAuthClientHandle>;
