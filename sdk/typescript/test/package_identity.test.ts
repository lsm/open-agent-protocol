import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
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

test("the CLI is the only runtime dependency, and it never depends back on the SDK", () => {
  const cli = lock.packages["node_modules/@oxap/cli"] as PackageJson | undefined;
  assert.equal(cli?.dependencies?.["@oxap/sdk"], undefined);
  assert.equal(cli?.optionalDependencies?.["@oxap/sdk"], undefined);
  assert.deepEqual(Object.keys(manifest.dependencies ?? {}).sort(), [
    "lossless-json",
    "nanoid",
    "ulid",
  ]);
});
