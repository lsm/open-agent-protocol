import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

const REPOSITORY = "lsm/oxap";
const WORKFLOW = "release-binaries.yml";
const ENVIRONMENT = "release";
const PLATFORMS = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-x64", "win32-arm64"];
const NPM_PACKAGES = [...PLATFORMS.map((target) => `@oxap/cli-${target}`), "@oxap/cli", "@oxap/sdk", "@oxap/client"];
const PYPI_PROJECT = "oxap-sdk";

function option(name) {
  const index = process.argv.indexOf(name);
  return index === -1 ? undefined : process.argv[index + 1];
}

function fail(message) {
  console.error(`release-channels: ${message}`);
  process.exit(1);
}

export function pep440(version) {
  const match = /^(\d+\.\d+\.\d+)(?:-(alpha|beta|rc)\.(\d+))?$/.exec(version);
  if (!match) fail(`${version} is not a release version this script can map to PEP 440`);
  return match[2] ? `${match[1]}${{ alpha: "a", beta: "b", rc: "rc" }[match[2]]}${match[3]}` : match[1];
}

export function npmTarball(name, version) {
  return `${name.replace(/^@/, "").replace("/", "-")}-${version}.tgz`;
}

export function bundledNpmPackages(version, bundle) {
  return NPM_PACKAGES.filter((name) => existsSync(join(bundle, "npm", npmTarball(name, version))));
}

function workspace() {
  const out = execFileSync("cargo", ["metadata", "--no-deps", "--format-version", "1", "--manifest-path", "rust/Cargo.toml"], { encoding: "utf8" });
  return JSON.parse(out).packages;
}

export function publishOrder(packages) {
  const byName = new Map(packages.map((pkg) => [pkg.name, pkg]));
  const publishable = packages.filter((pkg) => pkg.publish === null);
  const order = [];
  const seen = new Set();
  const visit = (pkg) => {
    if (seen.has(pkg.name)) return;
    seen.add(pkg.name);
    for (const dep of pkg.dependencies) {
      const local = byName.get(dep.name);
      if (!local || dep.kind === "dev") continue;
      if (local.publish !== null) fail(`${pkg.name} depends on ${local.name}, which is publish = false`);
      visit(local);
    }
    order.push(pkg);
  };
  publishable.sort((a, b) => a.name.localeCompare(b.name)).forEach(visit);
  return order.map((pkg) => ({ name: pkg.name, version: pkg.version, manifest: pkg.manifest_path }));
}

function versions() {
  const tag = option("--tag");
  const root = JSON.parse(readFileSync("package.json", "utf8")).version;
  const npm = tag ? tag.replace(/^v/, "") : root;
  const python = pep440(npm);
  const found = {
    "package.json": root,
    "clients/ts/package.json": JSON.parse(readFileSync("clients/ts/package.json", "utf8")).version,
    "sdk/python/pyproject.toml": /^version = "([^"]+)"/m.exec(readFileSync("sdk/python/pyproject.toml", "utf8"))[1],
    "zig/build.zig.zon": /^\s*\.version = "([^"]+)"/m.exec(readFileSync("zig/build.zig.zon", "utf8"))[1],
  };
  for (const crate of publishOrder(workspace())) found[`crate ${crate.name}`] = crate.version;
  const wrong = Object.entries(found).filter(([where, version]) => version !== (where.startsWith("sdk/python") ? python : npm));
  if (wrong.length > 0) fail(`the release is ${npm} (${python} on PyPI), but ${wrong.map(([where, version]) => `${where} is ${version}`).join(", ")}`);
  console.log(npm);
}

function crates() {
  for (const crate of publishOrder(workspace())) console.log(crate.name);
}

async function present(url) {
  const response = await fetch(url, { headers: { "User-Agent": `${REPOSITORY} release workflow` } });
  if (response.status === 404) return false;
  if (!response.ok) fail(`${url} answered ${response.status}`);
  return true;
}

function onNpm(name, version) {
  try {
    execFileSync("npm", ["view", `${name}@${version}`, "version"], { stdio: ["ignore", "pipe", "ignore"] });
    return true;
  } catch {
    return false;
  }
}

