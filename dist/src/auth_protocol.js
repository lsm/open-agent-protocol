"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.MakaiAuthClient = exports.MakaiAuthError = exports.AUTH_STATUSES = void 0;
exports.flattenAuthEvent = flattenAuthEvent;
exports.createMakaiAuthClient = createMakaiAuthClient;
const ulid_1 = require("ulid");
const abort_signal_1 = require("./abort_signal");
const logger_1 = require("./logger");
const stdio_client_1 = require("./stdio_client");
const timeout_diagnostics_1 = require("./timeout_diagnostics");
exports.AUTH_STATUSES = [
    "authenticated",
    "login_required",
    "expired",
    "refreshing",
    "login_in_progress",
    "failed",
    "unknown",
];
const VALID_AUTH_STATUSES = new Set(exports.AUTH_STATUSES);
class MakaiAuthError extends Error {
    kind;
    code;
    diagnostics;
    constructor(message, options = {}) {
        super(message);
        this.name = "MakaiAuthError";
        this.kind = options.kind ?? "unknown";
        this.code = options.code;
        this.diagnostics = options.diagnostics;
    }
}
exports.MakaiAuthError = MakaiAuthError;
const AUTH_EVENT_VARIANTS = [
    "auth_url",
    "prompt",
    "progress",
    "success",
    "error",
];
function flattenAuthEvent(payload) {
    for (const variant of AUTH_EVENT_VARIANTS) {
        const value = payload[variant];
        if (value && typeof value === "object" && !Array.isArray(value)) {
            return normalizeAuthEvent(variant, value);
        }
    }
    throw new MakaiAuthError(`unknown auth_event variant: ${JSON.stringify(payload)}`, { kind: "unknown" });
}
function normalizeAuthEvent(variant, data) {
    const flow_id = stringField(data, "flow_id");
    const provider_id = stringField(data, "provider_id");
    switch (variant) {
        case "auth_url": {
            const event = {
                type: "auth_url",
                flow_id,
                provider_id,
                url: stringField(data, "url"),
            };
            const instructions = optionalStringField(data, "instructions");
            if (instructions !== undefined)
                event.instructions = instructions;
            return event;
        }
        case "prompt":
            return {
                type: "prompt",
                flow_id,
                prompt_id: stringField(data, "prompt_id"),
                provider_id,
                message: stringField(data, "message"),
                allow_empty: typeof data["allow_empty"] === "boolean" ? data["allow_empty"] : false,
            };
        case "progress":
            return {
                type: "progress",
                flow_id,
                provider_id,
                message: stringField(data, "message"),
            };
        case "success":
            return {
                type: "success",
                flow_id,
                provider_id,
            };
        case "error": {
            const event = {
                type: "error",
                flow_id,
                provider_id,
                message: stringField(data, "message"),
            };
            const code = optionalStringField(data, "code");
            if (code !== undefined)
                event.code = code;
            return event;
        }
    }
}
function stringField(data, key) {
    const value = data[key];
    if (typeof value !== "string") {
        throw new MakaiAuthError(`auth_event field "${key}" missing or not a string`, {
            kind: "transport_error",
        });
    }
    return value;
}
function optionalStringField(data, key) {
    const value = data[key];
    if (value === undefined || value === null)
        return undefined;
    if (typeof value !== "string")
        return undefined;
    return value.length > 0 ? value : undefined;
}
const PROTOCOL_VERSION = 1;
class MakaiAuthClient {
    transport;
    defaultHandlers;
    frameTimeoutMs;
    logger;
    constructor(transport, options = {}) {
        this.transport = transport;
        this.defaultHandlers = options.handlers;
        this.frameTimeoutMs = options.frameTimeoutMs ?? 30_000;
        this.logger = options.logger ?? (0, logger_1.getNoopLogger)();
    }
    async listProviders() {
        const streamId = (0, ulid_1.ulid)();
        const messageId = (0, ulid_1.ulid)();
        const envelope = {
            type: "auth_providers_request",
            stream_id: streamId,
            message_id: messageId,
            sequence: 1,
            timestamp: Date.now(),
            version: PROTOCOL_VERSION,
            payload: {},
        };
        this.logger.debug("auth: sending auth_providers_request", { stream_id: streamId });
        this.sendOrThrow(envelope);
        while (true) {
            const frame = await this.nextFrameForStream(streamId, {
                operation: "auth_providers_response",
                message_id: messageId,
            });
            if (frame.type === "ack")
                continue;
            if (frame.type === "nack") {
                throw nackToAuthError(frame);
            }
            if (frame.type === "auth_providers_response") {
                const providers = parseProviders(frame);
                this.logger.debug("auth: received providers list", { count: providers.length });
                return providers;
            }
            throw new MakaiAuthError(`unexpected envelope type while awaiting auth_providers_response: ${String(frame.type)}`, { kind: "transport_error" });
        }
    }
    async login(providerId, handlers, options) {
        const signal = options?.signal;
        if (signal?.aborted) {
            throw new MakaiAuthError("auth login aborted", { kind: "cancelled" });
        }
        const effective = handlers ?? this.defaultHandlers;
        const flowId = (0, ulid_1.ulid)();
        let outboundSequence = 1;
        let loginStarted = false;
        this.logger.info("auth: starting login flow", { provider_id: providerId, flow_id: flowId });
        const startMessageId = (0, ulid_1.ulid)();
        this.sendOrThrow({
            type: "auth_login_start",
            stream_id: flowId,
            message_id: startMessageId,
            sequence: outboundSequence++,
            timestamp: Date.now(),
            version: PROTOCOL_VERSION,
            payload: { provider_id: providerId },
        });
        loginStarted = true;
        let lastErrorEvent;
        let cancelled = false;
        while (true) {
            if (signal?.aborted) {
                if (loginStarted) {
                    this.bestEffortCancel(flowId, outboundSequence++);
                }
                throw new MakaiAuthError("auth login aborted", { kind: "cancelled" });
            }
            const frame = await this.nextFrameForStream(flowId, {
                operation: "auth_login_result/auth_event",
                message_id: startMessageId,
                provider_id: providerId,
            }, signal, () => outboundSequence++);
            if (frame.type === "ack")
                continue;
            if (frame.type === "nack") {
                throw nackToAuthError(frame);
            }
            if (frame.type === "auth_event") {
                const eventPayload = readPayload(frame);
                const event = flattenAuthEvent(eventPayload);
                this.logger.debug("auth: received auth event", { event_type: event.type, flow_id: flowId, provider_id: providerId });
                try {
                    effective?.onEvent?.(event);
                }
                catch (err) {
                    throw new MakaiAuthError(err instanceof Error ? err.message : String(err), { kind: "unknown" });
                }
                if (event.type === "error") {
                    lastErrorEvent = { code: event.code, message: event.message };
                    continue;
                }
                if (event.type === "prompt") {
                    if (!effective?.onPrompt) {
                        cancelled = true;
                        this.bestEffortCancel(flowId, outboundSequence++);
                        continue;
                    }
                    let answer;
                    try {
                        answer = await (0, abort_signal_1.raceWithAbort)(Promise.resolve(effective.onPrompt(event)), signal, "auth.login aborted during prompt");
                    }
                    catch (err) {
                        if ((0, abort_signal_1.isAbortError)(err)) {
                            this.bestEffortCancel(flowId, outboundSequence++);
                            throw new MakaiAuthError("auth login aborted", { kind: "cancelled" });
                        }
                        this.bestEffortCancel(flowId, outboundSequence++);
                        await this.drainLoginResult(flowId);
                        throw new MakaiAuthError(err instanceof Error ? err.message : String(err), { kind: "unknown" });
                    }
                    this.sendOrThrow({
                        type: "auth_prompt_response",
                        stream_id: flowId,
                        message_id: (0, ulid_1.ulid)(),
                        sequence: outboundSequence++,
                        timestamp: Date.now(),
                        version: PROTOCOL_VERSION,
                        payload: {
                            flow_id: flowId,
                            prompt_id: event.prompt_id,
                            answer: answer ?? "",
                        },
                    });
                    continue;
                }
                continue;
            }
            if (frame.type === "auth_login_result") {
                const payload = readPayload(frame);
                const status = typeof payload["status"] === "string" ? payload["status"] : undefined;
                if (status === "success") {
                    this.logger.info("auth: login succeeded", { provider_id: providerId, flow_id: flowId });
                    return { status: "success" };
                }
                if (status === "cancelled") {
                    this.logger.warn("auth: login cancelled", { provider_id: providerId, flow_id: flowId });
                    throw new MakaiAuthError(lastErrorEvent?.message ??
                        (cancelled
                            ? "auth login cancelled (no onPrompt handler configured)"
                            : "auth login cancelled"), { kind: "cancelled", code: lastErrorEvent?.code });
                }
                if (status === "failed") {
                    this.logger.error("auth: login failed", { provider_id: providerId, flow_id: flowId });
                    throw new MakaiAuthError(lastErrorEvent?.message ?? "auth login failed", { kind: "provider_error", code: lastErrorEvent?.code });
                }
                throw new MakaiAuthError(`unexpected auth_login_result status: ${String(status)}`, { kind: "unknown" });
            }
            throw new MakaiAuthError(`unexpected envelope type during login flow: ${String(frame.type)}`, { kind: "transport_error" });
        }
    }
    async drainLoginResult(flowId) {
        while (true) {
            const frame = await this.nextFrameForStream(flowId, {
                operation: "auth_login_result",
            });
            if (frame.type === "auth_login_result")
                return;
        }
    }
    async nextFrameForStream(streamId, context, signal, nextCancelSequence) {
        try {
            return (await (0, abort_signal_1.raceWithAbort)(this.transport.nextFrameForStream(streamId, this.frameTimeoutMs), signal, "auth.login aborted"));
        }
        catch (error) {
            if ((0, abort_signal_1.isAbortError)(error)) {
                const seq = nextCancelSequence ? nextCancelSequence() : 999;
                this.bestEffortCancel(streamId, seq);
                throw new MakaiAuthError("auth login aborted", { kind: "cancelled" });
            }
            const diagnosticsContext = { ...context, timeout_ms: this.frameTimeoutMs, stream_id: streamId };
            throw new MakaiAuthError((0, timeout_diagnostics_1.isTimeoutLikeError)(error)
                ? (0, timeout_diagnostics_1.formatTimeoutMessage)(diagnosticsContext)
                : error instanceof Error ? error.message : String(error), {
                kind: "transport_error",
                diagnostics: (0, timeout_diagnostics_1.isTimeoutLikeError)(error) ? (0, timeout_diagnostics_1.createTimeoutDiagnostics)(diagnosticsContext) : undefined,
            });
        }
    }
    sendOrThrow(envelope) {
        try {
            this.transport.send(envelope);
        }
        catch (error) {
            throw new MakaiAuthError(error instanceof Error ? error.message : String(error), { kind: "transport_error" });
        }
    }
    bestEffortCancel(flowId, sequence) {
        try {
            this.transport.send({
                type: "auth_cancel",
                stream_id: flowId,
                message_id: (0, ulid_1.ulid)(),
                sequence,
                timestamp: Date.now(),
                version: PROTOCOL_VERSION,
                payload: { flow_id: flowId },
            });
        }
        catch {
        }
    }
}
exports.MakaiAuthClient = MakaiAuthClient;
function readPayload(frame) {
    const payload = frame.payload;
    if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
        throw new MakaiAuthError(`envelope ${String(frame.type)} missing payload object`, { kind: "transport_error" });
    }
    return payload;
}
function parseProviders(frame) {
    const payload = readPayload(frame);
    const providers = payload["providers"];
    if (!Array.isArray(providers)) {
        throw new MakaiAuthError("auth_providers_response payload missing providers array", { kind: "transport_error" });
    }
    return providers.map((entry, index) => parseProvider(entry, index));
}
function parseProvider(entry, index) {
    if (!entry || typeof entry !== "object") {
        throw new MakaiAuthError(`provider entry at index ${index} is not an object`, { kind: "transport_error" });
    }
    const data = entry;
    const id = data["id"];
    const name = data["name"];
    const status = data["auth_status"];
    if (typeof id !== "string" || typeof name !== "string") {
        throw new MakaiAuthError(`provider entry at index ${index} missing id/name`, { kind: "transport_error" });
    }
    const provider = {
        id,
        name,
        auth_status: typeof status === "string" && VALID_AUTH_STATUSES.has(status)
            ? status
            : "unknown",
    };
    const lastError = data["last_error"];
    if (typeof lastError === "string" && lastError.length > 0) {
        provider.last_error = lastError;
    }
    return provider;
}
function nackToAuthError(frame) {
    const payload = frame.payload && typeof frame.payload === "object" && !Array.isArray(frame.payload)
        ? frame.payload
        : {};
    const reason = typeof payload["reason"] === "string" ? payload["reason"] : "transport nack";
    const code = typeof payload["error_code"] === "string" ? payload["error_code"] : undefined;
    return new MakaiAuthError(reason, { kind: "transport_error", code });
}
async function createMakaiAuthClient(options = {}) {
    const { handlers, frameTimeoutMs, logger, ...transportOptions } = options;
    const transport = await (0, stdio_client_1.createMakaiStdioClient)({ ...transportOptions, logger });
    await transport.connect();
    const auth = new MakaiAuthClient(transport, { handlers, frameTimeoutMs, logger });
    return {
        auth,
        close: () => transport.close(),
    };
}
