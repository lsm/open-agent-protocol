"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_path_1 = __importDefault(require("node:path"));
const node_test_1 = __importDefault(require("node:test"));
const src_1 = require("../src");
const sourceFixturesDir = node_path_1.default.resolve(__dirname, "../../sdk/typescript/test/fixtures");
(0, node_test_1.default)("connect succeeds with ready handshake and receives event frame", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "ready-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        client.send({ type: "stream_request", stream_id: "s1" });
        const frame = await client.nextFrame(5000);
        strict_1.default.equal(frame.type, "event");
        strict_1.default.equal(frame.stream_id, "s1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("nextFrameForStream preserves foreign frames for their owner", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "route-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        client.send({ type: "stream_request", stream_id: "s1" });
        client.send({ type: "stream_request", stream_id: "s2" });
        const secondFrame = await client.nextFrameForStream("s2", 5000);
        strict_1.default.equal(secondFrame.type, "event");
        strict_1.default.equal(secondFrame.stream_id, "s2");
        const firstFrame = await client.nextFrameForStream("s1", 5000);
        strict_1.default.equal(firstFrame.type, "event");
        strict_1.default.equal(firstFrame.stream_id, "s1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("nextFrameForSession preserves foreign session frames for their owner", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "route-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        client.send({ type: "agent_message", session_id: "a1" });
        client.send({ type: "agent_message", session_id: "a2" });
        const secondFrame = await client.nextFrameForSession("a2", 5000);
        strict_1.default.equal(secondFrame.type, "agent_event");
        strict_1.default.equal(secondFrame.session_id, "a2");
        const firstFrame = await client.nextFrameForSession("a1", 5000);
        strict_1.default.equal(firstFrame.type, "agent_event");
        strict_1.default.equal(firstFrame.session_id, "a1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("targeted frame reads preserve frames across stream and session owners", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "route-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        client.send({ type: "stream_request", stream_id: "s1" });
        client.send({ type: "agent_message", session_id: "a1" });
        const agentFrame = await client.nextFrameForSession("a1", 5000);
        strict_1.default.equal(agentFrame.type, "agent_event");
        strict_1.default.equal(agentFrame.session_id, "a1");
        const streamFrame = await client.nextFrameForStream("s1", 5000);
        strict_1.default.equal(streamFrame.type, "event");
        strict_1.default.equal(streamFrame.stream_id, "s1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("correlated session waits on one session each receive their own reply", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const first = client.nextFrameForSession("a1", 5000, { correlate: "req-first" });
        const second = client.nextFrameForSession("a1", 5000, { correlate: "req-second" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-second" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-first" });
        const [firstFrame, secondFrame] = await Promise.all([first, second]);
        strict_1.default.equal(firstFrame.in_reply_to, "req-first");
        strict_1.default.equal(secondFrame.in_reply_to, "req-second");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("correlated reply is not consumed by an uncorrelated waiter on the same session", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const established = client.nextFrameForSession("a1", 5000);
        const duplicate = client.nextFrameForSession("a1", 5000, { correlate: "req-duplicate" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-duplicate" });
        const duplicateFrame = await duplicate;
        strict_1.default.equal(duplicateFrame.in_reply_to, "req-duplicate");
        let timersFired = 0;
        setTimeout(() => { timersFired += 1; }, 25);
        setTimeout(() => { timersFired += 1; }, 75);
        const stolen = await Promise.race([
            established.then((frame) => ({ stole: true, in_reply_to: frame.in_reply_to })),
            new Promise((resolve) => setTimeout(() => resolve({ stole: false }), 150)),
        ]);
        strict_1.default.deepEqual(stolen, { stole: false });
        strict_1.default.equal(timersFired, 2);
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-output", payload: { omit_in_reply_to: true } });
        const establishedFrame = await established;
        strict_1.default.equal(establishedFrame.session_id, "a1");
        strict_1.default.equal(establishedFrame.in_reply_to, undefined);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("correlated stream waits on one stream each receive their own reply", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const first = client.nextFrameForStream("s1", 5000, { correlate: "req-first" });
        const second = client.nextFrameForStream("s1", 5000, { correlate: "req-second" });
        client.send({ type: "stream_request", stream_id: "s1", message_id: "req-first" });
        client.send({ type: "stream_request", stream_id: "s1", message_id: "req-second" });
        const [firstFrame, secondFrame] = await Promise.all([first, second]);
        strict_1.default.equal(firstFrame.in_reply_to, "req-first");
        strict_1.default.equal(secondFrame.in_reply_to, "req-second");
        strict_1.default.equal(firstFrame.stream_id, "s1");
        strict_1.default.equal(secondFrame.stream_id, "s1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("frames with unmatched or absent in_reply_to keep session-routed behavior", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const unmatched = client.nextFrameForSession("a1", 5000);
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-nobody-waiting-for" });
        const unmatchedFrame = await unmatched;
        strict_1.default.equal(unmatchedFrame.in_reply_to, "req-nobody-waiting-for");
        strict_1.default.equal(unmatchedFrame.session_id, "a1");
        const uncorrelated = client.nextFrameForSession("a1", 5000, { correlate: "req-registered" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-no-reply-to", payload: { omit_in_reply_to: true } });
        const uncorrelatedFrame = await uncorrelated;
        strict_1.default.equal(uncorrelatedFrame.in_reply_to, undefined);
        strict_1.default.equal(uncorrelatedFrame.session_id, "a1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("reply arriving while its owner is between waits is parked, not consumed by a foreign correlated waiter", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const established = client.nextFrameForSession("a1", 5000, { correlate: "req-a" });
        const ownerFirst = client.nextFrameForSession("a1", 5000, { correlate: "req-b" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-b" });
        strict_1.default.equal((await ownerFirst).in_reply_to, "req-b");
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-b" });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const stolen = await Promise.race([
            established.then((frame) => ({ stole: true, in_reply_to: frame.in_reply_to })),
            new Promise((resolve) => setTimeout(() => resolve({ stole: false }), 100)),
        ]);
        strict_1.default.deepEqual(stolen, { stole: false });
        const reclaimed = await client.nextFrameForSession("a1", 5000, { correlate: "req-b" });
        strict_1.default.equal(reclaimed.in_reply_to, "req-b");
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-a" });
        strict_1.default.equal((await established).in_reply_to, "req-a");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("a second reply parked by a foreign waiter survives its owner winning the concurrent read", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const owner = client.nextFrameForSession("a1", 5000, { correlate: "req-a" });
        const foreign = client.nextFrameForSession("a1", 5000, { correlate: "req-b" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-a", payload: { replies: 2 } });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-b" });
        strict_1.default.equal((await owner).in_reply_to, "req-a");
        strict_1.default.equal((await foreign).in_reply_to, "req-b");
        const second = await client.nextFrameForSession("a1", 2000, { correlate: "req-a" });
        strict_1.default.equal(second.in_reply_to, "req-a");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("concurrent waits sharing one correlate are each delivered while a foreign waiter holds the read lock", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const lockHolder = client.nextFrameForSession("a1", 3000, { correlate: "req-idle" });
        const firstOwner = client.nextFrameForSession("a1", 1500, { correlate: "req-a" });
        const secondOwner = client.nextFrameForSession("a1", 1500, { correlate: "req-a" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-a", payload: { replies: 2 } });
        const lockHolderSettled = lockHolder.then(() => "lock-holder", () => "lock-holder");
        const bothDelivered = Promise.all([firstOwner, secondOwner]).then(() => "owners", () => "owners");
        strict_1.default.equal(await Promise.race([bothDelivered, lockHolderSettled]), "owners");
        const delivered = await Promise.all([firstOwner, secondOwner]);
        strict_1.default.deepEqual(delivered.map((frame) => frame.in_reply_to), ["req-a", "req-a"]);
        await strict_1.default.rejects(lockHolder, /timed out/);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("replies-only wait parks uncorrelated frames for the route owner", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const duplicate = client.nextFrameForSession("a1", 5000, { correlate: "req-duplicate", repliesOnly: true });
        const owner = client.nextFrameForSession("a1", 5000, { correlate: "req-owner" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-output", payload: { omit_in_reply_to: true } });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const consumed = await Promise.race([
            duplicate.then((frame) => ({ by: "duplicate", type: frame.type })),
            new Promise((resolve) => setTimeout(() => resolve({ by: "none" }), 100)),
        ]);
        strict_1.default.deepEqual(consumed, { by: "none" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-duplicate" });
        strict_1.default.equal((await duplicate).in_reply_to, "req-duplicate");
        const ownerFrame = await owner;
        strict_1.default.equal(ownerFrame.session_id, "a1");
        strict_1.default.equal(ownerFrame.in_reply_to, undefined);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("aborted correlated wait leaves its parked reply for the replacement waiter", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const established = client.nextFrameForSession("a1", 5000);
        const controller = new AbortController();
        const aborted = client.nextFrameForSession("a1", 5000, { correlate: "req-aborted", signal: controller.signal });
        controller.abort();
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-aborted" });
        await strict_1.default.rejects(aborted, /frame wait for session a1 aborted/);
        const replacement = await client.nextFrameForSession("a1", 5000, { correlate: "req-aborted" });
        strict_1.default.equal(replacement.in_reply_to, "req-aborted");
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-established", payload: { omit_in_reply_to: true } });
        strict_1.default.equal((await established).session_id, "a1");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("replies-only wait skips frames already parked on the session queue", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const foreign = client.nextFrameForSession("a2", 5000);
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-owner-output", payload: { omit_in_reply_to: true } });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const duplicate = client.nextFrameForSession("a1", 5000, { correlate: "req-duplicate", repliesOnly: true });
        const skipped = await Promise.race([
            duplicate.then((frame) => ({ took: true, in_reply_to: frame.in_reply_to })),
            new Promise((resolve) => setTimeout(() => resolve({ took: false }), 100)),
        ]);
        strict_1.default.deepEqual(skipped, { took: false });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-duplicate" });
        strict_1.default.equal((await duplicate).in_reply_to, "req-duplicate");
        const owner = await client.nextFrameForSession("a1", 5000);
        strict_1.default.equal(owner.session_id, "a1");
        strict_1.default.equal(owner.in_reply_to, undefined);
        client.send({ type: "agent_message", session_id: "a2", message_id: "req-foreign", payload: { omit_in_reply_to: true } });
        strict_1.default.equal((await foreign).session_id, "a2");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("already-aborted correlated wait rejects without consuming its parked reply", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "correlate-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const foreignReader = client.nextFrameForSession("a1", 5000, { correlate: "req-foreign" });
        const ownerFirst = client.nextFrameForSession("a1", 5000, { correlate: "req-x" });
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-x" });
        strict_1.default.equal((await ownerFirst).in_reply_to, "req-x");
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-x" });
        await new Promise((resolve) => setTimeout(resolve, 100));
        const controller = new AbortController();
        controller.abort();
        await strict_1.default.rejects(client.nextFrameForSession("a1", 5000, { correlate: "req-x", signal: controller.signal }), /frame wait for session a1 aborted/);
        const replacement = await client.nextFrameForSession("a1", 5000, { correlate: "req-x" });
        strict_1.default.equal(replacement.in_reply_to, "req-x");
        client.send({ type: "agent_message", session_id: "a1", message_id: "req-foreign" });
        strict_1.default.equal((await foreignReader).in_reply_to, "req-foreign");
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("nextFrameForStream evicts late orphaned frames from the shared buffer", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "one-stream-server.js")],
        handshakeTimeoutMs: 5000,
        streamFrameQueueTtlMs: 20,
    });
    await client.connect();
    try {
        const orphanedWaiter = client.nextFrameForStream("s2", 30);
        await strict_1.default.rejects(orphanedWaiter, /timed out waiting for frame for stream s2 after 30ms/);
        const blocked = client.nextFrameForStream("s1", 80);
        client.send({ type: "stream_request", stream_id: "s2" });
        await strict_1.default.rejects(blocked, /timed out waiting for frame for stream s1 after 80ms/);
        await strict_1.default.rejects(() => client.nextFrameForStream("s2", 20), /timed out waiting for frame for stream s2 after 20ms/);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("createMakaiStdioClient forwards streamFrameQueueTtlMs", async () => {
    const client = await (0, src_1.createMakaiStdioClient)({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "one-stream-server.js")],
        handshakeTimeoutMs: 5000,
        streamFrameQueueTtlMs: 20,
    });
    await client.connect();
    try {
        const orphanedWaiter = client.nextFrameForStream("s2", 30);
        await strict_1.default.rejects(orphanedWaiter, /timed out waiting for frame for stream s2 after 30ms/);
        const blocked = client.nextFrameForStream("s1", 80);
        client.send({ type: "stream_request", stream_id: "s2" });
        await strict_1.default.rejects(blocked, /timed out waiting for frame for stream s1 after 80ms/);
        await strict_1.default.rejects(() => client.nextFrameForStream("s2", 20), /timed out waiting for frame for stream s2 after 20ms/);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("nextFrameForStream does not consume timeout budget while waiting for read lock", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "one-stream-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const blocked = client.nextFrameForStream("s1", 160);
        const delayed = new Promise((resolve) => setTimeout(resolve, 40))
            .then(() => client.nextFrameForStream("s2", 80));
        setTimeout(() => {
            client.send({ type: "stream_request", stream_id: "s2" });
        }, 110);
        const [delayedResult, blockedResult] = await Promise.allSettled([delayed, blocked]);
        strict_1.default.equal(delayedResult.status, "fulfilled");
        strict_1.default.equal(delayedResult.value.type, "event");
        strict_1.default.equal(delayedResult.value.stream_id, "s2");
        strict_1.default.equal(blockedResult.status, "rejected");
        strict_1.default.match(blockedResult.reason instanceof Error ? blockedResult.reason.message : String(blockedResult.reason), /timed out waiting for frame for stream s1 after 160ms/);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("nextFrameForSession does not consume timeout budget while waiting for read lock", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "route-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await client.connect();
    try {
        const blocked = client.nextFrameForSession("a1", 160);
        const delayed = new Promise((resolve) => setTimeout(resolve, 40))
            .then(() => client.nextFrameForSession("a2", 80));
        setTimeout(() => {
            client.send({ type: "agent_message", session_id: "a2" });
        }, 110);
        const [delayedResult, blockedResult] = await Promise.allSettled([delayed, blocked]);
        strict_1.default.equal(delayedResult.status, "fulfilled");
        strict_1.default.equal(delayedResult.value.type, "agent_event");
        strict_1.default.equal(delayedResult.value.session_id, "a2");
        strict_1.default.equal(blockedResult.status, "rejected");
        strict_1.default.match(blockedResult.reason instanceof Error ? blockedResult.reason.message : String(blockedResult.reason), /timed out waiting for frame for session a1 after 160ms/);
    }
    finally {
        await client.close();
    }
});
(0, node_test_1.default)("connect surfaces protocol error frame", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "error-server.js")],
        handshakeTimeoutMs: 5000,
    });
    await strict_1.default.rejects(() => client.connect(), (error) => error instanceof src_1.StdioProtocolError &&
        error.code === "version_mismatch" &&
        error.message.includes("unsupported protocol"));
    await client.close();
});
(0, node_test_1.default)("connect times out when no handshake frame arrives", async () => {
    const client = new src_1.MakaiStdioClient({
        command: process.execPath,
        args: [node_path_1.default.join(sourceFixturesDir, "silent-server.js")],
        handshakeTimeoutMs: 100,
    });
    await strict_1.default.rejects(() => client.connect(), /handshake timed out/);
    await client.close();
});
