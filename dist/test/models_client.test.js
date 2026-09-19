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
const fixtureScript = node_path_1.default.join(sourceFixturesDir, "models-server.js");
const ULID_RE = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
function makeDescriptor(overrides = {}) {
    return {
        model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
        model_id: "claude-sonnet-4-5",
        display_name: "Claude Sonnet 4.5",
        provider_id: "anthropic",
        api: "anthropic-messages",
        auth_status: "authenticated",
        lifecycle: "stable",
        capabilities: ["chat", "streaming", "tools", "reasoning"],
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
async function setupHarness(opts) {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-models-test-"));
    const responsePath = node_path_1.default.join(tmpDir, "response.json");
    const nackPath = node_path_1.default.join(tmpDir, "nack.json");
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const env = {
        ...process.env,
        MAKAI_TEST_REQUEST_LOG: logPath,
    };
    if (opts.responseDelayMs !== undefined) {
        env.MAKAI_TEST_RESPONSE_DELAY_MS = String(opts.responseDelayMs);
    }
    if (opts.nack) {
        node_fs_1.default.writeFileSync(nackPath, JSON.stringify(opts.nack));
        env.MAKAI_TEST_NACK_PATH = nackPath;
    }
    else {
        node_fs_1.default.writeFileSync(responsePath, JSON.stringify(opts.response ?? makeResponse([makeDescriptor()])));
        env.MAKAI_TEST_RESPONSE_PATH = responsePath;
    }
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [fixtureScript],
        env,
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    return {
        client,
        responsePath,
        nackPath,
        logPath,
        cleanup: async () => {
            await client.close();
            node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
        },
    };
}
function readLoggedRequests(logPath) {
    if (!node_fs_1.default.existsSync(logPath))
        return [];
    return node_fs_1.default
        .readFileSync(logPath, "utf8")
        .split(/\r?\n/)
        .filter((line) => line.length > 0)
        .map((line) => JSON.parse(line));
}
(0, node_test_1.default)("models.list parses and returns full ListModelsResponse shape", async () => {
    const expected = makeResponse([
        makeDescriptor({
            base_url: "https://api.anthropic.com",
            context_window: 200_000,
            max_output_tokens: 8_192,
            reasoning_default: "medium",
            metadata: { tier: "standard" },
        }),
        makeDescriptor({
            model_ref: "anthropic/anthropic-messages@claude-haiku-4-5",
            model_id: "claude-haiku-4-5",
            display_name: "Claude Haiku 4.5",
            lifecycle: "preview",
            source: "static_fallback",
            capabilities: ["chat"],
        }),
    ]);
    const harness = await setupHarness({ response: expected });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const result = await api.list();
        strict_1.default.equal(result.models.length, 2);
        strict_1.default.equal(result.fetched_at_ms, expected.fetched_at_ms);
        strict_1.default.equal(result.cache_max_age_ms, expected.cache_max_age_ms);
        const first = result.models[0];
        strict_1.default.equal(first.model_ref, "anthropic/anthropic-messages@claude-sonnet-4-5");
        strict_1.default.equal(first.base_url, "https://api.anthropic.com");
        strict_1.default.equal(first.context_window, 200_000);
        strict_1.default.equal(first.max_output_tokens, 8_192);
        strict_1.default.equal(first.reasoning_default, "medium");
        strict_1.default.deepEqual(first.metadata, { tier: "standard" });
        strict_1.default.deepEqual(first.capabilities, ["chat", "streaming", "tools", "reasoning"]);
        strict_1.default.equal(first.source, "dynamic");
        const second = result.models[1];
        strict_1.default.equal(second.lifecycle, "preview");
        strict_1.default.equal(second.source, "static_fallback");
        strict_1.default.equal(second.base_url, undefined);
        strict_1.default.equal(second.metadata, undefined);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list defaults missing cache_max_age_ms to 5 minutes", async () => {
    const responseWithoutCache = {
        models: [makeDescriptor()],
        fetched_at_ms: 1_760_000_000_500,
    };
    const harness = await setupHarness({ response: responseWithoutCache });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const result = await api.list();
        strict_1.default.equal(result.cache_max_age_ms, 300_000);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list tags malformed response errors with a synthetic code", async () => {
    const malformedResponse = {
        models: "not-an-array",
        fetched_at_ms: 1_760_000_000_500,
    };
    const harness = await setupHarness({ response: malformedResponse });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.list(), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "malformed_response" &&
            err.message === "models_response missing 'models' array");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list passes filter fields through on the wire", async () => {
    const harness = await setupHarness({
        response: makeResponse([makeDescriptor()]),
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await api.list({
            provider_id: "anthropic",
            api: "anthropic-messages",
            include_deprecated: true,
            include_login_required: false,
        });
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged.length, 1);
        const env = logged[0];
        strict_1.default.equal(env.type, "models_request");
        strict_1.default.equal(env.version, 1);
        strict_1.default.equal(typeof env.stream_id, "string");
        strict_1.default.equal(typeof env.message_id, "string");
        strict_1.default.match(env.stream_id, ULID_RE);
        strict_1.default.equal(env.message_id, env.stream_id);
        const payload = env.payload;
        strict_1.default.equal(payload.provider_id, "anthropic");
        strict_1.default.equal(payload.api, "anthropic-messages");
        strict_1.default.equal(payload.include_deprecated, true);
        strict_1.default.equal(payload.include_login_required, false);
        strict_1.default.equal(payload.model_id, undefined);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list serializes concurrent calls on the shared transport", async () => {
    const harness = await setupHarness({
        response: makeResponse([makeDescriptor()]),
        responseDelayMs: 50,
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await Promise.all([
            api.list({ provider_id: "anthropic" }),
            api.list({ provider_id: "openai" }),
        ]);
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged.length, 2);
        strict_1.default.equal(logged[0].payload.provider_id, "anthropic");
        strict_1.default.equal(logged[1].payload.provider_id, "openai");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list rejects unknown descriptor enum values", async () => {
    const harness = await setupHarness({
        response: makeResponse([
            makeDescriptor({ auth_status: "mystery" }),
        ]),
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.list(), (err) => err instanceof src_1.MakaiProtocolError &&
            err.message === "models[0].auth_status has unknown value: mystery");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list rejects unknown reasoning_default values", async () => {
    const harness = await setupHarness({
        response: makeResponse([
            makeDescriptor({ reasoning_default: "extreme" }),
        ]),
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.list(), (err) => err instanceof src_1.MakaiProtocolError &&
            err.message === "models[0].reasoning_default has unknown value: extreme");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve issues models_request with exact model_id filter", async () => {
    const target = makeDescriptor();
    const harness = await setupHarness({ response: makeResponse([target]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const result = await api.resolve({
            provider_id: "anthropic",
            api: "anthropic-messages",
            model_id: "claude-sonnet-4-5",
        });
        strict_1.default.equal(result.model.model_ref, target.model_ref);
        strict_1.default.equal(result.model.model_id, "claude-sonnet-4-5");
        const logged = readLoggedRequests(harness.logPath);
        strict_1.default.equal(logged.length, 1);
        const payload = logged[0].payload;
        strict_1.default.equal(payload.provider_id, "anthropic");
        strict_1.default.equal(payload.api, "anthropic-messages");
        strict_1.default.equal(payload.model_id, "claude-sonnet-4-5");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve throws invalid_request when the single result does not match", async () => {
    const harness = await setupHarness({
        response: makeResponse([
            makeDescriptor({
                provider_id: "openai",
                model_id: "gpt-4o-mini",
                model_ref: "openai/openai-responses@gpt-4o-mini",
            }),
        ]),
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.resolve({
            provider_id: "anthropic",
            api: "anthropic-messages",
            model_id: "claude-sonnet-4-5",
        }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "resolved model provider_id mismatch");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve throws invalid_request when runtime ignores requested api", async () => {
    const harness = await setupHarness({
        response: makeResponse([
            makeDescriptor({
                api: "openai-completions",
                model_ref: "anthropic/openai-completions@claude-sonnet-4-5",
            }),
        ]),
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.resolve({
            provider_id: "anthropic",
            api: "anthropic-messages",
            model_id: "claude-sonnet-4-5",
        }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "resolved model api mismatch");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve throws invalid_request when runtime returns multiple matches", async () => {
    const harness = await setupHarness({
        response: makeResponse([
            makeDescriptor({ api: "anthropic-messages" }),
            makeDescriptor({
                api: "openai-completions",
                model_ref: "anthropic/openai-completions@claude-sonnet-4-5",
            }),
        ]),
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.resolve({
            provider_id: "anthropic",
            model_id: "claude-sonnet-4-5",
        }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            /2 matches/.test(err.message));
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve throws invalid_request 'model not found' when no match", async () => {
    const harness = await setupHarness({ response: makeResponse([]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.resolve({
            provider_id: "anthropic",
            model_id: "non-existent-model",
        }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model not found");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list timeout includes actionable diagnostics", async () => {
    const harness = await setupHarness({
        response: makeResponse([makeDescriptor()]),
        responseDelayMs: 100,
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client, { responseTimeoutMs: 20 });
        await strict_1.default.rejects(() => api.list({ provider_id: "anthropic", api: "anthropic-messages" }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.message.includes("Timed out waiting for models_response after 20ms for provider 'anthropic'") &&
            err.message.includes("stream_id=") &&
            err.message.includes("message_id=") &&
            err.message.includes("Check network connectivity") &&
            err.diagnostics?.operation === "models_response" &&
            err.diagnostics.timeout_ms === 20 &&
            err.diagnostics.provider_id === "anthropic" &&
            err.diagnostics.api === "anthropic-messages" &&
            typeof err.diagnostics.stream_id === "string" &&
            err.diagnostics.message_id === err.diagnostics.stream_id);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list propagates nack error_code as MakaiProtocolError", async () => {
    const harness = await setupHarness({
        nack: { reason: "model not found", error_code: "invalid_request" },
    });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(() => api.list({ provider_id: "anthropic", model_id: "nope" }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model not found");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve rejects locally when provider_id or model_id is missing", async () => {
    const harness = await setupHarness({ response: makeResponse([makeDescriptor()]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await strict_1.default.rejects(
        // @ts-expect-error testing runtime guard
        () => api.resolve({ model_id: "claude-sonnet-4-5" }), (err) => err instanceof src_1.MakaiProtocolError && err.code === "invalid_request");
        await strict_1.default.rejects(
        // @ts-expect-error testing runtime guard
        () => api.resolve({ provider_id: "anthropic" }), (err) => err instanceof src_1.MakaiProtocolError && err.code === "invalid_request");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve rejects provider_id exceeding 256 characters before transport I/O", async () => {
    const harness = await setupHarness({ response: makeResponse([makeDescriptor()]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const longProviderId = "a".repeat(257);
        await strict_1.default.rejects(() => api.resolve({ provider_id: longProviderId, model_id: "claude-sonnet-4-5" }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "provider_id exceeds maximum length of 256 characters");
        strict_1.default.equal(readLoggedRequests(harness.logPath).length, 0);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve rejects model_id exceeding 256 characters before transport I/O", async () => {
    const harness = await setupHarness({ response: makeResponse([makeDescriptor()]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const longModelId = "a".repeat(257);
        await strict_1.default.rejects(() => api.resolve({ provider_id: "anthropic", model_id: longModelId }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_id exceeds maximum length of 256 characters");
        strict_1.default.equal(readLoggedRequests(harness.logPath).length, 0);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve accepts provider_id at exactly 256 characters", async () => {
    const descriptor = makeDescriptor({
        provider_id: "a".repeat(256),
        model_ref: `${"a".repeat(256)}/anthropic-messages@claude-sonnet-4-5`,
    });
    const harness = await setupHarness({ response: makeResponse([descriptor]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await api.resolve({ provider_id: "a".repeat(256), model_id: "claude-sonnet-4-5" });
    }
    catch (err) {
        strict_1.default.ok(err instanceof src_1.MakaiProtocolError && err.code === "invalid_request");
        strict_1.default.ok(!err.message.includes("exceeds maximum length"), `unexpected length validation error: ${err.message}`);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.resolve accepts model_id at exactly 256 characters", async () => {
    const longModelId = "a".repeat(256);
    const descriptor = makeDescriptor({
        model_id: longModelId,
        model_ref: `anthropic/anthropic-messages@${longModelId}`,
    });
    const harness = await setupHarness({ response: makeResponse([descriptor]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        await api.resolve({ provider_id: "anthropic", model_id: longModelId });
    }
    catch (err) {
        strict_1.default.ok(err instanceof src_1.MakaiProtocolError && err.code === "invalid_request");
        strict_1.default.ok(!err.message.includes("exceeds maximum length"), `unexpected length validation error: ${err.message}`);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list rejects provider_id filter exceeding 256 characters before transport I/O", async () => {
    const harness = await setupHarness({ response: makeResponse([makeDescriptor()]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const longProviderId = "a".repeat(257);
        await strict_1.default.rejects(() => api.list({ provider_id: longProviderId }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "provider_id exceeds maximum length of 256 characters");
        strict_1.default.equal(readLoggedRequests(harness.logPath).length, 0);
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("models.list rejects model_id filter exceeding 256 characters before transport I/O", async () => {
    const harness = await setupHarness({ response: makeResponse([makeDescriptor()]) });
    try {
        const api = (0, src_1.createMakaiModelsApi)(harness.client);
        const longModelId = "a".repeat(257);
        await strict_1.default.rejects(() => api.list({ model_id: longModelId }), (err) => err instanceof src_1.MakaiProtocolError &&
            err.code === "invalid_request" &&
            err.message === "model_id exceeds maximum length of 256 characters");
        strict_1.default.equal(readLoggedRequests(harness.logPath).length, 0);
    }
    finally {
        await harness.cleanup();
    }
});
