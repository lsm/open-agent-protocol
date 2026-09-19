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
class ScriptedTransport {
    sent = [];
    frames;
    failWith;
    constructor(frames, failWith) {
        this.frames = [...frames];
        this.failWith = failWith;
    }
    send(frame) {
        this.sent.push(frame);
    }
    async nextFrameForStream(streamId, _timeoutMs) {
        if (this.failWith)
            throw this.failWith;
        const frame = this.frames.shift();
        if (!frame)
            throw new Error(`no scripted frame for ${streamId}`);
        return { stream_id: streamId, ...frame };
    }
    async nextFrameForSession(sessionId, _timeoutMs) {
        if (this.failWith)
            throw this.failWith;
        const frame = this.frames.shift();
        if (!frame)
            throw new Error(`no scripted frame for ${sessionId}`);
        return { session_id: sessionId, ...frame };
    }
}
async function collect(iterable) {
    const events = [];
    for await (const event of iterable)
        events.push(event);
    return events;
}
(0, node_test_1.default)("provider stream emits exactly one terminal event", async () => {
    const transport = new ScriptedTransport([
        { type: "event", payload: { type: "message_start" } },
        { type: "event", payload: { type: "text_delta", delta: "hi" } },
        { type: "event", payload: { type: "message_end", usage: { input: 1, output: 2 }, stop_reason: "end_turn" } },
        { type: "event", payload: { type: "error", message: "late" } },
    ]);
    const events = await collect((0, src_1.createMakaiProviderApi)(transport).stream(REQUEST));
    const terminals = events.filter((event) => event.type === "message_end" || event.type === "error");
    strict_1.default.equal(terminals.length, 1);
    strict_1.default.equal(terminals[0]?.type, "message_end");
    strict_1.default.equal(events.at(-1)?.type, "message_end");
});
(0, node_test_1.default)("provider stream does not emit error after message_end and normalizes reasoning", async () => {
    const transport = new ScriptedTransport([
        { type: "event", payload: { type: "start", provider: "anthropic", api: "anthropic-messages", model: "claude" } },
        { type: "event", payload: { type: "reasoning", delta: "thinking" } },
        { type: "event", payload: { type: "toolcall_start", content_index: 0, id: "tc1", name: "lookup" } },
        { type: "event", payload: { type: "toolcall_delta", content_index: 0, delta: "{\"q\":" } },
        { type: "event", payload: { type: "toolcall_delta", content_index: 0, delta: "\"x\"}" } },
        { type: "event", payload: { type: "toolcall_end", content_index: 0 } },
        { type: "event", payload: { type: "done", reason: "tool_use", message: { usage: { input: 4, output: 5 } } } },
        { type: "stream_error", payload: { message: "late provider error" } },
    ]);
    const events = await collect((0, src_1.createMakaiProviderApi)(transport).stream(REQUEST));
    strict_1.default.deepEqual(events[0], {
        type: "message_start",
        provider_id: "anthropic",
        api: "anthropic-messages",
        model_id: "claude",
    });
    strict_1.default.deepEqual(events[1], { type: "thinking_delta", delta: "thinking" });
    strict_1.default.deepEqual(events[2], {
        type: "tool_call",
        tool_call_id: "tc1",
        name: "lookup",
        arguments_json: "{\"q\":\"x\"}",
    });
    strict_1.default.equal(events.at(-1)?.type, "message_end");
    strict_1.default.equal(events.some((event) => event.type === "error"), false);
});
(0, node_test_1.default)("agent stream emits agent_start first and agent_end last", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        { type: "event", payload: { type: "turn_start" } },
        { type: "event", payload: { type: "message_end", usage: { input: 1, output: 1 }, stop_reason: "end_turn" } },
        { type: "event", payload: { type: "turn_end", stop_reason: "end_turn" } },
        { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
        { type: "event", payload: { type: "error", message: "late" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    strict_1.default.equal(events[0]?.type, "agent_start");
    strict_1.default.equal(events.at(-1)?.type, "agent_end");
});
(0, node_test_1.default)("agent stream aggregates usage across multiple turns", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        { type: "agent_event", payload: { type: "agent_start", session_id: "abc" } },
        { type: "event", payload: { type: "turn_start" } },
        { type: "event", payload: { type: "message_end", usage: { input: 2, output: 3, cache_read: 5, cache_write: 7 } } },
        { type: "event", payload: { type: "turn_end", stop_reason: "tool_use" } },
        { type: "event", payload: { type: "turn_start" } },
        { type: "event", payload: { type: "message_end", usage: { input: 11, output: 13, cache_read: 17, cache_write: 19 } } },
        { type: "event", payload: { type: "turn_end", stop_reason: "end_turn" } },
        { type: "agent_event", payload: { type: "agent_end", usage: { input: 11, output: 13 }, stop_reason: "max_turns" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    const agentEnd = events.at(-1);
    strict_1.default.equal(agentEnd?.type, "agent_end");
    strict_1.default.deepEqual(agentEnd?.usage, { input: 13, output: 16, cache_read: 22, cache_write: 26 });
    strict_1.default.equal(agentEnd?.stop_reason, "max_turns");
    strict_1.default.equal(events.filter((event) => event.type === "turn_end").length, 2);
});
(0, node_test_1.default)("agent stream preserves missing aggregate usage as unknown", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        { type: "agent_event", payload: { type: "agent_start", session_id: "abc" } },
        { type: "event", payload: { type: "turn_start" } },
        { type: "event", payload: { type: "message_end", stop_reason: "end_turn" } },
        { type: "event", payload: { type: "turn_end", stop_reason: "end_turn" } },
        { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    const agentEnd = events.at(-1);
    strict_1.default.equal(agentEnd?.type, "agent_end");
    strict_1.default.equal(agentEnd?.usage, undefined);
});
(0, node_test_1.default)("agent stream preserves unknown cache usage while aggregating turns", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        { type: "agent_event", payload: { type: "agent_start", session_id: "abc" } },
        { type: "event", payload: { type: "message_end", usage: { input: 2, output: 3 } } },
        { type: "event", payload: { type: "message_end", usage: { input: 5, output: 7 } } },
        { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    const agentEnd = events.at(-1);
    strict_1.default.equal(agentEnd?.type, "agent_end");
    strict_1.default.deepEqual(agentEnd?.usage, { input: 7, output: 10 });
});
(0, node_test_1.default)("agent stream parses event_type encoded lifecycle events", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        { type: "event", event_type: "turn_start" },
        { type: "event", payload: { event: { event_type: "turn_end", stop_reason: "tool_use" } } },
        { type: "event", event_type: "agent_end", payload: { stop_reason: "end_turn" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    strict_1.default.deepEqual(events.map((event) => event.type), ["agent_start", "turn_start", "turn_end", "agent_end"]);
    strict_1.default.deepEqual(events[2], { type: "turn_end", stop_reason: "tool_use" });
    strict_1.default.deepEqual(events.at(-1), { type: "agent_end", stop_reason: "end_turn" });
});
(0, node_test_1.default)("agent stream executes tool_execute frames and sends tool_result", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        {
            type: "tool_execute",
            message_id: "tool-request-1",
            sequence: 3,
            payload: { tool_call_id: "call-1", tool_name: "sum", args_json: "{\"a\":2,\"b\":3}" },
        },
        { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream({
        ...REQUEST,
        tools: [{
                name: "sum",
                description: "sum numbers",
                parameters_schema_json: "{}",
                execute: (args) => `sum=${Number(args.a) + Number(args.b)}`,
            }],
    }));
    const toolResult = transport.sent.find((frame) => frame.type === "tool_result");
    strict_1.default.equal(toolResult?.session_id, transport.sent[0]?.session_id);
    strict_1.default.equal(toolResult?.in_reply_to, "tool-request-1");
    strict_1.default.equal(toolResult?.sequence, 4);
    strict_1.default.deepEqual(toolResult?.payload, {
        tool_call_id: "call-1",
        result_json: JSON.stringify([{ type: "text", text: "sum=5" }]),
        is_error: false,
    });
    strict_1.default.equal(events.at(-1)?.type, "agent_end");
});
(0, node_test_1.default)("agent stream returns tool error for non-executable tool_execute frames", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        {
            type: "tool_execute",
            message_id: "tool-request-1",
            sequence: 3,
            payload: { tool_call_id: "call-1", tool_name: "missing", args_json: "{}" },
        },
        { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
    ]);
    await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    const toolResult = transport.sent.find((frame) => frame.type === "tool_result");
    strict_1.default.deepEqual(toolResult?.payload, {
        tool_call_id: "call-1",
        result_json: JSON.stringify([{ type: "text", text: "Tool 'missing' is not executable by this client" }]),
        is_error: true,
    });
});
(0, node_test_1.default)("agent stream auth_required error retries before synthetic agent_start", async () => {
    const attempts = [
        [
            { type: "agent_started", payload: {} },
            { type: "event", payload: { type: "error", message: "login required", code: "auth_required", provider_id: "fixture" } },
        ],
        [
            { type: "agent_started", payload: {} },
            { type: "agent_event", payload: { type: "agent_start", session_id: "abc" } },
            { type: "event", payload: { type: "message_end", usage: { input: 1, output: 2 } } },
            { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
        ],
    ];
    const transport = new ScriptedTransport([]);
    transport.nextFrameForSession = async (sessionId) => {
        const agentStartCount = transport.sent.filter((frame) => frame.type === "agent_start").length;
        const frame = attempts[agentStartCount - 1]?.shift();
        if (!frame)
            throw new Error(`no scripted frame for ${sessionId}`);
        return { session_id: sessionId, ...frame };
    };
    const auth = {
        loginCalls: 0,
        async listProviders() { return []; },
        async login() { this.loginCalls += 1; return { status: "success" }; },
    };
    const events = await collect((0, src_1.createMakaiAgentApi)(transport, { auth, authRetryPolicy: "auto_once" }).stream(REQUEST));
    strict_1.default.equal(auth.loginCalls, 1);
    strict_1.default.equal(events[0]?.type, "agent_start");
    strict_1.default.equal(events.at(-1)?.type, "agent_end");
    strict_1.default.equal(events.some((event) => event.type === "error"), false);
});
(0, node_test_1.default)("agent stream single failure emits one error and no agent_end", async () => {
    const transport = new ScriptedTransport([
        { type: "agent_started", payload: {} },
        { type: "agent_event", payload: { type: "agent_start" } },
        { type: "event", payload: { type: "error", message: "boom", code: "provider_error" } },
        { type: "agent_event", payload: { type: "agent_end", stop_reason: "end_turn" } },
        { type: "stream_error", payload: { message: "duplicate" } },
    ]);
    const events = await collect((0, src_1.createMakaiAgentApi)(transport).stream(REQUEST));
    strict_1.default.equal(events.filter((event) => event.type === "error").length, 1);
    strict_1.default.deepEqual(events.at(-1), { type: "error", message: "boom", code: "provider_error" });
    strict_1.default.equal(events.some((event) => event.type === "agent_end"), false);
});
(0, node_test_1.default)("provider stream timeout includes actionable diagnostics", async () => {
    const transport = new ScriptedTransport([], new Error("timed out waiting for frame for stream s1 after 25ms"));
    const iterable = (0, src_1.createMakaiProviderApi)(transport, { responseTimeoutMs: 25 }).stream(REQUEST);
    await strict_1.default.rejects(async () => collect(iterable), (error) => error instanceof src_1.MakaiStreamError &&
        error.kind === "transport_error" &&
        error.message.includes("Timed out waiting for provider stream event after 25ms for provider 'fixture'") &&
        error.message.includes("model_ref='fixture/anthropic-messages@model'") &&
        error.message.includes("stream_id=") &&
        error.message.includes("Suggestions:") &&
        error.diagnostics?.operation === "provider stream event" &&
        error.diagnostics.timeout_ms === 25 &&
        error.diagnostics.provider_id === "fixture" &&
        error.diagnostics.api === "anthropic-messages" &&
        error.diagnostics.model_id === "model" &&
        typeof error.diagnostics.stream_id === "string" &&
        error.diagnostics.message_id === error.diagnostics.stream_id);
});
(0, node_test_1.default)("agent stream timeout includes actionable diagnostics", async () => {
    const transport = new ScriptedTransport([], new Error("timed out waiting for frame for session abc after 30ms"));
    const iterable = (0, src_1.createMakaiAgentApi)(transport, { responseTimeoutMs: 30 }).stream({
        ...REQUEST,
        options: { session_id: "abcdefghijklmnopqrstu" },
    });
    await strict_1.default.rejects(async () => collect(iterable), (error) => error instanceof src_1.MakaiStreamError &&
        error.kind === "transport_error" &&
        error.message.includes("Timed out waiting for agent stream event after 30ms for provider 'fixture'") &&
        error.message.includes("session_id=abcdefghijklmnopqrstu") &&
        error.diagnostics?.operation === "agent stream event" &&
        error.diagnostics.timeout_ms === 30 &&
        error.diagnostics.provider_id === "fixture" &&
        error.diagnostics.session_id === "abcdefghijklmnopqrstu");
});
(0, node_test_1.default)("MakaiStreamError is thrown on provider async iterator failure", async () => {
    const transport = new ScriptedTransport([], new Error("transport failed"));
    const iterable = (0, src_1.createMakaiProviderApi)(transport).stream(REQUEST);
    await strict_1.default.rejects(async () => collect(iterable), (error) => error instanceof src_1.MakaiStreamError &&
        error.kind === "transport_error" &&
        error.message === "transport failed");
});
(0, node_test_1.default)("MakaiStreamError is thrown on agent async iterator failure", async () => {
    const transport = new ScriptedTransport([], new Error("agent transport failed"));
    const iterable = (0, src_1.createMakaiAgentApi)(transport).stream(REQUEST);
    await strict_1.default.rejects(async () => collect(iterable), (error) => error instanceof src_1.MakaiStreamError &&
        error.kind === "transport_error" &&
        error.message === "agent transport failed");
});
(0, node_test_1.default)("envelope nacks surface as one MakaiStreamError failure", async () => {
    const transport = new ScriptedTransport([
        { type: "nack", payload: { reason: "invalid model", error_code: "invalid_request" } },
        { type: "stream_error", payload: { message: "duplicate" } },
    ]);
    await strict_1.default.rejects(async () => collect((0, src_1.createMakaiProviderApi)(transport).stream(REQUEST)), (error) => error instanceof src_1.MakaiStreamError &&
        error.kind === "provider_error" &&
        error.code === "invalid_request" &&
        error.message === "invalid model");
});
