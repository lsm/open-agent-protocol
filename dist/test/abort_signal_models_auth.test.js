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
function makeDescriptor(overrides = {}) {
    return {
        model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
        model_id: "claude-sonnet-4-5",
        display_name: "Claude Sonnet 4.5",
        provider_id: "anthropic",
        api: "anthropic-messages",
        auth_status: "authenticated",
        lifecycle: "stable",
        capabilities: ["chat", "streaming"],
        source: "dynamic",
        ...overrides,
    };
}
function makeResponse(models) {
    return {
        models,
        fetched_at_ms: 1_760_000_000_198,
        cache_max_age_ms: 300_000,
    };
}
async function setupModelsHarness() {
    const fixtureScript = node_path_1.default.join(sourceFixturesDir, "models-server.js");
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-abort-models-test-"));
    const responsePath = node_path_1.default.join(tmpDir, "response.json");
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    node_fs_1.default.writeFileSync(responsePath, JSON.stringify(makeResponse([makeDescriptor()])));
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath, MAKAI_TEST_RESPONSE_PATH: responsePath },
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    return {
        client,
        responsePath,
        logPath,
        cleanup: async () => {
            await client.close();
            node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
        },
    };
}
(0, node_test_1.default)("models.list rejects immediately with AbortSignal.abort()", async () => {
    const harness = await setupModelsHarness();
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.list({ signal: AbortSignal.abort() }), (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list rejects when signal is aborted during response wait", async () => {
    const fixtureScript = node_path_1.default.join(sourceFixturesDir, "models-server.js");
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-abort-models-wait-test-"));
    const responsePath = node_path_1.default.join(tmpDir, "response.json");
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    node_fs_1.default.writeFileSync(responsePath, JSON.stringify(makeResponse([makeDescriptor()])));
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [fixtureScript],
        env: {
            ...process.env,
            MAKAI_TEST_REQUEST_LOG: logPath,
            MAKAI_TEST_RESPONSE_PATH: responsePath,
            MAKAI_TEST_RESPONSE_DELAY_MS: "5000",
        },
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const api = (0, src_1.createMakaiModelsApi)(client, { responseTimeoutMs: 10000 });
        const controller = new AbortController();
        const listPromise = api.list({ provider_id: "anthropic", signal: controller.signal });
        await new Promise((resolve) => setTimeout(resolve, 10));
        controller.abort();
        await strict_1.default.rejects(() => listPromise, (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        await client.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("models.resolve rejects immediately with AbortSignal.abort()", async () => {
    const harness = await setupModelsHarness();
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.resolve({ provider_id: "anthropic", model_id: "test", signal: AbortSignal.abort() }), (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list succeeds when signal is not aborted", async () => {
    const harness = await setupModelsHarness();
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const controller = new AbortController();
        const result = await api.list({ signal: controller.signal });
        strict_1.default.equal(result.models.length, 1);
        strict_1.default.equal(controller.signal.aborted, false);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("auth.login rejects immediately with AbortSignal.abort()", async () => {
    const fixture = node_path_1.default.join(sourceFixturesDir, "auth-protocol-login-success-server.js");
    const client = await (0, src_1.createMakaiAuthClient)({
        command: process.execPath,
        args: [fixture],
        handshakeTimeoutMs: 5000,
        frameTimeoutMs: 5000,
    });
    try {
        await strict_1.default.rejects(() => client.auth.login("test-fixture", undefined, { signal: AbortSignal.abort() }), (error) => error instanceof src_1.MakaiAuthError && error.kind === "cancelled");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("auth.login succeeds when signal is not aborted", async () => {
    const fixture = node_path_1.default.join(sourceFixturesDir, "auth-protocol-login-success-server.js");
    const client = await (0, src_1.createMakaiAuthClient)({
        command: process.execPath,
        args: [fixture],
        handshakeTimeoutMs: 5000,
        frameTimeoutMs: 5000,
    });
    try {
        const controller = new AbortController();
        const result = await client.auth.login("test-fixture", { onPrompt: () => "letmein" }, { signal: controller.signal });
        strict_1.default.deepEqual(result, { status: "success" });
        strict_1.default.equal(controller.signal.aborted, false);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("createMakaiClient provider.complete rejects with AbortSignal.abort()", async () => {
    const fixtureScript = node_path_1.default.join(sourceFixturesDir, "execution-server.js");
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-abort-client-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
    });
    try {
        await strict_1.default.rejects(() => handle.provider.complete({
            model_ref: "anthropic/anthropic-messages@model",
            messages: [{ role: "user", content: "hello" }],
            options: { signal: AbortSignal.abort() },
        }), (error) => error instanceof Error && error.name === "AbortError");
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("createMakaiClient agent.stream rejects with AbortSignal.abort()", async () => {
    const fixtureScript = node_path_1.default.join(sourceFixturesDir, "execution-server.js");
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-abort-agent-stream-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const handle = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath },
        handshakeTimeoutMs: 5000,
        responseTimeoutMs: 5000,
    });
    try {
        const events = [];
        await strict_1.default.rejects(async () => {
            for await (const event of handle.agent.stream({
                model_ref: "anthropic/anthropic-messages@model",
                messages: [{ role: "user", content: "hello" }],
                options: { signal: AbortSignal.abort() },
            })) {
                events.push(event);
            }
        }, (error) => error instanceof Error && error.name === "AbortError");
        strict_1.default.equal(events.length, 0);
    }
    finally {
        await handle.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
