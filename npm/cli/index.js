"use strict";

function binaryPath() {
  const target = `${process.platform}-${process.arch}`;
  const binary = process.platform === "win32" ? "oxap.exe" : "oxap";
  try {
    return require.resolve(`@oxap/cli-${target}/bin/${binary}`);
  } catch {
    throw new Error(`@oxap/cli has no binary for ${target}: @oxap/cli-${target} is not installed`);
  }
}

module.exports = { binaryPath };
