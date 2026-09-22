
import { mkdirSync, copyFileSync, writeFileSync, chmodSync, readFileSync } from "node:fs";
import { join } from "node:path";

const ROOT = process.cwd();
const BIN_DIR = join(ROOT, "dist", "bin");
const NPM_DIR = join(ROOT, "dist", "npm");

const versionIdx = process.argv.indexOf("--version");
const VERSION =
  versionIdx !== -1
    ? process.argv[versionIdx + 1]
    : JSON.parse(readFileSync(join(ROOT, "package.json"), "utf-8")).version;

const PLATFORMS = [
  { target: "darwin-arm64", os: "darwin", cpu: "arm64", binary: "oapx-darwin-arm64" },
  { target: "darwin-x64", os: "darwin", cpu: "x64", binary: "oapx-darwin-x64" },
  { target: "linux-arm64", os: "linux", cpu: "arm64", binary: "oapx-linux-arm64" },
  { target: "linux-x64", os: "linux", cpu: "x64", binary: "oapx-linux-x64" },
  { target: "win32-x64", os: "win32", cpu: "x64", binary: "oapx-win32-x64" },
  { target: "win32-arm64", os: "win32", cpu: "arm64", binary: "oapx-win32-arm64" },
];

const withheldIdx = process.argv.indexOf("--withhold");
const WITHHELD_TARGETS = withheldIdx !== -1 ? process.argv[withheldIdx + 1].split(",").filter(Boolean) : [];

console.log(`Packaging npm packages (version ${VERSION})...\n`);

for (const { target, os, cpu, binary } of PLATFORMS) {
  const pkgName = `@oap-sdk/cli-${target}`;
  const pkgDir = join(NPM_DIR, `cli-${target}`);
  const binDir = join(pkgDir, "bin");

  mkdirSync(binDir, { recursive: true });

  const srcBinary = join(BIN_DIR, binary);
  const destBinary = join(binDir, os === "win32" ? "oapx.exe" : "oapx");

  if (!require("fs").existsSync(srcBinary)) {
    if (WITHHELD_TARGETS.includes(target)) {
      console.log(`  skip ${pkgName}: ${binary} was withheld by the release build`);
      continue;
    }
    throw new Error(`Binary not found: ${srcBinary} (required for ${pkgName})`);
  }
  copyFileSync(srcBinary, destBinary);
  if (os !== "win32") {
    chmodSync(destBinary, 0o755);
  }

  writeFileSync(
    join(pkgDir, "package.json"),
    JSON.stringify(
      {
        name: pkgName,
        version: VERSION,
        description: `oapx binary for ${os} ${cpu}`,
        os: [os],
        cpu: [cpu],
        bin: { oapx: os === "win32" ? "bin/oapx.exe" : "bin/oapx" },
        files: ["bin/"],
        license: "ISC",
        repository: {
          type: "git",
          url: "https://github.com/lsm/open-agent-protocol",
        },
      },
      null,
      2
    )
  );

  console.log(`  Created ${pkgName}`);
}

const mainDir = join(NPM_DIR, "oap-sdk");
mkdirSync(mainDir, { recursive: true });

copyFileSync(join(ROOT, "bin", "oapx.js"), join(mainDir, "oapx.js"));
chmodSync(join(mainDir, "oapx.js"), 0o755);

const srcDir = join(ROOT, "dist", "src");
const destSrcDir = join(mainDir, "dist", "src");
mkdirSync(destSrcDir, { recursive: true });

const mainPkg = JSON.parse(readFileSync(join(ROOT, "package.json"), "utf-8"));
mainPkg.version = VERSION;
mainPkg.bin = { oapx: "oapx.js" };
mainPkg.files = ["dist/src/", "oapx.js", "README.md"];
if (mainPkg.optionalDependencies) {
  for (const dep of Object.keys(mainPkg.optionalDependencies)) {
    if (dep.startsWith("@oap-sdk/cli-")) {
      mainPkg.optionalDependencies[dep] = VERSION;
    }
  }
}

writeFileSync(
  join(mainDir, "package.json"),
  JSON.stringify(mainPkg, null, 2)
);

function copyDir(src: string, dest: string) {
  mkdirSync(dest, { recursive: true });
  for (const entry of require("fs").readdirSync(src, { withFileTypes: true })) {
    const srcPath = join(src, entry.name);
    const destPath = join(dest, entry.name);
    if (entry.isDirectory()) {
      copyDir(srcPath, destPath);
    } else {
      copyFileSync(srcPath, destPath);
    }
  }
}
copyDir(srcDir, destSrcDir);

copyFileSync(join(ROOT, "README.md"), join(mainDir, "README.md"));

console.log(`  Created oap-sdk (main package)`);

console.log(`\nAll packages created in ${NPM_DIR}`);
console.log(`\nTo publish, run:`);
for (const { target } of PLATFORMS) {
  console.log(`  cd dist/npm/cli-${target} && npm publish --access public`);
}
console.log(`  cd dist/npm/oap-sdk && npm publish --access public`);
