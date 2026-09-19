"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_fs_1 = __importDefault(require("node:fs"));
const node_os_1 = __importDefault(require("node:os"));
const node_path_1 = __importDefault(require("node:path"));
const node_test_1 = __importDefault(require("node:test"));
const src_1 = require("../src");
const sourceFixturesDir = node_path_1.default.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const fixtureScript = node_path_1.default.join(sourceFixturesDir, "execution-server.js");
async function setupHarness(envOverrides = {}) {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-exec-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, ...envOverrides },
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    return {
        client,
        tmpDir,
        logPath,
        cleanup: async () => {
            await client.close();
            node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
        },
    };
}
function request() {
    return {
        model_ref: "anthropic/anthropic-messages@opaque-model-ref-with%3Acolon",
        messages: [{ role: "user", content: "hello" }],
        options: { temperature: 0.2, session_id: "testNanoIdSess1234567" },
    };
}
function readLoggedRequests(logPath) {
    if (!node_fs_1.default.existsSync(logPath))
        return [];
    return node_fs_1.default.readFileSync(logPath, "utf8").trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
}
async function waitForLoggedRequests(logPath, predicate, timeoutMs = 2000) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
        const logged = readLoggedRequests(logPath);
        if (predicate(logged) || Date.now() >= deadline)
            return logged;
        await new Promise((resolve) => setTimeout(resolve, 10));
    }
}
async function collect(iterable) {
    const out = [];
    for await (const item of iterable)
        out.push(item);
    return out;
}
(0, node_test_1.default)("client.provider.complete resolves with correct CompletionResponse shape", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const result = await provider.complete(request());
        strict_1.default.equal(result.message.role, "assistant");
        strict_1.default.deepEqual(result.message.content, [{ type: "text", text: "hello" }]);
        strict_1.default.deepEqual(result.usage, { input: 3, output: 5, cache_read: 1, cache_write: 0 });
        strict_1.default.equal(result.provider_id, "anthropic");
        strict_1.default.equal(result.api, "anthropic-messages");
        strict_1.default.equal(result.model_id, "claude-sonnet-4-5");
        strict_1.default.equal(result.stop_reason, "end_turn");
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged[0]?.type, "complete_request");
        const payload = logged[0]?.payload;
        strict_1.default.deepEqual(payload.model, {
            id: "opaque-model-ref-with:colon",
            name: "opaque-model-ref-with:colon",
            api: "anthropic-messages",
            provider: "anthropic",
            base_url: "",
        });
        strict_1.default.equal(payload.model_ref, request().model_ref);
        strict_1.default.deepEqual(payload.context.messages, request().messages);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.provider.complete keeps routing fields for non-canonical model_ref", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        await provider.complete({ ...request(), model_ref: "anthropic/anthropic-messages@opaque-model-ref-with:colon" });
        const payload = readLoggedRequests(harness.logPath)[0]?.payload;
        strict_1.default.deepEqual(payload.model, {
            id: "opaque-model-ref-with:colon",
            name: "opaque-model-ref-with:colon",
            api: "anthropic-messages",
            provider: "anthropic",
            base_url: "",
        });
        strict_1.default.equal(payload.model_ref, "anthropic/anthropic-messages@opaque-model-ref-with:colon");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.provider.complete maps system prompts and tool messages into provider context", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        await provider.complete({
            model_ref: "anthropic/anthropic-messages@opaque-model-ref-with%3Acolon",
            messages: [
                { role: "system", content: "You are helpful." },
                { role: "developer", content: [{ type: "text", text: "Prefer concise answers." }] },
                { role: "user", content: "hello" },
                { role: "tool", tool_call_id: "call-1", name: "lookup", content: "tool result" },
            ],
        });
        const payload = readLoggedRequests(harness.logPath)[0]?.payload;
        const context = payload.context;
        strict_1.default.equal(context.system_prompt, "You are helpful.\n\nPrefer concise answers.");
        strict_1.default.deepEqual(context.messages, [
            { role: "user", content: "hello" },
            {
                role: "tool",
                content: [{ type: "text", text: "tool result" }],
                name: "lookup",
                tool_name: "lookup",
                tool_call_id: "call-1",
            },
        ]);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete timeout includes actionable diagnostics", async () => {
    const harness = await setupHarness({ MAKAI_TEST_SUPPRESS_COMPLETE_RESPONSE: "1" });
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client, { responseTimeoutMs: 20 });
        await strict_1.default.rejects(() => provider.complete(request()), (err) => err instanceof src_1.MakaiStreamError &&
            err.kind === "transport_error" &&
            err.message.includes("Timed out waiting for provider complete_response after 20ms for provider 'anthropic'") &&
            err.message.includes("model_ref='anthropic/anthropic-messages@opaque-model-ref-with%3Acolon'") &&
            err.message.includes("stream_id=") &&
            err.message.includes("message_id=") &&
            err.message.includes("Check network connectivity") &&
            err.diagnostics?.operation === "provider complete_response" &&
            err.diagnostics.timeout_ms === 20 &&
            err.diagnostics.provider_id === "anthropic" &&
            err.diagnostics.api === "anthropic-messages" &&
            err.diagnostics.model_id === "opaque-model-ref-with:colon" &&
            typeof err.diagnostics.stream_id === "string" &&
            err.diagnostics.message_id === err.diagnostics.stream_id);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.provider.stream yields ProviderStreamEvent sequence including message_end", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const events = await collect(provider.stream(request()));
        strict_1.default.deepEqual(events, [
            { type: "message_start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
            { type: "text_delta", delta: "hel" },
            { type: "thinking_delta", delta: "thinking" },
            { type: "text_delta", delta: "lo" },
            { type: "message_end", usage: { input: 3, output: 5 }, stop_reason: "end_turn" },
        ]);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run resolves with correct AgentRunResponse", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-result-test-"));
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: [
                    { type: "text", text: "Use this tool" },
                    { type: "tool_call", id: "call-1", name: "lookup", arguments_json: "{\"q\":\"makai\"}" },
                ],
                usage: { input: 7, output: 9 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "tool_use",
            }],
    }));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const result = await agent.run(request());
        strict_1.default.deepEqual(result.message.content, [
            { type: "text", text: "Use this tool" },
            { type: "tool_call", tool_call_id: "call-1", name: "lookup", arguments_json: "{\"q\":\"makai\"}" },
        ]);
        strict_1.default.deepEqual(result.usage, { input: 7, output: 9 });
        strict_1.default.equal(result.provider_id, "anthropic");
        strict_1.default.equal(result.api, "anthropic-messages");
        strict_1.default.equal(result.model_id, "claude-sonnet-4-5");
        strict_1.default.equal(result.stop_reason, "tool_use");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run executes tool_execute frames and continues awaiting result", async () => {
    const transport = {
        sent: [],
        frames: [
            { type: "agent_started", payload: {} },
            {
                type: "tool_execute",
                session_id: "testNanoIdSess1234567",
                message_id: "tool-request-1",
                sequence: 3,
                payload: { tool_call_id: "call-1", tool_name: "sum", args_json: "{\"a\":2,\"b\":3}" },
            },
            {
                type: "agent_result",
                payload: {
                    result_json: JSON.stringify({
                        messages: [{
                                role: "assistant",
                                content: "done",
                                usage: { input: 1, output: 1 },
                                provider: "anthropic",
                                api: "anthropic-messages",
                                model: "claude-sonnet-4-5",
                                stop_reason: "end_turn",
                            }],
                    }),
                },
            },
        ],
        send(frame) { this.sent.push(frame); },
        async nextFrameForSession(sessionId) {
            const frame = this.frames.shift();
            if (!frame)
                throw new Error("stream exhausted");
            return { session_id: sessionId, ...frame };
        },
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const result = await agent.run({
        ...request(),
        tools: [{
                name: "sum",
                description: "sum numbers",
                parameters_schema_json: "{}",
                execute: (args) => `sum=${Number(args.a) + Number(args.b)}`,
            }],
    });
    const toolResult = transport.sent.find((frame) => frame.type === "tool_result");
    strict_1.default.equal(toolResult?.session_id, "testNanoIdSess1234567");
    strict_1.default.equal(toolResult?.in_reply_to, "tool-request-1");
    strict_1.default.deepEqual(toolResult?.payload, {
        tool_call_id: "call-1",
        result_json: JSON.stringify([{ type: "text", text: "sum=5" }]),
        is_error: false,
    });
    strict_1.default.equal(result.message.content, "done");
});
(0, node_test_1.default)("agent.run timeout includes actionable diagnostics", async () => {
    const harness = await setupHarness({ MAKAI_TEST_SUPPRESS_AGENT_MESSAGE_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 20 });
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError &&
            err.kind === "transport_error" &&
            err.message.includes("Timed out waiting for agent result after 20ms for provider 'anthropic'") &&
            err.message.includes("session_id=testNanoIdSess1234567") &&
            err.message.includes("Verify the makai binary") &&
            err.diagnostics?.operation === "agent result" &&
            err.diagnostics.timeout_ms === 20 &&
            err.diagnostics.provider_id === "anthropic" &&
            err.diagnostics.api === "anthropic-messages" &&
            err.diagnostics.model_id === "opaque-model-ref-with:colon" &&
            err.diagnostics.session_id === "testNanoIdSess1234567");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run rejects non-NanoID session IDs before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run({ ...request(), options: { ...request().options, session_id: "session-1" } }), (err) => err instanceof TypeError && err.message === "request.options.session_id must be a 21-character alphanumeric NanoID for agent transport");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.stream rejects non-NanoID session IDs before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(async () => collect(agent.stream({ ...request(), options: { ...request().options, session_id: "session-1" } })), (err) => err instanceof TypeError && err.message === "request.options.session_id must be a 21-character alphanumeric NanoID for agent transport");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run rejects UUID session IDs", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run({ ...request(), options: { ...request().options, session_id: "01890f3e-7b62-7cc4-8f68-7a6f6a1b1234" } }), (err) => err instanceof TypeError && err.message === "request.options.session_id must be a 21-character alphanumeric NanoID for agent transport");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run accepts valid NanoID session IDs", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-nanoid-session-test-"));
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const nanoId = "abcABC123xyzXYZ789mno";
        await agent.run({ ...request(), options: { ...request().options, session_id: nanoId } });
        strict_1.default.equal(readLoggedRequests(harness.logPath)[0]?.session_id, nanoId);
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream yields agent lifecycle events in order", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const events = await collect(agent.stream(request()));
        strict_1.default.deepEqual(events.map((event) => event.type), [
            "agent_start",
            "turn_start",
            "message_start",
            "text_delta",
            "tool_execution_start",
            "tool_execution_end",
            "turn_end",
            "agent_end",
        ]);
        strict_1.default.deepEqual(events[0], { type: "agent_start", session_id: "testNanoIdSess1234567" });
        strict_1.default.deepEqual(events.at(-1), { type: "agent_end", usage: { input: 7, output: 9 }, stop_reason: "end_turn" });
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged[0]?.type, "agent_start");
        strict_1.default.equal(typeof logged[0]?.session_id, "string");
        strict_1.default.equal(logged[0]?.stream_id, undefined);
        strict_1.default.equal(logged[1]?.type, "agent_message");
        strict_1.default.equal(logged[1]?.session_id, logged[0]?.session_id);
        strict_1.default.equal(logged[1]?.stream_id, undefined);
        strict_1.default.deepEqual(JSON.parse((logged[1]?.payload).message_json).messages, request().messages);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run event fallback returns only final assistant turn content", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-final-turn-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "agent_start", session_id: "session-1" },
        { type: "turn_start" },
        { type: "message_start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
        { type: "text_delta", delta: "lookup first" },
        { type: "tool_call", tool_call_id: "call-1", name: "lookup", arguments_json: "{\"q\":\"makai\"}" },
        { type: "message_end", usage: { input: 5, output: 6 }, stop_reason: "tool_use" },
        { type: "turn_end", stop_reason: "tool_use" },
        { type: "turn_start" },
        { type: "message_start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
        { type: "text_delta", delta: "final answer" },
        { type: "message_end", usage: { input: 7, output: 9 }, stop_reason: "end_turn" },
        { type: "turn_end", stop_reason: "end_turn" },
        { type: "agent_end", usage: { input: 7, output: 9 }, stop_reason: "end_turn" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const result = await agent.run(request());
        strict_1.default.equal(result.message.content, "final answer");
        strict_1.default.deepEqual(result.usage, { input: 7, output: 9 });
        strict_1.default.equal(result.provider_id, "anthropic");
        strict_1.default.equal(result.api, "anthropic-messages");
        strict_1.default.equal(result.model_id, "claude-sonnet-4-5");
        strict_1.default.equal(result.stop_reason, "end_turn");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream surfaces provider error details on turn_end and agent_end", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-error-events-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "agent_start", session_id: "testNanoIdSess1234567" },
        { type: "turn_start" },
        { type: "turn_end", stop_reason: "error", error_message: "fixture stream failure" },
        { type: "agent_end", stop_reason: "error", error_message: "fixture stream failure" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const events = await collect(agent.stream(request()));
        const turnEnd = events.find((event) => event.type === "turn_end");
        strict_1.default.equal(turnEnd?.type, "turn_end");
        strict_1.default.equal(turnEnd.type === "turn_end" ? turnEnd.error_message : undefined, "fixture stream failure");
        strict_1.default.equal(turnEnd.type === "turn_end" ? turnEnd.stop_reason : undefined, "error");
        const agentEnd = events.at(-1);
        strict_1.default.equal(agentEnd?.type, "agent_end");
        strict_1.default.equal(agentEnd.type === "agent_end" ? agentEnd.error_message : undefined, "fixture stream failure");
        strict_1.default.equal(agentEnd.type === "agent_end" ? agentEnd.stop_reason : undefined, "error");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run surfaces provider error details from event fallback", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-error-fallback-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "agent_start", session_id: "testNanoIdSess1234567" },
        { type: "turn_start" },
        { type: "turn_end", stop_reason: "error", error_message: "fixture stream failure" },
        { type: "agent_end", stop_reason: "error", error_message: "fixture stream failure" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const result = await agent.run(request());
        strict_1.default.equal(result.stop_reason, "error");
        strict_1.default.equal(result.error_message, "fixture stream failure");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run surfaces provider error details from agent_result", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-error-result-test-"));
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        type: "result",
        stop_reason: "error",
        model: "fixture-model",
        api: "fixture-error-api",
        provider: "fixture",
        timestamp: 1,
        input: 0,
        output: 0,
        cache_read: 0,
        cache_write: 0,
        content: [],
        error_message: "invalid anthropic URL",
    }));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const result = await agent.run(request());
        strict_1.default.equal(result.stop_reason, "error");
        strict_1.default.equal(result.error_message, "invalid anthropic URL");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run translates auth_required provider failures into MakaiAuthRequiredError", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-auth-result-test-"));
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        type: "result",
        stop_reason: "error",
        model: "fixture-model",
        api: "fixture-error-api",
        provider: "fixture",
        timestamp: 1,
        input: 0,
        output: 0,
        cache_read: 0,
        cache_write: 0,
        content: [],
        error_message: "auth_required",
    }));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiAuthRequiredError &&
            err.code === "auth_required" &&
            err.provider_id === "fixture" &&
            err.message === "auth_required");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream yields turn_end detail then throws retryable auth error", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-auth-stream-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "agent_start", session_id: "testNanoIdSess1234567" },
        { type: "turn_start" },
        { type: "turn_end", stop_reason: "error", error_message: "auth_required" },
        { type: "agent_end", stop_reason: "error", error_message: "auth_required" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const events = [];
        await strict_1.default.rejects(async () => {
            for await (const event of agent.stream(request()))
                events.push(event);
        }, (err) => err instanceof src_1.MakaiAuthRequiredError &&
            err.code === "auth_required" &&
            err.provider_id === "anthropic" &&
            err.message === "auth_required");
        strict_1.default.deepEqual(events.map((event) => event.type), ["agent_start", "turn_start", "turn_end"]);
        const turnEnd = events.at(-1);
        strict_1.default.equal(turnEnd?.type, "turn_end");
        strict_1.default.equal(turnEnd.type === "turn_end" ? turnEnd.error_message : undefined, "auth_required");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run resolves auth retry provider from response provider_id for opaque model_ref", async () => {
    const transport = {
        sent: [],
        frames: [
            { type: "agent_started", payload: {} },
            {
                type: "agent_result",
                payload: {
                    result_json: JSON.stringify({
                        type: "result",
                        stop_reason: "error",
                        model: "fixture-model",
                        api: "fixture-error-api",
                        provider: "fixture-provider",
                        timestamp: 1,
                        input: 0,
                        output: 0,
                        cache_read: 0,
                        cache_write: 0,
                        content: [],
                        error_message: "auth_required",
                    }),
                },
            },
        ],
        send(frame) { this.sent.push(frame); },
        async nextFrameForSession(sessionId) {
            const frame = this.frames.shift();
            if (!frame)
                throw new Error("stream exhausted");
            return { session_id: sessionId, ...frame };
        },
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    await strict_1.default.rejects(() => agent.run({ model_ref: "opaque-model-ref-no-provider", messages: [{ role: "user", content: "hello" }] }), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "fixture-provider" && err.code === "auth_required");
});
(0, node_test_1.default)("client.agent.stream auto_once retries after yielded auth lifecycle events", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-auth-retry-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "agent_start", session_id: "testNanoIdSess1234567" },
        { type: "message_start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
        { type: "message_end", stop_reason: "end_turn" },
        { type: "turn_start" },
        { type: "turn_end", stop_reason: "error", error_message: "auth_required" },
        { type: "agent_end", stop_reason: "error", error_message: "auth_required", provider_id: "anthropic" },
    ]));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = [];
        await strict_1.default.rejects(async () => {
            for await (const event of handle.agent.stream(request()))
                events.push(event);
        }, (err) => err instanceof src_1.MakaiAuthRequiredError && err.code === "auth_required" && err.provider_id === "anthropic");
        strict_1.default.deepEqual(events.map((event) => event.type), [
            "agent_start", "message_start", "message_end", "turn_start", "turn_end",
            "agent_start", "message_start", "message_end", "turn_start", "turn_end",
        ]);
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_message").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run does not auth-retry after tools have executed", async () => {
    const transport = {
        sent: [],
        frames: [
            { type: "agent_started", payload: {} },
            {
                type: "tool_execute",
                session_id: "testNanoIdSess1234567",
                message_id: "tool-request-1",
                sequence: 3,
                payload: { tool_call_id: "call-1", tool_name: "sum", args_json: "{\"a\":2,\"b\":3}" },
            },
            {
                type: "agent_result",
                payload: {
                    result_json: JSON.stringify({
                        type: "result",
                        stop_reason: "error",
                        model: "fixture-model",
                        api: "fixture-error-api",
                        provider: "fixture-provider",
                        timestamp: 1,
                        input: 1,
                        output: 1,
                        cache_read: 0,
                        cache_write: 0,
                        content: [],
                        error_message: "auth_required",
                    }),
                },
            },
        ],
        send(frame) { this.sent.push(frame); },
        async nextFrameForSession(sessionId) {
            const frame = this.frames.shift();
            if (!frame)
                throw new Error("stream exhausted");
            return { session_id: sessionId, ...frame };
        },
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    await strict_1.default.rejects(() => agent.run({
        ...request(),
        tools: [{
                name: "sum",
                description: "sum numbers",
                parameters_schema_json: "{}",
                execute: (args) => `sum=${Number(args.a) + Number(args.b)}`,
            }],
    }), (err) => err instanceof src_1.MakaiAuthRequiredError && err.code === "auth_required" && err.provider_id === "fixture-provider");
    strict_1.default.equal(transport.sent.filter((frame) => frame.type === "agent_start").length, 1);
});
(0, node_test_1.default)("client.agent.run scopes provider-specific auth patterns to the matching provider", async () => {
    const transportFor = (api, provider) => ({
        sent: [],
        frames: [
            { type: "agent_started", payload: {} },
            {
                type: "agent_result",
                payload: {
                    result_json: JSON.stringify({
                        type: "result",
                        stop_reason: "error",
                        model: "fixture-model",
                        api,
                        provider,
                        timestamp: 1,
                        input: 0,
                        output: 0,
                        cache_read: 0,
                        cache_write: 0,
                        content: [],
                        error_message: "permission_error: scope denied",
                    }),
                },
            },
        ],
        send(frame) { this.sent.push(frame); },
        async nextFrameForSession(sessionId) {
            const frame = this.frames.shift();
            if (!frame)
                throw new Error("stream exhausted");
            return { session_id: sessionId, ...frame };
        },
    });
    const generic = (0, src_1.createMakaiAgentApi)(transportFor("fixture-error-api", "fixture-provider"));
    const completion = await generic.run(request());
    strict_1.default.equal(completion.stop_reason, "error");
    strict_1.default.equal(completion.error_message, "permission_error: scope denied");
    const remapped = (0, src_1.createMakaiAgentApi)(transportFor("openai-completions", "anthropic"));
    const remappedCompletion = await remapped.run(request());
    strict_1.default.equal(remappedCompletion.stop_reason, "error");
    strict_1.default.equal(remappedCompletion.error_message, "permission_error: scope denied");
    const anthropic = (0, src_1.createMakaiAgentApi)(transportFor("anthropic-messages", "anthropic"));
    await strict_1.default.rejects(() => anthropic.run(request()), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "anthropic");
});
(0, node_test_1.default)("client.agent.run matches human-readable auth failure messages", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-auth-readable-test-"));
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        type: "result",
        stop_reason: "error",
        model: "fixture-model",
        api: "fixture-error-api",
        provider: "fixture-provider",
        timestamp: 1,
        input: 0,
        output: 0,
        cache_read: 0,
        cache_write: 0,
        content: [],
        error_message: "Authentication required for provider fixture",
    }));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "fixture-provider");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream resolves auth retry provider from the streamed agent_end", async () => {
    const transport = {
        sent: [],
        frames: [
            { type: "agent_started", payload: {} },
            {
                type: "agent_result",
                payload: {
                    result_json: JSON.stringify({
                        type: "result",
                        stop_reason: "error",
                        model: "fixture-model",
                        api: "fixture-error-api",
                        provider: "fixture-provider",
                        timestamp: 1,
                        input: 0,
                        output: 0,
                        cache_read: 0,
                        cache_write: 0,
                        content: [],
                        error_message: "auth_required",
                    }),
                },
            },
        ],
        send(frame) { this.sent.push(frame); },
        async nextFrameForSession(sessionId) {
            const frame = this.frames.shift();
            if (!frame)
                throw new Error("stream exhausted");
            return { session_id: sessionId, ...frame };
        },
    };
    const agent = (0, src_1.createMakaiAgentApi)(transport);
    const events = [];
    await strict_1.default.rejects(async () => {
        for await (const event of agent.stream({ model_ref: "opaque-model-ref-no-provider", messages: [{ role: "user", content: "hello" }] })) {
            events.push(event);
        }
    }, (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "fixture-provider" && err.code === "auth_required");
});
(0, node_test_1.default)("client.agent.run event fallback applies API-scoped auth via terminal agent_end api", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-fallback-api-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "agent_start", session_id: "testNanoIdSess1234567" },
        { type: "turn_start" },
        { type: "turn_end", stop_reason: "error", error_message: "permission_error: scope denied" },
        { type: "agent_end", stop_reason: "error", error_message: "permission_error: scope denied", provider_id: "anthropic", api: "anthropic-messages" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "anthropic" && err.code === "auth_required");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.stream buffers incremental tool calls into one tool_call event", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-tool-buffer-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "message_start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
        { type: "toolcall_start", content_index: 0, id: "call-1", name: "lookup" },
        { type: "toolcall_delta", content_index: 0, delta: "{\"q\":" },
        { type: "toolcall_delta", content_index: 0, delta: "\"makai\"}" },
        { type: "toolcall_end", content_index: 0 },
        { type: "message_end", usage: { input: 3, output: 5 }, stop_reason: "tool_use" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_PROVIDER_EVENTS_PATH: eventsPath });
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const events = await collect(provider.stream(request()));
        strict_1.default.deepEqual(events.map((event) => event.type), ["message_start", "tool_call", "message_end"]);
        strict_1.default.deepEqual(events[1], {
            type: "tool_call",
            tool_call_id: "call-1",
            name: "lookup",
            arguments_json: "{\"q\":\"makai\"}",
        });
        const payload = readLoggedRequests(harness.logPath)[0]?.payload;
        strict_1.default.equal(payload.include_partial, false);
        strict_1.default.deepEqual(payload.model, {
            id: "opaque-model-ref-with:colon",
            name: "opaque-model-ref-with:colon",
            api: "anthropic-messages",
            provider: "anthropic",
            base_url: "",
        });
        strict_1.default.deepEqual(payload.context.messages, request().messages);
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("stream error paths emit one terminal error event", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-exec-error-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([{ type: "message_start" }, { type: "error", message: "boom", code: "provider_error" }]));
    const harness = await setupHarness({ MAKAI_TEST_PROVIDER_EVENTS_PATH: eventsPath });
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const events = await collect(provider.stream(request()));
        strict_1.default.equal(events.filter((event) => event.type === "error").length, 1);
        strict_1.default.deepEqual(events.at(-1), { type: "error", message: "boom", code: "provider_error" });
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("provider stream_error frames preserve MakaiStreamError code", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-stream-error-code-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([{ type: "stream_error", message: "login required", code: "auth_required" }]));
    const harness = await setupHarness({ MAKAI_TEST_PROVIDER_EVENTS_PATH: eventsPath });
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        await strict_1.default.rejects(async () => collect(provider.stream(request())), (err) => err instanceof src_1.MakaiStreamError && err.message === "login required" && err.code === "auth_required");
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("agent stream error paths emit one terminal error event", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-error-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([{ type: "agent_start" }, { type: "error", message: "agent boom", code: "provider_error" }]));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_EVENTS_PATH: eventsPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const events = await collect(agent.stream(request()));
        strict_1.default.equal(events.filter((event) => event.type === "error").length, 1);
        strict_1.default.equal(events.some((event) => event.type === "agent_end"), false);
        strict_1.default.deepEqual(events.at(-1), { type: "error", message: "agent boom", code: "provider_error" });
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run throws MakaiStreamError on malformed result_json", async () => {
    const harness = await setupHarness({ MAKAI_TEST_AGENT_MALFORMED_RESULT_JSON: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.message === "malformed JSON in result_json" && err.kind === "transport_error");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.stream throws MakaiStreamError on malformed event_json", async () => {
    const harness = await setupHarness({ MAKAI_TEST_AGENT_MALFORMED_EVENT_JSON: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(async () => collect(agent.stream(request())), (err) => err instanceof src_1.MakaiStreamError && err.message === "malformed JSON in event_json" && err.kind === "transport_error");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("createMakaiClient wires all namespaces correctly", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-client-wiring-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        strict_1.default.equal(typeof handle.auth.listProviders, "function");
        strict_1.default.equal(typeof handle.models.list, "function");
        strict_1.default.equal(typeof handle.provider.complete, "function");
        strict_1.default.equal(typeof handle.provider.stream, "function");
        strict_1.default.equal(typeof handle.agent.run, "function");
        strict_1.default.equal(typeof handle.agent.stream, "function");
        strict_1.default.deepEqual(await handle.auth.listProviders(), []);
        strict_1.default.equal(Array.isArray((await handle.models.list()).models), true);
        await collect(handle.provider.stream(request()));
        const streamRequest = readLoggedRequests(logPath).find((entry) => entry.type === "stream_request");
        strict_1.default.equal((streamRequest?.payload).options.auth_retry_policy, "auto_once");
        strict_1.default.equal((streamRequest?.payload).model.api, "anthropic-messages");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.stream normalizes top-level start frame to message_start", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-start-frame-test-"));
    const eventsPath = node_path_1.default.join(tmpDir, "events.json");
    node_fs_1.default.writeFileSync(eventsPath, JSON.stringify([
        { type: "start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
        { type: "text_delta", delta: "hello" },
        { type: "done", usage: { input: 3, output: 5 }, stop_reason: "end_turn" },
    ]));
    const harness = await setupHarness({ MAKAI_TEST_PROVIDER_EVENTS_PATH: eventsPath });
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const events = await collect(provider.stream(request()));
        strict_1.default.deepEqual(events, [
            { type: "message_start", provider_id: "anthropic", api: "anthropic-messages", model_id: "claude-sonnet-4-5" },
            { type: "text_delta", delta: "hello" },
            { type: "message_end", usage: { input: 3, output: 5 }, stop_reason: "end_turn" },
        ]);
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.complete auto_once retries on auth_required nack and succeeds", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-complete-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const result = await handle.provider.complete(request());
        strict_1.default.deepEqual(result.message.content, [{ type: "text", text: "hello" }]);
        const logged = readLoggedRequests(logPath);
        const completeRequests = logged.filter((entry) => entry.type === "complete_request");
        strict_1.default.equal(completeRequests.length, 2);
        strict_1.default.equal(completeRequests[0]?.type, "complete_request");
        strict_1.default.equal(completeRequests[1]?.type, "complete_request");
        const loginStarts = logged.filter((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(loginStarts.length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.stream auto_once retries on auth_required nack and yields events", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = await collect(handle.provider.stream(request()));
        strict_1.default.equal(events[0]?.type, "message_start");
        const logged = readLoggedRequests(logPath);
        const streamRequests = logged.filter((entry) => entry.type === "stream_request");
        strict_1.default.equal(streamRequests.length, 2);
        const loginStarts = logged.filter((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(loginStarts.length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.complete auto_once retries at most once when auth_required persists", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-complete-limit-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ALWAYS: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete(request()), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.stream auto_once retries at most once when auth_required persists", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-stream-limit-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ALWAYS: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await strict_1.default.rejects(async () => collect(handle.provider.stream(request())), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "stream_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run auto_once retries on auth_required nack and succeeds", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-agent-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AGENT_RESULT_PATH: resultPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const result = await handle.agent.run(request());
        strict_1.default.equal(result.message.content, "ok");
        const logged = readLoggedRequests(logPath);
        const agentStarts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(agentStarts.length, 2);
        const loginStarts = logged.filter((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(loginStarts.length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run auto_once retries at most once when auth_required persists", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-agent-limit-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ALWAYS: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await strict_1.default.rejects(() => handle.agent.run(request()), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run auto_once uses fresh session_id on retry", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-agent-session-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AGENT_RESULT_PATH: resultPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await handle.agent.run(request());
        const logged = readLoggedRequests(logPath);
        const agentStarts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(agentStarts.length, 2);
        const firstSessionId = agentStarts[0]?.session_id;
        const secondSessionId = agentStarts[1]?.session_id;
        strict_1.default.equal(firstSessionId, "testNanoIdSess1234567");
        strict_1.default.notEqual(secondSessionId, firstSessionId);
        strict_1.default.match(secondSessionId, /^[0-9A-Za-z]{21}$/);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("manual auth_retry_policy does not retry on auth_required", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-manual-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete(request()), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required" && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        const completeRequests = logged.filter((entry) => entry.type === "complete_request");
        strict_1.default.equal(completeRequests.length, 1);
        const loginStarts = logged.filter((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(loginStarts.length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("auto_once normalizes login failure to auth_required with partial handlers", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-fail-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRES_PROMPT: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once", handlers: { onEvent: () => undefined } },
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete(request()), (err) => err instanceof src_1.MakaiAuthRequiredError && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        const completeRequests = logged.filter((entry) => entry.type === "complete_request");
        strict_1.default.equal(completeRequests.length, 1);
        const loginStarts = logged.filter((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(loginStarts.length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("manual policy backfills provider_id on auth_required nack missing provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-manual-backfill-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete(request()), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required" && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("manual policy backfills provider_id on auth_required stream nack missing provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-manual-backfill-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await strict_1.default.rejects(async () => {
            for await (const _event of handle.provider.stream(request())) {
            }
        }, (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required" && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "stream_request").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("manual policy backfills provider_id on agent run auth_required nack missing provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-manual-backfill-agent-run-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await strict_1.default.rejects(() => handle.agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required" && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("manual policy backfills provider_id on agent stream auth_required nack missing provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-manual-backfill-agent-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await strict_1.default.rejects(async () => {
            for await (const _event of handle.agent.stream(request())) {
            }
        }, (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required" && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("manual policy backfills provider_id for non-canonical model_ref on auth_required nack", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-manual-backfill-noncanon-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete({
            model_ref: "anthropic/anthropic-messages@opaque-model-ref-with:colon",
            messages: [{ role: "user", content: "hello" }],
        }), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required" && err.provider_id === "anthropic");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream auto_once retries on auth_required nack and yields events", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-agent-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = await collect(handle.agent.stream(request()));
        strict_1.default.equal(events[0]?.type, "agent_start");
        const logged = readLoggedRequests(logPath);
        const agentStarts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(agentStarts.length, 2);
        const loginStarts = logged.filter((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(loginStarts.length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream auto_once uses fresh session_id on retry", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-agent-stream-session-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await collect(handle.agent.stream(request()));
        const logged = readLoggedRequests(logPath);
        const agentStarts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(agentStarts.length, 2);
        const firstSessionId = agentStarts[0]?.session_id;
        const secondSessionId = agentStarts[1]?.session_id;
        strict_1.default.equal(firstSessionId, "testNanoIdSess1234567");
        strict_1.default.notEqual(secondSessionId, firstSessionId);
        strict_1.default.match(secondSessionId, /^[0-9A-Za-z]{21}$/);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream auto_once retries at most once when auth_required persists", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-agent-stream-limit-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ALWAYS: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await strict_1.default.rejects(async () => collect(handle.agent.stream(request())), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("per-request manual auth_retry_policy overrides client auto_once", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-override-manual-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete({ ...request(), options: { auth_retry_policy: "manual" } }), (err) => err instanceof src_1.MakaiStreamError && err.code === "auth_required");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("per-request auto_once auth_retry_policy overrides client manual", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-override-auto-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        const result = await handle.provider.complete({ ...request(), options: { auth_retry_policy: "auto_once" } });
        strict_1.default.deepEqual(result.message.content, [{ type: "text", text: "hello" }]);
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.complete auto_once retries when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-no-pid-complete-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const result = await handle.provider.complete(request());
        strict_1.default.deepEqual(result.message.content, [{ type: "text", text: "hello" }]);
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.stream auto_once retries when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-no-pid-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = await collect(handle.provider.stream(request()));
        strict_1.default.equal(events[0]?.type, "message_start");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "stream_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run auto_once retries when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-no-pid-agent-run-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1", MAKAI_TEST_AGENT_RESULT_PATH: resultPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const result = await handle.agent.run(request());
        strict_1.default.equal(result.message.content, "ok");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream auto_once retries when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-no-pid-agent-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = await collect(handle.agent.stream(request()));
        strict_1.default.equal(events[0]?.type, "agent_start");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.complete auto_once retries for non-canonical model_ref when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-noncanon-complete-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const result = await handle.provider.complete({
            model_ref: "anthropic/anthropic-messages@opaque-model-ref-with:colon",
            messages: [{ role: "user", content: "hello" }],
        });
        strict_1.default.deepEqual(result.message.content, [{ type: "text", text: "hello" }]);
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "complete_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
        const login = logged.find((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(login?.payload?.provider_id, "anthropic");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.provider.stream auto_once retries for non-canonical model_ref when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-noncanon-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = await collect(handle.provider.stream({
            model_ref: "anthropic/anthropic-messages@opaque-model-ref-with:colon",
            messages: [{ role: "user", content: "hello" }],
        }));
        strict_1.default.equal(events[0]?.type, "message_start");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "stream_request").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
        const login = logged.find((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(login?.payload?.provider_id, "anthropic");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run auto_once retries for non-canonical model_ref when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-noncanon-agent-run-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1", MAKAI_TEST_AGENT_RESULT_PATH: resultPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const result = await handle.agent.run({
            model_ref: "anthropic/anthropic-messages@opaque-model-ref-with:colon",
            messages: [{ role: "user", content: "hello" }],
        });
        strict_1.default.equal(result.message.content, "ok");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
        const login = logged.find((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(login?.payload?.provider_id, "anthropic");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream auto_once retries for non-canonical model_ref when error lacks provider_id", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-retry-noncanon-agent-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AUTH_REQUIRED_NO_PROVIDER_ID: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        const events = await collect(handle.agent.stream({
            model_ref: "anthropic/anthropic-messages@opaque-model-ref-with:colon",
            messages: [{ role: "user", content: "hello" }],
        }));
        strict_1.default.equal(events[0]?.type, "agent_start");
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(logged.filter((entry) => entry.type === "auth_login_start").length, 1);
        const login = logged.find((entry) => entry.type === "auth_login_start");
        strict_1.default.equal(login?.payload?.provider_id, "anthropic");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("agent_start payload includes session_id (#198)", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await collect(agent.stream(request()));
        const logged = readLoggedRequests(harness.logPath);
        const start = logged.find((entry) => entry.type === "agent_start");
        strict_1.default.ok(start);
        const payload = start?.payload;
        strict_1.default.equal(payload.session_id, "testNanoIdSess1234567");
        strict_1.default.equal(payload.resume_session_id, "testNanoIdSess1234567");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("acceptance: OAuth, model discovery, and provider execution share provider-agnostic client path", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-acceptance-provider-path-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const authStatePath = node_path_1.default.join(tmpDir, "auth-state.json");
    const model = {
        model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
        model_id: "claude-sonnet-4-5",
        display_name: "Claude Sonnet 4.5",
        provider_id: "anthropic",
        api: "anthropic-messages",
        auth_status: "authenticated",
        lifecycle: "stable",
        capabilities: ["chat", "streaming", "tools", "reasoning"],
        source: "dynamic",
    };
    const modelsPath = node_path_1.default.join(tmpDir, "models.json");
    node_fs_1.default.writeFileSync(modelsPath, JSON.stringify({ models: [model], fetched_at_ms: 1, cache_max_age_ms: 300000 }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: {
            ...process.env,
            MAKAI_TEST_REQUEST_LOG: logPath,
            MAKAI_TEST_AUTH_STATE_PATH: authStatePath,
            MAKAI_TEST_AUTH_REQUIRES_PROMPT: "1",
            MAKAI_TEST_MODELS_RESPONSE_PATH: modelsPath,
        },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "manual" },
    });
    try {
        await handle.auth.login("test-fixture", { onPrompt: () => "ok" });
        const listed = await handle.models.list({ provider_id: "anthropic" });
        strict_1.default.deepEqual(listed.models[0], model);
        const selectedModelRef = listed.models[0].model_ref;
        const result = await handle.provider.complete({
            model_ref: selectedModelRef,
            messages: [{ role: "user", content: "hello" }],
        });
        strict_1.default.deepEqual(result.message.content, [{ type: "text", text: "hello" }]);
        const logged = readLoggedRequests(logPath);
        strict_1.default.equal(logged.some((entry) => entry.type === "auth_login_start"), true);
        strict_1.default.equal(logged.some((entry) => entry.type === "models_request"), true);
        const completePayload = logged.find((entry) => entry.type === "complete_request")?.payload;
        strict_1.default.equal(completePayload.model_ref, selectedModelRef);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("acceptance: provider and agent model lists have identical output shape", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-acceptance-model-shape-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const model = {
        model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
        model_id: "claude-sonnet-4-5",
        display_name: "Claude Sonnet 4.5",
        provider_id: "anthropic",
        api: "anthropic-messages",
        auth_status: "authenticated",
        lifecycle: "stable",
        capabilities: ["chat", "streaming"],
        source: "dynamic",
    };
    const modelsPath = node_path_1.default.join(tmpDir, "models.json");
    node_fs_1.default.writeFileSync(modelsPath, JSON.stringify({ models: [model], fetched_at_ms: 7, cache_max_age_ms: 300000 }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_MODELS_RESPONSE_PATH: modelsPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
    });
    try {
        strict_1.default.deepEqual(await handle.models.list(), await handle.agent.models.list());
        strict_1.default.equal(readLoggedRequests(logPath).filter((entry) => entry.type === "models_request").length, 2);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("acceptance: provider and agent execution accept the same model_ref", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-acceptance-shared-model-ref-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AGENT_RESULT_PATH: resultPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
    });
    try {
        const shared = request();
        await handle.provider.complete(shared);
        await handle.agent.run(shared);
        const logged = readLoggedRequests(logPath);
        const completePayload = logged.find((entry) => entry.type === "complete_request")?.payload;
        const agentMessagePayload = logged.find((entry) => entry.type === "agent_message")?.payload;
        strict_1.default.equal(completePayload.model_ref, shared.model_ref);
        strict_1.default.equal(JSON.parse(agentMessagePayload.message_json).model_ref, shared.model_ref);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("provider.complete rejects model_ref exceeding 4096 characters before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longModelRef = "a".repeat(4097);
        await strict_1.default.rejects(() => provider.complete({ model_ref: longModelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref exceeds maximum length of 4096 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.stream rejects model_ref exceeding 4096 characters before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longModelRef = "a".repeat(4097);
        await strict_1.default.rejects(async () => collect(provider.stream({ model_ref: longModelRef, messages: [{ role: "user", content: "hi" }] })), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref exceeds maximum length of 4096 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("agent.run rejects model_ref exceeding 4096 characters before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const longModelRef = "a".repeat(4097);
        await strict_1.default.rejects(() => agent.run({ model_ref: longModelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref exceeds maximum length of 4096 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("agent.stream rejects model_ref exceeding 4096 characters before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const longModelRef = "a".repeat(4097);
        await strict_1.default.rejects(async () => collect(agent.stream({ model_ref: longModelRef, messages: [{ role: "user", content: "hi" }] })), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref exceeds maximum length of 4096 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete rejects canonical model_ref with provider segment exceeding 256 characters", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longProvider = "a".repeat(257);
        const modelRef = `${longProvider}/anthropic-messages@claude-sonnet-4-5`;
        await strict_1.default.rejects(() => provider.complete({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref provider segment exceeds maximum length of 256 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.stream rejects canonical model_ref with api segment exceeding 256 characters", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longApi = "a".repeat(257);
        const modelRef = `anthropic/${longApi}@claude-sonnet-4-5`;
        await strict_1.default.rejects(async () => collect(provider.stream({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] })), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref api segment exceeds maximum length of 256 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("agent.run rejects canonical model_ref with api segment exceeding 256 characters", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const longApi = "a".repeat(257);
        const modelRef = `anthropic/${longApi}@claude-sonnet-4-5`;
        await strict_1.default.rejects(() => agent.run({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref api segment exceeds maximum length of 256 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete rejects fallback model_ref with provider segment exceeding 256 characters", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longProvider = "a".repeat(257);
        const modelRef = `${longProvider}/anthropic-messages@model:id`;
        await strict_1.default.rejects(() => provider.complete({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref provider segment exceeds maximum length of 256 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete rejects fallback model_ref with api segment exceeding 256 characters", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longApi = "a".repeat(257);
        const modelRef = `anthropic/${longApi}@model:id`;
        await strict_1.default.rejects(() => provider.complete({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref api segment exceeds maximum length of 256 characters");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete rejects opaque model_ref exceeding 512 characters before transport I/O", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const longModelRef = "x".repeat(513);
        await strict_1.default.rejects(() => provider.complete({ model_ref: longModelRef, messages: [{ role: "user", content: "hi" }] }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_ref exceeds maximum length of 512 characters for opaque refs");
        strict_1.default.deepEqual(readLoggedRequests(harness.logPath), []);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete accepts opaque model_ref at exactly 512 characters", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const modelRef = "x".repeat(512);
        strict_1.default.equal(modelRef.length, 512);
        await provider.complete({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] });
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged.length, 1);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider.complete accepts canonical model_ref with max valid segment sizes", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const modelRef = `${"p".repeat(256)}/${"a".repeat(256)}@${"m".repeat(512)}`;
        strict_1.default.equal(modelRef.length, 256 + 1 + 256 + 1 + 512);
        await provider.complete({ model_ref: modelRef, messages: [{ role: "user", content: "hi" }] });
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged.length, 1);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.provider.complete surfaces error_message from error results", async () => {
    const errorResult = {
        role: "assistant",
        content: [{ type: "text", text: "" }],
        usage: { input: 0, output: 0, cache_read: 0, cache_write: 0 },
        provider_id: "anthropic",
        api: "anthropic-messages",
        model_id: "claude-sonnet-4-5",
        stop_reason: "error",
        error_message: "QueueFull",
    };
    const resultPath = node_path_1.default.join(node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-err-result-")), "result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify(errorResult));
    const harness = await setupHarness({ MAKAI_TEST_PROVIDER_RESULT_PATH: resultPath });
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client);
        const result = await provider.complete(request());
        strict_1.default.equal(result.stop_reason, "error");
        strict_1.default.equal(result.error_message, "QueueFull");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run surfaces error_message from error agent results", async () => {
    const errorResult = {
        messages: [{
                role: "assistant",
                content: [{ type: "text", text: "" }],
                usage: { input: 0, output: 0, cache_read: 0, cache_write: 0 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "error",
                error_message: "QueueFull",
            }],
    };
    const resultPath = node_path_1.default.join(node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-err-result-")), "result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify(errorResult));
    const harness = await setupHarness({ MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const result = await agent.run(request());
        strict_1.default.equal(result.stop_reason, "error");
        strict_1.default.equal(result.error_message, "QueueFull");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run tears down the session so the same session_id can be reused", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const first = await agent.run(request());
        strict_1.default.equal(first.stop_reason, "end_turn");
        const second = await agent.run(request());
        strict_1.default.equal(second.stop_reason, "end_turn");
        const logged = readLoggedRequests(harness.logPath);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        for (const stop of stops) {
            strict_1.default.equal(stop.session_id, "testNanoIdSess1234567");
            strict_1.default.equal(stop.sequence, 3);
            strict_1.default.equal(stop.payload.reason, "completed");
        }
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run sends agent_stop when the run fails and the id stays reusable", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-stop-error-"));
    const errorPath = node_path_1.default.join(tmpDir, "agent-error.json");
    node_fs_1.default.writeFileSync(errorPath, JSON.stringify({ code: "provider_error", message: "fixture agent failure" }));
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_AGENT_ERROR_PATH: errorPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.message === "fixture agent failure");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        strict_1.default.equal(stops[0]?.session_id, "testNanoIdSess1234567");
        strict_1.default.deepEqual(stops.map((entry) => entry.sequence), [2, 3]);
        strict_1.default.equal((stops.at(-1)?.payload).reason, "completed");
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.message === "fixture agent failure");
        const stopsAfterRetry = (await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 4))
            .filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stopsAfterRetry.length, 4);
        strict_1.default.deepEqual(stopsAfterRetry.slice(2).map((entry) => entry.sequence), [2, 3]);
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run probes both counter states when an uncorrelated runtime error may be an admission failure (#210 gap 7)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_ADMISSION_RUNTIME_ERROR: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.message === "admission allocation failure");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 1);
        strict_1.default.equal(stops[0]?.sequence, 2);
        strict_1.default.equal((stops[0]?.payload).reason, "completed");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.stream tears down the session when the consumer closes the iterator early", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const events = [];
        for await (const event of agent.stream(request())) {
            events.push(event);
            if (event.type === "agent_end")
                break;
        }
        strict_1.default.equal(events.at(-1)?.type, "agent_end");
        const result = await agent.run(request());
        strict_1.default.equal(result.stop_reason, "end_turn");
        const logged = readLoggedRequests(harness.logPath);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        for (const stop of stops) {
            strict_1.default.equal(stop.session_id, "testNanoIdSess1234567");
            strict_1.default.equal(stop.sequence, 3);
            strict_1.default.equal(stop.payload.reason, "completed");
        }
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run does not stop a session owned by another run after agent_busy", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_SUPPRESS_AGENT_MESSAGE_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 500 });
        const first = agent.run(request());
        await new Promise((resolve) => setTimeout(resolve, 25));
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.code === "agent_busy" && err.message === "session already exists");
        await strict_1.default.rejects(() => first, (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        strict_1.default.equal(stops[0]?.session_id, "testNanoIdSess1234567");
        strict_1.default.deepEqual(stops.map((entry) => entry.sequence), [2, 3]);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("concurrent client.agent.run on one session id: duplicate is rejected, established run completes", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 5000 });
        const establishedRun = agent.run(request());
        const duplicateRun = agent.run(request());
        const [established, duplicate] = await Promise.allSettled([establishedRun, duplicateRun]);
        strict_1.default.equal(established.status, "fulfilled");
        strict_1.default.equal(established.value.stop_reason, "end_turn");
        strict_1.default.equal(duplicate.status, "rejected");
        const reason = duplicate.reason;
        strict_1.default.ok(reason instanceof src_1.MakaiStreamError && reason.code === "agent_busy" && reason.message === "session already exists", `unexpected duplicate rejection: ${String(reason)}`);
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 1);
        strict_1.default.equal(stops[0]?.sequence, 3);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("concurrent client.agent.run duplicate receives the agent_error-shaped agent_busy rejection", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_AGENT_BUSY_AS_ERROR: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 5000 });
        const establishedRun = agent.run(request());
        const duplicateRun = agent.run(request());
        const [established, duplicate] = await Promise.allSettled([establishedRun, duplicateRun]);
        strict_1.default.equal(established.status, "fulfilled");
        strict_1.default.equal(established.value.stop_reason, "end_turn");
        strict_1.default.equal(duplicate.status, "rejected");
        const reason = duplicate.reason;
        strict_1.default.ok(reason instanceof src_1.MakaiStreamError && reason.code === "agent_busy" && reason.message === "session already exists", `unexpected duplicate rejection: ${String(reason)}`);
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 1);
        strict_1.default.equal(stops[0]?.sequence, 3);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("concurrent client.agent.stream on one session id: duplicate is rejected, established stream completes", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 5000 });
        const establishedStream = collect(agent.stream(request()));
        const duplicateStream = collect(agent.stream(request()));
        const [established, duplicate] = await Promise.allSettled([establishedStream, duplicateStream]);
        strict_1.default.equal(established.status, "fulfilled");
        const events = established.value;
        strict_1.default.equal(events.at(-1)?.type, "agent_end");
        strict_1.default.equal(duplicate.status, "rejected");
        const reason = duplicate.reason;
        strict_1.default.ok(reason instanceof src_1.MakaiStreamError && reason.code === "agent_busy" && reason.message === "session already exists", `unexpected duplicate rejection: ${String(reason)}`);
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 1);
        strict_1.default.equal(stops[0]?.sequence, 3);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run auth retry stops the abandoned session", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-stop-auth-retry-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_AGENT_RESULT_PATH: resultPath, MAKAI_TEST_TRACK_AGENT_SESSIONS: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await handle.agent.run(request());
        const logged = readLoggedRequests(logPath);
        const agentStarts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(agentStarts.length, 2);
        const firstSessionId = agentStarts[0]?.session_id;
        const secondSessionId = agentStarts[1]?.session_id;
        strict_1.default.equal(firstSessionId, "testNanoIdSess1234567");
        strict_1.default.match(secondSessionId, /^[0-9A-Za-z]{21}$/);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        strict_1.default.equal(stops[0]?.session_id, firstSessionId);
        strict_1.default.equal(stops[0]?.sequence, 2);
        strict_1.default.equal((stops[0]?.payload).reason, "completed");
        strict_1.default.equal(stops[1]?.session_id, secondSessionId);
        strict_1.default.equal(stops[1]?.sequence, 3);
        strict_1.default.equal((stops[1]?.payload).reason, "completed");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.stream tears down the session and drains the trailing terminal frame", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-stop-stream-"));
    const resultPath = node_path_1.default.join(tmpDir, "agent-result.json");
    node_fs_1.default.writeFileSync(resultPath, JSON.stringify({
        messages: [{
                role: "assistant",
                content: "ok",
                usage: { input: 1, output: 1 },
                provider: "anthropic",
                api: "anthropic-messages",
                model: "claude-sonnet-4-5",
                stop_reason: "end_turn",
            }],
    }));
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_AGENT_RESULT_PATH: resultPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        const events = await collect(agent.stream(request()));
        strict_1.default.equal(events.at(-1)?.type, "agent_end");
        const result = await agent.run(request());
        strict_1.default.equal(result.message.content, "ok");
        strict_1.default.equal(result.stop_reason, "end_turn");
        const logged = readLoggedRequests(harness.logPath);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        for (const stop of stops) {
            strict_1.default.equal(stop.session_id, "testNanoIdSess1234567");
            strict_1.default.equal(stop.sequence, 3);
            strict_1.default.equal(stop.payload.reason, "completed");
        }
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run does not stop a caller-supplied session when the start outcome is unknown (§6.1, #205)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_SUPPRESS_AGENT_START_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 300 });
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = readLoggedRequests(harness.logPath);
        const starts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(starts.length, 1);
        strict_1.default.equal(starts[0]?.session_id, "testNanoIdSess1234567");
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_stop").length, 0);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.code === "agent_busy" && err.message === "session already exists");
        const loggedAfterRetry = readLoggedRequests(harness.logPath);
        strict_1.default.equal(loggedAfterRetry.filter((entry) => entry.type === "agent_start").length, 2);
        strict_1.default.equal(loggedAfterRetry.filter((entry) => entry.type === "agent_stop").length, 0);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run still stops a client-generated session when the start outcome is unknown (#205)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_SUPPRESS_AGENT_START_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 300 });
        await strict_1.default.rejects(() => agent.run({ ...request(), options: { temperature: 0.2 } }), (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        const starts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(starts.length, 1);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 1);
        strict_1.default.equal(stops[0]?.session_id, starts[0]?.session_id);
        strict_1.default.equal(stops[0]?.sequence, 2);
        strict_1.default.equal((stops[0]?.payload).reason, "completed");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.stream does not stop a caller-supplied session when the start outcome is unknown (#205)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_SUPPRESS_AGENT_START_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 300 });
        await strict_1.default.rejects(() => collect(agent.stream(request())), (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_start").length, 1);
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_stop").length, 0);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run auth-retry attempt with a lost start reply still stops its SDK-generated session (#205)", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-stop-retry-lost-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_AUTH_REQUIRED_ONCE: "1", MAKAI_TEST_SUPPRESS_AGENT_START_RESPONSE: "1", MAKAI_TEST_TRACK_AGENT_SESSIONS: "1" },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 300,
        auth: { auth_retry_policy: "auto_once" },
    });
    try {
        await strict_1.default.rejects(() => handle.agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = await waitForLoggedRequests(logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const starts = logged.filter((entry) => entry.type === "agent_start");
        strict_1.default.equal(starts.length, 2);
        strict_1.default.equal(starts[0]?.session_id, "testNanoIdSess1234567");
        const retryId = starts[1]?.session_id;
        strict_1.default.match(retryId, /^[0-9A-Za-z]{21}$/);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        strict_1.default.equal(stops[0]?.session_id, "testNanoIdSess1234567");
        strict_1.default.equal(stops[0]?.sequence, 2);
        strict_1.default.equal(stops[1]?.session_id, retryId);
        strict_1.default.equal(stops[1]?.sequence, 2);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("session teardown drain consumes terminal-shaped frames before the current stop's reply (#205)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const frames = [
        { type: "agent_error", session_id: sessionId, message_id: "m-settlement", sequence: 4, timestamp: 1, version: 1, payload: { code: "internal_error", message: "fixture loop failure" } },
        { type: "agent_stopped", session_id: sessionId, message_id: "m-stale", sequence: 9, timestamp: 1, version: 1, in_reply_to: "earlier-stop-message-id", payload: {} },
        { type: "agent_stopped", session_id: sessionId, message_id: "m-current", sequence: 9, timestamp: 1, version: 1, in_reply_to: "current-stop-message-id", payload: {} },
    ];
    const consumed = [];
    const transport = {
        nextFrameForSession: async (sid, timeoutMs) => {
            const frame = frames.shift();
            if (!frame)
                throw new Error(`timed out waiting for frame for session ${sid} after ${timeoutMs ?? 1000}ms`);
            consumed.push(String(frame.type));
            return frame;
        },
    };
    await (0, src_1.drainSessionFramesUntilQuiescent)(transport, sessionId, 20, 500, { stopReplyTo: "current-stop-message-id" });
    strict_1.default.deepEqual(consumed, ["agent_error", "agent_stopped", "agent_stopped"]);
    strict_1.default.equal(frames.length, 0);
});
(0, node_test_1.default)("client.agent.run drains the failure pair's settlement before the error surfaces, so an immediate same-id run is not poisoned (#205)", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-failure-pair-"));
    const pairPath = node_path_1.default.join(tmpDir, "failure-pair.json");
    node_fs_1.default.writeFileSync(pairPath, JSON.stringify({ code: "internal_error", message: "fixture loop failure" }));
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_AGENT_FAILURE_PAIR_PATH: pairPath });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.message === "fixture loop failure" && err.code === "internal_error");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        strict_1.default.equal(logged.filter((entry) => entry.type === "agent_stop").length, 1);
        strict_1.default.equal((logged.find((entry) => entry.type === "agent_stop")?.payload).reason, "completed");
        const second = await agent.run(request());
        strict_1.default.equal(second.stop_reason, "end_turn");
        const loggedAfter = await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const stops = loggedAfter.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        for (const stop of stops) {
            strict_1.default.equal(stop.session_id, "testNanoIdSess1234567");
            strict_1.default.equal(stop.sequence, 3);
            strict_1.default.equal(stop.payload.reason, "completed");
        }
    }
    finally {
        await harness.cleanup();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.agent.run rolls the sequence tracker back on a correlated agent_message rejection and retries with the right sequence (#210 gap 7)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_REJECT_FIRST_AGENT_MESSAGE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client);
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.code === "invalid_request" && err.message === "invalid sequence");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.some((entry) => entry.type === "agent_stop"));
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 1);
        strict_1.default.equal(stops[0]?.session_id, "testNanoIdSess1234567");
        strict_1.default.equal(stops[0]?.sequence, 2);
        const second = await agent.run(request());
        strict_1.default.equal(second.stop_reason, "end_turn");
        const loggedAfter = await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const stopsAfter = loggedAfter.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stopsAfter.length, 2);
        strict_1.default.equal(stopsAfter[1]?.sequence, 3);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.run probes both counter states after an unknown message outcome, so timeout-then-retry on a caller-supplied id works (#210 gap 7)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_SUPPRESS_AGENT_MESSAGE_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 300 });
        await strict_1.default.rejects(() => agent.run(request()), (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        strict_1.default.equal(stops[0]?.session_id, "testNanoIdSess1234567");
        strict_1.default.deepEqual(stops.map((entry) => entry.sequence), [2, 3]);
        const second = await agent.run(request());
        strict_1.default.equal(second.stop_reason, "end_turn");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("client.agent.stream probes both counter states after an unknown message outcome (#210 gap 7)", async () => {
    const harness = await setupHarness({ MAKAI_TEST_TRACK_AGENT_SESSIONS: "1", MAKAI_TEST_SUPPRESS_AGENT_MESSAGE_RESPONSE: "1" });
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { responseTimeoutMs: 300 });
        await strict_1.default.rejects(() => collect(agent.stream(request())), (err) => err instanceof src_1.MakaiStreamError && err.kind === "transport_error");
        const logged = await waitForLoggedRequests(harness.logPath, (entries) => entries.filter((entry) => entry.type === "agent_stop").length >= 2);
        const stops = logged.filter((entry) => entry.type === "agent_stop");
        strict_1.default.equal(stops.length, 2);
        strict_1.default.deepEqual(stops.map((entry) => entry.sequence), [2, 3]);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("stopAgentWithSequenceProbe retries at the post-send sequence on a correlated invalid_request and settles at the accepted value (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const sentStops = [];
    const replies = [];
    const waitCorrelates = [];
    const transport = {
        send: (frame) => {
            if (frame.type !== "agent_stop")
                return;
            const messageId = String(frame.message_id);
            sentStops.push({ sequence: frame.sequence, messageId });
            if (sentStops.length === 1) {
                replies.push({ type: "agent_error", session_id: sessionId, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: messageId, payload: { code: "invalid_request", message: "invalid sequence" } });
            }
            else {
                replies.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: messageId, payload: {} });
            }
        },
        nextFrameForSession: async (sid, timeoutMs, wait) => {
            waitCorrelates.push(wait?.correlate);
            const frame = replies.shift();
            if (!frame)
                throw new Error(`timed out waiting for frame for session ${sid} after ${timeoutMs ?? 1000}ms`);
            return frame;
        },
    };
    const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 20, 500);
    strict_1.default.equal(acceptedAt, 3);
    strict_1.default.deepEqual(sentStops.map((stop) => stop.sequence), [2, 3]);
    strict_1.default.equal(waitCorrelates[0], sentStops[0]?.messageId);
    strict_1.default.ok(waitCorrelates.slice(1).includes(sentStops[1]?.messageId));
});
(0, node_test_1.default)("stopAgentWithSequenceProbe accepts the pre-send state without a retry and recognizes the nack rejection shape (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    {
        const replies = [];
        const sentStops = [];
        const transport = {
            send: (frame) => {
                if (frame.type !== "agent_stop")
                    return;
                sentStops.push(frame.sequence);
                replies.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
            },
            nextFrameForSession: async () => {
                const frame = replies.shift();
                if (!frame)
                    throw new Error("timed out");
                return frame;
            },
        };
        const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 20, 500);
        strict_1.default.equal(acceptedAt, 2);
        strict_1.default.deepEqual(sentStops, [2]);
    }
    {
        const replies = [];
        const sentStops = [];
        const transport = {
            send: (frame) => {
                if (frame.type !== "agent_stop")
                    return;
                sentStops.push(frame.sequence);
                if (sentStops.length === 1) {
                    replies.push({ type: "nack", session_id: sessionId, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { error_code: "invalid_sequence", reason: "invalid sequence" } });
                }
                else {
                    replies.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
                }
            },
            nextFrameForSession: async () => {
                const frame = replies.shift();
                if (!frame)
                    throw new Error("timed out");
                return frame;
            },
        };
        const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 20, 500);
        strict_1.default.equal(acceptedAt, 3);
        strict_1.default.deepEqual(sentStops, [2, 3]);
    }
});
(0, node_test_1.default)("stopAgentWithSequenceProbe is bounded: no reply and a non-invalid_request rejection both end the probe without a retry (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    {
        const sentStops = [];
        const transport = {
            send: (frame) => {
                if (frame.type === "agent_stop")
                    sentStops.push(frame.sequence);
            },
            nextFrameForSession: async () => {
                throw new Error("timed out");
            },
        };
        const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 20, 60);
        strict_1.default.equal(acceptedAt, undefined);
        strict_1.default.deepEqual(sentStops, [2]);
    }
    {
        const replies = [];
        const sentStops = [];
        const transport = {
            send: (frame) => {
                if (frame.type !== "agent_stop")
                    return;
                sentStops.push(frame.sequence);
                replies.push({ type: "agent_error", session_id: sessionId, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { code: "agent_not_found", message: "session not found" } });
            },
            nextFrameForSession: async () => {
                const frame = replies.shift();
                if (!frame)
                    throw new Error("timed out");
                return frame;
            },
        };
        const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 20, 500);
        strict_1.default.equal(acceptedAt, undefined);
        strict_1.default.deepEqual(sentStops, [2]);
    }
});
(0, node_test_1.default)("stopAgentSession drains queued session output after a successful sequence probe (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const queue = [];
    queue.push({ type: "agent_result", session_id: sessionId, message_id: "m-stale-output", sequence: 9, timestamp: 1, version: 1, payload: { result_json: "{\"stale\":true}" } });
    const sentStops = [];
    const waitCorrelates = [];
    const drainedFrameIds = [];
    const transport = {
        send: (frame) => {
            if (frame.type !== "agent_stop")
                return;
            sentStops.push(frame.sequence);
            queue.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
        },
        nextFrameForSession: async (_sid, _timeoutMs, wait) => {
            const correlate = wait?.correlate;
            waitCorrelates.push(correlate);
            const frame = correlate !== undefined
                ? queue.find((entry) => entry.in_reply_to === correlate)
                : queue.find((entry) => entry.in_reply_to === undefined);
            if (!frame)
                throw new Error("timed out");
            queue.splice(queue.indexOf(frame), 1);
            if (correlate === undefined)
                drainedFrameIds.push(String(frame.message_id));
            return frame;
        },
    };
    const api = (0, src_1.createMakaiAgentApi)(transport, {});
    await api.stopAgentSession({ nextSequence: 3, unresolvedMessageSequence: 2, startReplyObserved: true, idClientGenerated: false, stopped: false, sessionId }, sessionId, 3, "timeout", { drain: "quiescent" });
    strict_1.default.deepEqual(sentStops, [2]);
    strict_1.default.ok(waitCorrelates[0] !== undefined);
    strict_1.default.ok(waitCorrelates.includes(undefined));
    strict_1.default.deepEqual(drainedFrameIds, ["m-stale-output"]);
    strict_1.default.equal(queue.length, 0);
});
(0, node_test_1.default)("the post-probe drain consumes only already-queued output, not a re-registered session's frames (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const queue = [];
    const sentStops = [];
    const uncorrelatedTimeouts = [];
    const transport = {
        send: (frame) => {
            if (frame.type !== "agent_stop")
                return;
            sentStops.push(frame.sequence);
            queue.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
        },
        nextFrameForSession: async (_sid, timeoutMs, wait) => {
            const correlate = wait?.correlate;
            if (correlate === undefined)
                uncorrelatedTimeouts.push(timeoutMs ?? 0);
            const matches = correlate !== undefined
                ? (entry) => entry.in_reply_to === correlate
                : (entry) => entry.in_reply_to === undefined;
            const immediate = queue.findIndex(matches);
            if (immediate >= 0)
                return queue.splice(immediate, 1)[0];
            throw new Error("timed out");
        },
    };
    queue.push({ type: "agent_result", session_id: sessionId, message_id: "m-parked", sequence: 9, timestamp: 1, version: 1, payload: { result_json: "{\"stale\":true}" } });
    const api = (0, src_1.createMakaiAgentApi)(transport, {});
    await api.stopAgentSession({ nextSequence: 3, unresolvedMessageSequence: 2, startReplyObserved: true, idClientGenerated: false, stopped: false, sessionId }, sessionId, 3, "timeout", { drain: "quiescent" });
    strict_1.default.deepEqual(sentStops, [2]);
    strict_1.default.equal(queue.length, 0);
    strict_1.default.ok(uncorrelatedTimeouts.length >= 1);
    strict_1.default.ok(uncorrelatedTimeouts.every((timeout) => timeout === 0), "the post-probe drain must request only immediate (0ms) dequeues");
});
(0, node_test_1.default)("the abort-path background drain is backlog-only, not a lingering route reader (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const queue = [];
    const sentStops = [];
    const uncorrelatedTimeouts = [];
    const transport = {
        send: (frame) => {
            if (frame.type !== "agent_stop")
                return;
            sentStops.push(frame.sequence);
            queue.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
        },
        nextFrameForSession: async (_sid, timeoutMs, wait) => {
            const correlate = wait?.correlate;
            if (correlate === undefined)
                uncorrelatedTimeouts.push(timeoutMs ?? 0);
            const matches = correlate !== undefined
                ? (entry) => entry.in_reply_to === correlate
                : (entry) => entry.in_reply_to === undefined;
            const immediate = queue.findIndex(matches);
            if (immediate >= 0)
                return queue.splice(immediate, 1)[0];
            throw new Error("timed out");
        },
    };
    queue.push({ type: "agent_result", session_id: sessionId, message_id: "m-parked", sequence: 9, timestamp: 1, version: 1, payload: { result_json: "{\"stale\":true}" } });
    const api = (0, src_1.createMakaiAgentApi)(transport, {});
    await api.stopAgentSession({ nextSequence: 3, unresolvedMessageSequence: 2, startReplyObserved: true, idClientGenerated: false, stopped: false, sessionId }, sessionId, 3, "client aborted", { drain: "background" });
    await new Promise((resolve) => setTimeout(resolve, 10));
    strict_1.default.deepEqual(sentStops, [2]);
    strict_1.default.equal(queue.length, 0);
    strict_1.default.ok(uncorrelatedTimeouts.length >= 1);
    strict_1.default.ok(uncorrelatedTimeouts.every((timeout) => timeout === 0), "the background post-probe drain must request only immediate (0ms) dequeues");
});
(0, node_test_1.default)("stopAgentWithSequenceProbe waits through idle windows for a delayed rejection before retrying (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const sentStops = [];
    const replies = [];
    const transport = {
        send: (frame) => {
            if (frame.type !== "agent_stop")
                return;
            sentStops.push(frame.sequence);
            if (sentStops.length === 1) {
                setTimeout(() => {
                    replies.push({ type: "agent_error", session_id: sessionId, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { code: "invalid_request", message: "invalid sequence" } });
                }, 120);
            }
            else {
                replies.push({ type: "agent_stopped", session_id: sessionId, message_id: "m-stopped", sequence: 9, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: {} });
            }
        },
        nextFrameForSession: async (_sid, timeoutMs, wait) => {
            const correlate = wait?.correlate;
            const match = correlate !== undefined ? replies.find((entry) => entry.in_reply_to === correlate) : undefined;
            if (match) {
                replies.splice(replies.indexOf(match), 1);
                return match;
            }
            await new Promise((resolve) => setTimeout(resolve, Math.min(timeoutMs ?? 50, 50)));
            throw new Error("timed out");
        },
    };
    const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 50, 400);
    strict_1.default.equal(acceptedAt, 3);
    strict_1.default.deepEqual(sentStops, [2, 3]);
});
(0, node_test_1.default)("stopAgentWithSequenceProbe treats the retry's own rejection as terminal (#210 gap 7)", async () => {
    const sessionId = "testNanoIdSess1234567";
    const sentStops = [];
    const replies = [];
    let readsAfterRetryRejection = 0;
    let retryRejected = false;
    const transport = {
        send: (frame) => {
            if (frame.type !== "agent_stop")
                return;
            sentStops.push(frame.sequence);
            replies.push({ type: "agent_error", session_id: sessionId, message_id: "m-reject", sequence: 0, timestamp: 1, version: 1, in_reply_to: frame.message_id, payload: { code: "invalid_request", message: "invalid sequence" } });
        },
        nextFrameForSession: async (_sid, timeoutMs, wait) => {
            const correlate = wait?.correlate;
            if (retryRejected)
                readsAfterRetryRejection += 1;
            const match = correlate !== undefined ? replies.find((entry) => entry.in_reply_to === correlate) : undefined;
            if (match) {
                replies.splice(replies.indexOf(match), 1);
                if (sentStops.length === 2)
                    retryRejected = true;
                return match;
            }
            await new Promise((resolve) => setTimeout(resolve, Math.min(timeoutMs ?? 50, 50)));
            throw new Error("timed out");
        },
    };
    const acceptedAt = await (0, src_1.stopAgentWithSequenceProbe)(transport, sessionId, { preSend: 2, postSend: 3 }, "timeout", 50, 400);
    strict_1.default.equal(acceptedAt, undefined);
    strict_1.default.deepEqual(sentStops, [2, 3]);
    strict_1.default.equal(readsAfterRetryRejection, 0, "the probe must not keep reading after both candidate states were rejected");
});
