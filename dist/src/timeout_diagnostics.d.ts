export type TimeoutDiagnosticContext = {
    operation: string;
    timeout_ms: number;
    stream_id?: string;
    message_id?: string;
    session_id?: string;
    provider_id?: string;
    api?: string;
    model_ref?: string;
    model_id?: string;
};
export type TimeoutDiagnostics = TimeoutDiagnosticContext & {
    suggestions: string[];
};
export type ErrorWithDiagnostics = Error & {
    diagnostics?: TimeoutDiagnostics;
};
export declare const TIMEOUT_SUGGESTIONS: readonly ["Verify the makai binary is installed, executable, and still running.", "Check network connectivity and provider service health.", "Review server logs using the included stream_id/message_id for correlation.", "Increase the responseTimeoutMs/frameTimeoutMs option if the provider is expected to be slow."];
export declare function createTimeoutDiagnostics(context: TimeoutDiagnosticContext): TimeoutDiagnostics;
export declare function formatTimeoutMessage(context: TimeoutDiagnosticContext): string;
export declare function timeoutError(message: string, context: TimeoutDiagnosticContext): ErrorWithDiagnostics;
export declare function isTimeoutLikeError(error: unknown): boolean;
