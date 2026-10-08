import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  type MakaiLogger,
  getNoopLogger,
  isNoopLogger,
  resolveMakaiBinary,
} from "../src";

type LogEntry = { level: string; message: string; context?: Record<string, unknown> };

function createCapturingLogger(): MakaiLogger & { entries: LogEntry[] } {
  const entries: LogEntry[] = [];
  return {
    entries,
    debug(message, context) { entries.push({ level: "debug", message, context }); },
    info(message, context) { entries.push({ level: "info", message, context }); },
    warn(message, context) { entries.push({ level: "warn", message, context }); },
    error(message, context) { entries.push({ level: "error", message, context }); },
  };
}

test("getNoopLogger returns a logger with all four methods", () => {
  const logger = getNoopLogger();
  assert.equal(typeof logger.debug, "function");
  assert.equal(typeof logger.info, "function");
  assert.equal(typeof logger.warn, "function");
  assert.equal(typeof logger.error, "function");
  logger.debug("test");
  logger.info("test");
  logger.warn("test");
  logger.error("test");
});

test("getNoopLogger returns the same singleton instance", () => {
  assert.strictEqual(getNoopLogger(), getNoopLogger());
});

test("binary resolver logs resolution steps", async () => {
  const logger = createCapturingLogger();
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-bin-log-"));
  const binaryPath = path.join(tempDir, process.platform === "win32" ? "oapx.exe" : "oapx");
  await fs.promises.writeFile(binaryPath, "fixture");

  const prev = process.env.OAP_SDK_BINARY_PATH;
  process.env.OAP_SDK_BINARY_PATH = binaryPath;
  try {
    await resolveMakaiBinary({ logger });
    const resolvingLog = logger.entries.find((e) => e.message === "binary: resolving from explicit path");
    assert.ok(resolvingLog, "expected 'binary: resolving from explicit path' log");
    assert.equal(resolvingLog.context?.path, path.resolve(binaryPath));

    const resolvedLog = logger.entries.find((e) => e.message === "binary: resolved from explicit path");
    assert.ok(resolvedLog, "expected 'binary: resolved from explicit path' log");
  } finally {
    if (prev === undefined) delete process.env.OAP_SDK_BINARY_PATH;
    else process.env.OAP_SDK_BINARY_PATH = prev;
    await fs.promises.rm(tempDir, { recursive: true, force: true });
  }
});

test("binary resolver logs auto resolution candidate checks", async () => {
  const logger = createCapturingLogger();
  const emptyCwd = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-cwd-"));
  const binaryName = process.platform === "win32" ? "oapx.exe" : "oapx";
  const resolveModule = (specifier: string): string => {
    throw new Error(`Cannot find module '${specifier}'`);
  };
  const prevPath = process.env.OAP_SDK_BINARY_PATH;
  const prevUrl = process.env.OAP_SDK_BINARY_URL;
  const prevChecksum = process.env.OAP_SDK_BINARY_SHA256;
  delete process.env.OAP_SDK_BINARY_PATH;
  delete process.env.OAP_SDK_BINARY_URL;
  delete process.env.OAP_SDK_BINARY_SHA256;
  const prevSearchPath = process.env.PATH;
  process.env.PATH = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-path-"));
  try {
    const resolved = await resolveMakaiBinary({ logger, cwd: emptyCwd, resolveModule });
    const notFoundLog = logger.entries.find((e) => e.message === "binary: bundled package not found");
    assert.ok(notFoundLog, "expected 'binary: bundled package not found' log");

    const candidateLogs = logger.entries.filter((e) => e.message === "binary: checking local candidate");
    assert.deepEqual(
      candidateLogs.map((e) => e.context?.path),
      [
        path.join(emptyCwd, "zig-out", "bin", binaryName),
        path.join(emptyCwd, "zig", "zig-out", "bin", binaryName),
      ],
    );

    const resolvedLog = logger.entries.find((e) => e.message === "binary: resolved from local candidate");
    assert.equal(resolvedLog, undefined, "empty cwd must not resolve a local candidate");

    const fallbackLog = logger.entries.find((e) => e.message === "binary: falling back to PATH lookup");
    assert.ok(fallbackLog, "expected 'binary: falling back to PATH lookup' log");
    assert.equal(fallbackLog.context?.binary, binaryName);
    assert.equal(resolved, binaryName);
  } finally {
    if (prevPath === undefined) delete process.env.OAP_SDK_BINARY_PATH;
    else process.env.OAP_SDK_BINARY_PATH = prevPath;
    if (prevUrl === undefined) delete process.env.OAP_SDK_BINARY_URL;
    else process.env.OAP_SDK_BINARY_URL = prevUrl;
    if (prevChecksum === undefined) delete process.env.OAP_SDK_BINARY_SHA256;
    else process.env.OAP_SDK_BINARY_SHA256 = prevChecksum;
    if (prevSearchPath === undefined) delete process.env.PATH;
    else process.env.PATH = prevSearchPath;
    await fs.promises.rm(emptyCwd, { recursive: true, force: true });
  }
});

