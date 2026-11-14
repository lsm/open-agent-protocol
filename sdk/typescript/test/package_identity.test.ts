import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";

const ROOT = path.resolve(__dirname, "../..");

type PackageJson = {
  name: string;
  version: string;
  optionalDependencies?: Record<string, string>;
  dependencies?: Record<string, string>;
  files?: string[];
};

const manifest = JSON.parse(readFileSync(path.join(ROOT, "package.json"), "utf8")) as PackageJson;
const lock = JSON.parse(readFileSync(path.join(ROOT, "package-lock.json"), "utf8")) as {
  name: string;
  version: string;
  packages: Record<string, PackageJson & { optional?: boolean }>;
};

test("the SDK publishes as @oxap/sdk under the OXAP line", () => {
  assert.equal(manifest.name, "@oxap/sdk");
  assert.equal(lock.name, manifest.name);
  assert.equal(lock.packages[""].name, manifest.name);
  assert.equal(lock.version, manifest.version);
  assert.equal(lock.packages[""].version, manifest.version);
});

test("the SDK depends on the same-version CLI for its default runtime", () => {
  const cli = manifest.optionalDependencies?.["@oxap/cli"];
  assert.equal(cli, manifest.version);
  const locked = lock.packages["node_modules/@oxap/cli"];
  assert.equal(locked?.version, manifest.version);
  assert.equal(locked?.optional, true);
});

test("the SDK keeps lossless-json, nanoid and ulid as its only required dependencies", () => {
  assert.deepEqual(Object.keys(manifest.dependencies ?? {}).sort(), [
    "lossless-json",
    "nanoid",
    "ulid",
  ]);
});

test("a tagged release packs the SDK pinned to the CLI at the tag's version", (t) => {
  const work = mkdtempSync(path.join(tmpdir(), "oxap-package-npm-"));
  t.after(() => rmSync(work, { recursive: true, force: true }));
  mkdirSync(path.join(work, "dist", "bin"), { recursive: true });
  mkdirSync(path.join(work, "dist", "src"), { recursive: true });
  writeFileSync(path.join(work, "dist", "src", "index.js"), "");
  mkdirSync(path.join(work, "sdk", "typescript"), { recursive: true });
  writeFileSync(path.join(work, "sdk", "typescript", "README.md"), "");
  writeFileSync(path.join(work, "package.json"), JSON.stringify(manifest));
  const tag = "9.9.9-tag.1";
  assert.notEqual(tag, manifest.version);
  execFileSync(
    process.execPath,
    [
      path.join(ROOT, "scripts", "package-npm.ts"),
      "--version",
      tag,
      "--withhold",
      "darwin-arm64,darwin-x64,linux-arm64,linux-x64,win32-x64,win32-arm64",
    ],
    { cwd: work, stdio: "pipe" },
  );
  const packed = JSON.parse(
    readFileSync(path.join(work, "dist", "npm", "oxap-sdk", "package.json"), "utf8"),
  ) as PackageJson;
  assert.equal(packed.name, "@oxap/sdk");
  assert.equal(packed.version, tag);
  assert.equal(packed.optionalDependencies?.["@oxap/cli"], tag);
});
