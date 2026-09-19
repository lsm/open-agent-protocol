import type { MakaiStdioClient } from "./stdio_client";
export declare function bestEffortCancelStream(transport: MakaiStdioClient, streamId: string): void;
export declare function bestEffortCancelAgent(transport: MakaiStdioClient, sessionId: string, sequence?: number): void;
export declare function bestEffortStopAgent(transport: MakaiStdioClient, sessionId: string, sequence: number, reason: string): string;
export declare function drainStreamFrames(transport: MakaiStdioClient, streamId: string, timeoutMs?: number): Promise<void>;
export declare function drainSessionFrames(transport: MakaiStdioClient, sessionId: string, timeoutMs?: number): Promise<void>;
export declare function drainSessionFramesUntilQuiescent(transport: MakaiStdioClient, sessionId: string, idleMs?: number, maxMs?: number, opts?: {
    stopReplyTo?: string;
}): Promise<void>;
export declare function stopAgentWithSequenceProbe(transport: MakaiStdioClient, sessionId: string, sequences: {
    preSend: number;
    postSend: number;
}, reason: string, idleMs?: number, maxMs?: number): Promise<number | undefined>;
