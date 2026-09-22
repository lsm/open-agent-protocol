import { createHash } from "node:crypto";
import { constants as fsConstants, promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import { getNoopLogger, type MakaiLogger } from "./logger";

export type ModuleResolver = (specifier: string) => string;

type BinaryResolverBaseOptions = {
  cacheDir?: string;
  cwd?: string;
  resolveModule?: ModuleResolver;
  logger?: MakaiLogger;
};

type BinaryPathResolverOptions = BinaryResolverBaseOptions & {
  type?: "path";
  binaryPath: string;
  binaryUrl?: undefined;
  checksumSha256?: string;
};

type BinaryUrlResolverOptions = BinaryResolverBaseOptions & {
  type?: "url";
  binaryPath?: undefined;
  binaryUrl: string;
  checksumSha256: string;
};

type BinaryAutoResolverOptions = BinaryResolverBaseOptions & {
  type?: "auto";
  binaryPath?: undefined;
  binaryUrl?: undefined;
  checksumSha256?: string;
};

export type BinaryResolverOptions =
  | BinaryPathResolverOptions
  | BinaryUrlResolverOptions
  | BinaryAutoResolverOptions;

const ENV_BINARY_PATH = "OAP_SDK_BINARY_PATH";
const ENV_BINARY_URL = "OAP_SDK_BINARY_URL";
const ENV_BINARY_SHA256 = "OAP_SDK_BINARY_SHA256";

function binaryNameForPlatform(platform = process.platform): string {
  return platform === "win32" ? "oapx.exe" : "oapx";
}

async function ensureFileExists(filePath: string): Promise<void> {
  await fs.access(filePath);
}

async function isExecutableFile(filePath: string): Promise<boolean> {
  try {
    const stats = await fs.stat(filePath);
    if (!stats.isFile()) return false;
    await fs.access(filePath, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}

async function fileExists(filePath: string): Promise<boolean> {
  try {
    await fs.access(filePath);
    return true;
  } catch (error: unknown) {
    if (error && typeof error === "object" && "code" in error && error.code === "ENOENT") {
      return false;
    }
    throw error;
  }
}

function sha256(content: Buffer): string {
  return createHash("sha256").update(content).digest("hex");
}

function requireChecksumForUrl(binaryUrl: string, checksumSha256: string | undefined): string {
  if (!checksumSha256) {
    throw new Error(`SHA256 checksum is required when downloading oapx binary from URL: ${binaryUrl}`);
  }
  return checksumSha256;
}

async function verifyChecksum(filePath: string, checksumSha256: string): Promise<void> {
  const content = await fs.readFile(filePath);
  const actual = sha256(content);
  if (actual !== checksumSha256.toLowerCase()) {
    throw new Error(`binary checksum mismatch: expected ${checksumSha256}, got ${actual}`);
  }
}

async function downloadToCache(url: string, targetPath: string, checksumSha256: string): Promise<void> {
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

  await fs.mkdir(path.dirname(targetPath), { recursive: true });
  const tempPath = `${targetPath}.tmp`;
  try {
    await fs.writeFile(tempPath, content);
    if (process.platform !== "win32") {
      await fs.chmod(tempPath, 0o755);
    }
    await fs.rename(tempPath, targetPath);
  } catch (error: unknown) {
    await fs.rm(tempPath, { force: true }).catch(() => undefined);
    throw error;
  }
}

export async function resolveMakaiBinary(options: BinaryResolverOptions = {}): Promise<string> {
  const logger = options.logger ?? getNoopLogger();

  const explicitBinaryPath = process.env[ENV_BINARY_PATH] ?? options.binaryPath;
  if (explicitBinaryPath) {
    const resolved = path.resolve(explicitBinaryPath);
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
    const cacheDir = options.cacheDir ?? path.join(os.homedir(), ".cache", "makai", "bin");
    const urlPathName = new URL(binaryUrl).pathname;
    const fileName = path.basename(urlPathName) || binaryName;
    const cachePath = path.join(cacheDir, fileName);
    logger.debug("binary: resolving from URL", { url: binaryUrl, cache_path: cachePath });

    if (await fileExists(cachePath)) {
      try {
        await verifyChecksum(cachePath, requiredChecksumSha256);
        logger.debug("binary: cached file checksum verified", { path: cachePath, sha256: requiredChecksumSha256 });
        return cachePath;
      } catch (error: unknown) {
        if (error instanceof Error && error.message.startsWith("binary checksum mismatch")) {
          logger.warn("binary: cached file checksum mismatch, re-downloading", { path: cachePath });
          await fs.rm(cachePath, { force: true });
        } else {
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
  const bundledPackage = `@oap-sdk/cli-${platformKey}`;
  const resolveModule = options.resolveModule ?? ((specifier: string) => require.resolve(specifier));
  try {
    const bundledPath = resolveModule(`${bundledPackage}/bin/${binaryName}`);
    logger.debug("binary: resolved from bundled package", { path: bundledPath, package: bundledPackage });
    return bundledPath;
  } catch {
    logger.debug("binary: bundled package not found", { package: bundledPackage });
  }

  const cwd = options.cwd ?? process.cwd();
  const localCandidates = [
    path.resolve(cwd, "zig-out", "bin", binaryName),
    path.resolve(cwd, "zig", "zig-out", "bin", binaryName),
  ];
  for (const candidate of localCandidates) {
    logger.debug("binary: checking local candidate", { path: candidate });
    if (await isExecutableFile(candidate)) {
      logger.debug("binary: resolved from local candidate", { path: candidate });
      return candidate;
    }
  }

  const onPath = await findOnPath(binaryName);
  if (onPath !== undefined) {
    logger.debug("binary: resolved from PATH", { path: onPath });
    return onPath;
  }

  logger.debug("binary: falling back to PATH lookup", { binary: binaryName });
  return binaryName;
}

async function findOnPath(name: string): Promise<string | undefined> {
  const raw = process.env.PATH;
  if (!raw) return undefined;
  for (const dir of raw.split(path.delimiter)) {
    if (dir.length === 0) continue;
    const candidate = path.join(dir, name);
    if (await isExecutableFile(candidate)) return candidate;
  }
  return undefined;
}
