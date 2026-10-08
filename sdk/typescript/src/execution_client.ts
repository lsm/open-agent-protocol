import type { MakaiAuthApi } from "./auth_protocol";
import type { BinaryResolverOptions } from "./binary_resolver";
import type { MakaiAgentApi, MakaiClientOptions, MakaiProviderApi } from "./execution_types";
import type { MakaiModelsApi } from "./models_types";
import { createOapClient } from "./oap_client";

export interface MakaiClient {
  auth: MakaiAuthApi;
  models: MakaiModelsApi;
  agent: MakaiAgentModelsApi;
  provider: MakaiProviderApi;
  close(): Promise<void>;
}

export interface MakaiAgentModelsApi extends MakaiAgentApi {
  models: MakaiModelsApi;
}

export type CreateMakaiClientOptions = MakaiClientOptions & {
  command?: string;
  args?: string[];
  cwd?: string;
  env?: NodeJS.ProcessEnv;
  resolver?: BinaryResolverOptions;
  handshakeTimeoutMs?: number;
};

export async function createMakaiClient(options: CreateMakaiClientOptions = {}): Promise<MakaiClient> {
  return createOapClient(options);
}
