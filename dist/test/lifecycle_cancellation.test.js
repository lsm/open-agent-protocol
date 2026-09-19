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
const sourceFixturesDir = node_path_1.default.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const pidReportingServer = node_path_1.default.join(sourceFixturesDir, "pid-reporting-server.js");
const streamCancelObserverServer = node_path_1.default.join(sourceFixturesDir, "stream-cancel-observer-server.js");
const providerRequest = {
    model_ref: "anthropic/anthropic-messages@claude-sonnet-4-5",
    messages: [{ role: "user", content: "hi" }],
};
async function tempFile(prefix) {
    const dir = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), prefix));
    return node_path_1.default.join(dir, "data");
}
async function readLines(filePath) {
    try {
        const raw = await node_fs_1.promises.readFile(filePath, "utf8");
        return raw.split("\n").filter((line) => line.length > 0);
    }
    catch {
        return [];
    }
}
function processIsAlive(pid) {
    try {
        process.kill(pid, 0);
        return true;
    }
    catch (error) {
        if (error && typeof error === "object" && "code" in error && error.code === "EPERM")
            return true;
        return false;
    }
}
async function waitForPidFile(pidFile, timeoutMs = 4000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const raw = await node_fs_1.promises.readFile(pidFile, "utf8").catch(() => "");
        const pid = Number(raw.trim());
        if (Number.isInteger(pid) && pid > 0)
            return pid;
        await new Promise((resolve) => setTimeout(resolve, 10));
    }
    throw new Error(`fixture never reported a pid via ${pidFile}`);
}
async function waitForExit(pid, timeoutMs = 4000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        if (!processIsAlive(pid))
            return true;
        await new Promise((resolve) => setTimeout(resolve, 10));
    }
    return false;
}
async function waitForLine(filePath, predicate, timeoutMs = 2000) {
    const deadline = Date.now() + timeoutMs;
    let lines = await readLines(filePath);
    while (Date.now() < deadline && !predicate(lines)) {
        await new Promise((resolve) => setTimeout(resolve, 20));
        lines = await readLines(filePath);
    }
    return lines;
}
(0, node_test_1.default)("handshake timeout terminates the spawned runtime process", async () => {
    const pidFile = await tempFile("makai-pid-timeout-");
    const client = await (0, src_1.createMakaiStdioClient)({
        command: process.execPath,
        args: [pidReportingServer],
        env: { ...process.env, MAKAI_TEST_PID_FILE: pidFile, MAKAI_TEST_HANDSHAKE: "silent" },
        handshakeTimeoutMs: 150,
    });
    await strict_1.default.rejects(() => client.connect(), /handshake timed out/);
    const pid = await waitForPidFile(pidFile);
    strict_1.default.equal(await waitForExit(pid), true, `pid ${pid} survived a failed handshake`);
    await client.close();
});
(0, node_test_1.default)("createMakaiClient leaves no orphan runtime when the handshake times out", async () => {
    const pidFile = await tempFile("makai-pid-factory-");
    await strict_1.default.rejects(() => (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [pidReportingServer],
        env: { ...process.env, MAKAI_TEST_PID_FILE: pidFile, MAKAI_TEST_HANDSHAKE: "silent" },
        handshakeTimeoutMs: 150,
    }), /handshake timed out/);
    const pid = await waitForPidFile(pidFile);
    strict_1.default.equal(await waitForExit(pid), true, `createMakaiClient orphaned pid ${pid}`);
});
(0, node_test_1.default)("createMakaiAuthClient leaves no orphan runtime when the handshake times out", async () => {
    const pidFile = await tempFile("makai-pid-auth-factory-");
    await strict_1.default.rejects(() => (0, src_1.createMakaiAuthClient)({
        command: process.execPath,
        args: [pidReportingServer],
        env: { ...process.env, MAKAI_TEST_PID_FILE: pidFile, MAKAI_TEST_HANDSHAKE: "silent" },
        handshakeTimeoutMs: 150,
    }), /handshake timed out/);
    const pid = await waitForPidFile(pidFile);
    strict_1.default.equal(await waitForExit(pid), true, `createMakaiAuthClient orphaned pid ${pid}`);
});
(0, node_test_1.default)("protocol version mismatch terminates the spawned runtime process", async () => {
    const pidFile = await tempFile("makai-pid-skew-");
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [pidReportingServer],
        env: { ...process.env, MAKAI_TEST_PID_FILE: pidFile, MAKAI_TEST_HANDSHAKE: "version_mismatch" },
        handshakeTimeoutMs: 3000,
    });
    await strict_1.default.rejects(() => client.connect(), /protocol version mismatch/);
    const pid = await waitForPidFile(pidFile);
    strict_1.default.equal(await waitForExit(pid), true, `pid ${pid} survived a version-skew handshake`);
    await client.close();
});
(0, node_test_1.default)("handshake error frame terminates the spawned runtime process", async () => {
    const pidFile = await tempFile("makai-pid-errorframe-");
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [pidReportingServer],
        env: { ...process.env, MAKAI_TEST_PID_FILE: pidFile, MAKAI_TEST_HANDSHAKE: "error_frame" },
        handshakeTimeoutMs: 3000,
    });
    await strict_1.default.rejects(() => client.connect(), /unsupported protocol/);
    const pid = await waitForPidFile(pidFile);
    strict_1.default.equal(await waitForExit(pid), true, `pid ${pid} survived a rejected handshake`);
    await client.close();
});
(0, node_test_1.default)("breaking out of provider.stream cancels the runtime stream", async () => {
    const frameLog = await tempFile("makai-frames-break-");
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        env: { ...process.env, MAKAI_TEST_FRAME_LOG: frameLog },
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 5000,
    });
    try {
        let received = 0;
        for await (const event of client.provider.stream(providerRequest)) {
            if (event.type === "text_delta")
                received += 1;
            if (received >= 2)
                break;
        }
        strict_1.default.equal(received, 2);
        const frames = await waitForLine(frameLog, (lines) => lines.includes("abort_request"));
        strict_1.default.ok(frames.includes("abort_request"), `expected abort_request, runtime saw ${JSON.stringify(frames)}`);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("provider.stream that runs to completion does not cancel the runtime stream", async () => {
    const frameLog = await tempFile("makai-frames-complete-");
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        env: { ...process.env, MAKAI_TEST_FRAME_LOG: frameLog, MAKAI_TEST_DELTA_COUNT: "2", MAKAI_TEST_DELTA_INTERVAL_MS: "5" },
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 5000,
    });
    try {
        const types = [];
        for await (const event of client.provider.stream(providerRequest))
            types.push(event.type);
        strict_1.default.equal(types.at(-1), "message_end");
        await new Promise((resolve) => setTimeout(resolve, 150));
        const frames = await readLines(frameLog);
        strict_1.default.ok(!frames.includes("abort_request"), `unexpected abort_request after clean completion: ${JSON.stringify(frames)}`);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("aborting provider.stream cancels the runtime stream exactly once", async () => {
    const frameLog = await tempFile("makai-frames-abort-");
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        env: { ...process.env, MAKAI_TEST_FRAME_LOG: frameLog },
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 5000,
    });
    try {
        const controller = new AbortController();
        await strict_1.default.rejects(async () => {
            let received = 0;
            for await (const event of client.provider.stream({ ...providerRequest, options: { signal: controller.signal } })) {
                if (event.type === "text_delta")
                    received += 1;
                if (received >= 2)
                    controller.abort();
            }
        }, (error) => error instanceof Error && error.name === "AbortError");
        const frames = await waitForLine(frameLog, (lines) => lines.includes("abort_request"));
        strict_1.default.equal(frames.filter((frame) => frame === "abort_request").length, 1, `frames: ${JSON.stringify(frames)}`);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("provider and agent calls on a closed transport reject with MakaiStreamError", async () => {
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 1000,
    });
    await client.close();
    const isTransportStreamError = (error) => error instanceof src_1.MakaiStreamError && error.kind === "transport_error";
    await strict_1.default.rejects(() => client.provider.complete(providerRequest), isTransportStreamError);
    await strict_1.default.rejects(async () => {
        for await (const _event of client.provider.stream(providerRequest))
            break;
    }, isTransportStreamError);
    await strict_1.default.rejects(() => client.agent.run(providerRequest), isTransportStreamError);
    await strict_1.default.rejects(async () => {
        for await (const _event of client.agent.stream(providerRequest))
            break;
    }, isTransportStreamError);
});
(0, node_test_1.default)("aborting a provider call rejects with a plain AbortError, not MakaiStreamError", async () => {
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 5000,
    });
    try {
        const controller = new AbortController();
        controller.abort();
        await strict_1.default.rejects(() => client.provider.complete({ ...providerRequest, options: { signal: controller.signal } }), (error) => error instanceof Error &&
            error.name === "AbortError" &&
            !(error instanceof src_1.MakaiStreamError));
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("models.list on a closed transport rejects with a plain Error, not MakaiProtocolError", async () => {
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 1000,
    });
    await client.close();
    await strict_1.default.rejects(() => client.models.list(), (error) => error instanceof Error &&
        !(error instanceof src_1.MakaiProtocolError) &&
        error.message === "client is not connected");
});
(0, node_test_1.default)("responseTimeoutMs does not govern the auth namespace", async () => {
    const client = await (0, src_1.createMakaiClient)({
        command: process.execPath,
        args: [streamCancelObserverServer],
        handshakeTimeoutMs: 3000,
        responseTimeoutMs: 120,
    });
    try {
        const settled = { done: false };
        const pending = client.auth
            .listProviders()
            .then(() => {
            settled.done = true;
        })
            .catch((error) => {
            settled.done = true;
            strict_1.default.ok(error instanceof src_1.MakaiAuthError);
        });
        await new Promise((resolve) => setTimeout(resolve, 600));
        strict_1.default.equal(settled.done, false, "auth.listProviders honoured responseTimeoutMs; update the README if this changed");
        void pending;
    }
    finally {
        await client.close();
    }
});
