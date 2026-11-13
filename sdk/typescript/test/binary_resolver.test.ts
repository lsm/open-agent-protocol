import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { promises as fs } from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { resolveMakaiBinary } from "../src";

const ENV_BINARY_PATH = "OAP_SDK_BINARY_PATH";
const ENV_BINARY_URL = "OAP_SDK_BINARY_URL";
const ENV_BINARY_SHA256 = "OAP_SDK_BINARY_SHA256";
const ENV_BINARY_PATH_OXAP = "OXAP_SDK_BINARY_PATH";
const ENV_BINARY_URL_OXAP = "OXAP_SDK_BINARY_URL";
const ENV_BINARY_SHA256_OXAP = "OXAP_SDK_BINARY_SHA256";

const RESOLVER_ENV = [
  ENV_BINARY_PATH_OXAP,
  ENV_BINARY_PATH,
  ENV_BINARY_URL_OXAP,
  ENV_BINARY_URL,
  ENV_BINARY_SHA256_OXAP,
  ENV_BINARY_SHA256,
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

function binaryName(name: "oxap" | "oapx"): string {
  return process.platform === "win32" ? `${name}.exe` : name;
}

async function writeStub(dir: string, name: "oxap" | "oapx"): Promise<string> {
  const stub = path.join(dir, binaryName(name));
  await fs.mkdir(dir, { recursive: true });
  await fs.writeFile(stub, "#!/bin/sh\n", { mode: 0o755 });
  return stub;
}

async function tempDir(t: { after(fn: () => unknown): void }, prefix: string): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), prefix));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  return dir;
}

function rejectAllModules(specifier: string): string {
  throw new Error(`Cannot find module '${specifier}'`);
}

const emptyPath = async (t: { after(fn: () => unknown): void }): Promise<string> => tempDir(t, "makai-empty-path-");

test("resolveMakaiBinary prefers OAP_SDK_BINARY_PATH override", async (t) => {
  t.after(clearResolverEnv());
  const temp = await tempDir(t, "makai-bin-path-");
  const binaryPath = path.join(temp, "oapx");
  await fs.writeFile(binaryPath, "fixture");

  process.env[ENV_BINARY_PATH] = binaryPath;
  assert.equal(await resolveMakaiBinary(), binaryPath);
});

test("resolveMakaiBinary prefers OXAP_SDK_BINARY_PATH over OAP_SDK_BINARY_PATH", async (t) => {
  t.after(clearResolverEnv());
  const temp = await tempDir(t, "makai-bin-path-");
  const fromOxap = path.join(temp, "from-oxap");
  const fromOap = path.join(temp, "from-oap");
  await fs.writeFile(fromOxap, "fixture");
  await fs.writeFile(fromOap, "fixture");

  process.env[ENV_BINARY_PATH_OXAP] = fromOxap;
  process.env[ENV_BINARY_PATH] = fromOap;
  assert.equal(await resolveMakaiBinary(), fromOxap);
});

test("resolveMakaiBinary reads OAP_SDK_BINARY_PATH when the OXAP variable is absent", async (t) => {
  t.after(clearResolverEnv());
  const temp = await tempDir(t, "makai-bin-path-");
  const legacy = path.join(temp, "from-oap");
  await fs.writeFile(legacy, "fixture");

  process.env[ENV_BINARY_PATH] = legacy;
  assert.equal(await resolveMakaiBinary(), legacy);
});

test("resolveMakaiBinary rejects URL override without checksum", async (t) => {
  t.after(clearResolverEnv());
  const cacheDir = await tempDir(t, "makai-cache-");
  process.env[ENV_BINARY_URL] = "http://127.0.0.1:1/makai-test.bin";
  await assert.rejects(
    () => resolveMakaiBinary({ cacheDir }),
    /SHA256 checksum is required when downloading oapx binary from URL/,
  );
});

test("resolveMakaiBinary rejects resolver URL option without checksum", async (t) => {
  t.after(clearResolverEnv());
  await assert.rejects(
    () =>
      // @ts-expect-error URL resolver options must include checksumSha256.
      resolveMakaiBinary({
        binaryUrl: "http://127.0.0.1:1/makai-test.bin",
      }),
    /SHA256 checksum is required when downloading oapx binary from URL/,
  );
});

