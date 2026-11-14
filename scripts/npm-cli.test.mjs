import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const TARGETS = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-x64", "win32-arm64"];

function workDir(t) {
  const dir = mkdtempSync(join(tmpdir(), "oxap-npm-cli-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function until(condition, what) {
  const deadline = Date.now() + 10000;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 20));
  }
}

test("a withheld target leaves no platform package directory behind", (t) => {
  const work = workDir(t);
  const host = `${process.platform}-${process.arch}`;
  mkdirSync(join(work, "dist", "bin"), { recursive: true });
  writeFileSync(join(work, "dist", "bin", `oxap-${host}`), "");
  mkdirSync(join(work, "dist", "src"), { recursive: true });
  writeFileSync(join(work, "dist", "src", "index.js"), "");
  mkdirSync(join(work, "sdk", "typescript"), { recursive: true });
  writeFileSync(join(work, "sdk", "typescript", "README.md"), "");
  cpSync(join(ROOT, "package.json"), join(work, "package.json"));
  const withheld = TARGETS.filter((target) => target !== host);
  execFileSync(
    process.execPath,
    [join(ROOT, "scripts", "package-npm.ts"), "--version", "0.0.0-test", "--withhold", withheld.join(",")],
    { cwd: work, stdio: "ignore" },
  );
  const npmDir = join(work, "dist", "npm");
  const platformDirs = readdirSync(npmDir).filter((name) => name.startsWith("cli-")).sort();
  assert.deepEqual(platformDirs, [`cli-${host}`]);
  for (const dir of readdirSync(npmDir)) {
    assert.ok(existsSync(join(npmDir, dir, "package.json")), `${dir} has no package.json`);
  }
});

test("SIGTERM to the oxap launcher also ends the runtime it started", { skip: process.platform === "win32" }, async (t) => {
  const work = workDir(t);
  const modules = join(work, "node_modules", "@oxap");
  cpSync(join(ROOT, "npm", "cli"), join(modules, "cli"), { recursive: true });
  const platform = join(modules, `cli-${process.platform}-${process.arch}`);
  mkdirSync(join(platform, "bin"), { recursive: true });
  writeFileSync(join(platform, "package.json"), JSON.stringify({ name: `@oxap/cli-${process.platform}-${process.arch}` }));
  const pidFile = join(work, "runtime.pid");
  const runtime = join(platform, "bin", "oxap");
  writeFileSync(runtime, `#!/bin/sh\necho $$ > "${pidFile}"\nexec sleep 60\n`);
  chmodSync(runtime, 0o755);

  const launcher = spawn(process.execPath, [join(modules, "cli", "bin", "oxap.js")], { stdio: "ignore" });
  const exited = new Promise((resolve) => launcher.on("exit", (code, signal) => resolve({ code, signal })));
  await until(() => existsSync(pidFile) && readFileSync(pidFile, "utf8").trim() !== "", "the runtime to start");
  const runtimePid = Number(readFileSync(pidFile, "utf8").trim());
  t.after(() => {
    if (alive(runtimePid)) process.kill(runtimePid, "SIGKILL");
  });

  launcher.kill("SIGTERM");
  const result = await exited;
  assert.equal(result.signal, "SIGTERM");
  await until(() => !alive(runtimePid), "the runtime to end after its launcher was terminated");
});
