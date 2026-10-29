import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

for (const failSetup of [false, true]) {
  test(`logger fixtures are removed after ${failSetup ? "setup failure" : "success"}`, (t) => {
    const root = mkdtempSync(join(tmpdir(), "logger-cleanup-proof-"));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const temporary = join(root, "tmp");
    mkdirSync(temporary);
    const preload = join(root, "fail-setup.cjs");
    writeFileSync(preload, `
      const fs = require('node:fs');
      const write = fs.promises.writeFile;
      fs.promises.writeFile = async function(path, ...args) {
        if (String(path).includes('makai-')) throw new Error('injected setup failure');
        return write.call(this, path, ...args);
      };
      require('node:module').syncBuiltinESMExports();
    `);
    const env = { ...process.env, TMPDIR: temporary, TMP: temporary, TEMP: temporary };
    delete env.NODE_TEST_CONTEXT;
    const result = spawnSync(process.execPath, [
      ...(failSetup ? ["--require", preload] : []),
      "--test", new URL("../dist/test/logger.test.js", import.meta.url).pathname,
    ], { env, encoding: "utf8" });
    assert.equal(result.status, failSetup ? 1 : 0, result.stdout + result.stderr);
    assert.deepEqual(readdirSync(temporary), [], "the suite must remove every fixture it created");
  });
}
