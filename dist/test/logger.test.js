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
function createCapturingLogger() {
    const entries = [];
    return {
        entries,
        debug(message, context) { entries.push({ level: "debug", message, context }); },
        info(message, context) { entries.push({ level: "info", message, context }); },
        warn(message, context) { entries.push({ level: "warn", message, context }); },
        error(message, context) { entries.push({ level: "error", message, context }); },
    };
}
async function setupHarness() {
    const logger = createCapturingLogger();
    const tmpDir = node_fs_1.default.mkdtempSync(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-logger-test-"));
    const logPath = node_path_1.default.join(tmpDir, "request.log");
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: logPath },
        handshakeTimeoutMs: 5000,
        logger,
    });
    await client.connect();
    return {
        client,
        logger,
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
        model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
        messages: [{ role: "user", content: "hello" }],
        options: { temperature: 0.2, session_id: "testNanoIdSess1234567" },
    };
}
(0, node_test_1.default)("getNoopLogger returns a logger with all four methods", () => {
    const logger = (0, src_1.getNoopLogger)();
    strict_1.default.equal(typeof logger.debug, "function");
    strict_1.default.equal(typeof logger.info, "function");
    strict_1.default.equal(typeof logger.warn, "function");
    strict_1.default.equal(typeof logger.error, "function");
    logger.debug("test");
    logger.info("test");
    logger.warn("test");
    logger.error("test");
});
(0, node_test_1.default)("getNoopLogger returns the same singleton instance", () => {
    strict_1.default.strictEqual((0, src_1.getNoopLogger)(), (0, src_1.getNoopLogger)());
});
(0, node_test_1.default)("stdio transport logs connect/handshake and close events", async () => {
    const logger = createCapturingLogger();
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "ready-server.js")],
        handshakeTimeoutMs: 5000,
        logger,
    });
    await client.connect();
    const spawnLog = logger.entries.find((e) => e.message === "stdio: spawning process");
    strict_1.default.ok(spawnLog, "expected 'stdio: spawning process' log");
    strict_1.default.equal(spawnLog.context?.command, process.execPath);
    const handshakeLog = logger.entries.find((e) => e.message === "stdio: waiting for handshake");
    strict_1.default.ok(handshakeLog, "expected 'stdio: waiting for handshake' log");
    const completeLog = logger.entries.find((e) => e.message === "stdio: handshake complete");
    strict_1.default.ok(completeLog, "expected 'stdio: handshake complete' log");
    await client.close();
    const closeLog = logger.entries.find((e) => e.message === "stdio: closing transport");
    strict_1.default.ok(closeLog, "expected 'stdio: closing transport' log");
});
(0, node_test_1.default)("stdio transport logs frame send and receive", async () => {
    const logger = createCapturingLogger();
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "ready-server.js")],
        handshakeTimeoutMs: 5000,
        logger,
    });
    await client.connect();
    logger.entries.length = 0;
    client.send({ type: "stream_request", stream_id: "s1" });
    const sendLog = logger.entries.find((e) => e.message === "stdio: sending frame");
    strict_1.default.ok(sendLog, "expected 'stdio: sending frame' log");
    strict_1.default.equal(sendLog.context?.type, "stream_request");
    strict_1.default.equal(sendLog.context?.stream_id, "s1");
    const frame = await client.nextFrame(5000);
    const receiveLog = logger.entries.find((e) => e.message === "stdio: received frame");
    strict_1.default.ok(receiveLog, "expected 'stdio: received frame' log");
    strict_1.default.equal(receiveLog.context?.type, frame.type);
    await client.close();
});
(0, node_test_1.default)("stdio transport logs error frame during handshake", async () => {
    const logger = createCapturingLogger();
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "error-server.js")],
        handshakeTimeoutMs: 5000,
        logger,
    });
    await strict_1.default.rejects(() => client.connect());
    const receiveLog = logger.entries.find((e) => e.message === "stdio: received frame");
    strict_1.default.ok(receiveLog, "expected 'stdio: received frame' log");
    strict_1.default.equal(receiveLog.context?.type, "error");
    await client.close();
});
(0, node_test_1.default)("binary resolver logs resolution steps", async () => {
    const logger = createCapturingLogger();
    const tempDir = await node_fs_1.default.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-bin-log-"));
    const binaryPath = node_path_1.default.join(tempDir, process.platform === "win32" ? "makai.exe" : "makai");
    await node_fs_1.default.promises.writeFile(binaryPath, "fixture");
    const prev = process.env.MAKAI_BINARY_PATH;
    process.env.MAKAI_BINARY_PATH = binaryPath;
    try {
        await (0, src_1.resolveMakaiBinary)({ logger });
        const resolvingLog = logger.entries.find((e) => e.message === "binary: resolving from explicit path");
        strict_1.default.ok(resolvingLog, "expected 'binary: resolving from explicit path' log");
        strict_1.default.equal(resolvingLog.context?.path, node_path_1.default.resolve(binaryPath));
        const resolvedLog = logger.entries.find((e) => e.message === "binary: resolved from explicit path");
        strict_1.default.ok(resolvedLog, "expected 'binary: resolved from explicit path' log");
    }
    finally {
        if (prev === undefined)
            delete process.env.MAKAI_BINARY_PATH;
        else
            process.env.MAKAI_BINARY_PATH = prev;
        await node_fs_1.default.promises.rm(tempDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("binary resolver logs auto resolution candidate checks", async () => {
    const logger = createCapturingLogger();
    const prevPath = process.env.MAKAI_BINARY_PATH;
    const prevUrl = process.env.MAKAI_BINARY_URL;
    const prevChecksum = process.env.MAKAI_BINARY_SHA256;
    delete process.env.MAKAI_BINARY_PATH;
    delete process.env.MAKAI_BINARY_URL;
    delete process.env.MAKAI_BINARY_SHA256;
    try {
        await (0, src_1.resolveMakaiBinary)({ logger });
        const candidateLogs = logger.entries.filter((e) => e.message === "binary: checking local candidate");
        strict_1.default.ok(candidateLogs.length >= 1, "expected at least one 'binary: checking local candidate' log");
        const fallbackLog = logger.entries.find((e) => e.message === "binary: falling back to PATH lookup");
        strict_1.default.ok(fallbackLog, "expected 'binary: falling back to PATH lookup' log");
    }
    finally {
        if (prevPath === undefined)
            delete process.env.MAKAI_BINARY_PATH;
        else
            process.env.MAKAI_BINARY_PATH = prevPath;
        if (prevUrl === undefined)
            delete process.env.MAKAI_BINARY_URL;
        else
            process.env.MAKAI_BINARY_URL = prevUrl;
        if (prevChecksum === undefined)
            delete process.env.MAKAI_BINARY_SHA256;
        else
            process.env.MAKAI_BINARY_SHA256 = prevChecksum;
    }
});
(0, node_test_1.default)("provider complete logs stream_request and frame exchange", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client, { logger: harness.logger });
        await provider.complete(request());
        const streamLog = harness.logger.entries.find((e) => e.message === "provider: sending complete_request");
        strict_1.default.ok(streamLog, "expected 'provider: sending complete_request' log");
        strict_1.default.equal(streamLog.context?.model_ref, request().model_ref);
        strict_1.default.ok(typeof streamLog.context?.stream_id === "string");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("provider stream logs start and terminal events", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client, { logger: harness.logger });
        const events = [];
        for await (const event of provider.stream(request())) {
            events.push(event);
        }
        const startLog = harness.logger.entries.find((e) => e.message === "provider: starting stream");
        strict_1.default.ok(startLog, "expected 'provider: starting stream' log");
        const requestLog = harness.logger.entries.find((e) => e.message === "provider: sending stream_request");
        strict_1.default.ok(requestLog, "expected 'provider: sending stream_request' log");
        const streamStartedLog = harness.logger.entries.find((e) => e.message === "provider: stream started");
        strict_1.default.ok(streamStartedLog, "expected 'provider: stream started' log");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("agent run logs agent_start", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { logger: harness.logger });
        await agent.run(request());
        const startLog = harness.logger.entries.find((e) => e.message === "agent: sending agent_start");
        strict_1.default.ok(startLog, "expected 'agent: sending agent_start' log");
        strict_1.default.ok(typeof startLog.context?.session_id === "string");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("agent stream logs start events", async () => {
    const harness = await setupHarness();
    try {
        const agent = (0, src_1.createMakaiAgentApi)(harness.client, { logger: harness.logger });
        const events = [];
        for await (const event of agent.stream(request())) {
            events.push(event);
        }
        const startLog = harness.logger.entries.find((e) => e.message === "agent: starting stream");
        strict_1.default.ok(startLog, "expected 'agent: starting stream' log");
        const agentStartLog = harness.logger.entries.find((e) => e.message === "agent: sending agent_start");
        strict_1.default.ok(agentStartLog, "expected 'agent: sending agent_start' log");
        const streamStartedLog = harness.logger.entries.find((e) => e.message === "agent: stream started");
        strict_1.default.ok(streamStartedLog, "expected 'agent: stream started' log");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("no logger configured results in zero overhead (no crashes)", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "ready-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    client.send({ type: "stream_request", stream_id: "s1" });
    const frame = await client.nextFrame(5000);
    strict_1.default.ok(frame);
    await client.close();
});
(0, node_test_1.default)("logger captures envelope type and stream_id in frame send context", async () => {
    const harness = await setupHarness();
    try {
        const provider = (0, src_1.createMakaiProviderApi)(harness.client, { logger: harness.logger });
        await provider.complete(request());
        const sendLogs = harness.logger.entries.filter((e) => e.message === "stdio: sending frame" && e.context?.type === "complete_request");
        strict_1.default.ok(sendLogs.length >= 1, "expected at least one complete_request send log");
        const streamId = sendLogs[0].context?.stream_id;
        strict_1.default.equal(typeof streamId, "string");
        strict_1.default.ok(streamId.length >= 10, "stream_id should be a ULID");
    }
    finally {
        await harness.cleanup();
    }
});
(0, node_test_1.default)("isNoopLogger identifies no-op logger and distinguishes custom loggers", () => {
    strict_1.default.ok((0, src_1.isNoopLogger)((0, src_1.getNoopLogger)()), "getNoopLogger() should be identified as no-op");
    const custom = createCapturingLogger();
    strict_1.default.ok(!(0, src_1.isNoopLogger)(custom), "custom logger should not be identified as no-op");
});
(0, node_test_1.default)("no-op logger skips context allocation on send hot path", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "ready-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    client.send({ type: "stream_request", stream_id: "s1" });
    const frame = await client.nextFrame(5000);
    strict_1.default.ok(frame);
    await client.close();
});
(0, node_test_1.default)("createMakaiAgentApiWithModels forwards logger to nested models API", async () => {
    const logger = createCapturingLogger();
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [fixtureScript],
        env: { ...process.env, MAKAI_TEST_REQUEST_LOG: node_path_1.default.join(node_os_1.default.tmpdir(), "makai-agent-models-test.log") },
        handshakeTimeoutMs: 5000,
        logger,
    });
    await client.connect();
    try {
        const agentWithModels = (0, src_1.createMakaiAgentApiWithModels)(client, { logger, responseTimeoutMs: 5000 });
        await agentWithModels.models.list();
        const modelsLog = logger.entries.find((e) => e.message === "models: sending models_request");
        strict_1.default.ok(modelsLog, "expected models API to log via forwarded logger");
    }
    finally {
        await client.close();
    }
});
