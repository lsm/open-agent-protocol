"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.bestEffortCancelStream = bestEffortCancelStream;
exports.bestEffortCancelAgent = bestEffortCancelAgent;
exports.bestEffortStopAgent = bestEffortStopAgent;
exports.drainStreamFrames = drainStreamFrames;
exports.drainSessionFrames = drainSessionFrames;
exports.drainSessionFramesUntilQuiescent = drainSessionFramesUntilQuiescent;
exports.stopAgentWithSequenceProbe = stopAgentWithSequenceProbe;
const ulid_1 = require("ulid");
const ENVELOPE_VERSION = 1;
function bestEffortCancelStream(transport, streamId) {
    try {
        transport.send({
            type: "abort_request",
            stream_id: streamId,
            message_id: (0, ulid_1.ulid)(),
            sequence: 2,
            timestamp: Date.now(),
            version: ENVELOPE_VERSION,
            payload: { target_stream_id: streamId, reason: "client aborted" },
        });
    }
    catch {
    }
}
function bestEffortCancelAgent(transport, sessionId, sequence = 2) {
    bestEffortStopAgent(transport, sessionId, sequence, "client aborted");
}
function bestEffortStopAgent(transport, sessionId, sequence, reason) {
    const messageId = (0, ulid_1.ulid)();
    try {
        transport.send({
            type: "agent_stop",
            session_id: sessionId,
            message_id: messageId,
            sequence,
            timestamp: Date.now(),
            version: ENVELOPE_VERSION,
            payload: { session_id: sessionId, reason },
        });
    }
    catch {
    }
    return messageId;
}
async function drainStreamFrames(transport, streamId, timeoutMs = 200) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const remaining = deadline - Date.now();
        if (remaining <= 0)
            break;
        const perFrameMs = Math.min(remaining, 50);
        try {
            await Promise.race([
                transport.nextFrameForStream(streamId, perFrameMs).catch(() => undefined),
                new Promise((resolve) => setTimeout(resolve, perFrameMs)),
            ]);
        }
        catch {
            break;
        }
    }
}
async function drainSessionFrames(transport, sessionId, timeoutMs = 200) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const remaining = deadline - Date.now();
        if (remaining <= 0)
            break;
        const perFrameMs = Math.min(remaining, 50);
        try {
            await Promise.race([
                transport.nextFrameForSession(sessionId, perFrameMs).catch(() => undefined),
                new Promise((resolve) => setTimeout(resolve, perFrameMs)),
            ]);
        }
        catch {
            break;
        }
    }
}
async function drainSessionFramesUntilQuiescent(transport, sessionId, idleMs = 50, maxMs = 250, opts = {}) {
    const deadline = performance.now() + maxMs;
    while (performance.now() < deadline) {
        const remaining = deadline - performance.now();
        if (remaining <= 0)
            break;
        const waitMs = Math.min(idleMs, remaining);
        const controller = new AbortController();
        const read = transport.nextFrameForSession(sessionId, waitMs, { signal: controller.signal }).catch(() => null);
        let budgetTimer;
        const budget = new Promise((resolve) => {
            budgetTimer = setTimeout(() => {
                controller.abort();
                resolve(null);
            }, remaining);
        });
        const frame = await Promise.race([read, budget]);
        if (budgetTimer !== undefined)
            clearTimeout(budgetTimer);
        if (!frame)
            return;
        if (opts.stopReplyTo !== undefined && frame.type === "agent_stopped" && frame.in_reply_to === opts.stopReplyTo)
            return;
    }
}
async function stopAgentWithSequenceProbe(transport, sessionId, sequences, reason, idleMs = 50, maxMs = 250) {
    let outstanding = { messageId: bestEffortStopAgent(transport, sessionId, sequences.preSend, reason), sequence: sequences.preSend };
    let retried = false;
    const deadline = performance.now() + maxMs;
    while (performance.now() < deadline) {
        const remaining = deadline - performance.now();
        if (remaining <= 0)
            break;
        const waitMs = Math.min(idleMs, remaining);
        const controller = new AbortController();
        const read = transport.nextFrameForSession(sessionId, waitMs, { correlate: outstanding.messageId, signal: controller.signal }).catch(() => null);
        let budgetTimer;
        const budget = new Promise((resolve) => {
            budgetTimer = setTimeout(() => {
                controller.abort();
                resolve(null);
            }, remaining);
        });
        const frame = await Promise.race([read, budget]);
        if (budgetTimer !== undefined)
            clearTimeout(budgetTimer);
        if (!frame) {
            continue;
        }
        if (frame.in_reply_to !== outstanding.messageId)
            continue;
        if (frame.type === "agent_stopped")
            return outstanding.sequence;
        const code = correlatedRejectionCode(frame);
        if (code === "invalid_request") {
            if (retried) {
                break;
            }
            retried = true;
            outstanding = { messageId: bestEffortStopAgent(transport, sessionId, sequences.postSend, reason), sequence: sequences.postSend };
            continue;
        }
        if (code === "agent_not_found")
            break;
    }
    return undefined;
}
function correlatedRejectionCode(frame) {
    if (frame.type !== "agent_error" && frame.type !== "nack")
        return undefined;
    const payload = frame.payload;
    if (payload === undefined || typeof payload !== "object" || payload === null)
        return undefined;
    const record = payload;
    const code = typeof record.code === "string" ? record.code : typeof record.error_code === "string" ? record.error_code : undefined;
    if (code === "invalid_sequence")
        return "invalid_request";
    return code;
}
