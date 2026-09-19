"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_test_1 = __importDefault(require("node:test"));
const src_1 = require("../src");
const REQUEST = {
    model_ref: "fixture/anthropic-messages@model",
    messages: [{ role: "user", content: "hello" }],
};
class AbortTestTransport {
    sent = [];
    frames;
    failWith;
    pendingEntries = [];
    constructor(frames = [], failWith) {
        this.frames = [...frames];
        this.failWith = failWith;
    }
    send(frame) {
        this.sent.push(frame);
    }
    rejectAll() {
        for (const entry of this.pendingEntries.splice(0)) {
            clearTimeout(entry.timer);
            entry.reject(new Error("transport cleaned up"));
        }
    }
    async nextFrameForStream(streamId, timeoutMs) {
        if (this.failWith)
            throw this.failWith;
        const frame = this.frames.shift();
        if (!frame) {
            return new Promise((_resolve, reject) => {
                const timer = setTimeout(() => {
                    const idx = this.pendingEntries.findIndex((e) => e.reject === reject);
                    if (idx >= 0)
                        this.pendingEntries.splice(idx, 1);
                    reject(new Error(`timed out waiting for frame for stream ${streamId} after ${timeoutMs ?? 1000}ms`));
                }, timeoutMs ?? 1000);
                this.pendingEntries.push({ reject, timer });
            });
        }
        return { stream_id: streamId, ...frame };
    }
    async nextFrameForSession(sessionId, timeoutMs) {
        if (this.failWith)
            throw this.failWith;
        const frame = this.frames.shift();
        if (!frame) {
            return new Promise((_resolve, reject) => {
                const timer = setTimeout(() => {
                    const idx = this.pendingEntries.findIndex((e) => e.reject === reject);
                    if (idx >= 0)
                        this.pendingEntries.splice(idx, 1);
                    reject(new Error(`timed out waiting for frame for session ${sessionId} after ${timeoutMs ?? 1000}ms`));
                }, timeoutMs ?? 1000);
                this.pendingEntries.push({ reject, timer });
            });
        }
        return { session_id: sessionId, ...frame };
    }
}
async function collect(iterable) {
    const events = [];
    for await (const event of iterable)
        events.push(event);
    return events;
}
async function flushMicrotasks() {
    await new Promise((resolve) => queueMicrotask(resolve));
}
(0, node_test_1.default)("provider.complete rejects immediately when AbortSignal.abort() is passed", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const signal = AbortSignal.abort();
    await strict_1.default.rejects(() => provider.complete({ ...REQUEST, options: { signal } }), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.complete rejects when signal is aborted during frame wait", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const completePromise = provider.complete({ ...REQUEST, options: { signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => completePromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 2);
    strict_1.default.equal(transport.sent[0]?.type, "complete_request");
    strict_1.default.equal(transport.sent[1]?.type, "abort_request");
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.complete with AbortSignal.timeout aborts after timeout", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 5);
    try {
        await strict_1.default.rejects(() => provider.complete({ ...REQUEST, options: { signal: controller.signal } }), (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        clearTimeout(timer);
        transport.rejectAll();
        await flushMicrotasks();
    }
});
(0, node_test_1.default)("provider.stream rejects immediately when AbortSignal.abort() is passed", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const signal = AbortSignal.abort();
    await strict_1.default.rejects(() => collect(provider.stream({ ...REQUEST, options: { signal } })), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.stream stops iteration when signal is aborted during streaming", async () => {
    const transport = new AbortTestTransport([
        { type: "event", payload: { type: "message_start" } },
        { type: "event", payload: { type: "text_delta", delta: "hello" } },
    ]);
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const events = [];
    const streamPromise = (async () => {
        for await (const event of provider.stream({ ...REQUEST, options: { signal: controller.signal } })) {
            events.push(event);
            controller.abort();
        }
    })();
    await strict_1.default.rejects(() => streamPromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.ok(events.length >= 1, "expected at least one event before abort");
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.stream with AbortSignal.timeout aborts after timeout", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 5);
    try {
        await strict_1.default.rejects(() => collect(provider.stream({ ...REQUEST, options: { signal: controller.signal } })), (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        clearTimeout(timer);
        transport.rejectAll();
        await flushMicrotasks();
    }
});
(0, node_test_1.default)("agent.run rejects immediately when AbortSignal.abort() is passed", async () => {
    const transport = new AbortTestTransport();
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const signal = AbortSignal.abort();
    await strict_1.default.rejects(() => agent.run({ ...REQUEST, options: { signal } }), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.run rejects when signal is aborted during frame wait", async () => {
    const transport = new AbortTestTransport();
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const runPromise = agent.run({ ...REQUEST, options: { signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => runPromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 2);
    strict_1.default.equal(transport.sent[0]?.type, "agent_start");
    strict_1.default.equal(transport.sent[0]?.sequence, 1);
    strict_1.default.equal(transport.sent[1]?.type, "agent_stop");
    strict_1.default.equal(transport.sent[1]?.sequence, 2);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.run abort completes the sequence probe before the abort surfaces, so an immediate same-id retry is not agent_busy (#210 gap 7)", async () => {
    const queue = [];
    const sent = [];
    const transport = {
        send(frame) {
            sent.push(frame);
            if (frame.type === "agent_start") {
                queue.push({ type: "agent_started", session_id: frame.session_id, message_id: "m-started", sequence: 1, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { session_id: frame.session_id } });
            }
            if (frame.type === "agent_stop") {
                if (frame.sequence < 3) {
                    queue.push({ type: "agent_error", session_id: frame.session_id, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { code: "invalid_request", message: "invalid sequence" } });
                }
                else {
                    queue.push({ type: "agent_stopped", session_id: frame.session_id, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
                }
            }
        },
        async nextFrameForSession(_sessionId, timeoutMs, wait) {
            const correlate = wait?.correlate;
            const index = correlate !== undefined
                ? queue.findIndex((entry) => entry.in_reply_to === correlate)
                : queue.findIndex((entry) => entry.in_reply_to === undefined);
            if (index >= 0)
                return queue.splice(index, 1)[0];
            await new Promise((resolve) => setTimeout(resolve, Math.min(timeoutMs ?? 50, 50)));
            throw new Error("timed out");
        },
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const runPromise = agent.run({ ...REQUEST, options: { signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 20));
    controller.abort();
    await strict_1.default.rejects(() => runPromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.deepEqual(sent.map((frame) => `${frame.type}:${frame.sequence}`), ["agent_start:1", "agent_message:2", "agent_stop:2", "agent_stop:3"]);
    await new Promise((resolve) => setTimeout(resolve, 260));
});
(0, node_test_1.default)("agent.run abort cancels the abandoned session read instead of leaving it pending (#210 gap 7)", async () => {
    const queue = [];
    let pendingReads = 0;
    const transport = {
        send(frame) {
            if (frame.type === "agent_start") {
                queue.push({ type: "agent_started", session_id: frame.session_id, message_id: "m-started", sequence: 1, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { session_id: frame.session_id } });
            }
            if (frame.type === "agent_stop") {
                if (frame.sequence < 3) {
                    queue.push({ type: "agent_error", session_id: frame.session_id, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { code: "invalid_request", message: "invalid sequence" } });
                }
                else {
                    queue.push({ type: "agent_stopped", session_id: frame.session_id, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
                }
            }
        },
        nextFrameForSession(_sessionId, timeoutMs, wait) {
            const correlate = wait?.correlate;
            const index = correlate !== undefined ? queue.findIndex((entry) => entry.in_reply_to === correlate) : -1;
            if (index >= 0)
                return Promise.resolve(queue.splice(index, 1)[0]);
            pendingReads += 1;
            return new Promise((_resolve, reject) => {
                const settle = () => {
                    pendingReads -= 1;
                    reject(new Error("timed out"));
                };
                const timer = setTimeout(settle, timeoutMs ?? 1000);
                wait?.signal?.addEventListener("abort", () => {
                    clearTimeout(timer);
                    pendingReads -= 1;
                    reject(new Error("aborted"));
                }, { once: true });
            });
        },
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport, { responseTimeoutMs: 5000 });
    const controller = new AbortController();
    const runPromise = agent.run({ ...REQUEST, options: { signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 20));
    controller.abort();
    await strict_1.default.rejects(() => runPromise, (error) => error instanceof Error && error.name === "AbortError");
    await new Promise((resolve) => setTimeout(resolve, 20));
    strict_1.default.equal(pendingReads, 0, "the abandoned session read must be aborted with the caller's signal");
});
(0, node_test_1.default)("agent.stream rejects immediately when AbortSignal.abort() is passed", async () => {
    const transport = new AbortTestTransport();
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const signal = AbortSignal.abort();
    await strict_1.default.rejects(() => collect(agent.stream({ ...REQUEST, options: { signal } })), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.stream stops iteration when signal is aborted during streaming", async () => {
    const transport = new AbortTestTransport([
        { type: "agent_started", payload: {} },
        { type: "event", payload: { type: "turn_start" } },
    ]);
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const events = [];
    const streamPromise = (async () => {
        for await (const event of agent.stream({ ...REQUEST, options: { signal: controller.signal } })) {
            events.push(event);
            controller.abort();
        }
    })();
    await strict_1.default.rejects(() => streamPromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.ok(events.length >= 1, "expected at least one event before abort");
    strict_1.default.deepEqual(transport.sent.map((frame) => frame.type), ["agent_start", "agent_message", "agent_stop"]);
    strict_1.default.deepEqual(transport.sent.map((frame) => frame.sequence), [1, 2, 3]);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.stream with AbortSignal.timeout aborts after timeout", async () => {
    const transport = new AbortTestTransport();
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 5);
    try {
        await strict_1.default.rejects(() => collect(agent.stream({ ...REQUEST, options: { signal: controller.signal } })), (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        clearTimeout(timer);
        transport.rejectAll();
        await flushMicrotasks();
    }
});
(0, node_test_1.default)("provider.complete removes abort listener after rejection", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const signal = controller.signal;
    const listenersBefore = listenerCount(signal);
    const completePromise = provider.complete({ ...REQUEST, options: { signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => completePromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(listenerCount(signal), listenersBefore);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.stream removes abort listener after rejection", async () => {
    const transport = new AbortTestTransport();
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const signal = controller.signal;
    const listenersBefore = listenerCount(signal);
    const streamPromise = collect(agent.stream({ ...REQUEST, options: { signal } }));
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => streamPromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(listenerCount(signal), listenersBefore);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.stream with pre-aborted signal does not send envelope", async () => {
    const transport = new AbortTestTransport([
        { type: "event", payload: { type: "message_start" } },
    ]);
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    await strict_1.default.rejects(() => collect(provider.stream({ ...REQUEST, options: { signal: AbortSignal.abort() } })), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.run with pre-aborted signal does not send envelope", async () => {
    const transport = new AbortTestTransport([
        { type: "agent_started", payload: {} },
    ]);
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    await strict_1.default.rejects(() => agent.run({ ...REQUEST, options: { signal: AbortSignal.abort() } }), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.complete succeeds when signal is not aborted", async () => {
    const transport = new AbortTestTransport([
        { type: "ack" },
        { type: "complete_response", payload: { message: { role: "assistant", content: "ok" }, usage: { input: 1, output: 1 } } },
    ]);
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const result = await provider.complete({ ...REQUEST, options: { signal: controller.signal } });
    strict_1.default.equal(result.message.role, "assistant");
    strict_1.default.equal(controller.signal.aborted, false);
});
(0, node_test_1.default)("provider.stream completes normally when signal is not aborted", async () => {
    const transport = new AbortTestTransport([
        { type: "message_start", provider_id: "fixture", api: "anthropic-messages", model_id: "model" },
        { type: "text_delta", delta: "hi" },
        { type: "message_end", usage: { input: 1, output: 2 }, stop_reason: "end_turn" },
    ]);
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const events = await collect(provider.stream({ ...REQUEST, options: { signal: controller.signal } }));
    strict_1.default.equal(events.length, 3);
    strict_1.default.equal(events.at(-1)?.type, "message_end");
    strict_1.default.equal(controller.signal.aborted, false);
});
(0, node_test_1.default)("abort rejection is a plain Error with name 'AbortError', not MakaiStreamError", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    try {
        await provider.complete({ ...REQUEST, options: { signal: AbortSignal.abort() } });
        strict_1.default.fail("expected rejection");
    }
    catch (error) {
        strict_1.default.ok(error instanceof Error);
        strict_1.default.equal(error.name, "AbortError");
        strict_1.default.equal(error instanceof src_1.MakaiStreamError, false);
    }
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.complete withAuthRetry aborts before auth retry sends second envelope", async () => {
    const transport = new AbortTestTransport();
    transport.nextFrameForStream = async (streamId, _timeoutMs) => {
        return {
            stream_id: streamId,
            type: "nack",
            payload: { reason: "login required", error_code: "auth_required", provider_id: "fixture" },
        };
    };
    const auth = {
        loginCalls: 0,
        async listProviders() { return []; },
        async login(_providerId, _handlers, options) {
            this.loginCalls += 1;
            return new Promise((resolve, reject) => {
                const signal = options?.signal;
                if (signal?.aborted) {
                    const error = new Error("login aborted");
                    error.name = "AbortError";
                    reject(error);
                    return;
                }
                const onAbort = () => {
                    const error = new Error("login aborted");
                    error.name = "AbortError";
                    reject(error);
                };
                signal?.addEventListener("abort", onAbort, { once: true });
            });
        },
    };
    const controller = new AbortController();
    const completePromise = (0, src_1.createMakaiProviderApi)(transport, {
        auth,
        authRetryPolicy: "auto_once",
    }).complete({ ...REQUEST, options: { auth_retry_policy: "auto_once", signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => completePromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.filter((f) => f.type === "complete_request").length, 1);
    strict_1.default.equal(auth.loginCalls, 1);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.run withAuthRetry aborts before retry sends second agent_start", async () => {
    const transport = new AbortTestTransport();
    transport.nextFrameForSession = async (sessionId, _timeoutMs) => {
        return {
            session_id: sessionId,
            type: "nack",
            payload: { reason: "login required", error_code: "auth_required", provider_id: "fixture" },
        };
    };
    const auth = {
        loginCalls: 0,
        async listProviders() { return []; },
        async login(_providerId, _handlers, options) {
            this.loginCalls += 1;
            return new Promise((resolve, reject) => {
                const signal = options?.signal;
                if (signal?.aborted) {
                    const error = new Error("login aborted");
                    error.name = "AbortError";
                    reject(error);
                    return;
                }
                const onAbort = () => {
                    const error = new Error("login aborted");
                    error.name = "AbortError";
                    reject(error);
                };
                signal?.addEventListener("abort", onAbort, { once: true });
            });
        },
    };
    const controller = new AbortController();
    const runPromise = (0, src_1.createMakaiAgentApi)(transport, {
        auth,
        authRetryPolicy: "auto_once",
    }).run({ ...REQUEST, options: { auth_retry_policy: "auto_once", signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => runPromise, (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.filter((f) => f.type === "agent_start").length, 1);
    strict_1.default.equal(auth.loginCalls, 1);
    transport.rejectAll();
    await flushMicrotasks();
});
function listenerCount(signal) {
    if ("listenerCount" in signal && typeof signal.listenerCount === "function") {
        return signal.listenerCount("abort");
    }
    return 0;
}
(0, node_test_1.default)("provider.complete sends abort_request cancel envelope on abort", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const completePromise = provider.complete({ ...REQUEST, options: { signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => completePromise, (error) => error instanceof Error && error.name === "AbortError");
    const cancelFrame = transport.sent.find((f) => f.type === "abort_request");
    strict_1.default.ok(cancelFrame, "expected abort_request frame");
    const payload = cancelFrame.payload;
    strict_1.default.equal(typeof payload.target_stream_id, "string");
    strict_1.default.equal(payload.reason, "client aborted");
    const requestFrame = transport.sent.find((f) => f.type === "complete_request");
    strict_1.default.equal(payload.target_stream_id, requestFrame?.stream_id);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.stream sends abort_request cancel envelope on abort", async () => {
    const transport = new AbortTestTransport();
    let frameCount = 0;
    transport.nextFrameForStream = async (streamId, _timeoutMs) => {
        frameCount++;
        if (frameCount === 1) {
            return { stream_id: streamId, type: "event", payload: { type: "message_start" } };
        }
        return { stream_id: streamId, type: "event", payload: { type: "text_delta", delta: "hi" } };
    };
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    const controller = new AbortController();
    const events = [];
    const streamPromise = (async () => {
        for await (const event of provider.stream({ ...REQUEST, options: { signal: controller.signal } })) {
            events.push(event);
            controller.abort();
        }
    })();
    await strict_1.default.rejects(() => streamPromise, (error) => error instanceof Error && error.name === "AbortError");
    const cancelFrames = transport.sent.filter((f) => f.type === "abort_request");
    strict_1.default.equal(cancelFrames.length, 1, "expected exactly one abort_request frame");
    const payload = cancelFrames[0].payload;
    strict_1.default.equal(payload.reason, "client aborted");
    const requestFrame = transport.sent.find((f) => f.type === "stream_request");
    strict_1.default.equal(payload.target_stream_id, requestFrame?.stream_id);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.run sends agent_stop cancel envelope on abort", async () => {
    const transport = new AbortTestTransport();
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const runPromise = agent.run({ ...REQUEST, options: { signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => runPromise, (error) => error instanceof Error && error.name === "AbortError");
    const cancelFrame = transport.sent.find((f) => f.type === "agent_stop");
    strict_1.default.ok(cancelFrame, "expected agent_stop frame");
    const payload = cancelFrame.payload;
    strict_1.default.equal(payload.reason, "client aborted");
    const startFrame = transport.sent.find((f) => f.type === "agent_start");
    strict_1.default.equal(cancelFrame.session_id, startFrame?.session_id);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("agent.stream sends agent_stop cancel envelope on abort", async () => {
    const transport = new AbortTestTransport();
    let frameCount = 0;
    transport.nextFrameForSession = async (sessionId, _timeoutMs) => {
        frameCount++;
        if (frameCount === 1) {
            return { session_id: sessionId, type: "agent_started", payload: {} };
        }
        return {
            session_id: sessionId,
            type: "agent_event",
            payload: { event_json: JSON.stringify({ type: "turn_start" }) },
        };
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const controller = new AbortController();
    const events = [];
    const streamPromise = (async () => {
        for await (const event of agent.stream({ ...REQUEST, options: { signal: controller.signal } })) {
            events.push(event);
            controller.abort();
        }
    })();
    await strict_1.default.rejects(() => streamPromise, (error) => error instanceof Error && error.name === "AbortError");
    const cancelFrames = transport.sent.filter((f) => f.type === "agent_stop");
    strict_1.default.equal(cancelFrames.length, 1, "expected exactly one agent_stop frame");
    const payload = cancelFrames[0].payload;
    strict_1.default.equal(payload.reason, "client aborted");
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("cancel is not sent when abort occurs before transport I/O", async () => {
    const transport = new AbortTestTransport();
    const provider = (0, src_1.createMakaiProviderApi)(transport);
    await strict_1.default.rejects(() => provider.complete({ ...REQUEST, options: { signal: AbortSignal.abort() } }), (error) => error instanceof Error && error.name === "AbortError");
    strict_1.default.equal(transport.sent.length, 0);
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("provider.complete withAuthRetry sends cancel on abort during auth login", async () => {
    const transport = new AbortTestTransport();
    transport.nextFrameForStream = async (streamId, _timeoutMs) => {
        return {
            stream_id: streamId,
            type: "nack",
            payload: { reason: "login required", error_code: "auth_required", provider_id: "fixture" },
        };
    };
    const auth = {
        async listProviders() { return []; },
        async login(_providerId, _handlers, options) {
            return new Promise((resolve, reject) => {
                const signal = options?.signal;
                if (signal?.aborted) {
                    const error = new Error("login aborted");
                    error.name = "AbortError";
                    reject(error);
                    return;
                }
                const onAbort = () => {
                    const error = new Error("login aborted");
                    error.name = "AbortError";
                    reject(error);
                };
                signal?.addEventListener("abort", onAbort, { once: true });
            });
        },
    };
    const controller = new AbortController();
    const completePromise = (0, src_1.createMakaiProviderApi)(transport, {
        auth,
        authRetryPolicy: "auto_once",
    }).complete({ ...REQUEST, options: { auth_retry_policy: "auto_once", signal: controller.signal } });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => completePromise, (error) => error instanceof Error && error.name === "AbortError");
    const cancelFrame = transport.sent.find((f) => f.type === "abort_request");
    strict_1.default.ok(cancelFrame, "expected abort_request frame from withAuthRetry onAbort");
    const payload = cancelFrame.payload;
    strict_1.default.equal(payload.reason, "client aborted");
    transport.rejectAll();
    await flushMicrotasks();
});
(0, node_test_1.default)("models.list sends abort_request cancel envelope on abort", async () => {
    const transport = new AbortTestTransport();
    const models = (0, src_1.createMakaiModelsApi)(transport);
    const controller = new AbortController();
    const listPromise = models.list({ provider_id: "anthropic", signal: controller.signal });
    await new Promise((resolve) => setTimeout(resolve, 5));
    controller.abort();
    await strict_1.default.rejects(() => listPromise, (error) => error instanceof Error && error.name === "AbortError");
    const cancelFrame = transport.sent.find((f) => f.type === "abort_request");
    strict_1.default.ok(cancelFrame, "expected abort_request frame");
    const payload = cancelFrame.payload;
    strict_1.default.equal(typeof payload.target_stream_id, "string");
    strict_1.default.equal(payload.reason, "client aborted");
    const requestFrame = transport.sent.find((f) => f.type === "models_request");
    strict_1.default.equal(payload.target_stream_id, requestFrame?.stream_id);
    transport.rejectAll();
    await flushMicrotasks();
});
