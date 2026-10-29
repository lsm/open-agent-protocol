import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

for (const failSetup of [false, true]) {
  test(`demo fixtures are removed after ${failSetup ? "setup failure" : "success"}`, (t) => {
    const root = mkdtempSync(join(tmpdir(), "demo-cleanup-proof-"));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const temporary = join(root, "tmp");
    mkdirSync(temporary);
    const preload = join(root, "fail-setup.cjs");
    writeFileSync(preload, `
      const demo = require(${JSON.stringify(new URL("../dist/demo/server.js", import.meta.url).pathname)});
      demo.startDemoServer = async () => { throw new Error('injected startup failure'); };
    `);
    const env = { ...process.env, TMPDIR: temporary, TMP: temporary, TEMP: temporary, NODE_DISABLE_COMPILE_CACHE: "1" };
    delete env.NODE_TEST_CONTEXT;
    const result = spawnSync(process.execPath, [
      ...(failSetup ? ["--require", preload] : []),
      "--test", new URL("../dist/test/demo_server.test.js", import.meta.url).pathname,
    ], { env, encoding: "utf8" });
    assert.equal(result.status, failSetup ? 1 : 0, result.stdout + result.stderr);
    if (failSetup) assert.match(result.stdout + result.stderr, /injected startup failure/);
    assert.deepEqual(readdirSync(temporary), [], "the suite must remove every fixture it created");
  });
}
