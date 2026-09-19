export interface MakaiLogger {
    debug(message: string, context?: Record<string, unknown>): void;
    info(message: string, context?: Record<string, unknown>): void;
    warn(message: string, context?: Record<string, unknown>): void;
    error(message: string, context?: Record<string, unknown>): void;
}
export declare function getNoopLogger(): MakaiLogger;
export declare function isNoopLogger(logger: MakaiLogger): boolean;
