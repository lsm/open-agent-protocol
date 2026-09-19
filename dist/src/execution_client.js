"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.createMakaiProviderApi = createMakaiProviderApi;
exports.createMakaiAgentApi = createMakaiAgentApi;
exports.createMakaiAgentApiWithModels = createMakaiAgentApiWithModels;
exports.createMakaiClient = createMakaiClient;
const node_crypto_1 = require("node:crypto");
const ulid_1 = require("ulid");
const abort_signal_1 = require("./abort_signal");
const auth_protocol_1 = require("./auth_protocol");
const cancel_helpers_1 = require("./cancel_helpers");
const model_ref_1 = require("./diagnostics/model_ref");
const logger_1 = require("./logger");
const models_client_1 = require("./models_client");
const models_types_1 = require("./models_types");
const stdio_client_1 = require("./stdio_client");
const timeout_diagnostics_1 = require("./timeout_diagnostics");
const execution_types_1 = require("./execution_types");
const ENVELOPE_VERSION = 1;
const DEFAULT_RESPONSE_TIMEOUT_MS = 30_000;
const NANO_ID_ALPHABET = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
function generateNanoId() {
    let id = "";
    for (let i = 0; i < 21; i++) {
        id += NANO_ID_ALPHABET[(0, node_crypto_1.randomInt)(NANO_ID_ALPHABET.length)];
    }
    return id;
}
function createMakaiProviderApi(transport, options = {}) {
    return new StdioProviderApi(transport, options);
}
function createMakaiAgentApi(transport, options = {}) {
    return new StdioAgentApi(transport, options);
}
function createMakaiAgentApiWithModels(transport, options = {}) {
    return Object.assign(new StdioAgentApi(transport, options), {
        models: (0, models_client_1.createMakaiModelsApi)(transport, { responseTimeoutMs: options.responseTimeoutMs, logger: options.logger }),
    });
}
class StdioProviderApi {
    transport;
    responseTimeoutMs;
    authRetryPolicy;
    auth;
    authHandlers;
    logger;
    constructor(transport, options) {
        this.transport = transport;
        this.responseTimeoutMs = options.responseTimeoutMs ?? DEFAULT_RESPONSE_TIMEOUT_MS;
        this.authRetryPolicy = options.authRetryPolicy;
        this.auth = options.auth;
        this.authHandlers = options.authHandlers;
        this.logger = options.logger ?? (0, logger_1.getNoopLogger)();
    }
    async complete(request) {
        const signal = request.options?.signal;
        (0, abort_signal_1.checkAbort)(signal, "provider.complete aborted before start");
        const effectivePolicy = request.options?.auth_retry_policy ?? this.authRetryPolicy;
        const activeStreamId = {};
        return withAuthRetry(() => this.completeOnce(request, effectivePolicy, signal, activeStreamId), {
            auth: this.auth,
            authHandlers: this.authHandlers,
            authRetryPolicy: effectivePolicy,
            fallbackProviderId: providerIdFromRequest(request),
            signal,
            logger: this.logger,
            onAbort: () => this.cancelActiveStream(activeStreamId),
        });
    }
    async completeOnce(request, effectivePolicy, signal, activeStreamId) {
        (0, abort_signal_1.checkAbort)(signal, "provider.complete aborted");
        const streamId = (0, ulid_1.ulid)();
        if (activeStreamId) {
            activeStreamId.value = streamId;
            activeStreamId.cancelled = false;
        }
        const fallbackProviderId = providerIdFromRequest(request);
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("provider: sending complete_request", { stream_id: streamId, model_ref: request.model_ref });
        }
        sendOrStreamError(this.transport, buildEnvelope("complete_request", streamId, buildExecutionPayload(request, { authRetryPolicy: effectivePolicy })));
        const timeoutContext = executionTimeoutContext("provider complete_response", this.responseTimeoutMs, streamId, request);
        try {
            while (true) {
                (0, abort_signal_1.checkAbort)(signal, "provider.complete aborted");
                const frame = await (0, abort_signal_1.raceWithAbort)(nextFrame(this.transport, streamId, timeoutContext), signal, "provider.complete aborted");
                if (frame.type === "ack")
                    continue;
                if (frame.type === "nack")
                    throw nackToStreamError(frame, fallbackProviderId);
                if (frame.type === "stream_error")
                    throw streamErrorFrameToError(frame);
                if (frame.type === "result" || frame.type === "complete_response") {
                    return parseCompletionResponse(frame.payload ?? frame);
                }
                throw new execution_types_1.MakaiStreamError(`unexpected frame type while awaiting provider result: ${String(frame.type)}`, { kind: "transport_error" });
            }
        }
        catch (error) {
            if ((0, abort_signal_1.isAbortError)(error)) {
                (0, cancel_helpers_1.bestEffortCancelStream)(this.transport, streamId);
                (0, cancel_helpers_1.drainStreamFrames)(this.transport, streamId);
            }
            throw error;
        }
    }
    async *stream(request) {
        const signal = request.options?.signal;
        (0, abort_signal_1.checkAbort)(signal, "provider.stream aborted before start");
        const effectivePolicy = request.options?.auth_retry_policy ?? this.authRetryPolicy;
        const fallbackProviderId = providerIdFromRequest(request);
        const activeStreamId = {};
        let attempt = this.streamAttempt(request, effectivePolicy, signal, activeStreamId);
        let iterator = attempt[Symbol.asyncIterator]();
        let yielded = false;
        let retried = false;
        let settled = false;
        let sawTerminal = false;
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("provider: starting stream", { model_ref: request.model_ref });
        }
        try {
            while (true) {
                (0, abort_signal_1.checkAbort)(signal, "provider.stream aborted");
                let result;
                try {
                    result = await (0, abort_signal_1.raceWithAbort)(iterator.next(), signal, "provider.stream aborted");
                }
                catch (error) {
                    if ((0, abort_signal_1.isAbortError)(error)) {
                        throw error;
                    }
                    if (!yielded && !retried && isRetryableAuthError(error) && effectivePolicy === "auto_once" && this.auth) {
                        const providerId = error.provider_id ?? fallbackProviderId;
                        if (providerId) {
                            try {
                                await (0, abort_signal_1.raceWithAbort)(this.auth.login(providerId, this.authHandlers, { signal }), signal, "provider.stream aborted during auth retry");
                            }
                            catch (authError) {
                                if ((0, abort_signal_1.isAbortError)(authError)) {
                                    throw authError;
                                }
                                throw authRequiredError(providerId, error.message);
                            }
                            retried = true;
                            attempt = this.streamAttempt(request, effectivePolicy, signal, activeStreamId);
                            iterator = attempt[Symbol.asyncIterator]();
                            continue;
                        }
                    }
                    if (isRetryableAuthError(error)) {
                        const providerId = error.provider_id ?? fallbackProviderId;
                        if (providerId)
                            throw authRequiredError(providerId, error.message);
                    }
                    throw error;
                }
                if (result.done) {
                    settled = true;
                    return;
                }
                yielded = true;
                if (result.value.type === "message_end" || result.value.type === "error")
                    sawTerminal = true;
                yield result.value;
            }
        }
        catch (error) {
            settled = true;
            if ((0, abort_signal_1.isAbortError)(error))
                this.cancelActiveStream(activeStreamId);
            throw error;
        }
        finally {
            if (!settled && !sawTerminal)
                this.cancelActiveStream(activeStreamId);
            const closed = iterator.return?.();
            if (closed)
                void closed.catch(() => undefined);
        }
    }
    cancelActiveStream(state) {
        if (state.cancelled)
            return;
        const streamId = state.value;
        if (!streamId)
            return;
        state.cancelled = true;
        this.logger.debug("provider: cancelling active stream", { stream_id: streamId });
        (0, cancel_helpers_1.bestEffortCancelStream)(this.transport, streamId);
        void (0, cancel_helpers_1.drainStreamFrames)(this.transport, streamId);
    }
    async *streamAttempt(request, effectivePolicy, signal, activeStreamId) {
        (0, abort_signal_1.checkAbort)(signal, "provider.stream aborted");
        const streamId = (0, ulid_1.ulid)();
        if (activeStreamId) {
            activeStreamId.value = streamId;
            activeStreamId.cancelled = false;
        }
        const fallbackProviderId = providerIdFromRequest(request);
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("provider: sending stream_request", { stream_id: streamId, model_ref: request.model_ref });
        }
        sendOrStreamError(this.transport, buildEnvelope("stream_request", streamId, buildExecutionPayload(request, { suppressPartial: true, authRetryPolicy: effectivePolicy })));
        const timeoutContext = executionTimeoutContext("provider stream event", this.responseTimeoutMs, streamId, request);
        let terminal = false;
        const toolBuffers = new Map();
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("provider: stream started", { stream_id: streamId });
        }
        try {
            while (!terminal) {
                (0, abort_signal_1.checkAbort)(signal, "provider.stream aborted");
                const frame = await (0, abort_signal_1.raceWithAbort)(nextFrame(this.transport, streamId, timeoutContext), signal, "provider.stream aborted");
                if (frame.type === "ack")
                    continue;
                if (frame.type === "nack")
                    throw nackToStreamError(frame, fallbackProviderId);
                const event = normalizeProviderFrame(frame, toolBuffers);
                if (!event)
                    continue;
                if (event.type === "error") {
                    terminal = true;
                    if (event.code === "auth_required") {
                        throw new execution_types_1.MakaiStreamError(event.message, { kind: "provider_error", code: event.code, provider_id: event.provider_id });
                    }
                    yield event;
                    continue;
                }
                if (event.type === "message_end")
                    terminal = true;
                yield event;
            }
        }
        catch (error) {
            if (error instanceof execution_types_1.MakaiStreamError) {
                this.logger.error("provider: stream error", { kind: error.kind, code: error.code, message: error.message });
                throw error;
            }
            if ((0, abort_signal_1.isAbortError)(error)) {
                throw error;
            }
            this.logger.error("provider: unexpected stream error", { error: error instanceof Error ? error.message : String(error) });
            throw new execution_types_1.MakaiStreamError(error instanceof Error ? error.message : String(error), { kind: "transport_error" });
        }
    }
}
class StdioAgentApi {
    transport;
    responseTimeoutMs;
    authRetryPolicy;
    auth;
    authHandlers;
    logger;
    constructor(transport, options) {
        this.transport = transport;
        this.responseTimeoutMs = options.responseTimeoutMs ?? DEFAULT_RESPONSE_TIMEOUT_MS;
        this.authRetryPolicy = options.authRetryPolicy;
        this.auth = options.auth;
        this.authHandlers = options.authHandlers;
        this.logger = options.logger ?? (0, logger_1.getNoopLogger)();
    }
    async run(request) {
        const signal = request.options?.signal;
        (0, abort_signal_1.checkAbort)(signal, "agent.run aborted before start");
        const effectivePolicy = request.options?.auth_retry_policy ?? this.authRetryPolicy;
        let retryRequest = request;
        let retryIdClientGenerated = request.options?.session_id === undefined;
        const activeSession = { nextSequence: 1 };
        return withAuthRetry(() => this.runOnce(retryRequest, effectivePolicy, signal, activeSession, retryIdClientGenerated), {
            auth: this.auth,
            authHandlers: this.authHandlers,
            authRetryPolicy: effectivePolicy,
            fallbackProviderId: providerIdFromRequest(request),
            signal,
            logger: this.logger,
            beforeRetry: () => {
                this.stopAgentSession(activeSession, activeSession.sessionId, activeSession.nextSequence, "completed");
                retryRequest = {
                    ...request,
                    options: { ...request.options, session_id: generateNanoId() },
                };
                retryIdClientGenerated = true;
            },
            onAbort: async () => {
                await this.stopAgentSession(activeSession, activeSession.sessionId, activeSession.nextSequence, "client aborted", { drain: "background" });
            },
        });
    }
    async stopAgentSession(session, sessionId, sequence, reason, options = {}) {
        if (sessionId === undefined)
            return Promise.resolve();
        if (session) {
            if (session.stopped)
                return Promise.resolve();
            if (session.startReplyObserved !== true && session.idClientGenerated !== true) {
                session.stopped = true;
                return Promise.resolve();
            }
            session.stopped = true;
        }
        if (session?.unresolvedMessageSequence !== undefined) {
            const preSend = session.unresolvedMessageSequence;
            session.unresolvedMessageSequence = undefined;
            await (0, cancel_helpers_1.stopAgentWithSequenceProbe)(this.transport, sessionId, { preSend, postSend: sequence }, reason);
            if (options.drain === "background") {
                (0, cancel_helpers_1.drainSessionFramesUntilQuiescent)(this.transport, sessionId, 0, 250);
                return;
            }
            await (0, cancel_helpers_1.drainSessionFramesUntilQuiescent)(this.transport, sessionId, 0, 250);
            return;
        }
        const stopMessageId = (0, cancel_helpers_1.bestEffortStopAgent)(this.transport, sessionId, sequence, reason);
        if (options.drain === "quiescent") {
            return (0, cancel_helpers_1.drainSessionFramesUntilQuiescent)(this.transport, sessionId, 50, 250, { stopReplyTo: stopMessageId });
        }
        if (options.drain === "background") {
            (0, cancel_helpers_1.drainSessionFramesUntilQuiescent)(this.transport, sessionId, 0, 250);
        }
        return Promise.resolve();
    }
    rollbackUnresolvedMessage(activeSession) {
        if (activeSession?.unresolvedMessageSequence === undefined)
            return;
        activeSession.nextSequence = activeSession.unresolvedMessageSequence;
        activeSession.unresolvedMessageSequence = undefined;
    }
    abandonForeignAgentSession(activeSession) {
        if (activeSession)
            activeSession.stopped = true;
    }
    async runOnce(request, effectivePolicy, signal, activeSession, idClientGenerated = request.options?.session_id === undefined) {
        (0, abort_signal_1.checkAbort)(signal, "agent.run aborted");
        const sessionId = agentSessionId(request);
        if (activeSession) {
            activeSession.sessionId = sessionId;
            activeSession.nextSequence = 1;
            activeSession.stopped = false;
            activeSession.idClientGenerated = idClientGenerated;
            activeSession.startReplyObserved = false;
        }
        const fallbackProviderId = providerIdFromRequest(request);
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("agent: sending agent_start", { session_id: sessionId, model_ref: request.model_ref });
        }
        const startEnvelope = buildAgentEnvelope("agent_start", sessionId, 1, buildAgentStartPayload(request, sessionId));
        sendOrStreamError(this.transport, startEnvelope);
        const startMessageId = startEnvelope.message_id;
        if (activeSession)
            activeSession.nextSequence = 2;
        const timeoutContext = agentTimeoutContext("agent result", this.responseTimeoutMs, sessionId, request);
        const events = [];
        const toolBuffers = new Map();
        let messageSent = false;
        let messageMessageId;
        let correlateId = startMessageId;
        let startAccepted = false;
        let toolsExecuted = false;
        const teardownSession = (reason, drain = "none") => this.stopAgentSession(activeSession, sessionId, activeSession?.nextSequence ?? 2, reason, { drain });
        try {
            while (true) {
                (0, abort_signal_1.checkAbort)(signal, "agent.run aborted");
                const frame = await (0, abort_signal_1.raceWithAbort)(nextAgentFrame(this.transport, sessionId, timeoutContext, { correlate: correlateId, repliesOnly: !startAccepted, signal }), signal, "agent.run aborted");
                if (frame.type === "ack" || frame.type === "agent_stopped")
                    continue;
                if (!startAccepted && frame.type !== "agent_started" && frame.type !== "nack" && frame.type !== "agent_error") {
                    continue;
                }
                if (frame.type === "nack") {
                    if (!startAccepted && frame.in_reply_to !== undefined && frame.in_reply_to !== startMessageId) {
                        continue;
                    }
                    if (activeSession)
                        activeSession.startReplyObserved = true;
                    if (messageSent && frame.in_reply_to === messageMessageId) {
                        this.rollbackUnresolvedMessage(activeSession);
                    }
                    const error = nackToStreamError(frame, fallbackProviderId);
                    if (!startAccepted && error.code === "agent_busy")
                        this.abandonForeignAgentSession(activeSession);
                    throw error;
                }
                if (frame.type === "agent_error") {
                    if (!startAccepted && frame.in_reply_to !== undefined && frame.in_reply_to !== startMessageId) {
                        continue;
                    }
                    if (activeSession)
                        activeSession.startReplyObserved = true;
                    if (messageSent && frame.in_reply_to === messageMessageId) {
                        this.rollbackUnresolvedMessage(activeSession);
                    }
                    const error = streamErrorFrameToError(frame);
                    if (!startAccepted && error.code === "agent_busy") {
                        this.abandonForeignAgentSession(activeSession);
                    }
                    throw error;
                }
                if (frame.type === "agent_started") {
                    if (!startAccepted && frame.in_reply_to !== undefined && frame.in_reply_to !== startMessageId) {
                        continue;
                    }
                    startAccepted = true;
                    if (activeSession)
                        activeSession.startReplyObserved = true;
                    if (!messageSent) {
                        const messageEnvelope = buildAgentEnvelope("agent_message", sessionId, 2, buildAgentMessagePayload(request, sessionId, effectivePolicy));
                        this.transport.send(messageEnvelope);
                        correlateId = messageEnvelope.message_id;
                        messageMessageId = correlateId;
                        if (activeSession) {
                            activeSession.nextSequence = 3;
                            activeSession.unresolvedMessageSequence = 2;
                        }
                        messageSent = true;
                    }
                    continue;
                }
                if (frame.type === "agent_result") {
                    if (activeSession)
                        activeSession.unresolvedMessageSequence = undefined;
                    const response = responseOrAuthError(parseAgentRunResponse(readJsonStringPayload(frame, "result_json")), fallbackProviderId, { allowAuthRetry: !toolsExecuted });
                    await teardownSession("completed", "quiescent");
                    return response;
                }
                if (frame.type === "result" || frame.type === "complete_response") {
                    if (activeSession)
                        activeSession.unresolvedMessageSequence = undefined;
                    const response = responseOrAuthError(parseCompletionResponse(frame.payload ?? frame), fallbackProviderId, { allowAuthRetry: !toolsExecuted });
                    await teardownSession("completed", "quiescent");
                    return response;
                }
                if (frame.type === "tool_execute") {
                    if (activeSession)
                        activeSession.unresolvedMessageSequence = undefined;
                    this.transport.send(await executeAgentToolFrame(frame, request.tools ?? []));
                    toolsExecuted = true;
                    continue;
                }
                const normalized = normalizeAgentFrame(frame, toolBuffers);
                if (normalized.length === 0) {
                    throw new execution_types_1.MakaiStreamError(`unexpected frame type while awaiting agent result: ${String(frame.type)}`, { kind: "transport_error" });
                }
                if (activeSession)
                    activeSession.unresolvedMessageSequence = undefined;
                for (const event of normalized) {
                    if (event.type === "error") {
                        await teardownSession("completed", "quiescent");
                        throw new execution_types_1.MakaiStreamError(event.message, { kind: "provider_error", code: event.code, provider_id: event.provider_id });
                    }
                    if (event.type === "tool_execution_start" || event.type === "tool_execution_end")
                        toolsExecuted = true;
                    events.push(event);
                    if (event.type === "agent_end") {
                        const response = responseOrAuthError(buildAgentRunResponseFromEvents(events), fallbackProviderId, { allowAuthRetry: !toolsExecuted });
                        await teardownSession("completed", "quiescent");
                        return response;
                    }
                }
            }
        }
        catch (error) {
            if ((0, abort_signal_1.isAbortError)(error)) {
                await teardownSession("client aborted", "background");
            }
            else {
                await teardownSession("completed");
            }
            throw error;
        }
    }
    async *stream(request) {
        const signal = request.options?.signal;
        (0, abort_signal_1.checkAbort)(signal, "agent.stream aborted before start");
        const effectivePolicy = request.options?.auth_retry_policy ?? this.authRetryPolicy;
        const fallbackProviderId = providerIdFromRequest(request);
        let streamRequest = request;
        let attemptIdClientGenerated = request.options?.session_id === undefined;
        const activeSession = { nextSequence: 1 };
        let attempt = this.streamAttempt(streamRequest, effectivePolicy, signal, activeSession, attemptIdClientGenerated);
        let iterator = attempt[Symbol.asyncIterator]();
        let yieldedContent = false;
        let retried = false;
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("agent: starting stream", { model_ref: request.model_ref });
        }
        try {
            while (true) {
                (0, abort_signal_1.checkAbort)(signal, "agent.stream aborted");
                let result;
                try {
                    result = await (0, abort_signal_1.raceWithAbort)(iterator.next(), signal, "agent.stream aborted");
                }
                catch (error) {
                    if ((0, abort_signal_1.isAbortError)(error)) {
                        throw error;
                    }
                    if (!yieldedContent && !retried && isRetryableAuthError(error) && effectivePolicy === "auto_once" && this.auth) {
                        const providerId = error.provider_id ?? fallbackProviderId;
                        if (providerId) {
                            try {
                                await (0, abort_signal_1.raceWithAbort)(this.auth.login(providerId, this.authHandlers, { signal }), signal, "agent.stream aborted during auth retry");
                            }
                            catch (authError) {
                                if ((0, abort_signal_1.isAbortError)(authError)) {
                                    throw authError;
                                }
                                throw authRequiredError(providerId, error.message);
                            }
                            retried = true;
                            this.stopAgentSession(activeSession, activeSession.sessionId, activeSession.nextSequence, "completed");
                            streamRequest = {
                                ...request,
                                options: { ...request.options, session_id: generateNanoId() },
                            };
                            attemptIdClientGenerated = true;
                            attempt = this.streamAttempt(streamRequest, effectivePolicy, signal, activeSession, attemptIdClientGenerated);
                            iterator = attempt[Symbol.asyncIterator]();
                            continue;
                        }
                    }
                    if (isRetryableAuthError(error)) {
                        const providerId = error.provider_id ?? fallbackProviderId;
                        if (providerId)
                            throw authRequiredError(providerId, error.message);
                    }
                    throw error;
                }
                if (result.done)
                    return;
                if (!isReplayableAgentEvent(result.value))
                    yieldedContent = true;
                yield result.value;
            }
        }
        catch (error) {
            if ((0, abort_signal_1.isAbortError)(error)) {
                await this.stopAgentSession(activeSession, activeSession.sessionId, activeSession.nextSequence, "client aborted", { drain: "background" });
            }
            throw error;
        }
        finally {
            const closed = iterator.return?.();
            if (!signal?.aborted && closed)
                await closed.catch(() => undefined);
        }
    }
    async *streamAttempt(request, effectivePolicy, signal, activeSession, idClientGenerated = request.options?.session_id === undefined) {
        (0, abort_signal_1.checkAbort)(signal, "agent.stream aborted");
        const sessionId = agentSessionId(request);
        if (activeSession) {
            activeSession.sessionId = sessionId;
            activeSession.nextSequence = 1;
            activeSession.stopped = false;
            activeSession.idClientGenerated = idClientGenerated;
            activeSession.startReplyObserved = false;
        }
        const fallbackProviderId = providerIdFromRequest(request);
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("agent: sending agent_start", { session_id: sessionId, model_ref: request.model_ref });
        }
        const startEnvelope = buildAgentEnvelope("agent_start", sessionId, 1, buildAgentStartPayload(request, sessionId));
        sendOrStreamError(this.transport, startEnvelope);
        const startMessageId = startEnvelope.message_id;
        if (activeSession)
            activeSession.nextSequence = 2;
        const timeoutContext = agentTimeoutContext("agent stream event", this.responseTimeoutMs, sessionId, request);
        let terminal = false;
        let messageSent = false;
        let started = false;
        let startAccepted = false;
        let messageMessageId;
        let correlateId = startMessageId;
        let threw = false;
        let aggregateUsage;
        const toolBuffers = new Map();
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("agent: stream started", { session_id: sessionId });
        }
        try {
            while (!terminal) {
                (0, abort_signal_1.checkAbort)(signal, "agent.stream aborted");
                const frame = await (0, abort_signal_1.raceWithAbort)(nextAgentFrame(this.transport, sessionId, timeoutContext, { correlate: correlateId, repliesOnly: !startAccepted, signal }), signal, "agent.stream aborted");
                if (frame.type === "ack" || frame.type === "agent_stopped")
                    continue;
                if (!startAccepted && frame.type !== "agent_started" && frame.type !== "nack" && frame.type !== "agent_error") {
                    continue;
                }
                if (frame.type === "nack") {
                    if (!startAccepted && frame.in_reply_to !== undefined && frame.in_reply_to !== startMessageId) {
                        continue;
                    }
                    if (activeSession)
                        activeSession.startReplyObserved = true;
                    if (messageSent && frame.in_reply_to === messageMessageId) {
                        this.rollbackUnresolvedMessage(activeSession);
                    }
                    const error = nackToStreamError(frame, fallbackProviderId);
                    if (!startAccepted && error.code === "agent_busy")
                        this.abandonForeignAgentSession(activeSession);
                    throw error;
                }
                if (frame.type === "agent_error" && !startAccepted) {
                    if (frame.in_reply_to !== undefined && frame.in_reply_to !== startMessageId) {
                        continue;
                    }
                    if (activeSession)
                        activeSession.startReplyObserved = true;
                    if (streamErrorFrameToError(frame).code === "agent_busy") {
                        this.abandonForeignAgentSession(activeSession);
                    }
                }
                if (messageSent && frame.type === "agent_error" && frame.in_reply_to === messageMessageId) {
                    this.rollbackUnresolvedMessage(activeSession);
                }
                else if (messageSent && activeSession?.unresolvedMessageSequence !== undefined
                    && frame.type !== "agent_started" && frame.type !== "ack" && frame.type !== "agent_stopped"
                    && frame.type !== "agent_error") {
                    activeSession.unresolvedMessageSequence = undefined;
                }
                if (frame.type === "agent_started" && !messageSent) {
                    if (frame.in_reply_to !== undefined && frame.in_reply_to !== startMessageId) {
                        continue;
                    }
                    startAccepted = true;
                    if (activeSession)
                        activeSession.startReplyObserved = true;
                    const messageEnvelope = buildAgentEnvelope("agent_message", sessionId, 2, buildAgentMessagePayload(request, sessionId, effectivePolicy));
                    this.transport.send(messageEnvelope);
                    correlateId = messageEnvelope.message_id;
                    messageMessageId = correlateId;
                    if (activeSession) {
                        activeSession.nextSequence = 3;
                        activeSession.unresolvedMessageSequence = 2;
                    }
                    messageSent = true;
                    continue;
                }
                if (frame.type === "tool_execute") {
                    if (activeSession)
                        activeSession.unresolvedMessageSequence = undefined;
                    this.transport.send(await executeAgentToolFrame(frame, request.tools ?? []));
                    continue;
                }
                const events = normalizeAgentFrame(frame, toolBuffers);
                if (events.length > 0 && activeSession?.unresolvedMessageSequence !== undefined && frame.type !== "agent_error") {
                    activeSession.unresolvedMessageSequence = undefined;
                }
                for (const rawEvent of events) {
                    let event = rawEvent;
                    if (event.type === "error" && event.code === "auth_required") {
                        terminal = true;
                        throw new execution_types_1.MakaiStreamError(event.message, { kind: "provider_error", code: event.code, provider_id: event.provider_id });
                    }
                    if (!started) {
                        started = true;
                        if (event.type !== "agent_start") {
                            yield { type: "agent_start", session_id: sessionId };
                        }
                    }
                    if (event.type === "message_end" && event.usage) {
                        aggregateUsage = aggregateUsage ? addUsage(aggregateUsage, event.usage) : event.usage;
                    }
                    if (event.type === "agent_end") {
                        const usage = aggregateUsage ?? event.usage;
                        event = usage ? { ...event, usage } : { ...event };
                        if (!usage)
                            delete event.usage;
                        terminal = true;
                        if (event.stop_reason === "error" && isAuthFailureMessage(event.error_message, { providerId: event.provider_id, api: event.api })) {
                            const resolvedProviderId = event.provider_id ?? fallbackProviderId;
                            throw new execution_types_1.MakaiStreamError(event.error_message ?? "auth_required", {
                                kind: "provider_error",
                                code: "auth_required",
                                ...(resolvedProviderId ? { provider_id: resolvedProviderId } : {}),
                            });
                        }
                    }
                    else if (event.type === "error") {
                        terminal = true;
                    }
                    yield event;
                    if (terminal)
                        break;
                }
            }
        }
        catch (error) {
            threw = true;
            if (error instanceof execution_types_1.MakaiStreamError) {
                this.logger.error("agent: stream error", { kind: error.kind, code: error.code, message: error.message });
                await this.stopAgentSession(activeSession, sessionId, activeSession?.nextSequence ?? 2, "completed");
                throw error;
            }
            if ((0, abort_signal_1.isAbortError)(error)) {
                throw error;
            }
            this.logger.error("agent: unexpected stream error", { error: error instanceof Error ? error.message : String(error) });
            await this.stopAgentSession(activeSession, sessionId, activeSession?.nextSequence ?? 2, "completed");
            throw new execution_types_1.MakaiStreamError(error instanceof Error ? error.message : String(error), { kind: "transport_error" });
        }
        finally {
            if (!threw) {
                await this.stopAgentSession(activeSession, sessionId, activeSession?.nextSequence ?? 2, "completed", { drain: "quiescent" });
            }
        }
    }
}
function sendOrStreamError(transport, envelope) {
    try {
        transport.send(envelope);
    }
    catch (error) {
        if (error instanceof execution_types_1.MakaiStreamError)
            throw error;
        throw new execution_types_1.MakaiStreamError(error instanceof Error ? error.message : String(error), { kind: "transport_error" });
    }
}
function buildEnvelope(type, streamId, payload) {
    return {
        type,
        stream_id: streamId,
        message_id: streamId,
        sequence: 1,
        timestamp: Date.now(),
        version: ENVELOPE_VERSION,
        payload,
    };
}
function buildAgentEnvelope(type, sessionId, sequence, payload) {
    return {
        type,
        session_id: sessionId,
        message_id: (0, ulid_1.ulid)(),
        sequence,
        timestamp: Date.now(),
        version: ENVELOPE_VERSION,
        payload,
    };
}
function buildAgentReplyEnvelope(type, requestFrame, payload) {
    return {
        type,
        session_id: requestFrame.session_id,
        message_id: (0, ulid_1.ulid)(),
        sequence: numericValue(requestFrame.sequence, 0) + 1,
        timestamp: Date.now(),
        version: ENVELOPE_VERSION,
        in_reply_to: requestFrame.message_id,
        payload,
    };
}
async function executeAgentToolFrame(frame, tools) {
    const payload = readPayloadOrFrame(frame);
    const toolCallId = stringValue(payload.tool_call_id);
    const toolName = stringValue(payload.tool_name);
    const argsJson = stringValue(payload.args_json);
    const tool = tools.find((candidate) => candidate.name === toolName);
    if (!tool?.execute) {
        return buildToolResultEnvelope(frame, toolCallId, `Tool '${toolName}' is not executable by this client`, true);
    }
    try {
        const args = parseToolArguments(argsJson);
        const result = await tool.execute(args, { tool_call_id: toolCallId, tool_name: toolName, args_json: argsJson });
        return buildToolResultEnvelope(frame, toolCallId, result, false);
    }
    catch (error) {
        return buildToolResultEnvelope(frame, toolCallId, error instanceof Error ? error.message : String(error), true);
    }
}
function buildToolResultEnvelope(frame, toolCallId, result, isError) {
    return buildAgentReplyEnvelope("tool_result", frame, {
        tool_call_id: toolCallId,
        result_json: serializeToolResultContent(result),
        is_error: isError,
    });
}
function parseToolArguments(argsJson) {
    const parsed = argsJson.length > 0 ? JSON.parse(argsJson) : {};
    if (!isObject(parsed))
        throw new Error("tool arguments must be a JSON object");
    return parsed;
}
function serializeToolResultContent(result) {
    if (typeof result === "string")
        return JSON.stringify([{ type: "text", text: result }]);
    return JSON.stringify(result);
}
function buildExecutionPayload(request, options = {}) {
    validateExecutionRequest(request);
    const model = modelFromRef(request.model_ref);
    const payload = {
        model,
        context: executionContext(request),
        model_ref: request.model_ref,
    };
    const serializedOptions = serializeOptionsWithDefaults(request.options, options.authRetryPolicy);
    if (Object.keys(serializedOptions).length > 0)
        payload.options = serializedOptions;
    if (options.suppressPartial)
        payload.include_partial = false;
    return payload;
}
function buildAgentStartPayload(request, sessionId) {
    validateExecutionRequest(request);
    return {
        session_id: sessionId,
        resume_session_id: sessionId,
        config_json: JSON.stringify({ model_ref: request.model_ref, tools: request.tools ?? [] }),
    };
}
function buildAgentMessagePayload(request, sessionId, authRetryPolicy) {
    const payload = {
        session_id: sessionId,
        message_json: JSON.stringify({ model_ref: request.model_ref, messages: request.messages, tools: request.tools ?? [] }),
    };
    const serializedOptions = serializeOptionsWithDefaults(request.options, authRetryPolicy);
    if (Object.keys(serializedOptions).length > 0)
        payload.options_json = JSON.stringify(serializedOptions);
    return payload;
}
const MAX_MODEL_REF_LENGTH = 4096;
const MAX_IDENTIFIER_LENGTH = 256;
const MAX_MODEL_FIELD_LENGTH = 512;
function validateExecutionRequest(request) {
    if (!request || typeof request.model_ref !== "string" || request.model_ref.length === 0) {
        throw new TypeError("request requires opaque model_ref");
    }
    if (request.model_ref.length > MAX_MODEL_REF_LENGTH) {
        throw new models_types_1.MakaiProtocolError(`model_ref exceeds maximum length of ${MAX_MODEL_REF_LENGTH} characters`, "invalid_request");
    }
    validateModelRefSegments(request.model_ref);
    if (!Array.isArray(request.messages)) {
        throw new TypeError("request requires messages array");
    }
}
function validateModelRefSegments(modelRef) {
    try {
        const parsed = (0, model_ref_1.parseModelRef)(modelRef);
        validateModelSegments(parsed.providerId, parsed.api, parsed.modelId);
        return;
    }
    catch {
    }
    const slashIndex = modelRef.indexOf("/");
    const atIndex = modelRef.indexOf("@");
    if (slashIndex !== -1 && atIndex !== -1 && slashIndex < atIndex) {
        const provider = modelRef.slice(0, slashIndex);
        const api = modelRef.slice(slashIndex + 1, atIndex);
        const id = modelRef.slice(atIndex + 1);
        if (provider.length > 0 && api.length > 0) {
            validateModelSegments(provider, api, id);
            return;
        }
    }
    if (modelRef.length > MAX_MODEL_FIELD_LENGTH) {
        throw new models_types_1.MakaiProtocolError(`model_ref exceeds maximum length of ${MAX_MODEL_FIELD_LENGTH} characters for opaque refs`, "invalid_request");
    }
}
function modelFromRef(modelRef) {
    try {
        const parsed = (0, model_ref_1.parseModelRef)(modelRef);
        validateModelSegments(parsed.providerId, parsed.api, parsed.modelId);
        return {
            id: parsed.modelId,
            name: parsed.modelId,
            api: parsed.api,
            provider: parsed.providerId,
            base_url: "",
        };
    }
    catch {
        const slashIndex = modelRef.indexOf("/");
        const atIndex = modelRef.indexOf("@");
        if (slashIndex !== -1 && atIndex !== -1 && slashIndex < atIndex) {
            const provider = modelRef.slice(0, slashIndex);
            const api = modelRef.slice(slashIndex + 1, atIndex);
            const id = modelRef.slice(atIndex + 1);
            if (provider.length > 0 && api.length > 0) {
                validateModelSegments(provider, api, id);
                return {
                    id,
                    name: id,
                    api,
                    provider,
                    base_url: "",
                };
            }
        }
        return opaqueModel(modelRef);
    }
}
function validateModelSegments(provider, api, id) {
    if (provider.length > MAX_IDENTIFIER_LENGTH) {
        throw new models_types_1.MakaiProtocolError(`model_ref provider segment exceeds maximum length of ${MAX_IDENTIFIER_LENGTH} characters`, "invalid_request");
    }
    if (api.length > MAX_IDENTIFIER_LENGTH) {
        throw new models_types_1.MakaiProtocolError(`model_ref api segment exceeds maximum length of ${MAX_IDENTIFIER_LENGTH} characters`, "invalid_request");
    }
    if (id.length > MAX_MODEL_FIELD_LENGTH) {
        throw new models_types_1.MakaiProtocolError(`model_ref model_id segment exceeds maximum length of ${MAX_MODEL_FIELD_LENGTH} characters`, "invalid_request");
    }
}
function opaqueModel(modelRef) {
    return {
        id: modelRef,
        name: modelRef,
        api: "",
        provider: "",
        base_url: "",
    };
}
function executionContext(request) {
    const messages = [];
    const systemPrompts = [];
    for (const message of request.messages) {
        if (message.role === "system" || message.role === "developer") {
            systemPrompts.push(contentAsPromptText(message.content));
        }
        else {
            messages.push(serializeChatMessage(message));
        }
    }
    const context = { messages };
    if (systemPrompts.length > 0)
        context.system_prompt = systemPrompts.join("\n\n");
    if (request.tools)
        context.tools = request.tools.map(serializeTool);
    return context;
}
function agentSessionId(request) {
    const sessionId = request.options?.session_id;
    if (sessionId === undefined)
        return generateNanoId();
    if (!isNanoId(sessionId)) {
        throw new TypeError("request.options.session_id must be a 21-character alphanumeric NanoID for agent transport");
    }
    return sessionId;
}
function isNanoId(value) {
    return /^[0-9A-Za-z]{21}$/.test(value);
}
function serializeChatMessage(message) {
    const out = { role: message.role, content: serializeMessageContent(message) };
    if (message.name)
        out.name = message.name;
    if (message.role === "tool")
        out.tool_name = message.name ?? "";
    if (message.tool_call_id)
        out.tool_call_id = message.tool_call_id;
    return out;
}
function serializeMessageContent(message) {
    if (message.role === "tool")
        return contentAsUserParts(message.content);
    return message.content;
}
function contentAsPromptText(content) {
    if (typeof content === "string")
        return content;
    return content.map(contentPartText).filter((part) => part.length > 0).join("\n");
}
function contentPartText(part) {
    if (part.type === "text")
        return part.text;
    if (part.type === "thinking")
        return part.thinking;
    if (part.type === "tool_result")
        return typeof part.content === "string" ? part.content : contentAsPromptText(part.content);
    return "";
}
function contentAsUserParts(content) {
    if (typeof content === "string")
        return [{ type: "text", text: content }];
    return content;
}
function serializeTool(tool) {
    return {
        name: tool.name,
        description: tool.description,
        parameters_schema_json: tool.parameters_schema_json,
    };
}
function serializeOptionsWithDefaults(options, authRetryPolicy) {
    const out = {};
    if (authRetryPolicy !== undefined)
        out.auth_retry_policy = authRetryPolicy;
    if (!options)
        return out;
    for (const key of ["temperature", "max_tokens", "reasoning_effort", "auth_retry_policy", "session_id", "metadata"]) {
        if (options[key] !== undefined)
            out[key] = options[key];
    }
    return out;
}
async function nextFrame(transport, streamId, context) {
    try {
        return await transport.nextFrameForStream(streamId, context.timeout_ms);
    }
    catch (error) {
        throw timeoutAwareStreamError(error, context);
    }
}
async function nextAgentFrame(transport, sessionId, context, wait) {
    try {
        return await transport.nextFrameForSession(sessionId, context.timeout_ms, wait);
    }
    catch (error) {
        throw timeoutAwareStreamError(error, context);
    }
}
function timeoutAwareStreamError(error, context) {
    if ((0, timeout_diagnostics_1.isTimeoutLikeError)(error)) {
        return new execution_types_1.MakaiStreamError((0, timeout_diagnostics_1.formatTimeoutMessage)(context), {
            kind: "transport_error",
            diagnostics: (0, timeout_diagnostics_1.createTimeoutDiagnostics)(context),
        });
    }
    return new execution_types_1.MakaiStreamError(error instanceof Error ? error.message : String(error), { kind: "transport_error" });
}
function executionTimeoutContext(operation, timeoutMs, streamId, request) {
    const parsed = safeParseModelRef(request.model_ref);
    return {
        operation,
        timeout_ms: timeoutMs,
        stream_id: streamId,
        message_id: streamId,
        provider_id: parsed?.providerId,
        api: parsed?.api,
        model_ref: request.model_ref,
        model_id: parsed?.modelId,
    };
}
function agentTimeoutContext(operation, timeoutMs, sessionId, request) {
    const parsed = safeParseModelRef(request.model_ref);
    return {
        operation,
        timeout_ms: timeoutMs,
        session_id: sessionId,
        provider_id: parsed?.providerId,
        api: parsed?.api,
        model_ref: request.model_ref,
        model_id: parsed?.modelId,
    };
}
function safeParseModelRef(modelRef) {
    try {
        return (0, model_ref_1.parseModelRef)(modelRef);
    }
    catch {
        return undefined;
    }
}
function normalizeProviderFrame(frame, toolBuffers) {
    if (frame.type === "event") {
        const payload = readPayloadOrFrame(frame);
        return normalizeProviderEvent(payload.event && isObject(payload.event) ? payload.event : payload, toolBuffers);
    }
    if (frame.type === "stream_error")
        return parseError(readPayloadOrFrame(frame));
    if (frame.type === "start" || frame.type === "message_start")
        return messageStartFrom(readPayloadOrFrame(frame));
    if (frame.type === "text_delta")
        return { type: "text_delta", delta: stringValue(readPayloadOrFrame(frame).delta) };
    if (frame.type === "thinking_delta" || frame.type === "reasoning_delta" || frame.type === "reasoning") {
        return { type: "thinking_delta", delta: stringValue(readPayloadOrFrame(frame).delta ?? readPayloadOrFrame(frame).reasoning) };
    }
    if (frame.type === "tool_call")
        return parseToolCall(readPayloadOrFrame(frame));
    if (frame.type === "toolcall_start" || frame.type === "toolcall_delta" || frame.type === "toolcall_end") {
        return normalizeProviderEvent(readPayloadOrFrame(frame), toolBuffers);
    }
    if (frame.type === "message_end" || frame.type === "done" || frame.type === "result")
        return messageEndFrom(readPayloadOrFrame(frame));
    if (frame.type === "error")
        return parseError(readPayloadOrFrame(frame));
    return undefined;
}
function normalizeProviderEvent(payload, toolBuffers) {
    const type = stringValue(payload.type === "event" ? payload.event_type : payload.type ?? payload.event_type);
    if (type === "start")
        return messageStartFrom(payload.message && isObject(payload.message) ? payload.message : payload);
    if (type === "text_delta")
        return { type: "text_delta", delta: stringValue(payload.delta) };
    if (type === "thinking_delta" || type === "reasoning_delta" || type === "reasoning") {
        return { type: "thinking_delta", delta: stringValue(payload.delta ?? payload.reasoning) };
    }
    if (type === "toolcall_start") {
        const index = numericValue(payload.content_index, toolBuffers.size);
        toolBuffers.set(index, { id: optionalString(payload.id), name: optionalString(payload.name), args: "" });
        return undefined;
    }
    if (type === "toolcall_delta") {
        const index = numericValue(payload.content_index, 0);
        const existing = toolBuffers.get(index) ?? { args: "" };
        existing.args += stringValue(payload.delta);
        toolBuffers.set(index, existing);
        return undefined;
    }
    if (type === "toolcall_end")
        return parseBufferedToolCall(payload, toolBuffers);
    if (type === "tool_call")
        return parseToolCall(payload);
    if (type === "done" || type === "message_end")
        return messageEndFrom(payload);
    if (type === "stream_error" || type === "error")
        return parseError(payload);
    return undefined;
}
function normalizeAgentFrame(frame, toolBuffers) {
    if (frame.type === "agent_result")
        return [agentEndFrom(readJsonStringPayload(frame, "result_json"))];
    if (frame.type === "agent_error")
        return [parseError(readPayloadOrFrame(frame))];
    if (frame.type === "agent_event")
        return normalizeAgentEvent(readJsonStringPayload(frame, "event_json"), toolBuffers);
    if (frame.type === "event") {
        const payload = eventEnvelopePayload(frame);
        const inner = payload.event && isObject(payload.event) ? payload.event : payload;
        const type = eventKind(inner);
        if (isAgentEventType(type))
            return normalizeAgentEvent({ ...inner, type }, toolBuffers);
    }
    const providerEvent = normalizeProviderFrame(frame, toolBuffers);
    return providerEvent ? [providerEvent] : [];
}
function normalizeAgentEvent(event, toolBuffers) {
    const type = eventKind(event);
    const data = event.type || event.event_type ? event : (event[type] && isObject(event[type]) ? event[type] : event);
    switch (type) {
        case "agent_start":
            return [{ type: "agent_start", ...(optionalString(data.session_id) ? { session_id: optionalString(data.session_id) } : {}) }];
        case "turn_start":
            return [{ type: "turn_start" }];
        case "turn_end":
            return [{
                    type: "turn_end",
                    ...(optionalString(data.stop_reason) ? { stop_reason: optionalString(data.stop_reason) } : {}),
                    ...(optionalString(data.error_message) ? { error_message: optionalString(data.error_message) } : {}),
                }];
        case "tool_execution_start":
            return [{ type: "tool_execution_start", tool_call_id: stringValue(data.tool_call_id), tool_name: stringValue(data.tool_name) }];
        case "tool_execution_end":
            return [{ type: "tool_execution_end", tool_call_id: stringValue(data.tool_call_id), ...(typeof data.is_error === "boolean" ? { is_error: data.is_error } : {}) }];
        case "tool_execution_update":
            return [];
        case "agent_end":
            return [agentEndFrom(data)];
        case "message_start":
            return [messageStartFrom(data.message && isObject(data.message) ? data.message : data)];
        case "message_update": {
            const inner = data.event && isObject(data.event) ? data.event : data;
            const providerEvent = normalizeProviderEvent(inner, toolBuffers);
            return providerEvent ? [providerEvent] : [];
        }
        case "text_delta":
        case "thinking_delta":
        case "reasoning_delta":
        case "reasoning":
        case "toolcall_start":
        case "toolcall_delta":
        case "toolcall_end":
        case "tool_call": {
            const providerEvent = normalizeProviderEvent(event, toolBuffers);
            return providerEvent ? [providerEvent] : [];
        }
        case "message_end":
            return [messageEndFrom(data.message && isObject(data.message) ? data.message : data)];
        case "error":
            return [parseError(data)];
        default:
            return [];
    }
}
function parseCompletionResponse(raw) {
    const data = isObject(raw) ? raw : {};
    const message = data.message && isObject(data.message) ? data.message : data;
    const content = parseContent(message.content);
    return {
        message: { role: "assistant", content },
        usage: parseUsage(message.usage ?? data.usage ?? data),
        provider_id: stringValue(message.provider_id ?? message.provider ?? data.provider_id ?? data.provider),
        api: stringValue(message.api ?? data.api),
        model_id: stringValue(message.model_id ?? message.model ?? data.model_id ?? data.model),
        stop_reason: optionalString(message.stop_reason ?? data.stop_reason ?? data.reason),
        error_message: optionalString(message.error_message ?? data.error_message),
    };
}
function parseAgentRunResponse(raw) {
    const data = isObject(raw) ? raw : {};
    if (data.message && isObject(data.message))
        return parseCompletionResponse(data);
    const messages = Array.isArray(data.messages) ? data.messages.filter(isObject) : [];
    const assistantMessage = [...messages].reverse().find((message) => message.role === "assistant") ?? data;
    const terminal = data.result && isObject(data.result) ? data.result : data;
    return buildCompletionResponseFromMessage(assistantMessage, terminal);
}
function buildAgentRunResponseFromEvents(events) {
    const reversed = [...events].reverse();
    const terminal = reversed.find((event) => event.type === "message_end" || event.type === "agent_end");
    const messageEnd = reversed.find((event) => event.type === "message_end");
    const finalMessageEvents = finalAssistantMessageEvents(events);
    const start = finalMessageEvents.find((event) => event.type === "message_start");
    const terminalProviderId = terminal && "provider_id" in terminal && typeof terminal.provider_id === "string" ? terminal.provider_id : undefined;
    const terminalApi = terminal && "api" in terminal && typeof terminal.api === "string" ? terminal.api : undefined;
    const content = contentFromEvents(finalMessageEvents);
    return {
        message: { role: "assistant", content },
        usage: (terminal && "usage" in terminal ? terminal.usage : undefined) ?? messageEnd?.usage,
        provider_id: start?.provider_id ?? terminalProviderId ?? "",
        api: start?.api ?? terminalApi ?? "",
        model_id: start?.model_id ?? "",
        stop_reason: terminal && "stop_reason" in terminal ? terminal.stop_reason : undefined,
        error_message: terminal && "error_message" in terminal ? terminal.error_message : undefined,
    };
}
function finalAssistantMessageEvents(events) {
    const startIndex = events.map((event) => event.type).lastIndexOf("message_start");
    if (startIndex < 0)
        return events;
    const endOffset = events.slice(startIndex + 1).findIndex((event) => event.type === "message_end");
    const endIndex = endOffset < 0 ? events.length : startIndex + 1 + endOffset + 1;
    return events.slice(startIndex, endIndex);
}
function buildCompletionResponseFromMessage(message, terminal) {
    return {
        message: { role: "assistant", content: parseContent(message.content) },
        usage: parseUsage(message.usage ?? terminal.usage ?? terminal),
        provider_id: stringValue(message.provider_id ?? message.provider ?? terminal.provider_id ?? terminal.provider),
        api: stringValue(message.api ?? terminal.api),
        model_id: stringValue(message.model_id ?? message.model ?? terminal.model_id ?? terminal.model),
        stop_reason: optionalString(message.stop_reason ?? terminal.stop_reason ?? terminal.reason),
        error_message: optionalString(message.error_message ?? terminal.error_message),
    };
}
function contentFromEvents(events) {
    const parts = [];
    let text = "";
    for (const event of events) {
        if (event.type === "text_delta") {
            text += event.delta;
            continue;
        }
        if (event.type === "thinking_delta") {
            if (text.length > 0) {
                parts.push({ type: "text", text });
                text = "";
            }
            parts.push({ type: "thinking", thinking: event.delta });
            continue;
        }
        if (event.type === "tool_call") {
            if (text.length > 0) {
                parts.push({ type: "text", text });
                text = "";
            }
            parts.push({
                type: "tool_call",
                tool_call_id: event.tool_call_id,
                name: event.name,
                arguments_json: event.arguments_json,
            });
        }
    }
    if (parts.length === 0)
        return text;
    if (text.length > 0)
        parts.push({ type: "text", text });
    return parts;
}
function parseContent(raw) {
    if (typeof raw === "string")
        return raw;
    if (!Array.isArray(raw))
        return "";
    return raw.map((part) => {
        if (!isObject(part))
            return { type: "text", text: String(part) };
        if (part.type === "tool_call")
            return { type: "tool_call", tool_call_id: stringValue(part.tool_call_id ?? part.id), name: stringValue(part.name), arguments_json: stringValue(part.arguments_json) };
        return part;
    });
}
function messageStartFrom(data) {
    return {
        type: "message_start",
        ...(optionalString(data.provider_id ?? data.provider) ? { provider_id: optionalString(data.provider_id ?? data.provider) } : {}),
        ...(optionalString(data.api) ? { api: optionalString(data.api) } : {}),
        ...(optionalString(data.model_id ?? data.model) ? { model_id: optionalString(data.model_id ?? data.model) } : {}),
    };
}
function messageEndFrom(data) {
    const message = data.message && isObject(data.message) ? data.message : data;
    return {
        type: "message_end",
        usage: parseUsage(message.usage ?? data.usage ?? message),
        stop_reason: optionalString(data.stop_reason ?? data.reason ?? message.stop_reason),
        ...(optionalString(data.error_message ?? message.error_message) ? { error_message: optionalString(data.error_message ?? message.error_message) } : {}),
    };
}
function agentEndFrom(data) {
    const usage = parseUsage(data.usage ?? data);
    return {
        type: "agent_end",
        ...(usage ? { usage } : {}),
        ...(optionalString(data.stop_reason ?? data.reason) ? { stop_reason: optionalString(data.stop_reason ?? data.reason) } : {}),
        ...(optionalString(data.error_message) ? { error_message: optionalString(data.error_message) } : {}),
        ...(optionalString(data.provider_id ?? data.provider) ? { provider_id: optionalString(data.provider_id ?? data.provider) } : {}),
        ...(optionalString(data.api) ? { api: optionalString(data.api) } : {}),
    };
}
function parseToolCall(data) {
    return { type: "tool_call", tool_call_id: stringValue(data.tool_call_id ?? data.id), name: stringValue(data.name), arguments_json: stringValue(data.arguments_json) };
}
function parseBufferedToolCall(data, toolBuffers) {
    const index = numericValue(data.content_index, 0);
    const buffered = toolBuffers.get(index);
    toolBuffers.delete(index);
    return { type: "tool_call", tool_call_id: stringValue(data.tool_call_id ?? data.id ?? buffered?.id), name: stringValue(data.name ?? buffered?.name), arguments_json: stringValue(data.arguments_json ?? buffered?.args) };
}
function parseError(data) {
    return {
        type: "error",
        message: stringValue(data.message ?? data.error_message ?? data.reason, "stream error"),
        code: optionalString(data.code ?? data.error_code),
        ...(optionalString(data.provider_id) ? { provider_id: optionalString(data.provider_id) } : {}),
    };
}
function parseUsage(raw) {
    if (!isObject(raw))
        return undefined;
    const input = raw.input ?? raw.input_tokens;
    const output = raw.output ?? raw.output_tokens;
    if (typeof input !== "number" || typeof output !== "number")
        return undefined;
    const usage = { input, output };
    if (typeof raw.cache_read === "number")
        usage.cache_read = raw.cache_read;
    if (typeof raw.cache_write === "number")
        usage.cache_write = raw.cache_write;
    return usage;
}
function addUsage(left, right) {
    const usage = {
        input: left.input + right.input,
        output: left.output + right.output,
    };
    if (left.cache_read !== undefined || right.cache_read !== undefined) {
        usage.cache_read = (left.cache_read ?? 0) + (right.cache_read ?? 0);
    }
    if (left.cache_write !== undefined || right.cache_write !== undefined) {
        usage.cache_write = (left.cache_write ?? 0) + (right.cache_write ?? 0);
    }
    return usage;
}
function nackToStreamError(frame, fallbackProviderId) {
    const payload = readPayloadOrFrame(frame);
    const code = optionalString(payload.error_code);
    let providerId = optionalString(payload.provider_id);
    if (!providerId && code === "auth_required") {
        providerId = fallbackProviderId;
    }
    return new execution_types_1.MakaiStreamError(stringValue(payload.reason, "request rejected"), {
        kind: "provider_error",
        code,
        provider_id: providerId,
    });
}
function streamErrorFrameToError(frame) {
    const payload = readPayloadOrFrame(frame);
    return new execution_types_1.MakaiStreamError(stringValue(payload.message ?? payload.reason, "stream error"), {
        kind: "provider_error",
        code: optionalString(payload.code ?? payload.error_code),
        provider_id: optionalString(payload.provider_id),
    });
}
function readPayloadOrFrame(frame) {
    return frame.payload && isObject(frame.payload) ? frame.payload : frame;
}
function eventEnvelopePayload(frame) {
    const payload = readPayloadOrFrame(frame);
    if (payload === frame)
        return payload;
    return { ...frame, ...payload };
}
function readJsonStringPayload(frame, key) {
    const payload = readPayloadOrFrame(frame);
    const json = payload[key] ?? payload.event_json ?? payload.result_json;
    if (typeof json === "string") {
        try {
            const parsed = JSON.parse(json);
            return isObject(parsed) ? parsed : {};
        }
        catch {
            throw new execution_types_1.MakaiStreamError(`malformed JSON in ${key}`, { kind: "transport_error" });
        }
    }
    return payload;
}
function eventKind(event) {
    const explicitType = optionalString(event.type);
    const eventType = optionalString(event.event_type);
    return explicitType === "event" && eventType ? eventType : (explicitType ?? eventType ?? firstKey(event));
}
function isAgentEventType(type) {
    return type === "agent_start" ||
        type === "agent_end" ||
        type === "turn_start" ||
        type === "turn_end" ||
        type === "tool_execution_start" ||
        type === "tool_execution_end" ||
        type === "tool_execution_update";
}
function stringValue(value, fallback = "") {
    return typeof value === "string" ? value : fallback;
}
function optionalString(value) {
    return typeof value === "string" && value.length > 0 ? value : undefined;
}
function numericValue(value, fallback) {
    return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}
