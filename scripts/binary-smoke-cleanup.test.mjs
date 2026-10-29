import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

test("real-binary smoke test removes its home when connection fails", (t) => {
  const temporary = mkdtempSync(join(tmpdir(), "smoke-cleanup-proof-"));
  t.after(() => rmSync(temporary, { recursive: true, force: true }));
  const env = { ...process.env, TMPDIR: temporary, TMP: temporary, TEMP: temporary,
    NODE_DISABLE_COMPILE_CACHE: "1", OAP_SDK_BINARY_PATH: join(temporary, "missing-oapx") };
  delete env.NODE_TEST_CONTEXT;
  const result = spawnSync(process.execPath, ["--test",
    new URL("../dist/test/makai_binary_smoke.test.js", import.meta.url).pathname,
  ], { env, encoding: "utf8" });
  assert.equal(result.status, 1, result.stdout + result.stderr);
  assert.match(result.stdout + result.stderr, /ENOENT|not found|does not exist/);
  assert.deepEqual(readdirSync(temporary), [], "connection failure must release the isolated home");
});
