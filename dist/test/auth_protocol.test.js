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
const ULID_RE = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
function fixtureClientOptions(fixture, extra = {}) {
    return {
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, fixture)],
        handshakeTimeoutMs: 5000,
        frameTimeoutMs: 5000,
        ...extra,
    };
}
(0, node_test_1.default)("flattenAuthEvent normalizes every Zig union wire variant", () => {
    strict_1.default.deepEqual((0, src_1.flattenAuthEvent)({
        auth_url: {
            flow_id: "00000000000000000000000000",
            provider_id: "fixture",
            url: "https://example.test/auth",
            instructions: "open browser",
        },
    }), {
        type: "auth_url",
        flow_id: "00000000000000000000000000",
        provider_id: "fixture",
        url: "https://example.test/auth",
        instructions: "open browser",
    });
    strict_1.default.deepEqual((0, src_1.flattenAuthEvent)({
        prompt: {
            flow_id: "00000000000000000000000001",
            prompt_id: "device_code",
            provider_id: "fixture",
            message: "enter code",
            allow_empty: false,
        },
    }), {
        type: "prompt",
        flow_id: "00000000000000000000000001",
        prompt_id: "device_code",
        provider_id: "fixture",
        message: "enter code",
        allow_empty: false,
    });
    strict_1.default.deepEqual((0, src_1.flattenAuthEvent)({
        progress: {
            flow_id: "00000000000000000000000002",
            provider_id: "fixture",
            message: "waiting",
        },
    }), {
        type: "progress",
        flow_id: "00000000000000000000000002",
        provider_id: "fixture",
        message: "waiting",
    });
    strict_1.default.deepEqual((0, src_1.flattenAuthEvent)({
        success: {
            flow_id: "00000000000000000000000003",
            provider_id: "fixture",
        },
    }), {
        type: "success",
        flow_id: "00000000000000000000000003",
        provider_id: "fixture",
    });
    strict_1.default.deepEqual((0, src_1.flattenAuthEvent)({
        error: {
            flow_id: "00000000000000000000000004",
            provider_id: "fixture",
            code: "auth_failed",
            message: "boom",
        },
    }), {
        type: "error",
        flow_id: "00000000000000000000000004",
        provider_id: "fixture",
        code: "auth_failed",
        message: "boom",
    });
});
(0, node_test_1.default)("client.auth.listProviders parses providers payload", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-providers-server.js"));
    try {
        const providers = await client.auth.listProviders();
        strict_1.default.equal(providers.length, 3);
        strict_1.default.deepEqual(providers[0], {
            id: "anthropic",
            name: "Anthropic",
            auth_status: "login_required",
        });
        strict_1.default.equal(providers[1]?.id, "github-copilot");
        strict_1.default.equal(providers[1]?.auth_status, "authenticated");
        strict_1.default.equal(providers[2]?.auth_status, "failed");
        strict_1.default.equal(providers[2]?.last_error, "previous attempt rejected");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.listProviders preserves frames for concurrent auth streams", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-concurrent-server.js"));
    try {
        const [first, second] = await Promise.all([
            client.auth.listProviders(),
            client.auth.listProviders(),
        ]);
        strict_1.default.equal(first[0]?.id, "anthropic");
        strict_1.default.equal(second[0]?.id, "anthropic");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.listProviders emits ULID stream and message IDs on stdio", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-wire-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const frameLogPath = node_path_1.default.join(tmpDir, "frames.log");
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-providers-server.js", {
        env: {
            ...process.env,
            MAKAI_TEST_REQUEST_LOG: logPath,
            MAKAI_TEST_FRAME_LOG: frameLogPath,
        },
    }));
    try {
        await client.auth.listProviders();
        const request = JSON.parse(node_fs_1.default.readFileSync(logPath, "utf8").trim());
        strict_1.default.equal(request.type, "auth_providers_request");
        strict_1.default.equal(typeof request.stream_id, "string");
        strict_1.default.equal(typeof request.message_id, "string");
        strict_1.default.match(request.stream_id, ULID_RE);
        strict_1.default.match(request.message_id, ULID_RE);
        const frames = node_fs_1.default.readFileSync(frameLogPath, "utf8")
            .trim()
            .split("\n")
            .map((line) => JSON.parse(line));
        strict_1.default.equal(frames.length, 2);
        for (const frame of frames) {
            strict_1.default.equal(frame.stream_id, request.stream_id);
            strict_1.default.equal(frame.in_reply_to, request.message_id);
            strict_1.default.equal(typeof frame.message_id, "string");
            strict_1.default.match(frame.message_id, ULID_RE);
        }
    }
    finally {
        await client.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.auth.login emits ULID flow IDs on stdio", async () => {
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-login-wire-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-login-success-server.js", {
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath },
    }));
    try {
        await client.auth.login("test-fixture", { onPrompt: () => "letmein" });
        const request = JSON.parse(node_fs_1.default.readFileSync(logPath, "utf8").trim());
        strict_1.default.equal(request.type, "auth_login_start");
        strict_1.default.equal(typeof request.stream_id, "string");
        strict_1.default.equal(typeof request.message_id, "string");
        strict_1.default.match(request.stream_id, ULID_RE);
        strict_1.default.match(request.message_id, ULID_RE);
    }
    finally {
        await client.close();
        node_fs_1.default.rmSync(tmpDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("client.auth.login resolves on success after prompt loop", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-login-success-server.js"));
    try {
        const events = [];
        const result = await client.auth.login("test-fixture", {
            onEvent: (event) => events.push(event),
            onPrompt: async (prompt) => {
                strict_1.default.equal(prompt.type, "prompt");
                strict_1.default.equal(prompt.allow_empty, false);
                strict_1.default.equal(prompt.prompt_id, "device_code");
                return "letmein";
            },
        });
        strict_1.default.deepEqual(result, { status: "success" });
        strict_1.default.equal(events.some((e) => e.type === "auth_url"), true);
        strict_1.default.equal(events.some((e) => e.type === "prompt"), true);
        strict_1.default.equal(events.some((e) => e.type === "success"), true);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.login drains cancellation after prompt handler throws", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-prompt-throw-cleanup-server.js"));
    try {
        await strict_1.default.rejects(() => client.auth.login("test-fixture", {
            onPrompt: () => {
                throw new Error("prompt handler failed");
            },
        }), /prompt handler failed/);
        const result = await client.auth.login("test-fixture", {
            onPrompt: () => "letmein",
        });
        strict_1.default.deepEqual(result, { status: "success" });
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.login rejects with cancelled error on cancelled login result", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-login-cancelled-server.js"));
    try {
        await strict_1.default.rejects(() => client.auth.login("test-fixture"), (error) => error instanceof src_1.MakaiAuthError && error.kind === "cancelled");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.listProviders timeout includes actionable diagnostics", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-providers-server.js", {
        frameTimeoutMs: 20,
        env: { ...process.env, MAKAI_TEST_RESPONSE_DELAY_MS: "100" },
    }));
    try {
        await strict_1.default.rejects(() => client.auth.listProviders(), (error) => error instanceof src_1.MakaiAuthError &&
            error.kind === "transport_error" &&
            error.message.includes("Timed out waiting for auth_providers_response after 20ms") &&
            error.message.includes("stream_id=") &&
            error.message.includes("message_id=") &&
            error.message.includes("Verify the makai binary") &&
            error.diagnostics?.operation === "auth_providers_response" &&
            error.diagnostics.timeout_ms === 20 &&
            typeof error.diagnostics.stream_id === "string" &&
            typeof error.diagnostics.message_id === "string");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.login rejects with provider_error and propagates code/message on failure", async () => {
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-login-failed-server.js"));
    try {
        await strict_1.default.rejects(() => client.auth.login("test-fixture"), (error) => error instanceof src_1.MakaiAuthError &&
            error.kind === "provider_error" &&
            error.code === "auth_failed" &&
            error.message.includes("fixture auth failed"));
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.login per-call handlers override client-level defaults", async () => {
    const calls = [];
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-login-success-server.js", {
        handlers: {
            onPrompt: () => {
                calls.push("client-default");
                return "wrong-answer";
            },
            onEvent: () => {
                calls.push("client-default-event");
            },
        },
    }));
    try {
        const result = await client.auth.login("test-fixture", {
            onPrompt: () => {
                calls.push("per-call");
                return "letmein";
            },
        });
        strict_1.default.deepEqual(result, { status: "success" });
        strict_1.default.equal(calls.includes("client-default"), false);
        strict_1.default.equal(calls.includes("client-default-event"), false);
        strict_1.default.equal(calls.includes("per-call"), true);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("client.auth.login falls back to client-level handlers when no per-call handlers given", async () => {
    const calls = [];
    const client = await (0, src_1.createMakaiAuthClient)(fixtureClientOptions("auth-protocol-login-success-server.js", {
        handlers: {
            onPrompt: () => {
                calls.push("client-default");
                return "letmein";
            },
        },
    }));
    try {
        const result = await client.auth.login("test-fixture");
        strict_1.default.deepEqual(result, { status: "success" });
        strict_1.default.deepEqual(calls, ["client-default"]);
    }
    finally {
        await client.close();
    }
});