function firstKey(value) {
    return Object.keys(value)[0] ?? "";
}
function isObject(value) {
    return typeof value === "object" && value !== null && !Array.isArray(value);
}
function isRetryableAuthError(error) {
    return error instanceof execution_types_1.MakaiStreamError && !(error instanceof execution_types_1.MakaiAuthRequiredError) && error.code === "auth_required";
}
function authRequiredError(providerId, message) {
    return new execution_types_1.MakaiAuthRequiredError(providerId, message);
}
function isReplayableAgentEvent(event) {
    return event.type === "agent_start" ||
        event.type === "turn_start" ||
        event.type === "turn_end" ||
        event.type === "message_start" ||
        event.type === "message_end";
}
function isAuthFailureMessage(message, identity) {
    if (!message)
        return false;
    const normalized = message.toLowerCase();
    if (normalized === "auth_required" ||
        normalized === "auth_expired" ||
        normalized === "auth_refresh_failed" ||
        normalized.includes("authentication required") ||
        normalized.includes("401") ||
        normalized.includes("403") ||
        normalized.includes("unauthorized") ||
        normalized.includes("forbidden")) {
        return true;
    }
    return identity?.api === "anthropic-messages" && (normalized.includes("authentication_error") ||
        normalized.includes("permission_error") ||
        normalized.includes("invalid api key"));
}
function responseOrAuthError(response, providerId, options) {
    if (response.stop_reason === "error" && isAuthFailureMessage(response.error_message, { providerId: response.provider_id, api: response.api })) {
        const resolvedProviderId = response.provider_id || providerId;
        const message = response.error_message ?? "auth_required";
        if (options.allowAuthRetry) {
            throw new execution_types_1.MakaiStreamError(message, {
                kind: "provider_error",
                code: "auth_required",
                ...(resolvedProviderId ? { provider_id: resolvedProviderId } : {}),
            });
        }
        if (resolvedProviderId) {
            throw authRequiredError(resolvedProviderId, message);
        }
        throw new execution_types_1.MakaiStreamError(message, { kind: "provider_error", code: "auth_required" });
    }
    return response;
}
function providerIdFromRequest(request) {
    try {
        const parsed = (0, model_ref_1.parseModelRef)(request.model_ref);
        return parsed.providerId;
    }
    catch {
        const slashIndex = request.model_ref.indexOf("/");
        const atIndex = request.model_ref.indexOf("@");
        if (slashIndex !== -1 && atIndex !== -1 && slashIndex < atIndex) {
            const provider = request.model_ref.slice(0, slashIndex);
            if (provider.length > 0)
                return provider;
        }
        return undefined;
    }
}
async function withAuthRetry(operation, options) {
    try {
        return await operation();
    }
    catch (error) {
        if ((0, abort_signal_1.isAbortError)(error))
            throw error;
        if (isRetryableAuthError(error) && options.authRetryPolicy === "auto_once" && options.auth) {
            (0, abort_signal_1.checkAbort)(options.signal, "operation aborted during auth retry");
            const providerId = error.provider_id ?? options.fallbackProviderId;
            if (!providerId)
                throw error;
            const logger = options.logger ?? (0, logger_1.getNoopLogger)();
            logger.info("execution: auto-retrying after auth_required", { provider_id: providerId });
            try {
                await (0, abort_signal_1.raceWithAbort)(options.auth.login(providerId, options.authHandlers, { signal: options.signal }), options.signal, "operation aborted during auth login");
            }
            catch (loginError) {
                if ((0, abort_signal_1.isAbortError)(loginError)) {
                    await options.onAbort?.();
                    throw loginError;
                }
                if (loginError instanceof auth_protocol_1.MakaiAuthError && loginError.kind === "cancelled" && options.signal?.aborted) {
                    await options.onAbort?.();
                    const abortError = new Error("operation aborted during auth retry");
                    abortError.name = "AbortError";
                    throw abortError;
                }
                throw authRequiredError(providerId, error.message);
            }
            (0, abort_signal_1.checkAbort)(options.signal, "operation aborted before retry");
            options.beforeRetry?.();
            try {
                return await operation();
            }
            catch (retryError) {
                if ((0, abort_signal_1.isAbortError)(retryError))
                    throw retryError;
                if (isRetryableAuthError(retryError)) {
                    const retryProviderId = retryError.provider_id ?? providerId;
                    throw authRequiredError(retryProviderId, retryError.message);
                }
                throw retryError;
            }
        }
        if (isRetryableAuthError(error)) {
            const providerId = error.provider_id ?? options.fallbackProviderId;
            if (providerId)
                throw authRequiredError(providerId, error.message);
        }
        throw error;
    }
}
async function createMakaiClient(options = {}) {
    const { auth: authOptions, responseTimeoutMs, frameTimeoutMs, logger, ...transportOptions } = options;
    const transport = await (0, stdio_client_1.createMakaiStdioClient)({ ...transportOptions, logger });
    await transport.connect();
    const authClient = new auth_protocol_1.MakaiAuthClient(transport, { handlers: authOptions?.handlers, frameTimeoutMs, logger });
    const executionOptions = {
        responseTimeoutMs: responseTimeoutMs ?? frameTimeoutMs,
        authRetryPolicy: authOptions?.auth_retry_policy,
        auth: authClient,
        authHandlers: authOptions?.handlers,
        logger,
    };
    return {
        auth: authClient,
        models: (0, models_client_1.createMakaiModelsApi)(transport, { responseTimeoutMs: responseTimeoutMs ?? frameTimeoutMs, logger }),
        agent: createMakaiAgentApiWithModels(transport, executionOptions),
        provider: createMakaiProviderApi(transport, executionOptions),
        close: () => transport.close(),
    };
}
