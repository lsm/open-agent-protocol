"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
exports.resolveMakaiBinary = resolveMakaiBinary;
const node_crypto_1 = require("node:crypto");
const node_fs_1 = require("node:fs");
const node_os_1 = __importDefault(require("node:os"));
const node_path_1 = __importDefault(require("node:path"));
const logger_1 = require("./logger");
const ENV_BINARY_PATH = "MAKAI_BINARY_PATH";
const ENV_BINARY_URL = "MAKAI_BINARY_URL";
const ENV_BINARY_SHA256 = "MAKAI_BINARY_SHA256";
function binaryNameForPlatform(platform = process.platform) {
    return platform === "win32" ? "makai.exe" : "makai";
}
async function ensureFileExists(filePath) {
    await node_fs_1.promises.access(filePath);
}
async function fileExists(filePath) {
    try {
        await node_fs_1.promises.access(filePath);
        return true;
    }
    catch (error) {
        if (error && typeof error === "object" && "code" in error && error.code === "ENOENT") {
            return false;
        }
        throw error;
    }
}
function sha256(content) {
    return (0, node_crypto_1.createHash)("sha256").update(content).digest("hex");
}
function requireChecksumForUrl(binaryUrl, checksumSha256) {
    if (!checksumSha256) {
        throw new Error(`SHA256 checksum is required when downloading makai binary from URL: ${binaryUrl}`);
    }
    return checksumSha256;
}
async function verifyChecksum(filePath, checksumSha256) {
    const content = await node_fs_1.promises.readFile(filePath);
    const actual = sha256(content);
    if (actual !== checksumSha256.toLowerCase()) {
        throw new Error(`binary checksum mismatch: expected ${checksumSha256}, got ${actual}`);
    }
}
async function downloadToCache(url, targetPath, checksumSha256) {
    const response = await fetch(url);
    if (!response.ok) {
        throw new Error(`failed to download binary: ${response.status} ${response.statusText}`);
    }
    const arrayBuffer = await response.arrayBuffer();
    const content = Buffer.from(arrayBuffer);
    const actual = sha256(content);
    if (actual !== checksumSha256.toLowerCase()) {
        throw new Error(`binary checksum mismatch: expected ${checksumSha256}, got ${actual}`);
    }
    await node_fs_1.promises.mkdir(node_path_1.default.dirname(targetPath), { recursive: true });
    const tempPath = `${targetPath}.tmp`;
    try {
        await node_fs_1.promises.writeFile(tempPath, content);
        if (process.platform !== "win32") {
            await node_fs_1.promises.chmod(tempPath, 0o755);
        }
        await node_fs_1.promises.rename(tempPath, targetPath);
    }
    catch (error) {
        await node_fs_1.promises.rm(tempPath, { force: true }).catch(() => undefined);
        throw error;
    }
}
async function resolveMakaiBinary(options = {}) {
    const logger = options.logger ?? (0, logger_1.getNoopLogger)();
    const explicitBinaryPath = process.env[ENV_BINARY_PATH] ?? options.binaryPath;
    if (explicitBinaryPath) {
        const resolved = node_path_1.default.resolve(explicitBinaryPath);
        logger.debug("binary: resolving from explicit path", { path: resolved });
        await ensureFileExists(resolved);
        logger.debug("binary: resolved from explicit path", { path: resolved });
        return resolved;
    }
    const binaryUrl = process.env[ENV_BINARY_URL] ?? options.binaryUrl;
    const checksumSha256 = process.env[ENV_BINARY_SHA256] ?? options.checksumSha256;
    if (binaryUrl) {
        const requiredChecksumSha256 = requireChecksumForUrl(binaryUrl, checksumSha256);
        const binaryName = binaryNameForPlatform();
        const cacheDir = options.cacheDir ?? node_path_1.default.join(node_os_1.default.homedir(), ".cache", "makai", "bin");
        const urlPathName = new URL(binaryUrl).pathname;
        const fileName = node_path_1.default.basename(urlPathName) || binaryName;
        const cachePath = node_path_1.default.join(cacheDir, fileName);
        logger.debug("binary: resolving from URL", { url: binaryUrl, cache_path: cachePath });
        if (await fileExists(cachePath)) {
            try {
                await verifyChecksum(cachePath, requiredChecksumSha256);
                logger.debug("binary: cached file checksum verified", { path: cachePath, sha256: requiredChecksumSha256 });
                return cachePath;
            }
            catch (error) {
                if (error instanceof Error && error.message.startsWith("binary checksum mismatch")) {
                    logger.warn("binary: cached file checksum mismatch, re-downloading", { path: cachePath });
                    await node_fs_1.promises.rm(cachePath, { force: true });
                }
                else {
                    throw error;
                }
            }
        }
        if (!(await fileExists(cachePath))) {
            logger.debug("binary: downloading from URL", { url: binaryUrl, target: cachePath });
            await downloadToCache(binaryUrl, cachePath, requiredChecksumSha256);
            logger.info("binary: download complete", { path: cachePath, sha256: requiredChecksumSha256 });
            return cachePath;
        }
        return cachePath;
    }
    const binaryName = binaryNameForPlatform();
    const platformKey = `${process.platform}-${process.arch}`;
    const bundledPackage = `@makai/cli-${platformKey}`;
    try {
        const bundledBinaryName = process.platform === "win32" ? "makai.exe" : "makai";
        const bundledPath = require.resolve(`${bundledPackage}/bin/${bundledBinaryName}`);
        logger.debug("binary: resolved from bundled package", { path: bundledPath, package: bundledPackage });
        return bundledPath;
    }
    catch {
        logger.debug("binary: bundled package not found", { package: bundledPackage });
    }
    const localCandidates = [
        node_path_1.default.resolve(process.cwd(), "zig-out", "bin", binaryName),
        node_path_1.default.resolve(process.cwd(), "zig", "zig-out", "bin", binaryName),
    ];
    for (const candidate of localCandidates) {
        logger.debug("binary: checking local candidate", { path: candidate });
        if (await fileExists(candidate)) {
            logger.debug("binary: resolved from local candidate", { path: candidate });
            return candidate;
        }
    }
    logger.debug("binary: falling back to PATH lookup", { binary: binaryName });
    return "makai";
}
