import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  createOapClient,
  type MakaiLogger,
  getNoopLogger,
  isNoopLogger,
  resolveMakaiBinary,
} from "../src";

type LogEntry = { level: string; message: string; context?: Record<string, unknown> };

const RESOLVER_ENV = [
  "OXAP_SDK_BINARY_PATH",
  "OAP_SDK_BINARY_PATH",
  "OXAP_SDK_BINARY_URL",
  "OAP_SDK_BINARY_URL",
  "OXAP_SDK_BINARY_SHA256",
  "OAP_SDK_BINARY_SHA256",
];

function clearResolverEnv(): () => void {
  const previous = new Map(RESOLVER_ENV.map((name) => [name, process.env[name]]));
  for (const name of RESOLVER_ENV) delete process.env[name];
  return () => {
    for (const [name, value] of previous) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  };
}

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

test("binary resolver logs resolution steps", async (t) => {
  t.after(clearResolverEnv());
  const logger = createCapturingLogger();
  const tempDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-bin-log-"));
  t.after(() => fs.promises.rm(tempDir, { recursive: true, force: true }));
  const binaryPath = path.join(tempDir, process.platform === "win32" ? "oapx.exe" : "oapx");
  await fs.promises.writeFile(binaryPath, "fixture");

  process.env.OAP_SDK_BINARY_PATH = binaryPath;
  await resolveMakaiBinary({ logger });
  const resolvingLog = logger.entries.find((e) => e.message === "binary: resolving from explicit path");
  assert.ok(resolvingLog, "expected 'binary: resolving from explicit path' log");
  assert.equal(resolvingLog.context?.path, path.resolve(binaryPath));

  const resolvedLog = logger.entries.find((e) => e.message === "binary: resolved from explicit path");
  assert.ok(resolvedLog, "expected 'binary: resolved from explicit path' log");
});

test("binary resolver logs auto resolution candidate checks", async (t) => {
  const logger = createCapturingLogger();
  const emptyCwd = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-cwd-"));
  t.after(() => fs.promises.rm(emptyCwd, { recursive: true, force: true }));
  const binaryName = process.platform === "win32" ? "oxap.exe" : "oxap";
  const legacyName = process.platform === "win32" ? "oapx.exe" : "oapx";
  const resolveModule = (specifier: string): string => {
    throw new Error(`Cannot find module '${specifier}'`);
  };
  const prevSearchPath = process.env.PATH;
  t.after(clearResolverEnv());
  t.after(() => {
    if (prevSearchPath === undefined) delete process.env.PATH;
    else process.env.PATH = prevSearchPath;
  });
  const emptyPath = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-path-"));
  t.after(() => fs.promises.rm(emptyPath, { recursive: true, force: true }));
  process.env.PATH = emptyPath;

  const resolved = await resolveMakaiBinary({ logger, cwd: emptyCwd, resolveModule });
  const notFoundLog = logger.entries.find((e) => e.message === "binary: bundled package not found");
  assert.ok(notFoundLog, "expected 'binary: bundled package not found' log");

  const candidateLogs = logger.entries.filter((e) => e.message === "binary: checking local candidate");
  assert.deepEqual(
    candidateLogs.map((e) => e.context?.path),
    [
      path.join(emptyCwd, "zig-out", "bin", binaryName),
      path.join(emptyCwd, "zig", "zig-out", "bin", binaryName),
      path.join(emptyCwd, "zig-out", "bin", legacyName),
      path.join(emptyCwd, "zig", "zig-out", "bin", legacyName),
    ],
  );

  const resolvedLog = logger.entries.find((e) => e.message === "binary: resolved from local candidate");
  assert.equal(resolvedLog, undefined, "empty cwd must not resolve a local candidate");

  const fallbackLog = logger.entries.find((e) => e.message === "binary: falling back to PATH lookup");
  assert.ok(fallbackLog, "expected 'binary: falling back to PATH lookup' log");
  assert.equal(fallbackLog.context?.binary, binaryName);
  assert.equal(resolved, binaryName);
});

