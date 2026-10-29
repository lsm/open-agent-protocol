import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

for (const failSetup of [false, true]) {
  test(`changelog fixtures are removed after ${failSetup ? "setup failure" : "success"}`, (t) => {
    const root = mkdtempSync(join(tmpdir(), "changelog-cleanup-proof-"));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const temporary = join(root, "tmp");
    mkdirSync(temporary);
    const preload = join(root, "fail-setup.cjs");
    writeFileSync(preload, `
      const fs = require('node:fs');
      const write = fs.writeFileSync;
      fs.writeFileSync = function(path, ...args) {
        if (String(path).includes('changelog-release-')) throw new Error('injected setup failure');
        return write.call(this, path, ...args);
      };
      require('node:module').syncBuiltinESMExports();
    `);
    const env = { ...process.env, TMPDIR: temporary, TMP: temporary, TEMP: temporary, NODE_DISABLE_COMPILE_CACHE: "1" };
    delete env.NODE_TEST_CONTEXT;
    const result = spawnSync(process.execPath, [
      ...(failSetup ? ["--require", preload] : []),
      "--test", fileURLToPath(new URL("./changelog-release.test.mjs", import.meta.url)),
    ], { env, encoding: "utf8" });
    assert.equal(result.status, failSetup ? 1 : 0, result.stdout + result.stderr);
    assert.deepEqual(readdirSync(temporary), [], "the suite must remove every fixture it created");
  });
}
