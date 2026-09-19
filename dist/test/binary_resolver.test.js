"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
const strict_1 = __importDefault(require("node:assert/strict"));
const node_crypto_1 = require("node:crypto");
const node_fs_1 = require("node:fs");
const node_http_1 = __importDefault(require("node:http"));
const node_os_1 = __importDefault(require("node:os"));
const node_path_1 = __importDefault(require("node:path"));
const node_test_1 = __importDefault(require("node:test"));
const src_1 = require("../src");
const ENV_BINARY_PATH = "MAKAI_BINARY_PATH";
const ENV_BINARY_URL = "MAKAI_BINARY_URL";
const ENV_BINARY_SHA256 = "MAKAI_BINARY_SHA256";
(0, node_test_1.default)("resolveMakaiBinary prefers MAKAI_BINARY_PATH override", async () => {
    const tempDir = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-bin-path-"));
    const binaryPath = node_path_1.default.join(tempDir, process.platform === "win32" ? "makai.exe" : "makai");
    await node_fs_1.promises.writeFile(binaryPath, "fixture");
    const prev = process.env[ENV_BINARY_PATH];
    process.env[ENV_BINARY_PATH] = binaryPath;
    try {
        const resolved = await (0, src_1.resolveMakaiBinary)();
        strict_1.default.equal(resolved, binaryPath);
    }
    finally {
        if (prev === undefined)
            delete process.env[ENV_BINARY_PATH];
        else
            process.env[ENV_BINARY_PATH] = prev;
        await node_fs_1.promises.rm(tempDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("resolveMakaiBinary rejects URL override without checksum", async () => {
    const cacheDir = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-cache-"));
    const prevUrl = process.env[ENV_BINARY_URL];
    const prevChecksum = process.env[ENV_BINARY_SHA256];
    const prevPath = process.env[ENV_BINARY_PATH];
    process.env[ENV_BINARY_URL] = "http://127.0.0.1:1/makai-test.bin";
    delete process.env[ENV_BINARY_SHA256];
    delete process.env[ENV_BINARY_PATH];
    try {
        await strict_1.default.rejects(() => (0, src_1.resolveMakaiBinary)({ cacheDir }), /SHA256 checksum is required when downloading makai binary from URL/);
    }
    finally {
        if (prevUrl === undefined)
            delete process.env[ENV_BINARY_URL];
        else
            process.env[ENV_BINARY_URL] = prevUrl;
        if (prevChecksum === undefined)
            delete process.env[ENV_BINARY_SHA256];
        else
            process.env[ENV_BINARY_SHA256] = prevChecksum;
        if (prevPath === undefined)
            delete process.env[ENV_BINARY_PATH];
        else
            process.env[ENV_BINARY_PATH] = prevPath;
        await node_fs_1.promises.rm(cacheDir, { recursive: true, force: true });
    }
});
(0, node_test_1.default)("resolveMakaiBinary rejects resolver URL option without checksum", async () => {
    const prevUrl = process.env[ENV_BINARY_URL];
    const prevChecksum = process.env[ENV_BINARY_SHA256];
    const prevPath = process.env[ENV_BINARY_PATH];
    delete process.env[ENV_BINARY_URL];
    delete process.env[ENV_BINARY_SHA256];
    delete process.env[ENV_BINARY_PATH];
    try {
        await strict_1.default.rejects(() => 
        // @ts-expect-error URL resolver options must include checksumSha256.
        (0, src_1.resolveMakaiBinary)({
            binaryUrl: "http://127.0.0.1:1/makai-test.bin",
        }), /SHA256 checksum is required when downloading makai binary from URL/);
    }
    finally {
        if (prevUrl === undefined)
            delete process.env[ENV_BINARY_URL];
        else
            process.env[ENV_BINARY_URL] = prevUrl;
        if (prevChecksum === undefined)
            delete process.env[ENV_BINARY_SHA256];
        else
            process.env[ENV_BINARY_SHA256] = prevChecksum;
        if (prevPath === undefined)
            delete process.env[ENV_BINARY_PATH];
        else
            process.env[ENV_BINARY_PATH] = prevPath;
    }
});
(0, node_test_1.default)("resolveMakaiBinary downloads URL override to cache with checksum", async () => {
    const payload = Buffer.from("makai-binary-content");
    const checksum = (0, node_crypto_1.createHash)("sha256").update(payload).digest("hex");
    const cacheDir = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-cache-"));
    const server = node_http_1.default.createServer((_req, res) => {
        res.writeHead(200, { "Content-Type": "application/octet-stream" });
        res.end(payload);
    });
    await new Promise((resolve) => server.listen(0, resolve));
    const address = server.address();
    if (!address || typeof address === "string") {
        server.close();
        throw new Error("test server did not expose a TCP address");
    }
    const prevUrl = process.env[ENV_BINARY_URL];
    const prevChecksum = process.env[ENV_BINARY_SHA256];
    const prevPath = process.env[ENV_BINARY_PATH];
    process.env[ENV_BINARY_URL] = `http://127.0.0.1:${address.port}/makai-test.bin`;
    process.env[ENV_BINARY_SHA256] = checksum;
    delete process.env[ENV_BINARY_PATH];
    try {
        const resolved = await (0, src_1.resolveMakaiBinary)({ cacheDir });
        const downloaded = await node_fs_1.promises.readFile(resolved);
        strict_1.default.equal(downloaded.toString("utf8"), payload.toString("utf8"));
    }
    finally {
        if (prevUrl === undefined)
            delete process.env[ENV_BINARY_URL];
        else
            process.env[ENV_BINARY_URL] = prevUrl;
        if (prevChecksum === undefined)
            delete process.env[ENV_BINARY_SHA256];
        else
            process.env[ENV_BINARY_SHA256] = prevChecksum;
        if (prevPath === undefined)
            delete process.env[ENV_BINARY_PATH];
        else
            process.env[ENV_BINARY_PATH] = prevPath;
        await node_fs_1.promises.rm(cacheDir, { recursive: true, force: true });
        await new Promise((resolve) => server.close(() => resolve()));
    }
});
(0, node_test_1.default)("resolveMakaiBinary leaves no temp file when the download cannot be finalized", async () => {
    const payload = Buffer.from("makai-binary-content-for-rename-failure");
    const checksum = (0, node_crypto_1.createHash)("sha256").update(payload).digest("hex");
    const cacheDir = await node_fs_1.promises.mkdtemp(node_path_1.default.join(node_os_1.default.tmpdir(), "makai-cache-fail-"));
    const cachePath = node_path_1.default.join(cacheDir, "makai-test.bin");
    const server = node_http_1.default.createServer((_req, res) => {
        res.writeHead(200, { "Content-Type": "application/octet-stream" });
        setTimeout(() => res.end(payload), 200);
    });
    await new Promise((resolve) => server.listen(0, resolve));
    const address = server.address();
    if (!address || typeof address === "string") {
        server.close();
        throw new Error("test server did not expose a TCP address");
    }
    const occupy = setTimeout(() => {
        void (async () => {
            await node_fs_1.promises.mkdir(cachePath, { recursive: true });
            await node_fs_1.promises.writeFile(node_path_1.default.join(cachePath, "occupant"), "x");
        })();
    }, 50);
    const prevUrl = process.env[ENV_BINARY_URL];
    const prevChecksum = process.env[ENV_BINARY_SHA256];
    const prevPath = process.env[ENV_BINARY_PATH];
    process.env[ENV_BINARY_URL] = `http://127.0.0.1:${address.port}/makai-test.bin`;
    process.env[ENV_BINARY_SHA256] = checksum;
    delete process.env[ENV_BINARY_PATH];
    try {
        await strict_1.default.rejects((0, src_1.resolveMakaiBinary)({ cacheDir }));
        await strict_1.default.rejects(node_fs_1.promises.stat(`${cachePath}.tmp`), (error) => error.code === "ENOENT");
    }
    finally {
        clearTimeout(occupy);
        if (prevUrl === undefined)
            delete process.env[ENV_BINARY_URL];
        else
            process.env[ENV_BINARY_URL] = prevUrl;
        if (prevChecksum === undefined)
            delete process.env[ENV_BINARY_SHA256];
        else
            process.env[ENV_BINARY_SHA256] = prevChecksum;
        if (prevPath === undefined)
            delete process.env[ENV_BINARY_PATH];
        else
            process.env[ENV_BINARY_PATH] = prevPath;
        await node_fs_1.promises.rm(cacheDir, { recursive: true, force: true });
        await new Promise((resolve) => server.close(() => resolve()));
    }
});
