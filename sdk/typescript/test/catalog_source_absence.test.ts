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

function refusesSource(error: unknown, field: RegExp): boolean {
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
    env: { ...process.env, OAP_FIXTURE_SOURCE: stating },
  });
  try {
    return await use(client.models);
  } finally {
    await client.close();
  }
}

test("the OAP reader records an absent source as unknown", async () => {
  const models = await withOapModels("absent", async (api) => (await api.list()).models);
  assert.equal(models.length, 1);
  assert.equal(models[0].source, undefined, "an unstated source must stay unknown");
  assert.ok(!("source" in models[0]), "no value may be invented for the descriptor");
});

test("the OAP reader maps a stated source to its shared vocabulary", async () => {
  for (const [stated, want] of [
    ["discovered", "dynamic"],
    ["fallback", "static_fallback"],
  ] as const) {
    const models = await withOapModels(stated, async (api) => (await api.list()).models);
    assert.equal(models[0].source, want, `${stated} must read as ${want}`);
  }
});

test("the OAP reader refuses a present null or unknown source", async () => {
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
      (error: unknown) => refusesSource(error, /source/),
      `${what} must be a malformed response on the OAP path too`,
    );
  }
});

test("the default fixture still states a source, so no reader regressed to unknown", async () => {
  const models = await withOapModels("", async (api) => (await api.list()).models);
  assert.equal(models[0].source, "dynamic", "a stated discovered source still reads as dynamic");
});

test("the OAP reader refuses the shared aliases, which are not wire values", async () => {
  for (const [stating, alias] of [
    ["alias-dynamic", "dynamic"],
    ["alias-static-fallback", "static_fallback"],
  ] as const) {
    await assert.rejects(
      () =>
        withOapModels(stating, async (api) => {
          await api.list();
          return null;
        }),
      (error: unknown) => refusesSource(error, /source/),
      `${alias} is the shared vocabulary, not a wire value: the modelSource enum permits discovered and fallback only`,
    );
  }
});

test("a transport failure cannot stand in for a parser refusal", () => {
  const transportFailure = new Error("fixture process exited unexpectedly: source");
  assert.throws(
    () => refusesSource(transportFailure, /source/),
    (error: unknown) => {
      assert.ok(
        error instanceof assert.AssertionError,
        `a non-protocol failure must be rejected, got ${String(error)}`,
      );
      return true;
    },
    "the predicate must not accept a plain Error even when its message mentions the field",
  );

  const wrongCode = new MakaiProtocolError("source is wrong", "invalid_request");
  assert.throws(
    () => refusesSource(wrongCode, /source/),
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
