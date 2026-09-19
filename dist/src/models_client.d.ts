import { type MakaiLogger } from "./logger";
import { MakaiModelsApi } from "./models_types";
import { MakaiStdioClient } from "./stdio_client";
export interface ModelsApiOptions {
    responseTimeoutMs?: number;
    logger?: MakaiLogger;
}
export declare function createMakaiModelsApi(client: MakaiStdioClient, options?: ModelsApiOptions): MakaiModelsApi;
