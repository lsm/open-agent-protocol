"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.createMakaiModelsApi = createMakaiModelsApi;
const ulid_1 = require("ulid");
const abort_signal_1 = require("./abort_signal");
const cancel_helpers_1 = require("./cancel_helpers");
const logger_1 = require("./logger");
const models_types_1 = require("./models_types");
const timeout_diagnostics_1 = require("./timeout_diagnostics");
const ENVELOPE_VERSION = 1;
const DEFAULT_CACHE_MAX_AGE_MS = 300_000;
const DEFAULT_RESPONSE_TIMEOUT_MS = 5_000;
const MAX_PROVIDER_ID_LENGTH = 256;
const MAX_MODEL_ID_LENGTH = 256;
const KNOWN_AUTH_STATUSES = new Set([
    "authenticated",
    "login_required",
    "expired",
    "refreshing",
    "login_in_progress",
    "failed",
    "unknown",
]);
const KNOWN_LIFECYCLES = new Set([
    "stable",
    "preview",
    "deprecated",
]);
const KNOWN_CAPABILITIES = new Set([
    "chat",
    "streaming",
    "tools",
    "vision",
    "reasoning",
    "prompt_cache",
    "audio_input",
    "audio_output",
]);
const KNOWN_SOURCES = new Set([
    "dynamic",
    "static_fallback",
]);
const KNOWN_REASONING_LEVELS = new Set([
    "off",
    "minimal",
    "low",
    "medium",
    "high",
    "xhigh",
]);
function createMakaiModelsApi(client, options = {}) {
    return new StdioModelsApi(client, options);
}
const MALFORMED_RESPONSE_CODE = "malformed_response";
class StdioModelsApi {
    client;
    responseTimeoutMs;
    logger;
    constructor(client, options) {
        this.client = client;
        this.responseTimeoutMs = options.responseTimeoutMs ?? DEFAULT_RESPONSE_TIMEOUT_MS;
        this.logger = options.logger ?? (0, logger_1.getNoopLogger)();
    }
    async list(request = {}) {
        if (typeof request.provider_id === "string" && request.provider_id.length > MAX_PROVIDER_ID_LENGTH) {
            throw new models_types_1.MakaiProtocolError(`provider_id exceeds maximum length of ${MAX_PROVIDER_ID_LENGTH} characters`, "invalid_request");
        }
        if (typeof request.model_id === "string" && request.model_id.length > MAX_MODEL_ID_LENGTH) {
            throw new models_types_1.MakaiProtocolError(`model_id exceeds maximum length of ${MAX_MODEL_ID_LENGTH} characters`, "invalid_request");
        }
        return this.dispatch(request, request.signal);
    }
    async resolve(request) {
        if (!request || typeof request.provider_id !== "string" || request.provider_id.length === 0) {
            throw new models_types_1.MakaiProtocolError("resolve requires provider_id", "invalid_request");
        }
        if (request.provider_id.length > MAX_PROVIDER_ID_LENGTH) {
            throw new models_types_1.MakaiProtocolError(`provider_id exceeds maximum length of ${MAX_PROVIDER_ID_LENGTH} characters`, "invalid_request");
        }
        if (typeof request.model_id !== "string" || request.model_id.length === 0) {
            throw new models_types_1.MakaiProtocolError("resolve requires model_id", "invalid_request");
        }
        if (request.model_id.length > MAX_MODEL_ID_LENGTH) {
            throw new models_types_1.MakaiProtocolError(`model_id exceeds maximum length of ${MAX_MODEL_ID_LENGTH} characters`, "invalid_request");
        }
        const response = await this.dispatch({
            provider_id: request.provider_id,
            api: request.api,
            model_id: request.model_id,
        }, request.signal);
        if (response.models.length === 0) {
            throw new models_types_1.MakaiProtocolError("model not found", "invalid_request");
        }
        if (response.models.length > 1) {
            throw new models_types_1.MakaiProtocolError(`resolve returned ${response.models.length} matches; expected exactly 1`, "invalid_request");
        }
        const model = response.models[0];
        if (model.provider_id !== request.provider_id) {
            throw new models_types_1.MakaiProtocolError("resolved model provider_id mismatch", "invalid_request");
        }
        if (model.model_id !== request.model_id) {
            throw new models_types_1.MakaiProtocolError("resolved model_id mismatch", "invalid_request");
        }
        if (request.api !== undefined && model.api !== request.api) {
            throw new models_types_1.MakaiProtocolError("resolved model api mismatch", "invalid_request");
        }
        return { model };
    }
    async nextFrameForStream(streamId, timeoutMs, context, signal) {
        try {
            return await (0, abort_signal_1.raceWithAbort)(this.client.nextFrameForStream(streamId, timeoutMs), signal, "models.list aborted");
        }
        catch (error) {
            if ((0, abort_signal_1.isAbortError)(error))
                throw error;
            throw new models_types_1.MakaiProtocolError((0, timeout_diagnostics_1.isTimeoutLikeError)(error)
                ? (0, timeout_diagnostics_1.formatTimeoutMessage)(context)
                : error instanceof Error ? error.message : String(error), undefined, { diagnostics: (0, timeout_diagnostics_1.isTimeoutLikeError)(error) ? (0, timeout_diagnostics_1.createTimeoutDiagnostics)(context) : undefined });
        }
    }
    async dispatch(request, signal) {
        (0, abort_signal_1.checkAbort)(signal, "models.list aborted before start");
        const streamId = (0, ulid_1.ulid)();
        this.logger.debug("models: sending models_request", { stream_id: streamId, provider_id: request.provider_id, api: request.api });
        const envelope = {
            type: "models_request",
            stream_id: streamId,
            message_id: streamId,
            sequence: 1,
            timestamp: Date.now(),
            version: ENVELOPE_VERSION,
            payload: buildPayload(request),
        };
        this.client.send(envelope);
        const timeoutContext = {
            operation: "models_response",
            timeout_ms: this.responseTimeoutMs,
            stream_id: streamId,
            message_id: streamId,
            provider_id: request.provider_id,
            api: request.api,
            model_id: request.model_id,
        };
        const deadline = Date.now() + this.responseTimeoutMs;
        try {
            while (true) {
                (0, abort_signal_1.checkAbort)(signal, "models.list aborted");
                const remaining = deadline - Date.now();
                if (remaining <= 0) {
                    throw new models_types_1.MakaiProtocolError((0, timeout_diagnostics_1.formatTimeoutMessage)(timeoutContext), undefined, { diagnostics: (0, timeout_diagnostics_1.createTimeoutDiagnostics)(timeoutContext) });
                }
                const frame = await this.nextFrameForStream(streamId, remaining, timeoutContext, signal);
                switch (frame.type) {
                    case "ack":
                        continue;
                    case "nack":
                        throw nackToError(frame);
                    case "models_response": {
                        const response = parseModelsResponse(frame);
                        this.logger.debug("models: received models_response", { count: response.models.length, stream_id: streamId });
                        return response;
                    }
                    default:
                        throw malformedResponseError(`unexpected frame type while awaiting models_response: ${frame.type}`);
                }
            }
        }
        catch (error) {
            if ((0, abort_signal_1.isAbortError)(error)) {
                (0, cancel_helpers_1.bestEffortCancelStream)(this.client, streamId);
                (0, cancel_helpers_1.drainStreamFrames)(this.client, streamId);
            }
            throw error;
        }
    }
}
function buildPayload(request) {
    const payload = {};
    if (typeof request.provider_id === "string" && request.provider_id.length > 0) {
        payload.provider_id = request.provider_id;
    }
    if (typeof request.api === "string" && request.api.length > 0) {
        payload.api = request.api;
    }
    if (typeof request.model_id === "string" && request.model_id.length > 0) {
        payload.model_id = request.model_id;
    }
    if (typeof request.include_deprecated === "boolean") {
        payload.include_deprecated = request.include_deprecated;
    }
    if (typeof request.include_login_required === "boolean") {
        payload.include_login_required = request.include_login_required;
    }
    return payload;
}
function nackToError(frame) {
    const payload = isObject(frame.payload) ? frame.payload : {};
    const reason = typeof payload.reason === "string" && payload.reason.length > 0
        ? payload.reason
        : "models request rejected";
    const code = typeof payload.error_code === "string" ? payload.error_code : undefined;
    return new models_types_1.MakaiProtocolError(reason, code);
}
function malformedResponseError(message) {
    return new models_types_1.MakaiProtocolError(message, MALFORMED_RESPONSE_CODE);
}
function parseModelsResponse(frame) {
    if (!isObject(frame.payload)) {
        throw malformedResponseError("models_response missing payload object");
    }
    const payload = frame.payload;
    const modelsRaw = payload.models;
    if (!Array.isArray(modelsRaw)) {
        throw malformedResponseError("models_response missing 'models' array");
    }
    if (typeof payload.fetched_at_ms !== "number" || !Number.isFinite(payload.fetched_at_ms)) {
        throw malformedResponseError("models_response missing numeric 'fetched_at_ms'");
    }
    const cacheMaxAgeMs = typeof payload.cache_max_age_ms === "number" && Number.isFinite(payload.cache_max_age_ms)
        ? payload.cache_max_age_ms
        : DEFAULT_CACHE_MAX_AGE_MS;
    const models = modelsRaw.map((item, idx) => parseModelDescriptor(item, idx));
    return {
        models,
        fetched_at_ms: payload.fetched_at_ms,
        cache_max_age_ms: cacheMaxAgeMs,
    };
}
function parseModelDescriptor(raw, idx) {
    if (!isObject(raw)) {
        throw malformedResponseError(`models[${idx}] is not an object`);
    }
    const modelRef = requireString(raw.model_ref, `models[${idx}].model_ref`);
    const modelId = requireString(raw.model_id, `models[${idx}].model_id`);
    const displayName = requireString(raw.display_name, `models[${idx}].display_name`);
    const providerId = requireString(raw.provider_id, `models[${idx}].provider_id`);
    const api = requireString(raw.api, `models[${idx}].api`);
    const authStatus = requireKnownString(raw.auth_status, `models[${idx}].auth_status`, KNOWN_AUTH_STATUSES);
    const lifecycle = requireKnownString(raw.lifecycle, `models[${idx}].lifecycle`, KNOWN_LIFECYCLES);
    const source = requireKnownString(raw.source, `models[${idx}].source`, KNOWN_SOURCES);
    if (!Array.isArray(raw.capabilities)) {
        throw malformedResponseError(`models[${idx}].capabilities must be an array`);
    }
    const capabilities = raw.capabilities.map((cap, capIdx) => {
        if (typeof cap !== "string") {
            throw malformedResponseError(`models[${idx}].capabilities[${capIdx}] must be a string`);
        }
        if (!KNOWN_CAPABILITIES.has(cap)) {
            throw malformedResponseError(`models[${idx}].capabilities[${capIdx}] has unknown value: ${cap}`);
        }
        return cap;
    });
    const descriptor = {
        model_ref: modelRef,
        model_id: modelId,
        display_name: displayName,
        provider_id: providerId,
        api,
        auth_status: authStatus,
        lifecycle,
        capabilities,
        source,
    };
    if (typeof raw.base_url === "string" && raw.base_url.length > 0) {
        descriptor.base_url = raw.base_url;
    }
    if (typeof raw.context_window === "number" && Number.isFinite(raw.context_window)) {
        descriptor.context_window = raw.context_window;
    }
    if (typeof raw.max_output_tokens === "number" && Number.isFinite(raw.max_output_tokens)) {
        descriptor.max_output_tokens = raw.max_output_tokens;
    }
    if (raw.reasoning_default !== undefined) {
        descriptor.reasoning_default = requireKnownString(raw.reasoning_default, `models[${idx}].reasoning_default`, KNOWN_REASONING_LEVELS);
    }
    if (isObject(raw.metadata)) {
        const metadata = {};
        for (const [key, value] of Object.entries(raw.metadata)) {
            if (typeof value === "string") {
                metadata[key] = value;
            }
            else {
                throw malformedResponseError(`models[${idx}].metadata.${key} must be a string`);
            }
        }
        descriptor.metadata = metadata;
    }
    return descriptor;
}
function requireString(value, fieldName) {
    if (typeof value !== "string") {
        throw malformedResponseError(`${fieldName} must be a string`);
    }
    return value;
}
function requireKnownString(value, fieldName, knownValues) {
    const text = requireString(value, fieldName);
    if (!knownValues.has(text)) {
        throw malformedResponseError(`${fieldName} has unknown value: ${text}`);
    }
    return text;
}
function isObject(value) {
    return typeof value === "object" && value !== null && !Array.isArray(value);
}
