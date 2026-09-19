"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.MakaiStdioClient = exports.StdioProtocolError = void 0;
exports.createMakaiStdioClient = createMakaiStdioClient;
const node_child_process_1 = require("node:child_process");
const node_readline_1 = require("node:readline");
const binary_resolver_1 = require("./binary_resolver");
const logger_1 = require("./logger");
class StdioProtocolError extends Error {
    code;
    constructor(message, code) {
        super(message);
        this.code = code;
        this.name = "StdioProtocolError";
    }
}
exports.StdioProtocolError = StdioProtocolError;
const STREAM_FRAME_QUEUE_TTL_MS = 30_000;
class MakaiStdioClient {
    options;
    logger;
    child = null;
    lineReader = null;
    pendingHandshake = null;
    frameQueue = [];
    frameWaiters = [];
    streamFrameQueues = new Map();
    sessionFrameQueues = new Map();
    replyFrameQueues = new Map();
    activeCorrelates = new Map();
    correlateDeliveries = new Map();
    streamReadLock = Promise.resolve();
    constructor(options) {
        this.options = {
            ...options,
            args: options.args ?? [],
            expectedProtocolVersion: options.expectedProtocolVersion ?? "1",
            handshakeTimeoutMs: options.handshakeTimeoutMs ?? 1500,
            streamFrameQueueTtlMs: options.streamFrameQueueTtlMs ?? STREAM_FRAME_QUEUE_TTL_MS,
        };
        this.logger = options.logger ?? (0, logger_1.getNoopLogger)();
    }
    async connect() {
        if (this.child) {
            throw new Error("client is already connected");
        }
        this.logger.debug("stdio: spawning process", { command: this.options.command, args: this.options.args });
        const child = (0, node_child_process_1.spawn)(this.options.command, this.options.args, {
            cwd: this.options.cwd,
            env: this.options.env,
            stdio: "pipe",
        });
        this.child = child;
        child.on("error", (error) => {
            if (this.child !== child)
                return;
            this.logger.error("stdio: process error event", { error: error.message });
            this.failHandshakeIfPending(error);
            this.failPendingFrameWaiters(error);
        });
        child.on("exit", (code, signal) => {
            if (this.child !== child)
                return;
            this.logger.debug("stdio: process exited", { code, signal: signal ?? undefined });
            const error = new Error(`stdio process exited (code=${code}, signal=${signal})`);
            this.failHandshakeIfPending(error);
            this.failPendingFrameWaiters(error);
            this.cleanupProcessHandles();
        });
        this.lineReader = (0, node_readline_1.createInterface)({ input: child.stdout });
        this.lineReader.on("line", (line) => this.handleLine(line));
        this.logger.debug("stdio: waiting for handshake", { timeout_ms: this.options.handshakeTimeoutMs });
        try {
            await new Promise((resolve, reject) => {
                const timer = setTimeout(() => {
                    reject(new Error(`stdio handshake timed out after ${this.options.handshakeTimeoutMs}ms`));
                    this.pendingHandshake = null;
                }, this.options.handshakeTimeoutMs);
                this.pendingHandshake = { resolve, reject, timer };
            });
        }
        catch (error) {
            this.terminateChild();
            throw error;
        }
        this.logger.info("stdio: handshake complete");
    }
    terminateChild() {
        const child = this.child;
        if (!child)
            return;
        this.logger.debug("stdio: terminating process after failed handshake", { pid: child.pid });
        this.failPendingFrameWaiters(new Error("stdio handshake failed"));
        this.lineReader?.close();
        this.lineReader = null;
        this.child = null;
        try {
            child.stdin.end();
        }
        catch {
        }
        if (child.exitCode === null && child.signalCode === null) {
            try {
                child.kill();
            }
            catch {
            }
            const killer = setTimeout(() => {
                if (child.exitCode === null && child.signalCode === null) {
                    try {
                        child.kill("SIGKILL");
                    }
                    catch {
                    }
                }
            }, 500);
            killer.unref();
            child.once("exit", () => clearTimeout(killer));
        }
        child.unref();
    }
    send(frame) {
        if (!this.child) {
            throw new Error("client is not connected");
        }
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("stdio: sending frame", { type: frame.type, stream_id: frame.stream_id, session_id: frame.session_id, sequence: frame.sequence });
        }
        this.child.stdin.write(`${JSON.stringify(frame)}\n`);
    }
    nextFrame(timeoutMs = 1000) {
        if (this.frameQueue.length > 0) {
            return Promise.resolve(this.frameQueue.shift());
        }
        return new Promise((resolve, reject) => {
            const waiter = { resolve, reject, timer: undefined };
            waiter.timer = setTimeout(() => {
                const index = this.frameWaiters.indexOf(waiter);
                if (index >= 0)
                    this.frameWaiters.splice(index, 1);
                reject(new Error(`timed out waiting for frame after ${timeoutMs}ms`));
            }, timeoutMs);
            this.frameWaiters.push(waiter);
        });
    }
    async nextFrameForStream(streamId, timeoutMs = 1000, options) {
        if (streamId.length === 0) {
            throw new Error("streamId is required");
        }
        return this.waitForRoutedFrame("stream", streamId, timeoutMs, options);
    }
    async nextFrameForSession(sessionId, timeoutMs = 1000, options) {
        if (sessionId.length === 0) {
            throw new Error("sessionId is required");
        }
        return this.waitForRoutedFrame("session", sessionId, timeoutMs, options);
    }
    async waitForRoutedFrame(route, routeId, timeoutMs, options) {
        const correlate = options?.correlate;
        if (correlate !== undefined)
            this.retainCorrelate(correlate);
        try {
            if (options?.signal?.aborted) {
                throw new Error(`frame wait for session ${routeId} aborted`);
            }
            const queued = this.dequeueOwnFrame(route, routeId, correlate, options?.repliesOnly);
            if (queued)
                return queued;
            if (correlate === undefined) {
                return await this.withStreamReadLock(() => this.readRoutedLoop(route, routeId, timeoutMs, options));
            }
            const state = { settled: false };
            let signalPoke;
            const poke = new Promise((resolve) => {
                signalPoke = resolve;
            });
            const delivery = { signal: signalPoke, state, signalled: false };
            this.registerCorrelateDelivery(correlate, delivery);
            let winner;
            try {
                winner = await Promise.race([
                    this.withStreamReadLock(async () => {
                        if (state.settled)
                            throw new Error(`frame wait for ${route} ${routeId} superseded`);
                        return await this.readRoutedLoop(route, routeId, timeoutMs, options);
                    }),
                    poke.then(() => {
                        if (options?.signal?.aborted) {
                            throw new Error(`frame wait for session ${routeId} aborted`);
                        }
                        return undefined;
                    }),
                ]);
            }
            finally {
                state.settled = true;
                this.releaseCorrelateDelivery(correlate, delivery);
            }
            if (winner !== undefined)
                return winner;
            const delivered = this.dequeueRoutedFrame(this.replyFrameQueues, correlate);
            if (delivered !== undefined)
                return delivered;
            return await this.withStreamReadLock(() => this.readRoutedLoop(route, routeId, timeoutMs, options));
        }
        finally {
            if (correlate !== undefined)
                this.releaseCorrelate(correlate);
        }
    }
    async readRoutedLoop(route, routeId, timeoutMs, options) {
        if (options?.signal?.aborted) {
            throw new Error(`frame wait for session ${routeId} aborted`);
        }
        const deadline = Date.now() + timeoutMs;
        while (true) {
            const remainingMs = deadline - Date.now();
            if (remainingMs <= 0) {
                throw new Error(`timed out waiting for frame for ${route} ${routeId} after ${timeoutMs}ms`);
            }
            const queued = this.dequeueOwnFrame(route, routeId, options?.correlate, options?.repliesOnly);
            if (queued)
                return queued;
            let frame;
            try {
                frame = await this.nextFrame(remainingMs);
            }
            catch (error) {
                if (deadline - Date.now() <= 0 || isNextFrameTimeout(error, remainingMs)) {
                    throw new Error(`timed out waiting for frame for ${route} ${routeId} after ${timeoutMs}ms`);
                }
                throw error;
            }
            if (options?.signal?.aborted) {
                this.enqueueRoutableFrame(frame);
                throw new Error(`frame wait for session ${routeId} aborted`);
            }
            const frameReplyTo = typeof frame.in_reply_to === "string" ? frame.in_reply_to : undefined;
            if (options?.correlate !== undefined && frameReplyTo === options.correlate)
                return frame;
            if (frameReplyTo !== undefined && this.hasActiveCorrelate(frameReplyTo)) {
                this.enqueueRoutableFrame(frame);
                continue;
            }
            if (frameReplyTo !== undefined && options?.correlate !== undefined) {
                this.enqueueRoutableFrame(frame);
                continue;
            }
            if (options?.repliesOnly) {
                this.enqueueRoutableFrame(frame);
                continue;
            }
            if (this.frameMatchesRoute(frame, route, routeId))
                return frame;
            this.enqueueRoutableFrame(frame);
        }
    }
    async close() {
        if (!this.child)
            return;
        this.logger.debug("stdio: closing transport");
        const child = this.child;
        child.stdin.end();
        await Promise.race([
            new Promise((resolve) => {
                child.once("exit", () => resolve());
            }),
            new Promise((resolve) => {
                setTimeout(() => {
                    if (child.exitCode === null) {
                        child.kill();
                    }
                    resolve();
                }, 200);
            }),
        ]);
        this.cleanupProcessHandles();
    }
    dequeueStreamFrame(streamId) {
        return this.dequeueRoutedFrame(this.streamFrameQueues, streamId);
    }
    dequeueSessionFrame(sessionId) {
        return this.dequeueRoutedFrame(this.sessionFrameQueues, sessionId);
    }
    dequeueOwnFrame(route, routeId, correlate, repliesOnly = false) {
        this.pruneExpiredRoutedFrames();
        if (correlate !== undefined) {
            const reply = this.dequeueRoutedFrame(this.replyFrameQueues, correlate);
            if (reply)
                return reply;
        }
        const queues = route === "stream" ? this.streamFrameQueues : this.sessionFrameQueues;
        const queued = queues.get(routeId);
        if (!queued || queued.length === 0)
            return undefined;
        let index;
        if (correlate === undefined) {
            index = 0;
        }
        else if (repliesOnly) {
            index = queued.findIndex((entry) => entry.replyTo === correlate);
        }
        else {
            index = queued.findIndex((entry) => entry.replyTo === undefined || entry.replyTo === correlate);
        }
        if (index < 0)
            return undefined;
        const [entry] = queued.splice(index, 1);
        if (queued.length === 0)
            queues.delete(routeId);
        return entry.frame;
    }
    frameMatchesRoute(frame, route, routeId) {
        if (route === "stream") {
            return typeof frame.stream_id === "string" && frame.stream_id === routeId;
        }
        return typeof frame.session_id === "string" && frame.session_id === routeId;
    }
    dequeueRoutedFrame(queues, id) {
        this.pruneExpiredRoutedFrames();
        const queued = queues.get(id);
        if (!queued || queued.length === 0)
            return undefined;
        const entry = queued.shift();
        if (queued.length === 0)
            queues.delete(id);
        return entry.frame;
    }
    enqueueRoutableFrame(frame) {
        const frameReplyTo = typeof frame.in_reply_to === "string" ? frame.in_reply_to : undefined;
        if (frameReplyTo !== undefined && this.hasActiveCorrelate(frameReplyTo)) {
            this.deliverCorrelatedFrame(frameReplyTo, frame);
            return;
        }
        const frameStreamId = typeof frame.stream_id === "string" ? frame.stream_id : undefined;
        if (frameStreamId) {
            this.enqueueRoutedFrame(this.streamFrameQueues, frameStreamId, frame, frameReplyTo);
            return;
        }
        const frameSessionId = typeof frame.session_id === "string" ? frame.session_id : undefined;
        if (frameSessionId) {
            this.enqueueRoutedFrame(this.sessionFrameQueues, frameSessionId, frame, frameReplyTo);
        }
    }
    deliverCorrelatedFrame(correlate, frame) {
        this.enqueueRoutedFrame(this.replyFrameQueues, correlate, frame, correlate);
        const pending = this.correlateDeliveries.get(correlate);
        const waiting = pending?.find((entry) => !entry.signalled && !entry.state.settled);
        if (!waiting)
            return;
        waiting.signalled = true;
        waiting.signal();
    }
    registerCorrelateDelivery(correlate, delivery) {
        const pending = this.correlateDeliveries.get(correlate);
        if (pending)
            pending.push(delivery);
        else
            this.correlateDeliveries.set(correlate, [delivery]);
    }
    releaseCorrelateDelivery(correlate, delivery) {
        const pending = this.correlateDeliveries.get(correlate);
        if (!pending)
            return;
        const index = pending.indexOf(delivery);
        if (index >= 0)
            pending.splice(index, 1);
        if (pending.length === 0)
            this.correlateDeliveries.delete(correlate);
    }
    enqueueRoutedFrame(queues, id, frame, replyTo) {
        this.pruneExpiredRoutedFrames();
        const queued = queues.get(id) ?? [];
        queued.push({ frame, expiresAt: Date.now() + this.options.streamFrameQueueTtlMs, replyTo });
        queues.set(id, queued);
    }
    retainCorrelate(correlate) {
        this.activeCorrelates.set(correlate, (this.activeCorrelates.get(correlate) ?? 0) + 1);
    }
    releaseCorrelate(correlate) {
        const count = (this.activeCorrelates.get(correlate) ?? 0) - 1;
        if (count > 0)
            this.activeCorrelates.set(correlate, count);
        else
            this.activeCorrelates.delete(correlate);
    }
    hasActiveCorrelate(correlate) {
        return (this.activeCorrelates.get(correlate) ?? 0) > 0;
    }
    pruneExpiredRoutedFrames(now = Date.now()) {
        this.pruneExpiredQueue(this.streamFrameQueues, now);
        this.pruneExpiredQueue(this.sessionFrameQueues, now);
        this.pruneExpiredQueue(this.replyFrameQueues, now);
    }
    pruneExpiredQueue(queues, now) {
        for (const [id, queued] of queues) {
            const unexpired = queued.filter((entry) => entry.expiresAt > now);
            if (unexpired.length > 0) {
                if (unexpired.length !== queued.length) {
                    queues.set(id, unexpired);
                }
            }
            else {
                queues.delete(id);
            }
        }
    }
    async withStreamReadLock(operation) {
        const previous = this.streamReadLock;
        let release;
        this.streamReadLock = new Promise((resolve) => {
            release = resolve;
        });
        await previous;
        try {
            return await operation();
        }
        finally {
            release();
        }
    }
    handleLine(line) {
        let frame;
        try {
            frame = JSON.parse(line);
        }
        catch {
            this.logger.error("stdio: invalid JSON frame received", { line: line.length > 200 ? line.slice(0, 200) + "..." : line });
            this.failHandshakeIfPending(new Error(`invalid JSON frame: ${line}`));
            return;
        }
        if (!(0, logger_1.isNoopLogger)(this.logger)) {
            this.logger.debug("stdio: received frame", { type: frame.type, stream_id: frame.stream_id, session_id: frame.session_id, sequence: frame.sequence });
        }
        if (this.pendingHandshake) {
            const pending = this.pendingHandshake;
            this.pendingHandshake = null;
            clearTimeout(pending.timer);
            if (frame.type === "error") {
                pending.reject(new StdioProtocolError(String(frame.message ?? "stdio handshake failed"), typeof frame.code === "string" ? frame.code : undefined));
                return;
            }
            if (frame.type !== "ready") {
                pending.reject(new Error(`unexpected handshake frame type: ${frame.type}`));
                return;
            }
            const protocolVersion = String(frame.protocol_version ?? "");
            if (protocolVersion !== this.options.expectedProtocolVersion) {
                pending.reject(new StdioProtocolError(`protocol version mismatch (expected ${this.options.expectedProtocolVersion}, got ${protocolVersion})`, "version_mismatch"));
                return;
            }
            pending.resolve();
            return;
        }
        if (this.frameWaiters.length > 0) {
            const waiter = this.frameWaiters.shift();
            clearTimeout(waiter.timer);
            waiter.resolve(frame);
            return;
        }
        this.frameQueue.push(frame);
    }
    failHandshakeIfPending(error) {
        if (!this.pendingHandshake)
            return;
        const pending = this.pendingHandshake;
        this.pendingHandshake = null;
        clearTimeout(pending.timer);
        pending.reject(error);
    }
    failPendingFrameWaiters(error) {
        const waiters = this.frameWaiters.splice(0);
        for (const waiter of waiters) {
            clearTimeout(waiter.timer);
            waiter.reject(error);
        }
    }
    cleanupProcessHandles() {
        this.lineReader?.close();
        this.lineReader = null;
        this.child = null;
    }
}
exports.MakaiStdioClient = MakaiStdioClient;
function isNextFrameTimeout(error, timeoutMs) {
    return error instanceof Error && error.message === `timed out waiting for frame after ${timeoutMs}ms`;
}
async function createMakaiStdioClient(options = {}) {
    const resolverWithLogger = options.logger
        ? { ...options.resolver, logger: options.logger }
        : options.resolver ?? {};
    const command = options.command ?? (await (0, binary_resolver_1.resolveMakaiBinary)(resolverWithLogger));
    const args = options.args ?? ["--stdio"];
    return new MakaiStdioClient({
        command,
        args,
        cwd: options.cwd,
        env: options.env,
        expectedProtocolVersion: options.expectedProtocolVersion,
        handshakeTimeoutMs: options.handshakeTimeoutMs,
        streamFrameQueueTtlMs: options.streamFrameQueueTtlMs,
        logger: options.logger,
    });
}