test("binary resolver passes over a PATH entry that is not an executable file", async () => {
  const emptyCwd = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-cwd-"));
  const decoyDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-decoy-"));
  const realDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-real-"));
  const binaryName = process.platform === "win32" ? "oapx.exe" : "oapx";
  await fs.promises.mkdir(path.join(decoyDir, binaryName));
  const real = path.join(realDir, binaryName);
  await fs.promises.writeFile(real, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const resolveModule = (specifier: string): string => {
    throw new Error(`Cannot find module '${specifier}'`);
  };
  const prevBinaryPath = process.env.OAP_SDK_BINARY_PATH;
  const prevSearchPath = process.env.PATH;
  delete process.env.OAP_SDK_BINARY_PATH;
  process.env.PATH = [decoyDir, realDir].join(path.delimiter);
  try {
    assert.equal(await resolveMakaiBinary({ cwd: emptyCwd, resolveModule }), real);
  } finally {
    if (prevBinaryPath === undefined) delete process.env.OAP_SDK_BINARY_PATH;
    else process.env.OAP_SDK_BINARY_PATH = prevBinaryPath;
    if (prevSearchPath === undefined) delete process.env.PATH;
    else process.env.PATH = prevSearchPath;
    await fs.promises.rm(emptyCwd, { recursive: true, force: true });
    await fs.promises.rm(decoyDir, { recursive: true, force: true });
    await fs.promises.rm(realDir, { recursive: true, force: true });
  }
});

test("binary resolver ignores an install predating the rename on PATH", async () => {
  const emptyCwd = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-cwd-"));
  const pathDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-path-"));
  const legacy = path.join(pathDir, process.platform === "win32" ? "makai.exe" : "makai");
  await fs.promises.writeFile(legacy, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const resolveModule = (specifier: string): string => {
    throw new Error(`Cannot find module '${specifier}'`);
  };
  const prevBinaryPath = process.env.OAP_SDK_BINARY_PATH;
  const prevBinaryUrl = process.env.OAP_SDK_BINARY_URL;
  const prevSearchPath = process.env.PATH;
  delete process.env.OAP_SDK_BINARY_PATH;
  delete process.env.OAP_SDK_BINARY_URL;
  process.env.PATH = pathDir;
  try {
    assert.equal(await resolveMakaiBinary({ cwd: emptyCwd, resolveModule }), process.platform === "win32" ? "oapx.exe" : "oapx");
  } finally {
    if (prevBinaryPath === undefined) delete process.env.OAP_SDK_BINARY_PATH;
    else process.env.OAP_SDK_BINARY_PATH = prevBinaryPath;
    if (prevBinaryUrl === undefined) delete process.env.OAP_SDK_BINARY_URL;
    else process.env.OAP_SDK_BINARY_URL = prevBinaryUrl;
    if (prevSearchPath === undefined) delete process.env.PATH;
    else process.env.PATH = prevSearchPath;
    await fs.promises.rm(emptyCwd, { recursive: true, force: true });
    await fs.promises.rm(pathDir, { recursive: true, force: true });
  }
});

test("binary resolver logs resolution from the bundled package", async () => {
  const logger = createCapturingLogger();
  const bundledPath = path.join(path.sep, "bundled", "bin", process.platform === "win32" ? "oapx.exe" : "oapx");
  const specifiers: string[] = [];
  const resolveModule = (specifier: string): string => {
    specifiers.push(specifier);
    return bundledPath;
  };
  const prevPath = process.env.OAP_SDK_BINARY_PATH;
  const prevUrl = process.env.OAP_SDK_BINARY_URL;
  const prevChecksum = process.env.OAP_SDK_BINARY_SHA256;
  delete process.env.OAP_SDK_BINARY_PATH;
  delete process.env.OAP_SDK_BINARY_URL;
  delete process.env.OAP_SDK_BINARY_SHA256;
  try {
    const resolved = await resolveMakaiBinary({ logger, resolveModule });
    assert.equal(resolved, bundledPath);
    assert.deepEqual(specifiers, [
      `@oap-sdk/cli-${process.platform}-${process.arch}/bin/${process.platform === "win32" ? "oapx.exe" : "oapx"}`,
    ]);

    const bundledLog = logger.entries.find((e) => e.message === "binary: resolved from bundled package");
    assert.ok(bundledLog, "expected 'binary: resolved from bundled package' log");
    assert.equal(bundledLog.context?.path, bundledPath);
    assert.equal(bundledLog.context?.package, `@oap-sdk/cli-${process.platform}-${process.arch}`);

    const candidateLogs = logger.entries.filter((e) => e.message === "binary: checking local candidate");
    assert.deepEqual(candidateLogs, [], "bundled package must short-circuit the local candidate search");
  } finally {
    if (prevPath === undefined) delete process.env.OAP_SDK_BINARY_PATH;
    else process.env.OAP_SDK_BINARY_PATH = prevPath;
    if (prevUrl === undefined) delete process.env.OAP_SDK_BINARY_URL;
    else process.env.OAP_SDK_BINARY_URL = prevUrl;
    if (prevChecksum === undefined) delete process.env.OAP_SDK_BINARY_SHA256;
    else process.env.OAP_SDK_BINARY_SHA256 = prevChecksum;
  }
});

test("isNoopLogger identifies no-op logger and distinguishes custom loggers", () => {
  assert.ok(isNoopLogger(getNoopLogger()), "getNoopLogger() should be identified as no-op");
  const custom = createCapturingLogger();
  assert.ok(!isNoopLogger(custom), "custom logger should not be identified as no-op");
});