test("resolveMakaiBinary downloads URL override to cache with checksum", async (t) => {
  t.after(clearResolverEnv());
  const payload = Buffer.from("makai-binary-content");
  const checksum = createHash("sha256").update(payload).digest("hex");
  const cacheDir = await tempDir(t, "makai-cache-");

  const server = http.createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "application/octet-stream" });
    res.end(payload);
  });
  await new Promise<void>((resolve) => server.listen(0, resolve));
  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new Error("test server did not expose a TCP address");
  }

  process.env[ENV_BINARY_URL] = `http://127.0.0.1:${address.port}/makai-test.bin`;
  process.env[ENV_BINARY_SHA256] = checksum;
  try {
    const resolved = await resolveMakaiBinary({ cacheDir });
    const downloaded = await fs.readFile(resolved);
    assert.equal(downloaded.toString("utf8"), payload.toString("utf8"));
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("resolveMakaiBinary leaves no temp file when the download cannot be finalized", async (t) => {
  t.after(clearResolverEnv());
  const payload = Buffer.from("makai-binary-content-for-rename-failure");
  const checksum = createHash("sha256").update(payload).digest("hex");
  const cacheDir = await tempDir(t, "makai-cache-fail-");
  const cachePath = path.join(cacheDir, "makai-test.bin");

  const server = http.createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "application/octet-stream" });
    void (async () => {
      await fs.mkdir(cachePath, { recursive: true });
      await fs.writeFile(path.join(cachePath, "occupant"), "x");
      res.end(payload);
    })();
  });
  await new Promise<void>((resolve) => server.listen(0, resolve));
  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new Error("test server did not expose a TCP address");
  }

  process.env[ENV_BINARY_URL] = `http://127.0.0.1:${address.port}/makai-test.bin`;
  process.env[ENV_BINARY_SHA256] = checksum;
  try {
    await assert.rejects(resolveMakaiBinary({ cacheDir }));
    await assert.rejects(fs.stat(`${cachePath}.tmp`), (error: NodeJS.ErrnoException) => error.code === "ENOENT");
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("resolveMakaiBinary prefers the oxap local build over oapx", async (t) => {
  t.after(clearResolverEnv());
  const cwd = await tempDir(t, "makai-local-");
  const legacy = await writeStub(path.join(cwd, "zig-out", "bin"), "oapx");
  const preferred = await writeStub(path.join(cwd, "zig", "zig-out", "bin"), "oxap");

  assert.equal(await resolveMakaiBinary({ cwd, resolveModule: rejectAllModules }), preferred);
  assert.notEqual(preferred, legacy);
});

test("resolveMakaiBinary falls back to the oapx local build", async (t) => {
  t.after(clearResolverEnv());
  const cwd = await tempDir(t, "makai-local-");
  const legacy = await writeStub(path.join(cwd, "zig-out", "bin"), "oapx");

  assert.equal(await resolveMakaiBinary({ cwd, resolveModule: rejectAllModules }), legacy);
});

test("resolveMakaiBinary prefers oxap on PATH", async (t) => {
  t.after(clearResolverEnv());
  const pathDir = await tempDir(t, "makai-path-");
  await writeStub(pathDir, "oapx");
  const preferred = await writeStub(pathDir, "oxap");

  process.env.PATH = pathDir;
  assert.equal(await resolveMakaiBinary({ cwd: await tempDir(t, "makai-empty-cwd-"), resolveModule: rejectAllModules }), preferred);
});

test("resolveMakaiBinary finds oapx on PATH", async (t) => {
  t.after(clearResolverEnv());
  const pathDir = await tempDir(t, "makai-path-legacy-");
  const legacy = await writeStub(pathDir, "oapx");

  process.env.PATH = pathDir;
  assert.equal(await resolveMakaiBinary({ cwd: await tempDir(t, "makai-empty-cwd-"), resolveModule: rejectAllModules }), legacy);
});

test("resolveMakaiBinary reports the oxap name when nothing is found", async (t) => {
  t.after(clearResolverEnv());
  process.env.PATH = await emptyPath(t);
  assert.equal(
    await resolveMakaiBinary({ cwd: await tempDir(t, "makai-empty-cwd-"), resolveModule: rejectAllModules }),
    binaryName("oxap"),
  );
});

test("resolveMakaiBinary prefers the oxap bundled binary", async (t) => {
  t.after(clearResolverEnv());
  const specifiers: string[] = [];
  const bundled = `@oap-sdk/cli-${process.platform}-${process.arch}`;
  const resolveModule = (specifier: string): string => {
    specifiers.push(specifier);
    return path.join(path.sep, "bundled", "bin", path.basename(specifier));
  };

  assert.equal(
    await resolveMakaiBinary({ resolveModule }),
    path.join(path.sep, "bundled", "bin", binaryName("oxap")),
  );
  assert.deepEqual(specifiers, [`${bundled}/bin/${binaryName("oxap")}`]);
});

test("resolveMakaiBinary falls back to the oapx bundled binary", async (t) => {
  t.after(clearResolverEnv());
  const specifiers: string[] = [];
  const bundled = `@oap-sdk/cli-${process.platform}-${process.arch}`;
  const resolveModule = (specifier: string): string => {
    specifiers.push(specifier);
    if (specifier.endsWith(binaryName("oxap"))) throw new Error(`Cannot find module '${specifier}'`);
    return path.join(path.sep, "bundled", "bin", path.basename(specifier));
  };

  assert.equal(
    await resolveMakaiBinary({ resolveModule }),
    path.join(path.sep, "bundled", "bin", binaryName("oapx")),
  );
  assert.deepEqual(specifiers, [`${bundled}/bin/${binaryName("oxap")}`, `${bundled}/bin/${binaryName("oapx")}`]);
});

function urlServer(payloads: Map<string, Buffer>): Promise<{ url: string; requests: string[]; close: () => Promise<void> }> {
  const requests: string[] = [];
  const server = http.createServer((req, res) => {
    const body = payloads.get(req.url ?? "");
    if (!body) {
      res.writeHead(404);
      res.end();
      return;
    }
    requests.push(req.url ?? "");
    res.writeHead(200, { "Content-Type": "application/octet-stream" });
    res.end(body);
  });
  return new Promise((resolve) => {
    server.listen(0, () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        server.close();
        throw new Error("test server did not expose a TCP address");
      }
      resolve({
        url: `http://127.0.0.1:${address.port}`,
        requests,
        close: () => new Promise<void>((done) => server.close(() => done())),
      });
    });
  });
}

