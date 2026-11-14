#!/usr/bin/env node
/**
 * Packs @oxap/cli and the host's @oxap/cli-<target> around a real oxap binary,
 * installs both tarballs into a fresh consumer offline, and checks that the
 * `oxap` bin entry runs that binary with its stdout and exit code.
 *
 * Usage: node scripts/check-npm-cli.mjs <path-to-oxap>  (after `npm run build:sdk`)
 */

import { execFileSync, spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const TARGETS = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-x64", "win32-arm64"];
const VERSION = "0.0.0-check";

function fail(message) {
  console.error(`check-npm-cli: FAIL: ${message}`);
  process.exit(1);
}

function expect(condition, message) {
  if (!condition) fail(message);
}

const binaryArg = process.argv[2];
expect(binaryArg, "usage: node scripts/check-npm-cli.mjs <path-to-oxap>");
const binary = resolve(binaryArg);
const host = `${process.platform}-${process.arch}`;
expect(TARGETS.includes(host), `no platform package for ${host}`);

const distBin = join(ROOT, "dist", "bin");
const distNpm = join(ROOT, "dist", "npm");
rmSync(distBin, { recursive: true, force: true });
rmSync(distNpm, { recursive: true, force: true });
mkdirSync(distBin, { recursive: true });
copyFileSync(binary, join(distBin, `oxap-${host}`));

const withheld = TARGETS.filter((t) => t !== host).join(",");
execFileSync("npx", ["--yes", "tsx", "scripts/package-npm.ts", "--version", VERSION, "--withhold", withheld], {
  cwd: ROOT,
  stdio: "inherit",
});

const readManifest = (dir) => JSON.parse(readFileSync(join(distNpm, dir, "package.json"), "utf8"));
const cli = readManifest("cli");
expect(cli.name === "@oxap/cli", `entry package is named ${cli.name}`);
expect(cli.version === VERSION, `entry package version is ${cli.version}`);
expect(cli.bin?.oxap === "bin/oxap.js", `entry bin is ${JSON.stringify(cli.bin)}`);
expect(
  JSON.stringify(cli.optionalDependencies) === JSON.stringify({ [`@oxap/cli-${host}`]: VERSION }),
  `entry optionalDependencies are ${JSON.stringify(cli.optionalDependencies)}`,
);
expect(cli.dependencies === undefined, "entry package has hard dependencies");

const platform = readManifest(`cli-${host}`);
expect(platform.name === `@oxap/cli-${host}`, `platform package is named ${platform.name}`);
expect(platform.bin === undefined, "platform package declares a bin entry");

const work = mkdtempSync(join(tmpdir(), "oxap-cli-check-"));
try {
  const pack = (dir) =>
    JSON.parse(
      execFileSync("npm", ["pack", "--json", "--pack-destination", work], { cwd: join(distNpm, dir), encoding: "utf8" }),
    )[0].filename;
  const tarballs = [pack(`cli-${host}`), pack("cli")].map((f) => join(work, f));

  const app = join(work, "app");
  mkdirSync(app);
  execFileSync("npm", ["init", "-y"], { cwd: app, stdio: "ignore" });
  execFileSync("npm", ["install", "--offline", "--no-audit", "--no-fund", ...tarballs], { cwd: app, stdio: "inherit" });

  const resolved = execFileSync(process.execPath, ["-p", 'require("@oxap/cli").binaryPath()'], {
    cwd: app,
    encoding: "utf8",
  }).trim();
  const expected = join(app, "node_modules", "@oxap", `cli-${host}`, "bin", "oxap");
  expect(realpathSync(resolved) === realpathSync(expected), `binaryPath() is ${resolved}`);

  const bin = join(app, "node_modules", ".bin", "oxap");
  for (const args of [["--version"], ["serve", "ghost"]]) {
    const direct = spawnSync(binary, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    const viaNpm = spawnSync(bin, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
    console.log(`oxap ${args.join(" ")}: direct exit ${direct.status}, via npm bin exit ${viaNpm.status}`);
    expect(viaNpm.status === direct.status, `exit code differs for ${args.join(" ")}`);
    expect(viaNpm.stdout === direct.stdout, `stdout differs for ${args.join(" ")}`);
  }
  const version = spawnSync(bin, ["--version"], { encoding: "utf8" }).stdout.trim();
  expect(version.startsWith("oxap-"), `oxap --version printed ${JSON.stringify(version)}`);
  console.log(`installed oxap --version: ${version}`);
} finally {
  rmSync(work, { recursive: true, force: true });
}

console.log("check-npm-cli: OK");
