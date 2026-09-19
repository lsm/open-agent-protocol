import { BinaryResolverOptions } from "./binary_resolver";
import { type MakaiLogger } from "./logger";
export type StdioFrame = {
    type: string;
    [key: string]: unknown;
};
export type MakaiStdioClientOptions = {
    command: string;
    args?: string[];
    cwd?: string;
    env?: NodeJS.ProcessEnv;
    expectedProtocolVersion?: string;
    handshakeTimeoutMs?: number;
    streamFrameQueueTtlMs?: number;
    logger?: MakaiLogger;
};
/** @deprecated Use MakaiStdioClientOptions. Kept for backward compatibility. */
export type MakaiClientOptions = MakaiStdioClientOptions;
export declare class StdioProtocolError extends Error {
    readonly code?: string | undefined;
    constructor(message: string, code?: string | undefined);
}
export type FrameWaitOptions = {
    correlate?: string;
    repliesOnly?: boolean;
};
export type SessionFrameWaitOptions = FrameWaitOptions & {
    signal?: AbortSignal;
};
export declare class MakaiStdioClient {
    private readonly options;
    private readonly logger;
    private child;
    private lineReader;
    private pendingHandshake;
    private frameQueue;
    private frameWaiters;
    private streamFrameQueues;
    private sessionFrameQueues;
    private replyFrameQueues;
    private activeCorrelates;
    private correlateDeliveries;
    private streamReadLock;
    constructor(options: MakaiStdioClientOptions);
    connect(): Promise<void>;
    private terminateChild;
    send(frame: StdioFrame): void;
    nextFrame(timeoutMs?: number): Promise<StdioFrame>;
    nextFrameForStream(streamId: string, timeoutMs?: number, options?: FrameWaitOptions): Promise<StdioFrame>;
    nextFrameForSession(sessionId: string, timeoutMs?: number, options?: SessionFrameWaitOptions): Promise<StdioFrame>;
    private waitForRoutedFrame;
    private readRoutedLoop;
    close(): Promise<void>;
    private dequeueStreamFrame;
    private dequeueSessionFrame;
    private dequeueOwnFrame;
    private frameMatchesRoute;
    private dequeueRoutedFrame;
    private enqueueRoutableFrame;
    private deliverCorrelatedFrame;
    private registerCorrelateDelivery;
    private releaseCorrelateDelivery;
    private enqueueRoutedFrame;
    private retainCorrelate;
    private releaseCorrelate;
    private hasActiveCorrelate;
    private pruneExpiredRoutedFrames;
    private pruneExpiredQueue;
    private withStreamReadLock;
    private handleLine;
    private failHandshakeIfPending;
    private failPendingFrameWaiters;
    private cleanupProcessHandles;
}
export type CreateMakaiStdioClientOptions = Omit<MakaiStdioClientOptions, "command"> & {
    command?: string;
    resolver?: BinaryResolverOptions;
};
/** @deprecated Use CreateMakaiStdioClientOptions. Kept for backward compatibility. */
export type CreateMakaiClientOptions = CreateMakaiStdioClientOptions;
export declare function createMakaiStdioClient(options?: CreateMakaiStdioClientOptions): Promise<MakaiStdioClient>;