test("binary resolver passes over a PATH entry that is not an executable file", async (t) => {
  const emptyCwd = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-cwd-"));
  t.after(() => fs.promises.rm(emptyCwd, { recursive: true, force: true }));
  const decoyDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-decoy-"));
  t.after(() => fs.promises.rm(decoyDir, { recursive: true, force: true }));
  const realDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-real-"));
  t.after(() => fs.promises.rm(realDir, { recursive: true, force: true }));
  const binaryName = process.platform === "win32" ? "oapx.exe" : "oapx";
  await fs.promises.mkdir(path.join(decoyDir, binaryName));
  const real = path.join(realDir, binaryName);
  await fs.promises.writeFile(real, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const resolveModule = (specifier: string): string => {
    throw new Error(`Cannot find module '${specifier}'`);
  };
  const prevSearchPath = process.env.PATH;
  t.after(clearResolverEnv());
  t.after(() => {
    if (prevSearchPath === undefined) delete process.env.PATH;
    else process.env.PATH = prevSearchPath;
  });
  process.env.PATH = [decoyDir, realDir].join(path.delimiter);

  assert.equal(await resolveMakaiBinary({ cwd: emptyCwd, resolveModule }), real);
});

test("binary resolver ignores an install predating the rename on PATH", async (t) => {
  const emptyCwd = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-empty-cwd-"));
  t.after(() => fs.promises.rm(emptyCwd, { recursive: true, force: true }));
  const pathDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "makai-path-"));
  t.after(() => fs.promises.rm(pathDir, { recursive: true, force: true }));
  const legacy = path.join(pathDir, process.platform === "win32" ? "makai.exe" : "makai");
  await fs.promises.writeFile(legacy, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const resolveModule = (specifier: string): string => {
    throw new Error(`Cannot find module '${specifier}'`);
  };
  const prevSearchPath = process.env.PATH;
  t.after(clearResolverEnv());
  t.after(() => {
    if (prevSearchPath === undefined) delete process.env.PATH;
    else process.env.PATH = prevSearchPath;
  });
  process.env.PATH = pathDir;

  assert.equal(await resolveMakaiBinary({ cwd: emptyCwd, resolveModule }), process.platform === "win32" ? "oxap.exe" : "oxap");
});

test("binary resolver logs resolution from the bundled package", async (t) => {
  const logger = createCapturingLogger();
  const binaryName = process.platform === "win32" ? "oxap.exe" : "oxap";
  const bundledPath = path.join(path.sep, "bundled", "bin", binaryName);
  const specifiers: string[] = [];
  const resolveModule = (specifier: string): string => {
    specifiers.push(specifier);
    return bundledPath;
  };
  t.after(clearResolverEnv());

  const resolved = await resolveMakaiBinary({ logger, resolveModule });
  assert.equal(resolved, bundledPath);
  assert.deepEqual(specifiers, [
    `@oxap/cli-${process.platform}-${process.arch}/bin/${binaryName}`,
  ]);

  const bundledLog = logger.entries.find((e) => e.message === "binary: resolved from bundled package");
  assert.ok(bundledLog, "expected 'binary: resolved from bundled package' log");
  assert.equal(bundledLog.context?.path, bundledPath);
  assert.equal(bundledLog.context?.package, `@oxap/cli-${process.platform}-${process.arch}`);

  const candidateLogs = logger.entries.filter((e) => e.message === "binary: checking local candidate");
  assert.deepEqual(candidateLogs, [], "bundled package must short-circuit the local candidate search");
});

test("isNoopLogger identifies no-op logger and distinguishes custom loggers", () => {
  assert.ok(isNoopLogger(getNoopLogger()), "getNoopLogger() should be identified as no-op");
  const custom = createCapturingLogger();
  assert.ok(!isNoopLogger(custom), "custom logger should not be identified as no-op");
});

test("createOapClient hands its logger to binary resolution", async (t) => {
  t.after(clearResolverEnv());
  const logger = createCapturingLogger();
  await assert.rejects(() => createOapClient({ logger, resolver: { binaryPath: path.join(os.tmpdir(), "no-such-oapx") } }));
  assert.ok(logger.entries.length > 0, "the resolver logged nothing through the client's logger");
});
