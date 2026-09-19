export declare function checkAbort(signal: AbortSignal | undefined, context?: string): void;
export declare function raceWithAbort<T>(promise: Promise<T>, signal: AbortSignal | undefined, context?: string): Promise<T>;
export declare function isAbortError(error: unknown): error is Error & {
    name: "AbortError";
};
