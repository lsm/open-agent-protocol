import { type AuthFlowHandlers, type MakaiAuthApi } from "./auth_protocol";
import { type MakaiLogger } from "./logger";
import { type MakaiModelsApi } from "./models_types";
import { type CreateMakaiStdioClientOptions, MakaiStdioClient } from "./stdio_client";
import { type MakaiAgentApi, type MakaiClientOptions, type MakaiProviderApi, type RunOptions } from "./execution_types";
type ExecutionOptions = {
    responseTimeoutMs?: number;
    authRetryPolicy?: RunOptions["auth_retry_policy"];
    auth?: MakaiAuthApi;
    authHandlers?: AuthFlowHandlers;
    logger?: MakaiLogger;
};
export interface MakaiClient {
    auth: MakaiAuthApi;
    models: MakaiModelsApi;
    agent: MakaiAgentModelsApi;
    provider: MakaiProviderApi;
    close(): Promise<void>;
}
export type CreateMakaiClientOptions = CreateMakaiStdioClientOptions & MakaiClientOptions;
export declare function createMakaiProviderApi(transport: MakaiStdioClient, options?: ExecutionOptions): MakaiProviderApi;
export declare function createMakaiAgentApi(transport: MakaiStdioClient, options?: ExecutionOptions): MakaiAgentApi;
export interface MakaiAgentModelsApi extends MakaiAgentApi {
    models: MakaiModelsApi;
}
export declare function createMakaiAgentApiWithModels(transport: MakaiStdioClient, options?: ExecutionOptions): MakaiAgentModelsApi;
export declare function createMakaiClient(options?: CreateMakaiClientOptions): Promise<MakaiClient>;
export {};
