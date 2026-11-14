
const { mkdirSync, copyFileSync, writeFileSync, chmodSync, readFileSync, existsSync, readdirSync } = require("node:fs");
const { join } = require("node:path");

const ROOT = process.cwd();
const BIN_DIR = join(ROOT, "dist", "bin");
const NPM_DIR = join(ROOT, "dist", "npm");

const versionIdx = process.argv.indexOf("--version");
const VERSION =
  versionIdx !== -1
    ? process.argv[versionIdx + 1]
    : JSON.parse(readFileSync(join(ROOT, "package.json"), "utf-8")).version;

const PLATFORMS = [
  { target: "darwin-arm64", os: "darwin", cpu: "arm64", binary: "oxap-darwin-arm64" },
  { target: "darwin-x64", os: "darwin", cpu: "x64", binary: "oxap-darwin-x64" },
  { target: "linux-arm64", os: "linux", cpu: "arm64", binary: "oxap-linux-arm64" },
  { target: "linux-x64", os: "linux", cpu: "x64", binary: "oxap-linux-x64" },
  { target: "win32-x64", os: "win32", cpu: "x64", binary: "oxap-win32-x64" },
  { target: "win32-arm64", os: "win32", cpu: "arm64", binary: "oxap-win32-arm64" },
];

const REPOSITORY = { type: "git", url: "https://github.com/lsm/oxap" };
const packaged: string[] = [];

const withheldIdx = process.argv.indexOf("--withhold");
const WITHHELD_TARGETS = withheldIdx !== -1 ? process.argv[withheldIdx + 1].split(",").filter(Boolean) : [];

console.log(`Packaging npm packages (version ${VERSION})...\n`);

for (const { target, os, cpu, binary } of PLATFORMS) {
  const pkgName = `@oxap/cli-${target}`;
  const pkgDir = join(NPM_DIR, `cli-${target}`);
  const binDir = join(pkgDir, "bin");

  mkdirSync(binDir, { recursive: true });

  const srcBinary = join(BIN_DIR, binary);
  const destBinary = join(binDir, os === "win32" ? "oxap.exe" : "oxap");

  if (!existsSync(srcBinary)) {
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
        description: `oxap binary for ${os} ${cpu}; install @oxap/cli instead`,
        os: [os],
        cpu: [cpu],
        files: ["bin/"],
        license: "ISC",
        repository: REPOSITORY,
      },
      null,
      2
    )
  );

  packaged.push(pkgName);
  console.log(`  Created ${pkgName}`);
}

const cliDir = join(NPM_DIR, "cli");
copyDir(join(__dirname, "..", "npm", "cli"), cliDir);
chmodSync(join(cliDir, "bin", "oxap.js"), 0o755);
writeFileSync(
  join(cliDir, "package.json"),
  JSON.stringify(
    {
      name: "@oxap/cli",
      version: VERSION,
      description: "The oxap runtime, CLI and TUI",
      bin: { oxap: "bin/oxap.js" },
      main: "index.js",
      files: ["bin/", "index.js"],
      optionalDependencies: Object.fromEntries(packaged.map((name) => [name, VERSION])),
      license: "ISC",
      repository: REPOSITORY,
    },
    null,
    2
  )
);
console.log(`  Created @oxap/cli`);

const mainDir = join(NPM_DIR, "oxap-sdk");
mkdirSync(mainDir, { recursive: true });


const srcDir = join(ROOT, "dist", "src");
const destSrcDir = join(mainDir, "dist", "src");
mkdirSync(destSrcDir, { recursive: true });

const mainPkg = JSON.parse(readFileSync(join(ROOT, "package.json"), "utf-8"));
mainPkg.version = VERSION;
mainPkg.optionalDependencies = { ...mainPkg.optionalDependencies, "@oxap/cli": VERSION };
mainPkg.files = ["dist/src/", "README.md"];

writeFileSync(
  join(mainDir, "package.json"),
  JSON.stringify(mainPkg, null, 2)
);

function copyDir(src: string, dest: string) {
  mkdirSync(dest, { recursive: true });
  for (const entry of readdirSync(src, { withFileTypes: true })) {
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

copyFileSync(join(ROOT, "sdk", "typescript", "README.md"), join(mainDir, "README.md"));

console.log(`  Created @oxap/sdk (main package)`);

console.log(`\nAll packages created in ${NPM_DIR}`);
console.log(`\nTo publish, run:`);
for (const { target } of PLATFORMS) {
  console.log(`  cd dist/npm/cli-${target} && npm publish --access public`);
}
console.log(`  cd dist/npm/cli && npm publish --access public`);
console.log(`  cd dist/npm/oxap-sdk && npm publish --access public`);
