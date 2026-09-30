import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { createOapClient, MakaiProtocolError, type MakaiModelsApi } from "../src";

const fixturesDir = path.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const oapFixtureScript = path.join(fixturesDir, "oap-server.js");

async function withOapModels<T>(
  entry: string,
  use: (api: MakaiModelsApi) => Promise<T>,
): Promise<T> {
  const client = await createOapClient({
    command: process.execPath,
    args: [oapFixtureScript],
    env: { ...process.env, OAP_FIXTURE_ENTRY: entry },
  });
  try {
    return await use(client.models);
  } finally {
    await client.close();
  }
}

async function refusesNonObjectEntry(
  entry: string,
  use: (api: MakaiModelsApi) => Promise<unknown>,
): Promise<void> {
  let caught: unknown;
  try {
    await withOapModels(entry, use);
  } catch (error) {
    caught = error;
  }
  assert.ok(
    caught instanceof MakaiProtocolError,
    `a non-object entry must be refused, got ${String(caught)}`,
  );
  assert.equal(
    (caught as MakaiProtocolError).code,
    "malformed_response",
    "a refusal must carry the malformed_response code",
  );
  assert.match(
    (caught as MakaiProtocolError).message,
    /model entry must be an object/,
  );
}

test("the OAP catalog refuses a non-object entry instead of dropping it", async () => {
  await refusesNonObjectEntry("nonobject", (api) => api.list());
});

test("a malformed entry after a valid one is still refused", async () => {
  await refusesNonObjectEntry("trailing-nonobject", (api) => api.list());
});

test("the model_id filter really does skip the valid fixture row", async () => {
  const models = await withOapModels(
    "default",
    async (api) => (await api.list({ model_id: "no-such-model" })).models,
  );
  assert.equal(
    models.length,
    0,
    "a local model_id filter matching nothing must yield no models",
  );
});

test("a malformed entry is refused even when a local filter skipped the valid row", async () => {
  await refusesNonObjectEntry("trailing-nonobject", (api) =>
    api.list({ model_id: "no-such-model" }),
  );
});

test("the unfiltered catalog still lists the fixture model", async () => {
  const models = await withOapModels("default", async (api) => (await api.list()).models);
  assert.equal(models.length, 1);
  assert.equal(models[0].model_id, "mock");
});