test("resolveMakaiBinary prefers OXAP_SDK_BINARY_URL over OAP_SDK_BINARY_URL", async (t) => {
  t.after(clearResolverEnv());
  const preferred = Buffer.from("preferred");
  const legacy = Buffer.from("legacy");
  const server = await urlServer(new Map([["/dist/oxap", preferred], ["/dist/oapx", legacy]]));
  const cacheDir = await tempDir(t, "makai-url-");
  try {
    process.env[ENV_BINARY_URL_OXAP] = `${server.url}/dist/oxap`;
    process.env[ENV_BINARY_SHA256_OXAP] = createHash("sha256").update(preferred).digest("hex");
    process.env[ENV_BINARY_URL] = `${server.url}/dist/oapx`;
    process.env[ENV_BINARY_SHA256] = createHash("sha256").update(legacy).digest("hex");

    const resolved = await resolveMakaiBinary({ cacheDir });
    assert.equal(path.basename(resolved), binaryName("oxap"));
    assert.deepEqual((await fs.readFile(resolved)).toString("utf8"), preferred.toString("utf8"));
    assert.deepEqual(server.requests, ["/dist/oxap"]);
  } finally {
    await server.close();
  }
});

test("resolveMakaiBinary reads OAP_SDK_BINARY_URL when the OXAP pair is absent", async (t) => {
  t.after(clearResolverEnv());
  const legacy = Buffer.from("legacy");
  const server = await urlServer(new Map([["/dist/oapx", legacy]]));
  const cacheDir = await tempDir(t, "makai-url-legacy-");
  try {
    process.env[ENV_BINARY_URL] = `${server.url}/dist/oapx`;
    process.env[ENV_BINARY_SHA256] = createHash("sha256").update(legacy).digest("hex");

    const resolved = await resolveMakaiBinary({ cacheDir });
    assert.equal(path.basename(resolved), binaryName("oapx"));
    assert.deepEqual(server.requests, ["/dist/oapx"]);
  } finally {
    await server.close();
  }
});

