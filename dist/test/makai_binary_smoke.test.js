"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_fs_1 = require("node:fs");
const node_os_1 = __importDefault(require("node:os"));
const node_path_1 = __importDefault(require("node:path"));
const node_test_1 = __importDefault(require("node:test"));
const src_1 = require("../src");
const binaryPath = process.env.MAKAI_BINARY_PATH;
(0, node_test_1.default)("e2e: connect to makai binary over stdio", async (t) => {
    if (!binaryPath) {
        t.skip("MAKAI_BINARY_PATH is not set");
        return;
    }
    const client = await (0, src_1.createMakaiStdioClient)({
        resolver: { binaryPath },
        handshakeTimeoutMs: 1000,
    });
    await client.connect();
    await client.close();
});
(0, node_test_1.default)("e2e: nextFrame times out when the runtime sends nothing", async (t) => {
    if (!binaryPath) {
        t.skip("MAKAI_BINARY_PATH is not set");
        return;
    }
    const client = await (0, src_1.createMakaiStdioClient)({
        resolver: { binaryPath },
        handshakeTimeoutMs: 1000,
    });
    await client.connect();
    await strict_1.default.rejects(() => client.nextFrame(150), /timed out waiting for frame/);
    await client.close();
});
(0, node_test_1.default)("e2e: malformed envelope is rejected with a nack and the runtime stays up", async (t) => {
    if (!binaryPath) {
        t.skip("MAKAI_BINARY_PATH is not set");
        return;
    }
    const client = await (0, src_1.createMakaiStdioClient)({
        resolver: { binaryPath },
        handshakeTimeoutMs: 1000,
    });
    await client.connect();
    client.send({ type: "stream_request", stream_id: "e2e-smoke" });
    const rejection = (await client.nextFrame(2000));
    strict_1.default.equal(rejection.type, "nack");
    strict_1.default.equal(rejection.payload?.error_code, "invalid_request");
    strict_1.default.equal(typeof rejection.payload?.reason, "string");
    strict_1.default.ok((rejection.payload?.reason?.length ?? 0) > 0);
    client.send({ type: "stream_request", stream_id: "e2e-smoke-again" });
    const second = (await client.nextFrame(2000));
    strict_1.default.equal(second.type, "nack");
    await client.close();
});
(0, node_test_1.default)("e2e: version skew fails fast", async (t) => {
    if (!binaryPath) {
        t.skip("MAKAI_BINARY_PATH is not set");
        return;
    }
    const client = await (0, src_1.createMakaiStdioClient)({
        resolver: { binaryPath },
        expectedProtocolVersion: "2",
        handshakeTimeoutMs: 1000,
    });
    await strict_1.default.rejects(() => client.connect(), (error) => error instanceof src_1.StdioProtocolError &&
        error.code === "version_mismatch" &&
        error.message.includes("protocol version mismatch"));
    await client.close();
});
(0, node_test_1.default)("e2e: auth login persists credentials", async (t) => {
    if (!binaryPath) {
        t.skip("MAKAI_BINARY_PATH is not set");
        return;
    }
    const tempHome = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-auth-home-"));
    const client = await (0, src_1.createMakaiAuthClient)({
        resolver: { binaryPath },
        env: { ...process.env, HOME: tempHome },
        handshakeTimeoutMs: 1000,
    });
    try {
        await client.auth.login("test-fixture", {
            onPrompt: async () => "ok",
        });
        const authPath = node_path_1.default.join(tempHome, ".makai", "auth.json");
        const raw = await node_fs_1.promises.readFile(authPath, "utf8");
        const parsed = JSON.parse(raw);
        strict_1.default.equal(typeof parsed["test-fixture"]?.refresh, "string");
        strict_1.default.equal(typeof parsed["test-fixture"]?.access, "string");
    }
    finally {
        await client.close();
        await node_fs_1.promises.rm(tempHome, { recursive: true, force: true });
    }
});
