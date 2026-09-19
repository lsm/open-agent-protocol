"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_fs_1 = require("node:fs");
const node_fs_2 = __importDefault(require("node:fs"));
const node_os_1 = __importDefault(require("node:os"));
const node_path_1 = __importDefault(require("node:path"));
const node_test_1 = __importDefault(require("node:test"));
const server_1 = require("../demo/server");
const src_1 = require("../src");
const binaryPath = process.env.MAKAI_BINARY_PATH;
const sourceFixturesDir = node_path_1.default.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const executionFixture = node_path_1.default.join(sourceFixturesDir, "execution-server.js");
function fixtureServerOptions(tempHome, requestLog) {
    return {
        command: process.execPath,
        args: [executionFixture],
        env: {
            MAKAI_TEST_AUTH_REQUIRES_PROMPT: "1",
            MAKAI_TEST_AUTH_STATE_PATH: node_path_1.default.join(tempHome, "auth-state.json"),
            ...(requestLog ? { MAKAI_TEST_REQUEST_LOG: requestLog } : {}),
        },
        homeDir: tempHome,
    };
}
function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
}
async function waitForAuthStatus(baseUrl, sessionId, expected, timeoutMs = 12_000) {
    const deadline = Date.now() + timeoutMs;
    let last;
    while (Date.now() < deadline) {
        const response = await fetch(`${baseUrl}/api/auth/sessions/${sessionId}`);
        strict_1.default.equal(response.status, 200);
        last = (await response.json());
        if (last.status === expected)
            return last;
        await sleep(150);
    }
    throw new Error(`timed out waiting for ${expected}, last status=${last?.status ?? "unknown"}`);
}
(0, node_test_1.default)("demo: serves UI and metadata", async () => {
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-demo-home-"));
    const running = await (0, server_1.startDemoServer)({
        port: 0,
        ...fixtureServerOptions(tempHome),
    });
    try {
        const indexRes = await fetch(`${running.url}/`);
        strict_1.default.equal(indexRes.status, 200);
        const html = await indexRes.text();
        strict_1.default.equal(html.includes("Makai TS SDK Demo"), true);
        const metaRes = await fetch(`${running.url}/api/meta`);
        strict_1.default.equal(metaRes.status, 200);
        const meta = (await metaRes.json());
        strict_1.default.equal(meta.oauthProviders.some((provider) => provider.id === "test-fixture"), true);
        strict_1.default.equal(meta.chatProviders.some((provider) => provider.id === "test-fixture"), true);
    }
    finally {
        await running.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("demo: chat endpoint uses provider-agnostic SDK stream path for fixture provider", async () => {
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-demo-home-"));
    const logPath = node_path_1.default.join(tempHome, "request.log");
    const running = await (0, server_1.startDemoServer)({
        port: 0,
        ...fixtureServerOptions(tempHome, logPath),
    });
    try {
        const response = await fetch(`${running.url}/api/chat`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({
                provider: "test-fixture",
                model: "fixture-echo-v1",
                message: "hello world",
            }),
        });
        strict_1.default.equal(response.status, 200);
        const payload = (await response.json());
        strict_1.default.equal(payload.reply, "[fixture-echo-v1] dlrow olleh");
        const requests = node_fs_2.default.readFileSync(logPath, "utf8")
            .trim()
            .split(/\r?\n/)
            .filter(Boolean)
            .map((line) => JSON.parse(line));
        const streamRequest = requests.find((request) => request.type === "stream_request");
        strict_1.default.ok(streamRequest);
        strict_1.default.equal(streamRequest.payload.model_ref, "test-fixture/test-fixture@fixture-echo-v1");
        strict_1.default.equal(streamRequest.payload.context?.messages?.length, 1);
        strict_1.default.equal(streamRequest.payload.options?.auth_retry_policy, "manual");
    }
    finally {
        await running.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("demo: fixture chat works without configured Makai runtime", async () => {
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-demo-home-"));
    const running = await (0, server_1.startDemoServer)({
        port: 0,
        homeDir: tempHome,
        binaryPath: "",
        env: { MAKAI_BINARY_PATH: undefined },
    });
    try {
        const response = await fetch(`${running.url}/api/chat`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({
                provider: "test-fixture",
                model: "fixture-echo-v1",
                message: "binary free",
            }),
        });
        strict_1.default.equal(response.status, 200);
        const payload = (await response.json());
        strict_1.default.equal(payload.reply, "[fixture-echo-v1] eerf yranib");
    }
    finally {
        await running.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("demo: auth-required chat response is client error", async () => {
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-demo-home-"));
    const running = await (0, server_1.startDemoServer)({
        port: 0,
        command: process.execPath,
        args: [executionFixture],
        env: { MAKAI_TEST_AUTH_REQUIRED_ALWAYS: "1" },
        homeDir: tempHome,
    });
    try {
        const response = await fetch(`${running.url}/api/chat`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({
                provider: "anthropic",
                model: "claude-sonnet-4-5",
                message: "hello world",
            }),
        });
        strict_1.default.equal(response.status, 400);
        const payload = (await response.json());
        strict_1.default.match(payload.error, /login required|auth/i);
    }
    finally {
        await running.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("demo: auth fixture flow reaches cancelled terminal state", async () => {
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-demo-home-"));
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [executionFixture],
        env: { ...process.env, HOME: tempHome, MAKAI_TEST_AUTH_REQUIRES_PROMPT: "1" },
        frameTimeoutMs: 5000,
    });
    try {
        await strict_1.default.rejects(() => client.auth.login("test-fixture"), (error) => error instanceof src_1.MakaiAuthError && error.kind === "cancelled");
    }
    finally {
        await client.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("demo: oauth fixture flow persists auth credentials", async () => {
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-demo-home-"));
    const logPath = node_path_1.default.join(tempHome, "request.log");
    const running = await (0, server_1.startDemoServer)(binaryPath ? {
        port: 0,
        homeDir: tempHome,
        binaryPath,
        env: { MAKAI_TEST_REQUEST_LOG: logPath },
    } : {
        port: 0,
        ...fixtureServerOptions(tempHome, logPath),
    });
    try {
        const createRes = await fetch(`${running.url}/api/auth/sessions`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ provider: "test-fixture" }),
        });
        strict_1.default.equal(createRes.status, 200);
        const created = (await createRes.json());
        strict_1.default.equal(typeof created.sessionId, "string");
        const waiting = await waitForAuthStatus(running.url, created.sessionId, "waiting_for_input");
        strict_1.default.equal(typeof waiting.pendingPrompt?.message, "string");
        const respondRes = await fetch(`${running.url}/api/auth/sessions/${created.sessionId}/respond`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ answer: "ok" }),
        });
        strict_1.default.equal(respondRes.status, 200);
        await waitForAuthStatus(running.url, created.sessionId, "success");
        if (binaryPath) {
            const authRaw = await node_fs_1.promises.readFile(node_path_1.default.join(tempHome, ".makai", "auth.json"), "utf8");
            const auth = JSON.parse(authRaw);
            strict_1.default.equal(typeof auth["test-fixture"]?.access, "string");
            strict_1.default.equal(typeof auth["test-fixture"]?.refresh, "string");
        }
        if (!binaryPath) {
            const metaRes = await fetch(`${running.url}/api/meta`);
            strict_1.default.equal(metaRes.status, 200);
            const meta = (await metaRes.json());
            strict_1.default.equal(meta.chatProviders.find((provider) => provider.id === "test-fixture")?.authenticated, true);
            const requests = node_fs_2.default.readFileSync(logPath, "utf8")
                .trim()
                .split(/\r?\n/)
                .filter(Boolean)
                .map((line) => JSON.parse(line));
            strict_1.default.equal(requests.some((request) => request.type === "auth_login_start"), true);
            strict_1.default.equal(requests.some((request) => request.type === "auth_prompt_response"), true);
        }
    }
    finally {
        await running.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
