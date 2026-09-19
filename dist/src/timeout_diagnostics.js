"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.TIMEOUT_SUGGESTIONS = void 0;
exports.createTimeoutDiagnostics = createTimeoutDiagnostics;
exports.formatTimeoutMessage = formatTimeoutMessage;
exports.timeoutError = timeoutError;
exports.isTimeoutLikeError = isTimeoutLikeError;
exports.TIMEOUT_SUGGESTIONS = [
    "Verify the makai binary is installed, executable, and still running.",
    "Check network connectivity and provider service health.",
    "Review server logs using the included stream_id/message_id for correlation.",
    "Increase the responseTimeoutMs/frameTimeoutMs option if the provider is expected to be slow.",
];
function createTimeoutDiagnostics(context) {
    return {
        ...context,
        suggestions: [...exports.TIMEOUT_SUGGESTIONS],
    };
}
function formatTimeoutMessage(context) {
    const diagnostics = createTimeoutDiagnostics(context);
    const ids = formatIds(diagnostics);
    const provider = diagnostics.provider_id ? ` for provider '${diagnostics.provider_id}'` : "";
    const model = diagnostics.model_ref ? ` (model_ref='${diagnostics.model_ref}')` : "";
    return `Timed out waiting for ${diagnostics.operation} after ${diagnostics.timeout_ms}ms${provider}${model}${ids}. Suggestions: ${diagnostics.suggestions.join(" ")}`;
}
function timeoutError(message, context) {
    const error = new Error(message);
    error.diagnostics = createTimeoutDiagnostics(context);
    return error;
}
function isTimeoutLikeError(error) {
    return error instanceof Error && /^timed out waiting for|^Timed out waiting for|timed out after/.test(error.message);
}
function formatIds(diagnostics) {
    const fields = [];
    if (diagnostics.stream_id)
        fields.push(`stream_id=${diagnostics.stream_id}`);
    if (diagnostics.session_id)
        fields.push(`session_id=${diagnostics.session_id}`);
    if (diagnostics.message_id)
        fields.push(`message_id=${diagnostics.message_id}`);
    return fields.length > 0 ? ` (${fields.join(", ")})` : "";
}
