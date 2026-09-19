import { type Server } from "node:http";
type DemoServerOptions = {
    host?: string;
    port?: number;
    homeDir?: string;
    binaryPath?: string;
    command?: string;
    args?: string[];
    env?: NodeJS.ProcessEnv;
};
type RunningDemoServer = {
    server: Server;
    url: string;
    close: () => Promise<void>;
};
export declare function createDemoServer(options?: DemoServerOptions): Server;
export declare function startDemoServer(options?: DemoServerOptions): Promise<RunningDemoServer>;
export {};
