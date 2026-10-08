import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import {
  createOapClient,
  MakaiProtocolError,
  type MakaiModelsApi,
} from "../src";

const sourceFixturesDir = path.resolve(__dirname, "../../sdk/typescript/test/fixtures");
const oapFixtureScript = path.join(sourceFixturesDir, "oap-server.js");

function refusesLifecycle(error: unknown, field: RegExp): boolean {
  if (!(error instanceof MakaiProtocolError)) {
    assert.fail(
      `a parser refusal must be a MakaiProtocolError, got ${String(error)}`,
    );
  }
  assert.equal(
    error.code,
    "malformed_response",
    `a refusal must carry the malformed_response code, got ${String(error.code)}`,
  );
  assert.match(error.message, field);
  return true;
}

async function withOapModels<T>(
  stating: string,
  use: (api: MakaiModelsApi) => Promise<T>,
): Promise<T> {
  const client = await createOapClient({
    command: process.execPath,
    args: [oapFixtureScript],
    env: { ...process.env, OAP_FIXTURE_LIFECYCLE: stating },
  });
  try {
    return await use(client.models);
  } finally {
    await client.close();
  }
}

test("the OAP reader records an absent lifecycle as unknown", async () => {
  const models = await withOapModels("absent", async (api) => (await api.list()).models);
  assert.equal(models[0].lifecycle, undefined, "an unstated lifecycle must stay unknown");
  assert.ok(!("lifecycle" in models[0]), "no value may be invented for the descriptor");
});

test("the OAP reader keeps a stated lifecycle", async () => {
  for (const stated of ["stable", "preview"] as const) {
    const models = await withOapModels(stated, async (api) => (await api.list()).models);
    assert.equal(models[0].lifecycle, stated, `${stated} must read as ${stated}`);
  }
});

test("the OAP reader refuses a present null or unknown lifecycle", async () => {
  for (const [stating, what] of [
    ["null", "an explicit null"],
    ["number", "a number"],
    ["invented", "an invented literal"],
  ] as const) {
    await assert.rejects(
      () =>
        withOapModels(stating, async (api) => {
          await api.list();
          return null;
        }),
      (error: unknown) => refusesLifecycle(error, /lifecycle/),
      `${what} must be a malformed response on the OAP path too`,
    );
  }
});

test("a model that stated deprecated is filtered out unless asked for", async () => {
  const filtered = await withOapModels("deprecated", async (api) => (await api.list()).models);
  assert.equal(
    filtered.length,
    0,
    "a stated deprecated lifecycle must still be filtered from a default listing",
  );
  const included = await withOapModels("deprecated", async (api) =>
    (await api.list({ include_deprecated: true })).models,
  );
  assert.equal(included.length, 1, "and included when the listing asks for it");
});

test("a model that did not state a lifecycle is not filtered out", async () => {
  const models = await withOapModels("absent", async (api) => (await api.list()).models);
  assert.equal(
    models.length,
    1,
    "an unknown lifecycle must not silently drop the model from the listing",
  );
});

test("a transport failure cannot stand in for a parser refusal", () => {
  const transportFailure = new Error("fixture process exited unexpectedly: lifecycle");
  assert.throws(
    () => refusesLifecycle(transportFailure, /lifecycle/),
    (error: unknown) => {
      assert.ok(
        error instanceof assert.AssertionError,
        `a non-protocol failure must be rejected, got ${String(error)}`,
      );
      return true;
    },
    "the predicate must not accept a plain Error even when its message names the field",
  );

  const wrongCode = new MakaiProtocolError("lifecycle is wrong", "invalid_request");
  assert.throws(
    () => refusesLifecycle(wrongCode, /lifecycle/),
    (error: unknown) => {
      assert.ok(
        error instanceof assert.AssertionError,
        `a refusal with the wrong code must be rejected, got ${String(error)}`,
      );
      return true;
    },
    "a MakaiProtocolError carrying another code must not satisfy a parser-refusal case",
  );
});