test("resolveMakaiBinary does not borrow a checksum across the OXAP and legacy pairs", async (t) => {
  t.after(clearResolverEnv());
  const preferred = Buffer.from("preferred");
  const legacy = Buffer.from("legacy");
  const server = await urlServer(new Map([["/dist/oxap", preferred], ["/dist/oapx", legacy]]));
  const firstCache = path.join(await tempDir(t, "makai-url-pair-"), "cache");
  const secondCache = path.join(await tempDir(t, "makai-url-pair-"), "cache");
  try {
    process.env[ENV_BINARY_URL_OXAP] = `${server.url}/dist/oxap`;
    process.env[ENV_BINARY_SHA256] = createHash("sha256").update(legacy).digest("hex");
    await assert.rejects(
      () => resolveMakaiBinary({ cacheDir: firstCache }),
      /SHA256 checksum is required/,
    );

    delete process.env[ENV_BINARY_URL_OXAP];
    process.env[ENV_BINARY_URL] = `${server.url}/dist/oapx`;
    delete process.env[ENV_BINARY_SHA256];
    process.env[ENV_BINARY_SHA256_OXAP] = createHash("sha256").update(preferred).digest("hex");
    await assert.rejects(
      () => resolveMakaiBinary({ cacheDir: secondCache }),
      /SHA256 checksum is required/,
    );
    assert.deepEqual(server.requests, []);
  } finally {
    await server.close();
  }
});

test("resolveMakaiBinary takes the option checksum for an environment URL", async (t) => {
  t.after(clearResolverEnv());
  const payload = Buffer.from("env-url-option-checksum");
  const checksum = createHash("sha256").update(payload).digest("hex");
  const server = await urlServer(new Map([["/dist/oxap", payload]]));
  try {
    process.env[ENV_BINARY_URL_OXAP] = `${server.url}/dist/oxap`;
    const resolved = await resolveMakaiBinary({
      checksumSha256: checksum,
      cacheDir: await tempDir(t, "makai-env-option-checksum-"),
    });
    assert.deepEqual((await fs.readFile(resolved)).toString("utf8"), payload.toString("utf8"));
  } finally {
    await server.close();
  }
});

test("resolveMakaiBinary takes an environment checksum for an option URL, OXAP first", async (t) => {
  t.after(clearResolverEnv());
  const payload = Buffer.from("option-url");
  const checksum = createHash("sha256").update(payload).digest("hex");
  const wrong = "0".repeat(64);
  const server = await urlServer(new Map([["/dist/custom", payload]]));
  try {
    process.env[ENV_BINARY_SHA256_OXAP] = checksum;
    const preferred = await resolveMakaiBinary({
      binaryUrl: `${server.url}/dist/custom`,
      checksumSha256: wrong,
      cacheDir: await tempDir(t, "makai-url-option-oxap-"),
    });
    assert.deepEqual((await fs.readFile(preferred)).toString("utf8"), payload.toString("utf8"));

    delete process.env[ENV_BINARY_SHA256_OXAP];
    process.env[ENV_BINARY_SHA256] = checksum;
    const legacy = await resolveMakaiBinary({
      binaryUrl: `${server.url}/dist/custom`,
      checksumSha256: wrong,
      cacheDir: await tempDir(t, "makai-url-option-oap-"),
    });
    assert.deepEqual((await fs.readFile(legacy)).toString("utf8"), payload.toString("utf8"));
  } finally {
    await server.close();
  }
});
