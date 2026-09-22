#!/usr/bin/env node

/**
 * Makai CLI launcher.
 * Detects the current platform and spawns the correct compiled binary
 * from the matching @oap-sdk/cli-{platform} optional dependency.
 */

const { spawnSync } = require("child_process");

const PLATFORM_MAP = {
  "darwin-arm64": "@oap-sdk/cli-darwin-arm64",
  "darwin-x64": "@oap-sdk/cli-darwin-x64",
  "linux-arm64": "@oap-sdk/cli-linux-arm64",
  "linux-x64": "@oap-sdk/cli-linux-x64",
  "win32-x64": "@oap-sdk/cli-win32-x64",
  "win32-arm64": "@oap-sdk/cli-win32-arm64",
};

const platformKey = `${process.platform}-${process.arch}`;
const packageName = PLATFORM_MAP[platformKey];

if (!packageName) {
  console.error(
    `Error: Makai does not support ${process.platform} ${process.arch}.\n` +
      `Supported platforms: ${Object.keys(PLATFORM_MAP).join(", ")}`
  );
  process.exit(1);
}

let binaryPath;
for (const name of ["oapx", "makai"]) {
  const binaryName = process.platform === "win32" ? `${name}.exe` : name;
  try {
    binaryPath = require.resolve(`${packageName}/bin/${binaryName}`);
    break;
  } catch {
    continue;
  }
}
if (!binaryPath) {
  console.error(
    `Error: Could not find the oapx binary for ${platformKey}.\n` +
      `The package ${packageName} may not be installed.\n` +
      `Try reinstalling: npm install -g makai`
  );
  process.exit(1);
}

const result = spawnSync(binaryPath, process.argv.slice(2), {
  stdio: "inherit",
  env: process.env,
});

if (result.error) {
  console.error(`Error: Failed to execute Makai binary: ${result.error.message}`);
  process.exit(1);
}

process.exit(result.status ?? 1);
