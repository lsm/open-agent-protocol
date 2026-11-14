import assert from "node:assert/strict";
import { test } from "node:test";
import { npmTarball, pep440, publishOrder } from "./release-channels.mjs";

const crate = (name, deps = [], publish = null) => ({
  name,
  version: "0.1.0-alpha.33",
  publish,
  manifest_path: `/work/oxap/rust/${name}/Cargo.toml`,
  dependencies: deps.map(([dep, kind = null]) => ({ name: dep, kind })),
});

test("a crate is published after every workspace crate it depends on", () => {
  const order = publishOrder([
    crate("oxap-serve", [["oxap-adapter"], ["oxap-protocol"]]),
    crate("oxap-adapter", [["oxap-protocol"], ["serde"]]),
    crate("oxap-protocol"),
    crate("oxap-sdk"),
  ]).map((entry) => entry.name);
  assert.deepEqual(order, ["oxap-protocol", "oxap-adapter", "oxap-sdk", "oxap-serve"]);
});

test("an unpublishable crate is left out, and a dev-dependency on one is allowed", () => {
  const order = publishOrder([
    crate("oxap-sdk", [["oxap-protocol-fake", "dev"]]),
    crate("oxap-protocol-fake", [], []),
    crate("nocomment", [], []),
  ]).map((entry) => entry.name);
  assert.deepEqual(order, ["oxap-sdk"]);
});

test("the alpha line maps to its PEP 440 spelling", () => {
  assert.equal(pep440("0.1.0-alpha.33"), "0.1.0a33");
  assert.equal(pep440("0.1.0-rc.2"), "0.1.0rc2");
  assert.equal(pep440("1.0.0"), "1.0.0");
});

test("a scoped package packs to the file npm pack writes", () => {
  assert.equal(npmTarball("@oxap/cli-linux-x64", "0.1.0-alpha.33"), "oxap-cli-linux-x64-0.1.0-alpha.33.tgz");
});