async function registry() {
  const version = option("--version") ?? fail("registry needs --version");
  const channel = option("--channel");
  const rows = [];
  if (!channel || channel === "npm") {
    const bundle = option("--bundle");
    for (const name of bundle ? bundledNpmPackages(version, bundle) : NPM_PACKAGES) rows.push(["npm", `${name}@${version}`, onNpm(name, version)]);
  }
  if (!channel || channel === "crates") {
    for (const crate of publishOrder(workspace())) {
      rows.push(["crates", `${crate.name}@${version}`, await present(`https://crates.io/api/v1/crates/${crate.name}/${version}`)]);
    }
  }
  if (!channel || channel === "pypi") {
    rows.push(["pypi", `${PYPI_PROJECT}==${pep440(version)}`, await present(`https://pypi.org/pypi/${PYPI_PROJECT}/${pep440(version)}/json`)]);
  }
  for (const [where, what, ok] of rows) console.log(`${ok ? "present" : "missing"} ${where} ${what}`);
  const missing = rows.filter(([, , ok]) => !ok).length;
  console.log(`${rows.length - missing} present, ${missing} missing`);
  if (missing > 0 && process.argv.includes("--require-present")) process.exit(1);
}

function commands() {
  const version = option("--version") ?? fail("commands needs --version");
  const run = option("--run") ?? fail("commands needs --run");
  const sha = option("--sha") ?? fail("commands needs --sha");
  const distTag = version.includes("-") ? "next" : "latest";
  const order = publishOrder(workspace());
  const lines = [
    `# Publish ${version} by hand, once`,
    "",
    `Every package below was built, signed and checked by run ${run} at ${sha}. Run these in order, in one sitting; nothing here is done by CI.`,
    "",
    "## 1. Fetch and check the packages",
    "",
    "```sh",
    `gh run download ${run} --repo ${REPOSITORY} --name release-bundle --dir oxap-${version}`,
    `cd oxap-${version}`,
    "shasum -a 256 -c SHA256SUMS.txt",
    "```",
    "",
    "## 2. npm, platform packages first",
    "",
    "```sh",
    "npm login",
    ...NPM_PACKAGES.map((name) => `npm publish npm/${npmTarball(name, version)} --access public --tag ${distTag}`),
    "```",
    "",
    "## 3. crates.io, in dependency order",
    "",
    "```sh",
    "cargo login",
    `git -C <oxap checkout> checkout --detach ${sha}`,
    ...order.map((crate) => `cargo publish --locked --manifest-path <oxap checkout>/${crate.manifest.slice(crate.manifest.indexOf("rust/"))}`),
    "```",
    "",
    "## 4. Trusted publishers",
    "",
    `Each one names owner \`lsm\`, repository \`oxap\`, workflow \`${WORKFLOW}\` and environment \`${ENVIRONMENT}\`.`,
    "",
    `- GitHub: create the \`${ENVIRONMENT}\` environment with required reviewers before tagging: https://github.com/${REPOSITORY}/settings/environments`,
    ...NPM_PACKAGES.map((name) => `- npm: https://www.npmjs.com/package/${name}/access`),
    ...order.map((crate) => `- crates.io: https://crates.io/crates/${crate.name}/settings`),
    `- PyPI (pending publisher for \`${PYPI_PROJECT}\`): https://pypi.org/manage/account/publishing/`,
    "",
    "## 5. Tag in bootstrap mode, then leave it",
    "",
    "```sh",
    `gh variable set RELEASE_BOOTSTRAP --repo ${REPOSITORY} --body true`,
    `git -C <oxap checkout> tag -s v${version} ${sha} -m v${version}`,
    `git -C <oxap checkout> push origin v${version}`,
    `gh run watch --repo ${REPOSITORY} $(gh run list --repo ${REPOSITORY} --workflow ${WORKFLOW} --branch v${version} --limit 1 --json databaseId --jq '.[0].databaseId')`,
    `gh variable delete RELEASE_BOOTSTRAP --repo ${REPOSITORY}`,
    "```",
    "",
    "The tag run verifies that npm and crates.io already hold these versions instead of publishing them, publishes the GitHub Release, publishes PyPI through the pending publisher, then smoke-tests every registry.",
  ];
  console.log(lines.join("\n"));
}

const actions = { versions, crates, registry, commands };
const action = actions[process.argv[2]];
if (import.meta.url === `file://${process.argv[1]}`) {
  if (!action) fail(`usage: release-channels.mjs ${Object.keys(actions).join("|")} [options]`);
  await action();
}
